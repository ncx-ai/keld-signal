package tools

import (
	"fmt"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// ⚠️ CODEX RUNS NO HOOK A HUMAN HAS NOT APPROVED, AND SAYS NOTHING WHEN IT
// SKIPS ONE.
//
// Measured on codex-cli 0.153.4, 2026-09-15: the same `codex exec` run with
// three inline hooks registered fires all three under
// `--dangerously-bypass-hook-trust` and fires NONE without it — no warning, no
// log line, no entry written anywhere. So a keld install can be complete,
// correct and entirely inert, which is what an integration state must be able
// to say out loud.
//
// The approval is recorded in Codex's own `config.toml` as
//
//	[hooks.state."<source>:<event>:<i>:<j>"]
//	enabled = true
//	trusted_hash = "sha256:…"
//
// where `<source>` is the file the hook was declared in (`config.toml` for the
// inline hooks keld writes; another tool's `hooks.json` for its own), `<event>`
// is the snake_case event name, and `i`/`j` are the hook's position in the
// `[[hooks.<Event>]]` array and in that entry's inner `hooks` list.
//
// ⚠️ BOTH FIELDS ARE READ, AND READING ONLY `enabled` WAS THE DEFECT.
// `trusted_hash` is Codex's hash over its own NORMALISED HOOK IDENTITY — not
// over the command string, which is why six guesses at it failed and why the
// six hooks on the reference machine carry six different hashes for one
// identical command. Codex re-marks a CHANGED hook for review, so an approval
// recorded against the old command is still sitting there, still saying
// `enabled = true`, while Codex silently declines to run the hook. Deciding on
// `enabled` alone reported that machine healthy and capturing nothing.
//
// So the answer is Codex's own: the hook runs iff its state entry is not
// explicitly disabled AND the recorded hash equals the hash of the hook as it
// stands in the file right now. `codex_hash.go` computes that hash and carries
// the rule; trust is still never remembered across a keld release — it is
// recomputed from the config on every pass, and a release that edits the hook
// command returns the machine to unapproved by arithmetic rather than by hope.

// codexTrustEvents are the events whose approval the capture lane actually
// needs. `SessionStart` is deliberately NOT here: it names no prompt, so a
// machine that approved only the two below still captures everything.
var codexTrustEvents = []string{"UserPromptSubmit", "Stop"}

// CodexHooksTrusted reports whether Codex has been told it may run keld's
// hooks, reading the `[hooks.state]` tables out of the config text itself.
//
// `trusted` is true only when EVERY event in codexTrustEvents has a state
// entry naming keld's own hook, at its own index, for an INLINE source — the
// `config.toml` keld writes into — which is not disabled and whose
// `trusted_hash` matches the hook as it is written TODAY. Another tool's
// approvals, an explicit `enabled = false`, a partial approval, an approval
// with no hash, and an approval whose hash predates an edit to the command all
// read as untrusted, because each of them leaves Codex silently skipping at
// least one of keld's hooks.
//
// `known` is false when the config carries no `hooks.state` section at all,
// which is what a Codex too old to have the trust mechanism writes — and also
// what a brand-new one writes before the first approval of anything. The
// caller resolves that from `session_meta.cli_version`; answering "untrusted"
// here would send every such machine to a `/hooks` screen its Codex may not
// have. `trusted` is always false when `known` is false: this function never
// vouches for something it could not read.
func CodexHooksTrusted(configTOML []byte, hookCommandSubstr string) (trusted bool, known bool) {
	var doc map[string]any
	if err := toml.Unmarshal(configTOML, &doc); err != nil {
		return false, false
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		return false, false
	}
	state, _ := hooks["state"].(map[string]any)
	if state == nil {
		return false, false
	}

	for _, event := range codexTrustEvents {
		h, found := codexHookIndex(hooks, event, hookCommandSubstr)
		if !found {
			// keld's hook is not registered for this event at all, so there is
			// nothing for a human to have approved. Known, and not trusted.
			return false, true
		}
		want, err := codexHookHash(event, h.matcher, h.handler)
		if err != nil {
			// We could not model what Codex would hash, so we have nothing to
			// compare its record against. Known — the table is right there —
			// and NOT trusted: an uncomputable hash is an unmade comparison.
			return false, true
		}
		if !codexStateApproves(state, event, h.group, h.index, want) {
			return false, true
		}
	}
	return true, true
}

// codexHook is keld's hook as Codex sees it: where it sits in the event's
// array (the two numbers its state key is built from), the matcher of the
// group it sits in, and the handler table itself — everything the hash needs.
type codexHook struct {
	group   int
	index   int
	matcher *string
	handler map[string]any
}

// codexHookIndex finds keld's hook inside `[[hooks.<Event>]]`. It returns the
// two indices Codex's state key is built from — the position of the entry in
// the event's array, and of the command inside that entry's `hooks` list — and
// the definition itself, because the trust answer now turns on WHAT the hook
// is and not only on where it sits.
func codexHookIndex(hooks map[string]any, event, commandSubstr string) (codexHook, bool) {
	entries, _ := hooks[event].([]any)
	for ei, entry := range entries {
		e, _ := entry.(map[string]any)
		var matcher *string
		if m, ok := e["matcher"].(string); ok {
			matcher = &m
		}
		inner, _ := e["hooks"].([]any)
		for hi, h := range inner {
			hm, _ := h.(map[string]any)
			cmd, _ := hm["command"].(string)
			if cmd != "" && strings.Contains(cmd, commandSubstr) {
				return codexHook{group: ei, index: hi, matcher: matcher, handler: hm}, true
			}
		}
	}
	return codexHook{}, false
}

// codexStateApproves reports whether an inline-source state entry lets Codex
// run keld's hook for this event at these indices, against the hook as it
// stands now.
//
// The two conditions are Codex's own (`hook_enabled` + `hook_trust_status`):
// an ABSENT `enabled` is enabled — only an explicit `false` is a human saying
// no — and the recorded hash must equal `wantHash`. An entry with no
// `trusted_hash` at all is what Codex calls Untrusted: it has nothing to
// compare against, and neither have we.
func codexStateApproves(state map[string]any, event string, i, j int, wantHash string) bool {
	suffix := fmt.Sprintf(":%s:%d:%d", codexSnakeEvent(event), i, j)
	for key, v := range state {
		if !strings.HasSuffix(key, suffix) {
			continue
		}
		source := strings.TrimSuffix(key, suffix)
		if !codexInlineSource(source) {
			continue // somebody else's hook file, approved for their hook
		}
		entry, _ := v.(map[string]any)
		if enabled, ok := entry["enabled"].(bool); ok && !enabled {
			continue
		}
		if recorded, ok := entry["trusted_hash"].(string); ok && recorded == wantHash {
			return true
		}
	}
	return false
}

// codexInlineSource reports whether a state key's source names the config file
// keld's hooks are declared in. Matched by BASENAME rather than by an absolute
// path, because this function is given the config's text and not its location
// — and because CODEX_HOME moves it.
func codexInlineSource(source string) bool {
	return strings.HasSuffix(source, "config.toml")
}

// codexSnakeEvent converts Codex's TOML event name (UserPromptSubmit) to the
// snake_case form its state keys use (user_prompt_submit). Codex's own event
// list is the closed set; a name outside it simply never matches a key.
func codexSnakeEvent(event string) string {
	var b strings.Builder
	for i, r := range event {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// codexTrustEventsAreRegistered is a compile-time-ish guard used by the tests:
// every event whose approval is required must actually be one keld registers,
// or the check asks for an approval that can never be given.
func codexTrustEventsAreRegistered() bool {
	for _, want := range codexTrustEvents {
		found := false
		for _, ev := range telemetry.CodexHookEvents {
			if ev == want {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}
