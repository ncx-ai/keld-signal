package telemetry

import (
	"runtime"
	"strings"
	"testing"
)

// The recognizer must match the pinned command on EVERY platform. It used to be
// "keld __hook", which holds only while binPath ends in "keld" — on Windows it
// ends in "keld.exe", so the command reads `...\keld.exe __hook --source x` and
// the old substring was absent. Nothing tested a Windows-shaped path, so a
// Unix-only assumption survived in a comment that stated it outright.
func TestRecognizerMatchesEveryFormOfTheHookCommand(t *testing.T) {
	for _, tc := range []struct{ name, bin string }{
		{"bare (PATH-resolved)", ""},
		{"pinned unix", "/home/u/.local/bin/keld"},
		{"pinned macos pkg", "/usr/local/keld/keld"},
		{"pinned windows", `C:\Users\KingR\AppData\Local\Programs\keld\keld.exe`},
		{"pinned windows program files", `C:\Program Files\keld\keld.exe`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := HookCommand(tc.bin, "claude_code")
			if !strings.Contains(cmd, HookCommandSubstr) {
				t.Fatalf("recognizer %q does not match command %q — every 'is this hook mine?' "+
					"check would answer no on this platform", HookCommandSubstr, cmd)
			}
		})
	}
}

// Commands written by every previous version must still be recognised, or the
// fix would strand configs already on disk rather than repairing them.
func TestRecognizerStillMatchesCommandsWrittenByOlderVersions(t *testing.T) {
	for _, old := range []string{
		"keld __hook --source claude_code",
		"/usr/local/bin/keld __hook --source gemini",
		`C:\Users\KingR\AppData\Local\Programs\keld\keld.exe __hook --source claude_code`,
	} {
		if !strings.Contains(old, HookCommandSubstr) {
			t.Errorf("previously-written command %q is no longer recognised", old)
		}
	}
}

// The recognizer decides what gets DELETED from a user's config on uninstall, so
// it must not be so generic that it could match a non-keld hook. It carries
// keld's own flag, which no other tool emits.
func TestRecognizerIsSpecificEnoughToDeleteBy(t *testing.T) {
	if !strings.Contains(HookCommandSubstr, "__hook") {
		t.Fatalf("recognizer %q lost the distinguishing flag", HookCommandSubstr)
	}
	for _, foreign := range []string{
		"npm run hook",
		"/usr/bin/other-tool --source claude_code",
		"python3 hooks/on_prompt.py",
		"keldx __hooked --source x",
	} {
		if strings.Contains(foreign, HookCommandSubstr) {
			t.Errorf("recognizer matches a non-keld command %q — uninstall would delete it", foreign)
		}
	}
}

// A path separator in the recognizer would be re-broken by JSON escaping (the
// matchers compare against marshalled config, where `\` becomes `\\`) and would
// re-introduce the platform dependence this fix removes.
func TestRecognizerIsPathAndSeparatorFree(t *testing.T) {
	if strings.ContainsAny(HookCommandSubstr, `/\`) {
		t.Fatalf("recognizer %q contains a path separator; it is matched against JSON-escaped text "+
			"and must not depend on how a path is spelled", HookCommandSubstr)
	}
	_ = runtime.GOOS
}
