package settings

import "testing"

// ⚠️ THE RULE IS ABOUT WHERE A BLOCK LANDS, NOT WHETHER PUBLISHING IS ON.
//
// A dev granularity MISLABELS real work — a minute-long block is a false
// statement about something a person actually did — so it must never reach the
// ORG'S NUMBERS. Those live behind the real Atlas. A loopback endpoint is a
// mock on the machine itself: there are no org numbers there to corrupt, and
// publishing is exactly what a conformance run must exercise.
//
// Refusing on `AtlasEnabled()` alone conflated the two, and the cost was
// concrete: the conformance chain could not produce a block at all, so the one
// signal Atlas actually renders went untested on every tool and every platform.
func TestDevBlocksAreAllowedWhenAtlasIsLoopback(t *testing.T) {
	t.Setenv(DevBlocksEnv, "minute")
	on := true
	s := Settings{SendToAtlas: &on}

	for _, base := range []string{
		"http://127.0.0.1:8123",
		"http://localhost:9000/v1",
		"http://[::1]:7777",
	} {
		t.Setenv("KELD_API_URL", base)
		mode, refused := s.DevBlocksMode()
		if refused || mode != "minute" {
			t.Errorf("%s: mode=%q refused=%v — a mock on this machine has no org numbers to protect",
				base, mode, refused)
		}
	}
}

// The rule it must NOT weaken: a real Atlas still refuses, whatever the file or
// env says.
func TestDevBlocksStillRefusedAgainstARealAtlas(t *testing.T) {
	t.Setenv(DevBlocksEnv, "minute")
	on := true
	s := Settings{SendToAtlas: &on}

	for _, base := range []string{
		"https://atlas.keld.co",
		"https://atlas.dev.keld.co/v1",
		// ⚠️ A hostname that merely CONTAINS "localhost" is not loopback.
		"https://localhost.evil.example.com",
	} {
		t.Setenv("KELD_API_URL", base)
		if mode, refused := s.DevBlocksMode(); !refused || mode != "" {
			t.Errorf("%s: mode=%q refused=%v — a minute-long block must never reach an org", base, mode, refused)
		}
	}
}
