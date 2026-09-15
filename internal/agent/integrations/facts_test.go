package integrations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// isolate points HOME and KELD_HOME at temp dirs so nothing in this package
// ever reads or writes the developer's real machine — the defect teleproxy's
// tests shipped with and which is worse than the one they check for.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KELD_HOME", filepath.Join(home, ".keld"))
	return home
}

func TestReadWiringAnswersFromDiskEveryCall(t *testing.T) {
	home := isolate(t)
	e, _ := Get("claude_code")

	// Nothing on disk at all.
	if w := ReadWiring(e, Deps{}); w.ConfigPresent {
		t.Fatal("ConfigPresent = true with no ~/.claude")
	}

	// The directory appears: installed, still not configured.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	w := ReadWiring(e, Deps{})
	if !w.ConfigPresent {
		t.Fatal("ConfigPresent = false after ~/.claude appeared — the same call, a changed disk")
	}
	if w.ConfigMatchesAdapter {
		t.Fatal("ConfigMatchesAdapter = true for a directory with no settings.json")
	}
}

// `wired` is the config on disk, read back — never the manifest's memory of
// having written it (AC-1).
func TestReadWiringIgnoresAManifestThatClaimsMoreThanTheDisk(t *testing.T) {
	home := isolate(t)
	e, _ := Get("claude_code")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := &config.Manifest{Tools: map[string]config.ToolManifest{
		"claude_code": {Name: "claude_code", Managed: map[string]any{"hook_substr": "keld __hook"}},
	}}
	if w := ReadWiring(e, Deps{Manifest: m}); w.ConfigMatchesAdapter {
		t.Fatal("ConfigMatchesAdapter = true from a manifest entry alone, with no config file on disk")
	}
}

func TestReadWiringSeesTheProxyAddressInTheConfig(t *testing.T) {
	home := isolate(t)
	e, _ := Get("claude_code")
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"http://127.0.0.1:14318"}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	w := ReadWiring(e, Deps{ProxyAddr: "127.0.0.1:14318"})
	if !w.PointsAtProxy {
		t.Fatal("PointsAtProxy = false for a config naming the proxy address")
	}
	if w.ConfigMtime.IsZero() {
		t.Fatal("ConfigMtime is zero for a config file that exists")
	}
	w = ReadWiring(e, Deps{ProxyAddr: "127.0.0.1:19999"})
	if w.PointsAtProxy {
		t.Fatal("PointsAtProxy = true for a config naming a different address")
	}
}

// ⚠️ THE GEMINI TRAP. The adapter is named "gemini"; the source id is
// "gemini_cli". A facts reader that looked the adapter up by id would get an
// error and report Gemini unwired forever, silently.
func TestReadWiringResolvesTheAdapterByAdapterNameNotByID(t *testing.T) {
	isolate(t)
	e, _ := Get("gemini_cli")
	if e.AdapterName != "gemini" {
		t.Fatalf("catalogue changed: gemini_cli's AdapterName is %q", e.AdapterName)
	}
	var asked []string
	d := Deps{Adapter: func(name string) (tools.Adapter, error) {
		asked = append(asked, name)
		return tools.Get(name)
	}}
	ReadWiring(e, d)
	if len(asked) != 1 || asked[0] != "gemini" {
		t.Fatalf("adapter looked up as %v, want [gemini] — tools.Get(e.ID) would fail", asked)
	}
}

// Until WS-B lands, hook trust answers (false, false): CANNOT TELL, not
// "untrusted". Reporting untrusted from an unwritten function would tell every
// Codex machine to go and approve hooks.
func TestCodexHookTrustDefaultsToUnknownNotUntrusted(t *testing.T) {
	trusted, known := CodexHooksTrusted([]byte("[hooks]\n"), "keld __hook")
	if trusted {
		t.Fatal("the stub claimed trust")
	}
	if known {
		t.Fatal("the stub claimed to KNOW trust is absent; the seam must answer known=false until WS-B lands")
	}
}

func TestLanesPersistAcrossAReloadAndOnlyMoveForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integrations.json")
	l := LoadLanesAt(path)
	if l.Last("codex", OriginHook) != nil {
		t.Fatal("a fresh record claimed a lane instant")
	}
	at := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	l.RecordPointer("codex", OriginHook, at)

	// A reload sees it — the record is an instant on disk, so a daemon restart
	// does not turn every configured tool into a machine that has seen nothing.
	again := LoadLanesAt(path)
	got := again.Last("codex", OriginHook)
	if got == nil || !got.Equal(at) {
		t.Fatalf("after reload Last = %v, want %v", got, at)
	}

	// A drained spool from yesterday must not make a lane look quieter.
	again.RecordPointer("codex", OriginHook, at.Add(-time.Hour))
	got = again.Last("codex", OriginHook)
	if got == nil || !got.Equal(at) {
		t.Fatalf("an older pointer moved the instant backwards to %v", got)
	}
	again.RecordPointer("codex", OriginHook, at.Add(time.Hour))
	if got := again.Last("codex", OriginHook); got == nil || !got.Equal(at.Add(time.Hour)) {
		t.Fatalf("a newer pointer did not move the instant forward: %v", got)
	}
}

// The emitter's memory (WS-C2) and the lane instants share one file, so
// neither writer can erase the other's half.
func TestLastStateSharesTheFileWithTheLanes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "integrations.json")
	l := LoadLanesAt(path)
	at := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	l.RecordPointer("codex", OriginWatcher, at)
	l.SetLastState("codex", string(Broken))

	again := LoadLanesAt(path)
	if again.LastState("codex") != string(Broken) {
		t.Fatalf("LastState = %q after reload", again.LastState("codex"))
	}
	if got := again.Last("codex", OriginWatcher); got == nil || !got.Equal(at) {
		t.Fatalf("writing last_state dropped the lane instant: %v", got)
	}
}

func TestReadLanesJoinsOnTheSourceID(t *testing.T) {
	l := LoadLanesAt(filepath.Join(t.TempDir(), "integrations.json"))
	at := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	l.RecordPointer("gemini_cli", OriginWatcher, at)

	e, _ := Get("gemini_cli")
	f := ReadLanes(e, Deps{Lanes: l, TelemetryForward: func(string) *time.Time { return nil }})
	if f.LastWatcherPointer == nil || !f.LastWatcherPointer.Equal(at) {
		t.Fatalf("LastWatcherPointer = %v, want %v", f.LastWatcherPointer, at)
	}
	if f.LastHookPointer != nil {
		t.Fatal("LastHookPointer is set for a tool that has no hook lane")
	}
	if f.RowsForRecentPointers != nil {
		t.Fatal("RowsForRecentPointers is non-nil with no way to ask — unknown must stay nil")
	}
}
