package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// ⚠️ CODEX'S `trusted_hash` IS DERIVABLE, AND THIS FILE DERIVES IT.
//
// The comment this replaces said the hash "is NOT derivable from the command
// string — six encodings were tried", and both halves were true: it is not a
// function of the command string, and six guesses at one failed. The input is
// a NORMALISED HOOK IDENTITY that carries the event name as well, which is why
// the six hooks on this machine — whose commands are byte-identical — carry six
// different hashes. Guessing was never going to find it; reading Codex's source
// did, and a real input/output pair confirms it
// (`TestCodexHookHashIsCodexsOwnHash`).
//
// The rule, from openai/codex `codex-rs/hooks/src/engine/discovery.rs`
// (`hook_hash`) and `codex-rs/config/src/fingerprint.rs` (`version_for_toml`),
// read at codex-cli 0.153.4:
//
//  1. Build `{ event_name, matcher, hooks: [<one normalised handler>] }` —
//     the hook's matcher group with its handler list replaced by the single
//     handler being hashed, plus the snake_case event name.
//  2. Render it as TOML, then as JSON, then CANONICALISE: every object's keys
//     sorted, no whitespace. A field that is None is absent, not null.
//  3. `"sha256:" + hex(sha256(that JSON))`.
//
// Handler normalisation is where the non-obvious part lives, and every step of
// it changes the hash: the timeout DEFAULT is baked in (600s, or 1s capped at
// 3s for SessionEnd/Interrupt), `commandWindows` is dropped after being chosen
// between on Windows, `additionalContextLimit` is dropped for events that
// cannot emit additionalContext and dropped again when it equals Codex's own
// default of 2500, and `async` is always present. So an identical command with
// an explicit `timeout = 600` hashes the same as one with no timeout at all,
// and that is deliberate on Codex's side: it hashes what the hook DOES, not how
// it was written.
//
// ⚠️ THIS TRACKS A PRIVATE RULE IN SOMEBODY ELSE'S BINARY. If Codex changes it,
// our computed hash stops matching the recorded one and every Codex machine
// reads `approval_required` — wrong, but wrong in the safe direction: it asks
// for an approval that is already there rather than vouching for a hook Codex
// is silently skipping. `TestCodexHookHashIsCodexsOwnHash` is what turns that
// drift into a failing test instead of a fleet-wide false alarm; re-capture the
// pair from a real machine when Codex's hook trust changes.

// codexDefaultHookTimeoutSec and the SessionEnd/Interrupt pair mirror
// `normalize_command_hook`. They are part of the hash, not just of the runtime.
const (
	codexDefaultHookTimeoutSec      int64 = 600
	codexSessionEndDefaultTimeout   int64 = 1
	codexSessionEndMaxTimeoutSec    int64 = 3
	codexDefaultOutputTokenLimit    int64 = 2500
	codexHookHandlerTypeCommand           = "command"
	codexTrustedHashPrefix                = "sha256:"
	codexHookHashUnsupportedHandler       = "hook handler is not a command hook"
)

// codexShortTimeoutEvents are the two events Codex gives a one-second default
// and a three-second cap. keld registers neither; the rule is here so the hash
// is the general one rather than one that happens to fit our own hooks.
var codexShortTimeoutEvents = map[string]bool{"SessionEnd": true, "Interrupt": true}

// codexAdditionalContextEvents are the events whose hooks may emit
// `additionalContext`. For any other event Codex DROPS the limit before
// hashing, so two hooks that differ only there are the same hook to it.
var codexAdditionalContextEvents = map[string]bool{
	"PreToolUse": true, "PostToolUse": true, "SessionStart": true,
	"UserPromptSubmit": true, "SubagentStart": true,
}

// codexHookHash computes the `trusted_hash` Codex records for one hook
// handler, given the event it is registered for, its matcher group's matcher,
// and the handler table exactly as it appears in config.toml or hooks.json.
//
// An error means we could not model what Codex would hash — an MCP-tool
// handler, a missing command, a value of a shape TOML cannot have produced.
// Callers treat that as UNTRUSTED, never as trusted: a hash we cannot compute
// is a comparison we cannot make.
func codexHookHash(event string, matcher *string, handler map[string]any) (string, error) {
	normalized, err := codexNormalizeHandler(event, handler)
	if err != nil {
		return "", err
	}
	identity := map[string]any{
		"event_name": codexSnakeEvent(event),
		"hooks":      []any{normalized},
	}
	// Codex takes the matcher from `matcher_pattern_for_event`, which returns
	// None for the three events that cannot carry one — so a matcher written
	// against UserPromptSubmit, Stop or Interrupt is not in the hash even
	// though it is in the file.
	if m := codexMatcherForEvent(event, matcher); m != nil {
		identity["matcher"] = *m
	}
	var b strings.Builder
	if err := codexCanonicalJSON(&b, identity); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(b.String()))
	return codexTrustedHashPrefix + hex.EncodeToString(sum[:]), nil
}

