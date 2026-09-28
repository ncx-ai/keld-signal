package winproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ⚠️ THE SAME DEFECT SHIPPED IN FIVE SEPARATE PLACES, SO IT IS NOW A TEST.
//
// Every binary in this product is a CONSOLE program. When one is started by a
// parent that has no console of its own — a GUI installer, a -H windowsgui
// helper, a scheduled-task service — Windows ALLOCATES A NEW CONSOLE AND SHOWS
// IT. A black window appears on the person's screen, on a product whose entire
// Windows story is that no terminal ever appears.
//
// It was found and fixed independently in the wizard host, the service
// package's schtasks calls, the daemon's sidecar spawn, the installer's
// postinstall action, and the installer's two post-install steps. Every fix was
// correct; none generalised, because a bare exec.Command is invisible in review
// and only shows up as a flash on somebody's desktop.
//
// So: any spawn on a path that runs on Windows must be hidden. The list is
// explicit rather than a directory walk, because the right answer differs per
// file and "which processes can run on a Windows machine" is a judgement, not a
// pattern — a new entry here should be a deliberate decision.
func TestWindowsSpawnSitesHideTheirConsole(t *testing.T) {
	root := filepath.Join("..", "..")

	// file -> how the spawn is hidden there.
	hidden := map[string]string{
		"internal/cli/setup_guards.go":               "winproc.Hide(",
		"internal/agentcli/agentcli.go":              "winproc.Hide(",
		"internal/agent/hardware/hardware.go":        "winproc.Hide(",
		"internal/agent/update/update.go":            "winproc.Hide(",
		"internal/agent/daemon/reap_windows.go":      "winproc.Hide(",
		"internal/agent/daemon/procgroup_windows.go": "createNoWindow",
		"internal/agent/service/service_windows.go":  "command(",
		"cmd/keld-wizard-host/run.go":                "hideChildWindow(",
	}

	for rel, marker := range hidden {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v (if this file moved, move the guard with it rather than dropping it)", rel, err)
			continue
		}
		src := string(b)
		if !strings.Contains(src, "exec.Command") && !strings.Contains(src, "exec.CommandContext") {
			continue // no spawn here any more; nothing to hide
		}
		if !strings.Contains(src, marker) {
			t.Errorf("%s spawns a process but never %s — a console child of a console-less parent gets a visible window", rel, marker)
		}
	}
}
