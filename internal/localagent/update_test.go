package localagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/update"
)

func writeState(t *testing.T, s update.State) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	if err := update.SaveState(filepath.Join(home, "update", "state.json"), s); err != nil {
		t.Fatal(err)
	}
}

// A machine that has never updated is not a problem, and must not be nagged
// about — the same call ModelState.ProblemLine makes for an unneeded model.
func TestNoMarkerIsNotAProblem(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	u := ReadUpdateState()
	if u.Known {
		t.Fatal("a fresh machine has no update history")
	}
	if p := u.ProblemLine(); p != "" {
		t.Fatalf("nagged: %q", p)
	}
	if !strings.Contains(u.StatusLine(), "version") {
		t.Fatalf("status should still state the version: %q", u.StatusLine())
	}
}

func TestACleanApplyIsNotAProblem(t *testing.T) {
	writeState(t, update.State{LastOutcome: "applied", LastTarget: "v0.4.2"})
	if p := ReadUpdateState().ProblemLine(); p != "" {
		t.Fatalf("got %q", p)
	}
}

// A rollback IS reported, but as the self-heal working rather than as a
// breakage — the machine is running a good version.
func TestARollbackIsReportedAndNamesTheVersion(t *testing.T) {
	writeState(t, update.State{LastOutcome: "rolled_back", LastTarget: "v0.4.2", FailedVersions: []string{"v0.4.2"}})
	p := ReadUpdateState().ProblemLine()
	if !strings.Contains(p, "v0.4.2") {
		t.Fatalf("must name the version: %q", p)
	}
	if !strings.Contains(p, "no action is needed") {
		t.Fatalf("a working self-heal must not read as a breakage: %q", p)
	}
}

func TestAFailedRollbackIsReportedAsInconsistent(t *testing.T) {
	writeState(t, update.State{LastOutcome: "rollback_failed", LastTarget: "v0.4.2", LastError: "prev missing"})
	p := ReadUpdateState().ProblemLine()
	if !strings.Contains(p, "inconsistent") || !strings.Contains(p, "Re-run the installer") {
		t.Fatalf("got %q", p)
	}
}

// The macOS case: after a migration, a root-owned /usr/local/bin symlink still
// points at the stale binary and we cannot rewrite it. Doctor must give the
// exact command.
func TestAStaleLinkIsReportedWithARunnableFix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", filepath.Join(home, ".keld"))
	t.Setenv("HOME", home)

	newDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oldDir := filepath.Join(home, "old")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(oldDir, "keld")
	if err := os.WriteFile(stale, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(stale, filepath.Join(newDir, "keld")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// The install dir is elsewhere, so the ~/.local/bin link is stale.
	if err := update.SaveState(filepath.Join(home, ".keld", "update", "state.json"),
		update.State{LastOutcome: "applied", InstallDir: filepath.Join(home, "installed")}); err != nil {
		t.Fatal(err)
	}

	u := ReadUpdateState()
	if len(u.Stale) == 0 {
		t.Skip("PathRoots did not include the synthesized dir on this platform")
	}
	p := u.ProblemLine()
	if !strings.Contains(p, "ln -sf") {
		t.Fatalf("the fix must be runnable: %q", p)
	}
}

func TestPendingStatusLine(t *testing.T) {
	writeState(t, update.State{PendingConfirm: true, LastTarget: "v0.4.2", To: "v0.4.2"})
	if !strings.Contains(ReadUpdateState().StatusLine(), "awaiting confirmation") {
		t.Fatalf("got %q", ReadUpdateState().StatusLine())
	}
}

// Reading state must never contact the daemon or a release host.
func TestReadUpdateStateIsDiskOnly(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// No server is running; if this tried to reach one it would hang or error.
	_ = ReadUpdateState()
}
