package telemetry

import (
	"strings"
	"testing"
)

// TestHookCommandQuotesAWindowsPath.
//
// ⚠️ MEASURED ON A REAL RUNNER, NOT REASONED. The Windows conformance leg
// passed transcript, store_rows and telemetry and failed ONLY the enrichment
// lane, and a control hook added beside keld's own DID fire — so Claude Code
// runs hooks there, and keld's own command is what does not work.
//
// The control is the natural experiment: it was written QUOTED
// (`"C:\...\probe.cmd" "C:\...\marker"`) and fired; keld's was written BARE
// (`D:\a\...\keld.exe __hook --source claude_code`) and did not. A bare
// Windows path is a string of backslash escapes to anything shell-like, and
// what survives is not a path to anything.
func TestHookCommandQuotesAWindowsPath(t *testing.T) {
	got := HookCommand(`C:\Users\x\AppData\Local\Programs\keld\keld.exe`, "claude_code")
	want := `"C:\Users\x\AppData\Local\Programs\keld\keld.exe" __hook --source claude_code`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestHookCommandQuotesAPathWithSpaces: "C:\Program Files\..." is the default
// install location for most Windows software, and an unquoted space splits the
// command in two whatever the shell.
func TestHookCommandQuotesAPathWithSpaces(t *testing.T) {
	got := HookCommand(`/opt/my keld/keld`, "codex")
	if !strings.HasPrefix(got, `"/opt/my keld/keld" `) {
		t.Errorf("a path with a space must be quoted, got %s", got)
	}
}

// TestHookCommandLeavesAPlainUnixPathALONE. The blast radius is deliberately
// tiny: every machine already running has an unquoted command, setup rewrites
// it on the next run, and a needless change to millions of working Unix
// configs buys nothing. Quote only what cannot work bare.
func TestHookCommandLeavesAPlainUnixPathAlone(t *testing.T) {
	if got := HookCommand("/usr/local/keld/keld", "gemini"); got != "/usr/local/keld/keld __hook --source gemini" {
		t.Errorf("a plain unix path must be unchanged, got %s", got)
	}
	if got := HookCommand("", "claude_code"); got != "keld __hook --source claude_code" {
		t.Errorf("the bare fallback must be unchanged, got %s", got)
	}
}

// TestQuotingKeepsTheRecognizerWorking — HookCommandSubstr is what teardown
// and trust detection match on. If quoting moved it, every existing config
// would become unrecognisable and uninstall would silently leave hooks behind.
func TestQuotingKeepsTheRecognizerWorking(t *testing.T) {
	for _, bin := range []string{"", "/usr/local/keld/keld", `C:\x y\keld.exe`} {
		cmd := HookCommand(bin, "claude_code")
		if !strings.Contains(cmd, HookCommandSubstr) {
			t.Errorf("recognizer %q does not match %q", HookCommandSubstr, cmd)
		}
	}
}
