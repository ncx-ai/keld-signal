package integrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
)

// writeSources puts a per-source telemetry record on disk under an isolated
// KELD_HOME.
func writeSources(t *testing.T, sources map[string]time.Time) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"sources": sources})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(teleproxy.SourcesPath(), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// One tool's telemetry must not vouch for another's otel lane. It did, and the
// cost was a false `broken`: the borrowed instant is the ACTIVE half, and a
// tool nobody has used has a silent watcher for the other half.
func TestOneToolsTelemetryDoesNotVouchForAnother(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(teleproxy.SourcesPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSources(t, map[string]time.Time{"claude_code": time.Now()})

	if perSourceForward("claude_code") == nil {
		t.Error("the tool that actually forwarded reads silent")
	}
	if got := perSourceForward("gemini_cli"); got != nil {
		t.Errorf("gemini_cli borrowed another tool's forward: %v", got)
	}
}

// An EMPTY record is "not tracked yet", never "nothing has ever arrived": a
// machine that upgrades into this code has forwards and no per-source history,
// and reading that as silence would call every configured tool broken on the
// day it shipped.
func TestAnEmptyPerSourceRecordFallsBackRatherThanReportingSilence(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(teleproxy.SourcesPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	// No per-source file at all, but the machine-wide record says telemetry
	// has been forwarded.
	if err := os.WriteFile(teleproxy.StatePath(), []byte(`{"last_forward":"2026-09-18T10:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if perSourceForward("gemini_cli") == nil {
		t.Error("an untracked machine reported silence instead of falling back to the machine-wide instant")
	}
}

// Telemetry arriving under a service name nobody mapped is recorded as
// UNATTRIBUTED, and a tool with no entry of its own must not be called silent
// on the strength of that naming gap — it would read `broken` for a reason
// that is about teleproxy's name table, not about the tool.
func TestUnattributedTrafficKeepsTheCoarseAnswer(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(teleproxy.SourcesPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(teleproxy.StatePath(), []byte(`{"last_forward":"2026-09-18T10:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeSources(t, map[string]time.Time{
		"claude_code":           time.Now(),
		teleproxy.UnknownSource: time.Now(),
	})
	if perSourceForward("gemini_cli") == nil {
		t.Error("a tool with no entry was called silent while unattributed telemetry was on record")
	}
}
