package ingress

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

func doJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var r *bytes.Buffer
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r = bytes.NewBuffer(b)
	} else {
		r = bytes.NewBuffer(nil)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func decodeInto(t *testing.T, res *http.Response, v any) {
	t.Helper()
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestSettingsRouteRequiresSecret(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/v1/settings")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", res.StatusCode)
	}
}

func TestGetSettingsReturnsDefaultsAndEmptyReadonly(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()

	res := doJSON(t, http.MethodGet, srv.URL+"/v1/settings", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	var v settingsView
	decodeInto(t, res, &v)
	if !v.SendToAtlas {
		t.Fatal("absent send_to_atlas must read as ON")
	}
	if v.DevBlocks != "" {
		t.Fatalf("want no dev granularity by default, got %q", v.DevBlocks)
	}
	if v.ShowBreaks {
		t.Fatal("show_breaks defaults to false")
	}
	if len(v.WorkstreamsOff) != 0 {
		t.Fatalf("want an empty (never nil) list, got %v", v.WorkstreamsOff)
	}
	if v.Attribution {
		t.Fatal("attribution defaults to false")
	}
	if len(v.Readonly) != 0 {
		t.Fatalf("want no readonly keys with no env pins, got %v", v.Readonly)
	}
}

func TestGetSettingsReportsReadonlyKeysPinnedByEnv(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(settings.AtlasEnv, "0")
	t.Setenv(settings.DevBlocksEnv, "prompt")
	t.Setenv("KELD_ATTRIBUTION", "1")

	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()
	res := doJSON(t, http.MethodGet, srv.URL+"/v1/settings", nil)
	var v settingsView
	decodeInto(t, res, &v)

	want := map[string]bool{"send_to_atlas": true, "dev_blocks": true, "attribution": true}
	if len(v.Readonly) != len(want) {
		t.Fatalf("readonly = %v, want exactly %v", v.Readonly, want)
	}
	for _, k := range v.Readonly {
		if !want[k] {
			t.Fatalf("unexpected readonly key %q", k)
		}
	}
	if v.SendToAtlas {
		t.Fatal("KELD_ATLAS=0 must be reflected in the effective value too")
	}
	if !v.Attribution {
		t.Fatal("KELD_ATTRIBUTION=1 must be reflected in the effective value too")
	}
}

// An env value the resolver does not recognise falls through to the file, so
// it must NOT be reported as pinning anything — a false "readonly" would grey
// out a control that a write would actually still move.
func TestUnrecognisedEnvValueIsNotReportedAsReadonly(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(settings.AtlasEnv, "maybe")
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()
	res := doJSON(t, http.MethodGet, srv.URL+"/v1/settings", nil)
	var v settingsView
	decodeInto(t, res, &v)
	for _, k := range v.Readonly {
		if k == "send_to_atlas" {
			t.Fatal("an unrecognised KELD_ATLAS value must not be reported as pinning send_to_atlas")
		}
	}
}

func TestPutSettingsWritesFileAndReportsRestartRequired(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()

	// show_breaks alone: no restart required.
	res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"show_breaks": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	var out map[string]any
	decodeInto(t, res, &out)
	if out["restart_required"] != false {
		t.Fatalf("show_breaks alone must not require a restart, got %v", out)
	}
	if s := settings.Load(); !s.ShowBreaks {
		t.Fatal("show_breaks was not persisted")
	}

	// send_to_atlas: restart required.
	res = doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"send_to_atlas": false})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	decodeInto(t, res, &out)
	if out["restart_required"] != true {
		t.Fatalf("send_to_atlas must require a restart, got %v", out)
	}
	if s := settings.Load(); s.SendToAtlas == nil || *s.SendToAtlas {
		t.Fatal("send_to_atlas was not persisted as false")
	}
	// The earlier show_breaks write must survive this second, unrelated PUT.
	if s := settings.Load(); !s.ShowBreaks {
		t.Fatal("an unrelated PUT must not erase a previously-written key")
	}
}

// T39: dev granularity refused while Atlas is on, at the ROUTE, with the
// EXACT 409 body the contract names.
func TestPutSettingsDevBlocksRefusedWhileAtlasOn(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()

	res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"dev_blocks": "prompt"})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", res.StatusCode)
	}
	var out map[string]string
	decodeInto(t, res, &out)
	if out["error"] != "turn_off_send_to_atlas_first" {
		t.Fatalf("want the exact contract body, got %v", out)
	}
	if s := settings.Load(); s.DevBlocks != "" {
		t.Fatal("a refused PUT must not have written anything")
	}
}

