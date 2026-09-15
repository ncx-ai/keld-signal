package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
)

const routeSecret = "s3cret"

func integrationsServer(t *testing.T, snap integrationsSnapshot, apply integrationsApply) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	IntegrationsRoute(snap, apply)(mux, ingress.RequireSecret(routeSecret))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, secret string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set("x-keld-agent-secret", secret)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func fixtureResponse() integrations.Response {
	now := time.Date(2026, 9, 15, 9, 13, 0, 0, time.UTC)
	rows := integrations.Compute(now, integrations.Catalogue, map[string]integrations.Facts{}, integrations.Options{})
	return integrations.Respond(now, rows, true)
}

// AC-1: one row per known integration, each state inside the published
// vocabulary, and the vocabulary published beside them so a consumer derives
// its cases from the server.
func TestIntegrationsRouteAnswersTheWireShape(t *testing.T) {
	srv := integrationsServer(t, fixtureResponse, nil)
	res := do(t, srv, http.MethodGet, "/v1/integrations", routeSecret)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/integrations: %d", res.StatusCode)
	}
	var body integrations.Response
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Integrations) != len(integrations.Catalogue) {
		t.Fatalf("%d rows for a %d-entry catalogue", len(body.Integrations), len(integrations.Catalogue))
	}
	known := map[integrations.State]bool{}
	for _, s := range body.Vocabulary.States {
		known[s] = true
	}
	if len(body.Vocabulary.States) != len(integrations.States) {
		t.Fatalf("vocabulary.states has %d entries, want %d", len(body.Vocabulary.States), len(integrations.States))
	}
	if len(body.Vocabulary.WaitingOn) != len(integrations.WaitingOns) {
		t.Fatalf("vocabulary.waiting_on has %d entries, want %d", len(body.Vocabulary.WaitingOn), len(integrations.WaitingOns))
	}
	for _, in := range body.Integrations {
		if !known[in.State] {
			t.Fatalf("%s: state %q is outside vocabulary.states", in.ID, in.State)
		}
	}
	if !body.AutoSetup {
		t.Fatal("auto_setup did not survive the wire")
	}
}

// Loopback secret, exactly as /v1/settings does it.
func TestIntegrationsRouteRefusesWithoutTheSecret(t *testing.T) {
	srv := integrationsServer(t, fixtureResponse, nil)
	if res := do(t, srv, http.MethodGet, "/v1/integrations", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no secret: %d, want 401", res.StatusCode)
	}
	if res := do(t, srv, http.MethodGet, "/v1/integrations", "wrong"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong secret: %d, want 401", res.StatusCode)
	}
	if res := do(t, srv, http.MethodPost, "/v1/integrations/codex/setup", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("setup with no secret: %d, want 401", res.StatusCode)
	}
}

// AC-2's server half: the adapter runs, and the answer carries the backup and
// the restart notice.
func TestSetupRouteReportsTheBackupAndTheRestartNotice(t *testing.T) {
	var applied []string
	srv := integrationsServer(t, fixtureResponse, func(id string) (integrations.SetupResult, error) {
		applied = append(applied, id)
		return integrations.SetupResult{Backup: "/b/codex/config.toml", RestartRequired: true}, nil
	})
	res := do(t, srv, http.MethodPost, "/v1/integrations/codex/setup", routeSecret)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST setup: %d", res.StatusCode)
	}
	var out integrations.SetupResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Backup != "/b/codex/config.toml" || !out.RestartRequired {
		t.Fatalf("body = %+v", out)
	}
	if len(applied) != 1 || applied[0] != "codex" {
		t.Fatalf("applied = %v, want [codex]", applied)
	}
}

// An id outside the catalogue is a 404 and NOTHING is applied.
func TestSetupRouteRefusesAnUnknownIDWithoutApplying(t *testing.T) {
	called := false
	srv := integrationsServer(t, fixtureResponse, func(string) (integrations.SetupResult, error) {
		called = true
		return integrations.SetupResult{}, nil
	})
	res := do(t, srv, http.MethodPost, "/v1/integrations/emacs/setup", routeSecret)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id: %d, want 404", res.StatusCode)
	}
	if called {
		t.Fatal("an adapter ran for an id outside the catalogue")
	}
}

// A conflict is REPORTED on a 200, not resolved: the person owns that file.
func TestSetupRouteReportsAConflictRatherThanReplacing(t *testing.T) {
	srv := integrationsServer(t, fixtureResponse, func(string) (integrations.SetupResult, error) {
		return integrations.SetupResult{Conflict: "existing [otel] table"}, nil
	})
	res := do(t, srv, http.MethodPost, "/v1/integrations/codex/setup", routeSecret)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("conflict: %d, want 200 with the conflict named", res.StatusCode)
	}
	var out integrations.SetupResult
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Conflict == "" || out.RestartRequired {
		t.Fatalf("body = %+v", out)
	}
}

// Privacy: the body carries ids, instants, states, versions and one backup
// path. No transcript path, no prompt text, no span, no offset.
func TestIntegrationsRouteBodyCarriesNoTranscriptPath(t *testing.T) {
	srv := integrationsServer(t, fixtureResponse, nil)
	res := do(t, srv, http.MethodGet, "/v1/integrations", routeSecret)
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	// Path-shaped and text-shaped things only: the word "transcripts" appears
	// in the reader instruction, which is prose a person reads, not a path.
	for _, forbidden := range []string{".jsonl", "/projects/", "/Users/", "/home/", "prompt_text", "\"text\"", "\"span\"", "\"offset\""} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the body carries %q:\n%s", forbidden, raw)
		}
	}
}
