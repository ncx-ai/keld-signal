package tools

import (
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// codexHookEventNames returns the [[hooks.<Event>]] arrays present in text,
// read through a real TOML parse rather than a substring match — a hook block
// that parses differently from how it reads is exactly the class of thing a
// grep-shaped assertion misses.
func codexHookEventNames(t *testing.T, text string) map[string]int {
	t.Helper()
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `toml:"type"`
				Command string `toml:"command"`
			} `toml:"hooks"`
		} `toml:"hooks"`
	}
	if err := toml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("config does not parse as TOML: %v\n%s", err, text)
	}
	out := map[string]int{}
	for event, entries := range doc.Hooks {
		out[event] = len(entries)
	}
	return out
}

// TestCodexHookEventsAreTheCaptureSet is AC-9's config half at the source: the
// events keld registers with Codex are the ones that actually name a prompt.
//
// `UserPromptSubmit` is the human turn — it is the ONLY event whose payload
// carries a `turn_id` alongside the prompt, so it is the only one that can
// produce a pointer. `Stop` closes the same turn and carries the same
// `turn_id`, which is what lets the daemon see a turn end without reading the
// transcript. `SessionStart` stays because it is how the daemon learns a Codex
// session exists at all before any prompt arrives.
//
// `PreToolUse` is DROPPED. It fires once per tool call — dozens per turn on an
// agentic session — and its payload names no prompt, so every one of those
// invocations was a process spawn that could not produce a pointer.
func TestCodexHookEventsAreTheCaptureSet(t *testing.T) {
	want := []string{"SessionStart", "UserPromptSubmit", "Stop"}
	if len(telemetry.CodexHookEvents) != len(want) {
		t.Fatalf("CodexHookEvents = %v, want %v", telemetry.CodexHookEvents, want)
	}
	for i, ev := range want {
		if telemetry.CodexHookEvents[i] != ev {
			t.Fatalf("CodexHookEvents = %v, want %v", telemetry.CodexHookEvents, want)
		}
	}
	for _, ev := range telemetry.CodexHookEvents {
		if ev == "PreToolUse" {
			t.Fatal("PreToolUse is back: it names no prompt and fires per tool call")
		}
	}
}

// oldKeldCodexBlock is what keld wrote before this change: the [otel] table
// plus SessionStart and PreToolUse. A machine set up by any earlier release
// holds exactly this.
const oldKeldCodexBlock = `# >>> keld (managed by keld CLI — do not edit between markers)
[otel]
environment = "prod"
log_user_prompt = false
exporter = { otlp-http = { endpoint = "https://old/v1/logs", protocol = "json", headers = { "x-keld-ingest-token" = "old" } } }
metrics_exporter = { otlp-http = { endpoint = "https://old/v1/metrics", protocol = "json", headers = { "x-keld-ingest-token" = "old" } } }

[[hooks.SessionStart]]
hooks = [ { type = "command", command = 'keld __hook --source codex' } ]

[[hooks.PreToolUse]]
hooks = [ { type = "command", command = 'keld __hook --source codex' } ]
# <<< keld
`

// TestCodexApplyOverOldBlockSwapsTheHookSet (T5) is the upgrade path: a config
// already holding the SessionStart/PreToolUse block comes out with the capture
// set and no PreToolUse, and applying twice changes nothing further.
func TestCodexApplyOverOldBlockSwapsTheHookSet(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}

	cur := oldKeldCodexBlock
	plan := a.Apply(&cur, p, false)
	if plan.Conflict != "" {
		t.Fatalf("conflict on an existing keld block: %s", plan.Conflict)
	}
	if !plan.Changed {
		t.Fatal("swapping the hook set is a change")
	}

	events := codexHookEventNames(t, plan.AfterText)
	for _, want := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
		if events[want] != 1 {
			t.Errorf("hooks.%s appears %d times, want 1 (all events: %v)", want, events[want], events)
		}
	}
	if n, ok := events["PreToolUse"]; ok {
		t.Errorf("hooks.PreToolUse survived, %d entries", n)
	}

	// Idempotent: a second run over its own output is a no-op.
	after := plan.AfterText
	second := a.Apply(&after, p, false)
	if second.Conflict != "" {
		t.Fatalf("conflict on second apply: %s", second.Conflict)
	}
	if second.Changed {
		t.Error("second apply reported a change; setup must be idempotent")
	}
	if second.AfterText != after {
		t.Errorf("second apply rewrote the config:\n--first--\n%s\n--second--\n%s", after, second.AfterText)
	}
}

