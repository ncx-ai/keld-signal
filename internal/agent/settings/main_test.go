package settings

import (
	"os"
	"testing"
)

// TestMain points KELD_HOME at an empty directory so no test here reads the
// developer's real ~/.keld. DevBlocksMode resolves the Atlas address through
// paths.APIBase, which reads atlas_env from agent-config.json: on a machine
// switched to `keld signal env local`, TestDevBlocksRefusedWhileAtlasOn saw a
// loopback Atlas and passed the refusal it exists to check.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "keld-settings-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("KELD_HOME", home)
	os.Unsetenv("KELD_API_URL")
	os.Unsetenv("KELD_ATLAS_WEB_URL")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
