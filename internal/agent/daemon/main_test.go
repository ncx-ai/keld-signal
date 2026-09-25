package daemon

import (
	"os"
	"testing"
)

// TestMain points KELD_HOME at a throwaway directory for the whole package, so
// a test that builds the real wiring without isolating itself cannot touch the
// developer's ~/.keld. Tests that set their own KELD_HOME (t.Setenv) still win
// for their duration. See TestTheSuiteNeverUsesTheRealKeldHome for what this
// cost before it existed.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "daemon-home")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("KELD_HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
