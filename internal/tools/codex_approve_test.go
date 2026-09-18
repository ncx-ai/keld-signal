package tools

import (
	"strings"
	"testing"
)

// codexApproveFixture is a config shaped like the one keld writes: one entry
// per event, one command inside it.
// codexApproveFixture is the shape keld ACTUALLY writes, copied from a real
// config.toml: `command` is a STRING inside an inline `hooks = [ … ]` array,
// not a nested [[hooks.<Event>.hooks]] table with an argv list. A fixture that
// does not resemble production is how the prompt-id seam defect survived, so
// this one is taken from the file on disk.
const codexApproveFixture = `
[[hooks.UserPromptSubmit]]
hooks = [ { type = "command", command = 'keld __hook --source codex' } ]

[[hooks.Stop]]
hooks = [ { type = "command", command = 'keld __hook --source codex' } ]
`

// TestCodexApprovalTOMLSatisfiesTheRealTrustCheck is the whole point of the
// generator: what it emits must be what CodexHooksTrusted accepts.
//
// ⚠️ THIS IS WHY THE E2E FIXTURE COULD NOT STAY HAND-WRITTEN. It carried
// `trusted_hash = "sha256:1111…"`, which was true while `enabled = true` alone
// meant trusted and became false the day the hash was actually compared — the
// test then asserted a machine state Codex can no longer be in.
func TestCodexApprovalTOMLSatisfiesTheRealTrustCheck(t *testing.T) {
	block, err := CodexApprovalTOML([]byte(codexApproveFixture), "keld", "/home/e2e/.codex/config.toml")
	if err != nil {
		t.Fatalf("CodexApprovalTOML: %v", err)
	}
	if trusted, known := CodexHooksTrusted([]byte(codexApproveFixture+block), "keld"); !trusted || !known {
		t.Fatalf("generated approval did not satisfy CodexHooksTrusted: trusted=%v known=%v\nblock:\n%s",
			trusted, known, block)
	}
}

// TestCodexApprovalTOMLIsSpecificToTheCommandItApproved: an approval minted for
// today's hook must NOT vouch for an edited one, or the generator would hand
// every test a permanent yes and the changed-hook clause would be untestable.
func TestCodexApprovalTOMLIsSpecificToTheCommandItApproved(t *testing.T) {
	block, err := CodexApprovalTOML([]byte(codexApproveFixture), "keld", "/home/e2e/.codex/config.toml")
	if err != nil {
		t.Fatalf("CodexApprovalTOML: %v", err)
	}
	edited := strings.Replace(codexApproveFixture, "--source codex", "--source codex --v2", 1)
	if trusted, known := CodexHooksTrusted([]byte(edited+block), "keld"); trusted {
		t.Fatalf("an approval for the OLD command vouched for an edited one (known=%v)", known)
	}
}

// TestCodexApprovalTOMLRefusesWhenTheHookIsAbsent: nothing to approve is an
// error, never an empty block that would read as "approved nothing".
func TestCodexApprovalTOMLRefusesWhenTheHookIsAbsent(t *testing.T) {
	if _, err := CodexApprovalTOML([]byte("[[hooks.Stop]]\n"), "keld", "/x/config.toml"); err == nil {
		t.Fatal("expected an error when keld's hook is registered for no event")
	}
}