// thirdPartyCodexConfig is the shape of a real machine: another tool's hooks
// are registered from its OWN file and Codex records the human's approval of
// them as [hooks.state] tables. Nothing keld does may touch either.
const thirdPartyCodexConfig = `model = "gpt-5.5"

[[hooks.UserPromptSubmit]]
hooks = [ { type = "command", command = '/opt/othertool/hook.sh' } ]

[[hooks.PreToolUse]]
hooks = [ { type = "command", command = '/opt/othertool/hook.sh' } ]

[hooks.state."/home/dev/.codex/hooks.json:user_prompt_submit:0:0"]
enabled = true
trusted_hash = "sha256:6a0b6d4bfdfbd93c379149cf59b92daeca3a47fabeb65d04d419915ba3d86c23"

[hooks.state."/home/dev/.codex/hooks.json:pre_tool_use:0:0"]
enabled = true
trusted_hash = "sha256:bf2b1f4617cb62ca69c1f098414ed4638626d15b5d851a98109ae004d9848175"
`

// TestCodexApplyLeavesAThirdPartyConfigAlone (T6): everything outside keld's
// markers comes through byte-identical — the other tool's own hook entries and,
// crucially, its `hooks.state` trust records. Rewriting one of those would
// revoke a human's approval of somebody else's hook.
func TestCodexApplyLeavesAThirdPartyConfigAlone(t *testing.T) {
	a := &CodexAdapter{}
	cur := thirdPartyCodexConfig
	plan := a.Apply(&cur, SetupParams{Endpoint: "https://e", IngestToken: "tok"}, false)
	if plan.Conflict != "" {
		t.Fatalf("conflict on a third-party config: %s", plan.Conflict)
	}

	before, _, found := strings.Cut(plan.AfterText, "# >>> keld")
	if !found {
		t.Fatalf("no keld block written:\n%s", plan.AfterText)
	}
	if strings.TrimRight(before, "\n") != strings.TrimRight(thirdPartyCodexConfig, "\n") {
		t.Errorf("content outside keld's block changed:\n--want--\n%q\n--got--\n%q", thirdPartyCodexConfig, before)
	}
	if !strings.Contains(plan.AfterText, "/opt/othertool/hook.sh") {
		t.Error("the other tool's hook command is gone")
	}
	if strings.Count(plan.AfterText, "trusted_hash") != 2 {
		t.Error("a hooks.state trust record was lost or duplicated")
	}
}

// TestCodexRemoveTakesOnlyKeldsBlocks (T7): uninstall removes keld's block and
// leaves the third party's hooks and trust records exactly where they were.
func TestCodexRemoveTakesOnlyKeldsBlocks(t *testing.T) {
	a := &CodexAdapter{}
	cur := thirdPartyCodexConfig
	applied := a.Apply(&cur, SetupParams{Endpoint: "https://e", IngestToken: "tok"}, false).AfterText

	removed := a.Remove(&applied, map[string]any{"block": true})
	if !removed.Changed {
		t.Fatal("remove over a written block is a change")
	}
	if strings.Contains(removed.AfterText, "keld __hook") {
		t.Error("keld's hook command survived removal")
	}
	if strings.Contains(removed.AfterText, "# >>> keld") {
		t.Error("keld's markers survived removal")
	}
	if strings.TrimRight(removed.AfterText, "\n") != strings.TrimRight(thirdPartyCodexConfig, "\n") {
		t.Errorf("remove did not restore the original:\n--want--\n%q\n--got--\n%q", thirdPartyCodexConfig, removed.AfterText)
	}

	events := codexHookEventNames(t, removed.AfterText)
	if events["UserPromptSubmit"] != 1 || events["PreToolUse"] != 1 {
		t.Errorf("the other tool's hooks were disturbed: %v", events)
	}
}
