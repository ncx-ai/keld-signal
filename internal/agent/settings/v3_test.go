package settings

import (
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestAtlasEnabledDefaultsOnAndEnvWins(t *testing.T) {
	t.Setenv(AtlasEnv, "")
	if !(Settings{}).AtlasEnabled() {
		t.Fatal("absent key must mean ON")
	}
	off := false
	if (Settings{SendToAtlas: &off}).AtlasEnabled() {
		t.Fatal("explicit false must mean OFF")
	}
	t.Setenv(AtlasEnv, "1")
	if !(Settings{SendToAtlas: &off}).AtlasEnabled() {
		t.Fatal("KELD_ATLAS=1 must override the file")
	}
	t.Setenv(AtlasEnv, "0")
	if (Settings{}).AtlasEnabled() {
		t.Fatal("KELD_ATLAS=0 must override the default")
	}
}

func TestDevBlocksRefusedWhileAtlasOn(t *testing.T) {
	t.Setenv(AtlasEnv, "")
	t.Setenv(DevBlocksEnv, "")
	mode, refused := (Settings{DevBlocks: "minute"}).DevBlocksMode()
	if mode != "" || !refused {
		t.Fatalf("Atlas on: want (\"\", refused), got (%q, %v)", mode, refused)
	}
	off := false
	mode, refused = (Settings{DevBlocks: "minute", SendToAtlas: &off}).DevBlocksMode()
	if mode != "minute" || refused {
		t.Fatalf("Atlas off: want (minute, ok), got (%q, %v)", mode, refused)
	}
	mode, refused = (Settings{DevBlocks: "hourly", SendToAtlas: &off}).DevBlocksMode()
	if mode != "" || refused {
		t.Fatalf("unknown mode must fall back to production silently, got (%q, %v)", mode, refused)
	}
	t.Setenv(DevBlocksEnv, "prompt")
	mode, _ = (Settings{SendToAtlas: &off}).DevBlocksMode()
	if mode != "prompt" {
		t.Fatalf("env must win over the file, got %q", mode)
	}
}

func TestGroupOff(t *testing.T) {
	s := Settings{GroupsOff: []string{"marketing", " Sales "}}
	for _, k := range []string{"marketing", "Marketing", "sales"} {
		if !s.GroupOff(k) {
			t.Fatalf("%q should be off", k)
		}
	}
	if s.GroupOff("development") {
		t.Fatal("development should be on")
	}
}

// writeHook pairs the test's KELD_HOME (TestMain isolates it) with endpoint.
func writeHook(t *testing.T, endpoint string) {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_CTX_ENDPOINT", "")
	t.Setenv("KELD_CTX_TOKEN", "")
	body := `{"endpoint":"` + endpoint + `","ingest_token":"tok"}`
	if err := os.WriteFile(paths.HookConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDevBlocksRefusedWhilePairedToARealAtlas(t *testing.T) {
	t.Setenv(AtlasEnv, "")
	t.Setenv(DevBlocksEnv, "")
	// Paired to production, then pointed at a local Atlas with
	// `keld signal env local`: blocks still publish to production.
	writeHook(t, "https://atlas.keld.co/v1")
	if err := WriteAtlasEnv("local"); err != nil {
		t.Fatal(err)
	}
	if mode, refused := (Settings{DevBlocks: "minute"}).DevBlocksMode(); mode != "" || !refused {
		t.Fatalf("paired to prod, atlas_env local: want (\"\", refused), got (%q, %v)", mode, refused)
	}
	// Paired to the local Atlas as well: nothing real to corrupt.
	writeHook(t, "http://localhost:8000")
	if err := WriteAtlasEnv("local"); err != nil {
		t.Fatal(err)
	}
	if mode, refused := (Settings{DevBlocks: "minute"}).DevBlocksMode(); mode != "minute" || refused {
		t.Fatalf("paired to localhost, atlas_env local: want (minute, ok), got (%q, %v)", mode, refused)
	}
	// A pairing named by the environment counts the same as hook.json.
	t.Setenv("KELD_CTX_ENDPOINT", "https://atlas.keld.co/v1")
	if mode, refused := (Settings{DevBlocks: "minute"}).DevBlocksMode(); mode != "" || !refused {
		t.Fatalf("KELD_CTX_ENDPOINT real: want refused, got (%q, %v)", mode, refused)
	}
}
