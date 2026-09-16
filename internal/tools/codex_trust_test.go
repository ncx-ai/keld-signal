package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// keldCodexCommand is the substring a caller passes: the recognizer already
// used to spot keld's hook in a config, whatever binary path it was written
// with.
const keldCodexCommand = "keld __hook --source codex"

func readCodexTrustFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "codex", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// TestCodexHooksTrustedOnARealMachine is the state Gabriel's machine is in
// today, and it is the reason the Codex row must read `approval_required`
// rather than `broken`: another tool's hooks ARE approved — six of them — and
// keld's are not named at all. Codex therefore skips keld's hooks on every
// turn, SILENTLY (verified 2026-09-15 on 0.153.4: no warning, no log line, no
// state written; the same run under --dangerously-bypass-hook-trust fires all
// three). Nothing is broken; a human has not approved us yet.
func TestCodexHooksTrustedOnARealMachine(t *testing.T) {
	cfg := readCodexTrustFixture(t, "config-third-party-trusted.toml")
	trusted, known := CodexHooksTrusted(cfg, keldCodexCommand)
	if !known {
		t.Error("known=false, but this config HAS a hooks.state section — Codex is new enough to record trust")
	}
	if trusted {
		t.Error("trusted=true, but no state entry names keld's hooks")
	}

	// And the third party's own approvals must not be read as ours: the
	// source path is its hooks.json, not the config.toml keld writes into.
	if !strings.Contains(string(cfg), "hooks.json:user_prompt_submit") {
		t.Fatal("fixture no longer carries the third party's approval")
	}
}

// TestCodexHooksTrustedWhenApproved: once a human runs /hooks and approves,
// Codex writes a state entry whose source is the config.toml the inline hooks
// live in, at the same array index, with enabled = true.
func TestCodexHooksTrustedWhenApproved(t *testing.T) {
	trusted, known := CodexHooksTrusted(readCodexTrustFixture(t, "config-keld-trusted.toml"), keldCodexCommand)
	if !known {
		t.Error("known=false on a config with a hooks.state section")
	}
	if !trusted {
		t.Error("trusted=false, but both required events are approved for the inline source")
	}
}

// TestCodexHooksUntrustedWhenWrittenButNotApproved is the state a machine
// lands in the moment `keld signal setup` runs: the hooks are in the config
// and no human has approved them. This is the row that must say what to do,
// not that something failed.
func TestCodexHooksUntrustedWhenWrittenButNotApproved(t *testing.T) {
	trusted, known := CodexHooksTrusted(readCodexTrustFixture(t, "config-keld-untrusted.toml"), keldCodexCommand)
	if !known {
		t.Error("known=false on a config with a hooks.state section")
	}
	if trusted {
		t.Error("trusted=true with no approval for keld's inline hooks")
	}
}

// TestCodexHookTrustUnknownWithoutAStateSection: a Codex old enough to have no
// trust mechanism writes no `hooks.state` at all. Answering "untrusted" there
// would tell every such machine to go and approve something its Codex has no
// UI for. known=false hands the question to the caller, which resolves it from
// `session_meta.cli_version`.
func TestCodexHookTrustUnknownWithoutAStateSection(t *testing.T) {
	trusted, known := CodexHooksTrusted(readCodexTrustFixture(t, "config-no-hook-state.toml"), keldCodexCommand)
	if known {
		t.Error("known=true on a config with no hooks.state section")
	}
	if trusted {
		t.Error("trusted must be false whenever known is false")
	}
}

// TestCodexHookTrustRefusesToGuess: every input that cannot support an answer
// must come back (false, false) rather than a confident one. A trust check
// that guesses is worse than one that abstains — it either nags a machine that
// is fine or vouches for one that is silently dropping every prompt.
func TestCodexHookTrustRefusesToGuess(t *testing.T) {
	for _, tc := range []struct {
		name string
		toml string
	}{
		{"empty", ""},
		{"not toml", "this is not [ toml"},
		{"no hooks at all", "model = \"gpt-5.5\"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trusted, known := CodexHooksTrusted([]byte(tc.toml), keldCodexCommand)
			if trusted || known {
				t.Errorf("trusted=%v known=%v, want false/false", trusted, known)
			}
		})
	}
}

// TestCodexHookTrustNeedsEveryCaptureEvent: approving only UserPromptSubmit
// leaves Stop skipped, so the integration is half-wired and the row must still
// ask for approval. Partial approval reading as trusted is how a row goes
// green while half the lane is dead.
func TestCodexHookTrustNeedsEveryCaptureEvent(t *testing.T) {
	full := string(readCodexTrustFixture(t, "config-keld-trusted.toml"))
	partial := strings.Split(full, `[hooks.state."/Users/gabrielionescu/.codex/config.toml:stop:0:0"]`)[0]
	if partial == full {
		t.Fatal("fixture no longer carries a stop approval to remove")
	}
	trusted, known := CodexHooksTrusted([]byte(partial), keldCodexCommand)
	if !known {
		t.Error("known=false")
	}
	if trusted {
		t.Error("trusted=true with stop unapproved")
	}
}

// TestCodexHookTrustHonoursAnExplicitRefusal: `enabled = false` is a human
// saying no. It must never read as approval.
func TestCodexHookTrustHonoursAnExplicitRefusal(t *testing.T) {
	cfg := strings.Replace(
		string(readCodexTrustFixture(t, "config-keld-trusted.toml")),
		`[hooks.state."/Users/gabrielionescu/.codex/config.toml:stop:0:0"]`+"\nenabled = true",
		`[hooks.state."/Users/gabrielionescu/.codex/config.toml:stop:0:0"]`+"\nenabled = false",
		1,
	)
	trusted, known := CodexHooksTrusted([]byte(cfg), keldCodexCommand)
	if !known || trusted {
		t.Errorf("trusted=%v known=%v, want false/true", trusted, known)
	}
}

