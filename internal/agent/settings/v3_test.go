package settings

import "testing"

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

func TestWorkstreamOff(t *testing.T) {
	s := Settings{WorkstreamsOff: []string{"marketing", " Sales "}}
	for _, k := range []string{"marketing", "Marketing", "sales"} {
		if !s.WorkstreamOff(k) {
			t.Fatalf("%q should be off", k)
		}
	}
	if s.WorkstreamOff("development") {
		t.Fatal("development should be on")
	}
}
