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
