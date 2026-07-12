package clientevents

import (
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestInstallIDStableAcrossCalls(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	id, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID() error: %v", err)
	}
	if id == "" {
		t.Fatal("InstallID() returned empty id")
	}

	id2, err := InstallID()
	if err != nil {
		t.Fatalf("second InstallID() error: %v", err)
	}
	if id2 != id {
		t.Fatalf("InstallID() not stable: first=%q second=%q", id, id2)
	}

	info, err := os.Stat(paths.InstallIDPath())
	if err != nil {
		t.Fatalf("expected install-id file to exist: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected install-id file mode 0600, got %o", mode)
	}
}

func TestInstallIDRegeneratesFromEmptyFile(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	// Simulate a torn write from a crash between file create and flush: the
	// install-id file exists but is empty. InstallID() must never return "" with
	// a nil error — it must regenerate a real id.
	if err := os.WriteFile(paths.InstallIDPath(), []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	id, err := InstallID()
	if err != nil {
		t.Fatalf("InstallID() error: %v", err)
	}
	if id == "" {
		t.Fatal("InstallID() returned empty id for an empty/whitespace file")
	}

	// And it must be stable on the next call now that a real id is persisted.
	id2, err := InstallID()
	if err != nil {
		t.Fatalf("second InstallID() error: %v", err)
	}
	if id2 != id {
		t.Fatalf("InstallID() not stable after regenerate: first=%q second=%q", id, id2)
	}
}
