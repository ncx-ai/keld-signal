package ingress

import (
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/devgen"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

func TestGenerateIsRefusedUnlessTheSettingIsOn(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", DevGenerateRoute(driveStub)))
	defer srv.Close()

	res := doJSON(t, http.MethodPost, srv.URL+"/v1/dev/generate", map[string]any{})
	defer res.Body.Close()
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 while dev_generate is off, got %d", res.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out["error"] != "dev_generate_off" {
		t.Fatalf("want the refusal named, got %v", out)
	}
}

func TestGenerateWritesATranscriptAndMarksIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	// devgen writes under the OS home, not KELD_HOME: the transcript has to land
	// in the root the watcher already reads, and the checkout under a dot-free
	// sibling. Both are redirected here so the test touches nothing real.
	t.Setenv("HOME", home)

	on := true
	if err := settings.WriteV3Settings(settings.V3Patch{DevGenerate: &on}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	srv := httptest.NewServer(DiscardHandler("s3cret", DevGenerateRoute(driveStub)))
	defer srv.Close()

	res := doJSON(t, http.MethodPost, srv.URL+"/v1/dev/generate", map[string]any{})
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := os.ReadFile("/dev/null")
		t.Fatalf("want 200, got %d %s", res.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	session, _ := out["session"].(string)
	if !devgen.IsGenerated(session) {
		t.Fatalf("session %q carries no devgen marker — these rows reach Atlas", session)
	}
	path, _ := out["transcript"].(string)
	if path == "" {
		t.Fatal("no transcript path reported")
	}
	if !strings.HasPrefix(path, filepath.Join(home, ".claude", "projects")) {
		t.Fatalf("the transcript must land in the watched root, got %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the reported transcript does not exist: %v", err)
	}
	// The route reports a real repository and real work, because those are what
	// decide whether the block attributes and what it costs.
	if out["repo"] == "" || out["prompts"].(float64) < 1 || out["tokens"].(float64) <= 0 {
		t.Fatalf("the result describes no work: %v", out)
	}
}

func TestRepoChoicesAlwaysIncludeAnUnmatchableOne(t *testing.T) {
	// ⚠️ Without this, every generated block attributes cleanly and the Projects
	// pane's own job — work with nowhere to put it — is never exercised.
	rng := rand.New(rand.NewSource(1))
	choices := devRepoChoices(settings.Settings{}, rng)
	if len(choices) != 4 {
		t.Fatalf("want 3 defaults plus one random, got %d", len(choices))
	}
	if !strings.Contains(choices[len(choices)-1].Remote, "unclaimed-org") {
		t.Fatalf("the last choice must be the unmatchable one, got %q",
			choices[len(choices)-1].Remote)
	}
}

func TestConfiguredReposReplaceTheDefaults(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	set := settings.Settings{DevRepos: []string{"github.com/acme/web", "github.com/acme/api"}}
	choices := devRepoChoices(set, rng)
	if len(choices) != 3 {
		t.Fatalf("want 2 configured plus one random, got %d", len(choices))
	}
	if choices[0].Remote != "github.com/acme/web" || choices[1].Remote != "github.com/acme/api" {
		t.Fatalf("configured repositories were not used: %+v", choices)
	}
}

func TestDevGenerateNeedsNoRestart(t *testing.T) {
	// handleDevGenerate calls settings.Load() per request, so the toggle takes
	// effect immediately. The restart list is "keys nothing re-reads while the
	// daemon runs" — adding these by symmetry would make the page demand a
	// restart it does not need, which is the mirror of the attribution defect.
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()

	res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"dev_generate": true})
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	if out["restart_required"] != false {
		t.Fatalf("dev_generate is read per request; it must not require a restart: %v", out)
	}
	if !settings.Load().DevGenerate {
		t.Fatal("dev_generate was not persisted")
	}
}

// driveStub stands in for the daemon's pipeline hook. It reports one block, so
// the route's own behaviour is under test rather than the emitter's — the
// end-to-end claim that a block really lands is made by ui/e2e/devgen.spec.ts
// against a live daemon, which is the only place it can honestly be made.
func driveStub(session, path string) int { return 1 }
