package telemetry

import "testing"

// TestHookCommandNeedsRepairRecognisesWhatQuoteBinWouldHaveFixed.
//
// ⚠️ ONE RULE, TWO USERS. HookCommand QUOTES a binary that cannot survive
// being read bare; this answers whether a command ALREADY ON DISK was written
// before that rule existed. They must agree, or the detector either misses a
// broken machine or rewrites a healthy one forever.
func TestHookCommandNeedsRepairRecognisesWhatQuoteBinWouldHaveFixed(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		{"the Windows bug: bare backslash path", `C:\Users\x\keld.exe __hook --source claude_code`, true},
		{"bare path with a space", `/opt/my keld/keld __hook --source codex`, true},
		{"already repaired", `"C:\Users\x\keld.exe" __hook --source claude_code`, false},
		{"plain unix path", `/usr/local/keld/keld __hook --source gemini`, false},
		{"bare fallback", `keld __hook --source claude_code`, false},
		{"not keld's hook at all", `/usr/bin/somethingelse --flag`, false},
	}
	for _, c := range cases {
		if got := HookCommandNeedsRepair(c.cmd); got != c.want {
			t.Errorf("%s: HookCommandNeedsRepair(%q) = %v, want %v", c.name, c.cmd, got, c.want)
		}
	}
}

// TestRepairIsIdempotent — what HookCommand writes must never be seen as
// needing repair, or the detector rewrites the same config every minute
// forever. Checked against the writer itself rather than against a literal.
func TestRepairIsIdempotent(t *testing.T) {
	for _, bin := range []string{"", "/usr/local/keld/keld", `C:\Program Files\keld\keld.exe`, `/opt/my keld/keld`} {
		cmd := HookCommand(bin, "claude_code")
		if HookCommandNeedsRepair(cmd) {
			t.Errorf("HookCommand(%q) produced %q, which the detector would try to repair forever", bin, cmd)
		}
	}
}