// codexMatcherForEvent mirrors `matcher_pattern_for_event`.
func codexMatcherForEvent(event string, matcher *string) *string {
	switch event {
	case "UserPromptSubmit", "Stop", "Interrupt":
		return nil
	}
	return matcher
}

// codexNormalizeHandler mirrors the `HookHandlerConfig::Command` arm of
// `append_matcher_groups`, which is what actually gets hashed — not the table
// as written.
func codexNormalizeHandler(event string, handler map[string]any) (map[string]any, error) {
	kind, _ := handler["type"].(string)
	if kind != codexHookHandlerTypeCommand {
		return nil, fmt.Errorf("%s: %q", codexHookHashUnsupportedHandler, kind)
	}
	command, _ := handler["command"].(string)
	// Windows picks `commandWindows` when present — and Codex hashes the
	// CHOSEN command with `command_windows` then set to None, so the same file
	// hashes differently on the two platforms. Reading the config of the
	// machine we are running on is the only reading available to us anyway.
	if runtime.GOOS == "windows" {
		if w, ok := codexHandlerString(handler, "commandWindows", "command_windows"); ok {
			command = w
		}
	}
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("hook command is empty")
	}

	timeout, err := codexNormalizeTimeout(event, handler)
	if err != nil {
		return nil, err
	}

	async := false
	if v, ok := handler["async"]; ok {
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("hook async is %T, want bool", v)
		}
		async = b
	}

	normalized := map[string]any{
		"type":    codexHookHandlerTypeCommand,
		"command": command,
		"timeout": timeout,
		"async":   async,
	}
	if s, ok := codexHandlerString(handler, "statusMessage"); ok {
		normalized["statusMessage"] = s
	}
	// Dropped for an event that cannot emit additionalContext, and dropped
	// again when it merely restates Codex's own default.
	if codexAdditionalContextEvents[event] {
		if v, ok := handler["additionalContextLimit"]; ok {
			limit, err := codexInt(v)
			if err != nil {
				return nil, fmt.Errorf("additionalContextLimit: %w", err)
			}
			if limit != codexDefaultOutputTokenLimit {
				normalized["additionalContextLimit"] = limit
			}
		}
	}
	return normalized, nil
}

// codexNormalizeTimeout mirrors `normalize_command_hook`, whose DEFAULTS are
// in the hash: a hook with no `timeout` hashes as one with `timeout = 600`.
func codexNormalizeTimeout(event string, handler map[string]any) (int64, error) {
	var (
		given int64
		have  bool
	)
	if v, ok := handler["timeout"]; ok {
		n, err := codexInt(v)
		if err != nil {
			return 0, fmt.Errorf("timeout: %w", err)
		}
		given, have = n, true
	}
	if codexShortTimeoutEvents[event] {
		if !have {
			return codexSessionEndDefaultTimeout, nil
		}
		if given < 1 {
			return 1, nil
		}
		if given > codexSessionEndMaxTimeoutSec {
			return codexSessionEndMaxTimeoutSec, nil
		}
		return given, nil
	}
	if !have {
		return codexDefaultHookTimeoutSec, nil
	}
	if given < 1 {
		return 1, nil
	}
	return given, nil
}

// codexHandlerString reads the first of `names` present as a string.
func codexHandlerString(handler map[string]any, names ...string) (string, bool) {
	for _, n := range names {
		if v, ok := handler[n]; ok {
			if s, ok := v.(string); ok {
				return s, true
			}
		}
	}
	return "", false
}

// codexInt accepts the integer shapes the two decoders that feed this produce:
// go-toml yields int64, encoding/json with UseNumber yields json.Number, and a
// plain encoding/json yields float64. A non-integral float is refused rather
// than rounded — a hash computed off a rounded value would silently disagree
// with Codex's.
func codexInt(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case json.Number:
		return n.Int64()
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("%v is not an integer", n)
		}
		return int64(n), nil
	default:
		return 0, fmt.Errorf("%T is not an integer", v)
	}
}

// codexCanonicalJSON writes `canonical_json` + `serde_json::to_vec`: keys
// sorted, no whitespace.
//
// ⚠️ It is NOT `encoding/json`, and the difference is load-bearing. Go escapes
// `<`, `>` and `&` by default and ALWAYS escapes U+2028/U+2029; serde_json
// escapes none of those, and uses `\b`/`\f` where Go writes ``/``.
// A hook command holding any of them would hash differently under the standard
// encoder — i.e. would read as changed forever — and nothing would say why.
func codexCanonicalJSON(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case string:
		codexJSONString(b, t)
	case bool:
		b.WriteString(strconv.FormatBool(t))
	case int64:
		b.WriteString(strconv.FormatInt(t, 10))
	case []any:
		b.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := codexCanonicalJSON(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys) // byte order, which is Rust's String Ord
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			codexJSONString(b, k)
			b.WriteByte(':')
			if err := codexCanonicalJSON(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("cannot canonicalise %T", v)
	}
	return nil
}

// codexJSONString writes serde_json's string escaping, byte for byte. Bytes
// above 0x7f pass through unchanged — serde_json emits UTF-8 raw, as does this.
func codexJSONString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(b, `\u%04x`, c)
				continue
			}
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}