// The SAME PUT, with send_to_atlas turned off in the same request, must
// succeed: the refusal is evaluated on the MERGED effective view.
func TestPutSettingsDevBlocksAllowedWhenTurningAtlasOffInTheSameRequest(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()

	res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings",
		map[string]any{"send_to_atlas": false, "dev_blocks": "minute"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	s := settings.Load()
	if s.DevBlocks != "minute" {
		t.Fatalf("dev_blocks not persisted, got %q", s.DevBlocks)
	}
}

// A PUT that does not touch send_to_atlas or dev_blocks must never be
// blocked by a pre-existing bad combination it did not create.
func TestPutSettingsUnrelatedKeyNotBlockedByStaleDevBlocks(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"send_to_atlas":true,"dev_blocks":"bin"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()
	res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"show_breaks": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an unrelated PUT must not be refused, got %d", res.StatusCode)
	}
}

func TestPutSettingsInvalidDevBlocksValue(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()
	res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"dev_blocks": "hourly"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", res.StatusCode)
	}
}

func TestPutSettingsMalformedBody(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/settings", bytes.NewBufferString("{not json"))
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", res.StatusCode)
	}
}

// The restart trigger fires only when BOTH ?restart=1 is present AND the
// write actually required one — never on its own, in either direction.
func TestPutSettingsTriggersRestartOnlyWhenAskedAndNeeded(t *testing.T) {
	calls := make(chan struct{}, 8)
	restart := func() error {
		calls <- struct{}{}
		return nil
	}

	t.Run("restart=1 with a restart-requiring key: fires", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(restart)))
		defer srv.Close()
		res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings?restart=1", map[string]any{"send_to_atlas": false})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", res.StatusCode)
		}
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatal("restart was never triggered")
		}
	})

	t.Run("no restart param: does not fire", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(restart)))
		defer srv.Close()
		res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"send_to_atlas": false})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", res.StatusCode)
		}
		select {
		case <-calls:
			t.Fatal("restart must not fire without ?restart=1")
		case <-time.After(400 * time.Millisecond):
		}
	})

	t.Run("restart=1 but no restart-requiring key: does not fire", func(t *testing.T) {
		t.Setenv("KELD_HOME", t.TempDir())
		srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(restart)))
		defer srv.Close()
		res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings?restart=1", map[string]any{"show_breaks": true})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("want 200, got %d", res.StatusCode)
		}
		select {
		case <-calls:
			t.Fatal("restart must not fire when the write did not require one")
		case <-time.After(400 * time.Millisecond):
		}
	})
}

// T33: the attribution toggle writes the file, and the resolved value is what
// would gate KELD_TEXTEMBED=1 downstream (daemon.go: encoderNeeded :=
// attribOn || features.TextEmbedEnabled(), attribOn := attrib.Enabled(...)).
// Asserted via settings, per the task: not by spawning the sidecar.
func TestPutSettingsAttributionWritesFileAndWouldGateTheEncoder(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", SettingsRoute(nil)))
	defer srv.Close()

	before := settings.Load()
	if before.Attribution {
		t.Fatal("attribution must default to false")
	}

	res := doJSON(t, http.MethodPut, srv.URL+"/v1/settings", map[string]any{"attribution": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	var out map[string]any
	decodeInto(t, res, &out)
	// ⚠️ Attribution IS a restart key, and this test asserted the opposite
	// until 2026-09-05. The daemon resolves the gate once, at startAttributor,
	// so a PUT that reported no restart left the toggle a silent no-op: the
	// file said on, the page raised no restart bar, and nothing attributed.
	if out["restart_required"] != true {
		t.Fatalf("attribution is read once at startup, so it must require a restart, got %v", out)
	}

	after := settings.Load()
	if !after.Attribution {
		t.Fatal("attribution was not persisted")
	}
	// This is the exact predicate daemon.go's attribOn resolves through
	// (attrib.Enabled(set.Attribution)), which feeds encoderNeeded and, from
	// there, sidecarEnv's KELD_TEXTEMBED=1 — checked here without spawning
	// anything.
	if !attrib.Enabled(after.Attribution) {
		t.Fatal("attrib.Enabled must read the newly-written value as on")
	}
}
