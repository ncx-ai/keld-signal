package tools

import (
	"fmt"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

// CodexApprovalTOML returns the `[hooks.state]` block Codex itself would write
// once a human approved keld's hooks in `/hooks` — for the hooks as the given
// config declares them TODAY.
//
// ⚠️ THIS EXISTS FOR TEST HARNESSES, AND IT CANNOT GRANT TRUST ON A REAL
// MACHINE. Writing this block into a real config.toml would be forging a
// human's approval; what it is for is the conformance chain and the Playwright
// journey, which need a machine in the approved state without a person driving
// a TUI. The daemon never calls it.
//
// It exists at all because the approval is no longer a constant. Until the
// hash was derivable, "approved" was `enabled = true` and a fixture could spell
// that out; the e2e journey did, with a placeholder `trusted_hash`. The day
// CodexHooksTrusted began COMPARING the hash, that fixture asserted a state
// Codex can never be in, and the journey failed — correctly. A generated block
// cannot drift that way, because it is built from the same walk and the same
// hash function the trust check reads.
//
// What it deliberately does NOT do is prove the hash is right: it shares
// codexHookHash with the code under test, so a wrong hash would agree with
// itself here. `TestCodexHookHashIsCodexsOwnHash` is the oracle for that,
// pinned against hashes Codex actually recorded on a real machine. This
// function's job is only to put a harness in the approved state.
//
// `source` is the path Codex would name in the state key. Only its BASENAME is
// load-bearing (codexInlineSource matches on `config.toml`), so a harness may
// pass the path the fixture pretends to be at.
func CodexApprovalTOML(configTOML []byte, hookCommandSubstr, source string) (string, error) {
	var doc map[string]any
	if err := toml.Unmarshal(configTOML, &doc); err != nil {
		return "", fmt.Errorf("codex approval: parse config: %w", err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return "", fmt.Errorf("codex approval: the config declares no hooks at all")
	}

	var b strings.Builder
	for _, event := range codexTrustEvents {
		h, found := codexHookIndex(hooks, event, hookCommandSubstr)
		if !found {
			// Refused rather than skipped. An approval block covering one of
			// the two events would leave CodexHooksTrusted answering "not
			// trusted" with nothing to say why, which is the harness failure
			// that is hardest to read.
			return "", fmt.Errorf("codex approval: no hook matching %q is registered for %s", hookCommandSubstr, event)
		}
		want, err := codexHookHash(event, h.matcher, h.handler)
		if err != nil {
			return "", fmt.Errorf("codex approval: %s: %w", event, err)
		}
		fmt.Fprintf(&b, "\n[hooks.state.%q]\nenabled = true\ntrusted_hash = %q\n",
			fmt.Sprintf("%s:%s:%d:%d", source, codexSnakeEvent(event), h.group, h.index), want)
	}
	return b.String(), nil
}