// TestCodexHookTrustIgnoresAnotherToolsApproval: an entry with the same event
// and index but a DIFFERENT source path belongs to somebody else's hook file.
// Reading it as ours would vouch for hooks Codex is still skipping.
func TestCodexHookTrustIgnoresAnotherToolsApproval(t *testing.T) {
	cfg := strings.ReplaceAll(
		string(readCodexTrustFixture(t, "config-keld-trusted.toml")),
		"/Users/gabrielionescu/.codex/config.toml:",
		"/opt/othertool/hooks.json:",
	)
	trusted, known := CodexHooksTrusted([]byte(cfg), keldCodexCommand)
	if !known {
		t.Error("known=false")
	}
	if trusted {
		t.Error("trusted=true off another source's approvals")
	}
}

// TestCodexTrustEventsAreEventsWeRegister: asking for approval of an event
// keld never registers is asking for something a human cannot give.
func TestCodexTrustEventsAreEventsWeRegister(t *testing.T) {
	if !codexTrustEventsAreRegistered() {
		t.Fatalf("codexTrustEvents %v is not a subset of telemetry.CodexHookEvents", codexTrustEvents)
	}
}

// TestCodexHookHashIsCodexsOwnHash is the empirical anchor for everything
// below it: a REAL input/output pair, taken off this machine on 2026-09-15.
//
// `hooks-third-party.json` is another tool's hook file verbatim, and
// `config-third-party-trusted.toml` carries the six `trusted_hash` values
// Codex 0.153.4 itself wrote after a human approved those hooks in `/hooks`.
// Note all six COMMANDS are byte-identical and all six HASHES differ — which
// is what says the event name is inside the hash and the command alone is not
// the input. If codexHookHash ever stops reproducing these six, our reading of
// Codex's rule has drifted from Codex's and every trust answer is a guess.
func TestCodexHookHashIsCodexsOwnHash(t *testing.T) {
	var file struct {
		Hooks map[string][]struct {
			Matcher *string          `json:"matcher"`
			Hooks   []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	raw := readCodexTrustFixture(t, "hooks-third-party.json")
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&file); err != nil {
		t.Fatalf("decode hooks-third-party.json: %v", err)
	}
	if len(file.Hooks) != 6 {
		t.Fatalf("fixture holds %d events, want the 6 Codex recorded", len(file.Hooks))
	}

	var cfg map[string]any
	if err := toml.Unmarshal(readCodexTrustFixture(t, "config-third-party-trusted.toml"), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	state, _ := cfg["hooks"].(map[string]any)["state"].(map[string]any)
	if len(state) != 6 {
		t.Fatalf("config fixture holds %d state entries, want 6", len(state))
	}

	for event, groups := range file.Hooks {
		for gi, group := range groups {
			for hi, handler := range group.Hooks {
				got, err := codexHookHash(event, group.Matcher, handler)
				if err != nil {
					t.Fatalf("%s: %v", event, err)
				}
				key := fmt.Sprintf("/Users/gabrielionescu/.codex/hooks.json:%s:%d:%d",
					codexSnakeEvent(event), gi, hi)
				entry, ok := state[key].(map[string]any)
				if !ok {
					t.Fatalf("no recorded state for %s", key)
				}
				want, _ := entry["trusted_hash"].(string)
				if got != want {
					t.Errorf("%s\n got  %s\n want %s (Codex's own)", key, got, want)
				}
			}
		}
	}
}

// TestCodexHookTrustFailsWhenTheCommandChanged is AC-9's last clause. Codex
// records trust against the hook's hash, not its position, and marks a CHANGED
// hook for review again — so a keld release that edits the hook command leaves
// the approval in place and STOPS RUNNING THE HOOK. Reading `enabled = true`
// alone reports that machine as healthy while it captures nothing, which is
// the silent-inert failure `approval_required` exists to make loud.
//
// The fixture is `config-keld-trusted.toml` with the command a release would
// have edited; the approvals are untouched and still name the old command's
// hash, exactly as Codex would leave them.
func TestCodexHookTrustFailsWhenTheCommandChanged(t *testing.T) {
	trusted, known := CodexHooksTrusted(readCodexTrustFixture(t, "config-keld-changed.toml"), keldCodexCommand)
	if !known {
		t.Error("known=false — the config HAS a hooks.state section, so we CAN tell")
	}
	if trusted {
		t.Error("trusted=true after the hook command changed; Codex will refuse to run it")
	}
}

// TestCodexHookTrustRefusesAnApprovalWithNoHash: a state entry carrying
// `enabled = true` and no `trusted_hash` is what Codex calls Untrusted — it
// has nothing to compare the current hook against, and neither have we.
// Vouching for it would be exactly the positional reading this test replaced.
func TestCodexHookTrustRefusesAnApprovalWithNoHash(t *testing.T) {
	cfg := string(readCodexTrustFixture(t, "config-keld-trusted.toml"))
	stripped := regexp.MustCompile(`(?m)^trusted_hash = .*\n`).ReplaceAllString(cfg, "")
	if strings.Contains(stripped, "trusted_hash") {
		t.Fatal("fixture still carries a trusted_hash")
	}
	trusted, known := CodexHooksTrusted([]byte(stripped), keldCodexCommand)
	if !known || trusted {
		t.Errorf("trusted=%v known=%v, want false/true", trusted, known)
	}
}
