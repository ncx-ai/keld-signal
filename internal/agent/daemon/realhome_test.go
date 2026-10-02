package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/sidecarinstall"
)

// ⚠️ **THIS SUITE USED TO WRITE THE DEVELOPER'S REAL ~/.keld.** Two route tests
// build the real v3 wiring (newV3) with no KELD_HOME of their own, and newV3
// migrates state/projects.json to projects.json on the way up. So `go test
// ./...` on a working machine performed that migration on its real state: it
// wrote an empty projects.json beside a live projects.json, and the next
// build of the daemon read the empty one — every project the person had made
// since vanished from the page. Found on a developer Mac on 2026-09-25. The
// teleproxy suite hit the same class of bug and was fixed the same way
// (internal/agent/teleproxy/main_test.go). A test that mutates the machine it
// runs on is a worse defect than the one it checks for.
func TestTheSuiteNeverUsesTheRealKeldHome(t *testing.T) {
	// realHome, not os.UserHomeDir(): TestMain has replaced HOME by now.
	real := realHome
	if real == "" {
		t.Skip("no home directory to compare against")
	}
	if home, _ := os.UserHomeDir(); home == real {
		t.Fatalf("HOME is still the real home %s: a test can find and pkill the installed sidecar, and install over it (TestMain)", real)
	}
	if engineInstallDir(t) == filepath.Join(real, ".local", "bin") {
		t.Fatal("the engine install target is the real ~/.local/bin (TestMain)")
	}
	state := paths.StateDir()
	if strings.HasPrefix(filepath.Clean(state), filepath.Join(real, ".keld")) {
		t.Fatalf("state dir %s is the real ~/.keld — the suite must isolate KELD_HOME (TestMain)", state)
	}
}

func engineInstallDir(t *testing.T) string {
	t.Helper()
	d, err := sidecarinstall.DestDir()
	if err != nil {
		t.Fatal(err)
	}
	return d
}
