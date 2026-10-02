package daemon

import (
	"errors"
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/sidecarinstall"
)

// realHome is the developer's home directory, captured before TestMain replaces
// HOME, so a guard test can compare against it.
var realHome string

// TestMain isolates the whole package from the machine it runs on. Tests that
// set their own KELD_HOME or HOME (t.Setenv) still win for their duration.
//
//   - KELD_HOME → a temp dir, so nothing writes the developer's ~/.keld. See
//     TestTheSuiteNeverUsesTheRealKeldHome for what that cost before it existed.
//   - HOME → a temp dir as well. ⚠️ KELD_HOME alone was not enough, and this
//     cost a live machine its analysis service on 2026-10-02: the sidecar is
//     looked up in ~/.local/bin (wellKnownSidecarDirs), and a test that reaches
//     sidecarService found the INSTALLED one there and ran
//     reapStaleSidecars — `pkill -f keld-agent-sidecar`, which killed the
//     running Signal's sidecar on every `go test ./...`. And Run() starts the
//     shared engine manager, whose install target is ~/.local/bin
//     (sidecarinstall.DestDir), so every run also downloaded 316 MB and
//     replaced the installed engine with the latest STABLE one (a test binary
//     is version "dev", which resolves no tag), downgrading a release-candidate
//     machine from v3.3.0-rc.2 to v3.2.1.
//   - The shared engine manager never installs anything in a test, into any
//     home: a 316 MB download per run is not a test's business either way.
func TestMain(m *testing.M) {
	realHome, _ = os.UserHomeDir()
	keldHome, err := os.MkdirTemp("", "daemon-home")
	if err != nil {
		panic(err)
	}
	userHome, err := os.MkdirTemp("", "daemon-userhome")
	if err != nil {
		panic(err)
	}
	for k, v := range map[string]string{"KELD_HOME": keldHome, "HOME": userHome, "USERPROFILE": userHome, "KELD_SIDECAR_BIN": ""} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}
	currentEngineManager().install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		return sidecarinstall.Result{}, errors.New("engine installs are disabled in the daemon test suite")
	}
	code := m.Run()
	_ = os.RemoveAll(keldHome)
	_ = os.RemoveAll(userHome)
	os.Exit(code)
}
