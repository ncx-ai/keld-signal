# Prompted-LLM vs GLiNER2 Classification Study — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an offline harness that measures whether a prompted local LLM reading a multi-turn conversational window classifies `domain` / `task_type` / `subcategory` better than the production GLiNER2 backend, and run it.

**Architecture:** A new self-contained package `internal/agent/enrich/llmstudy` with four units — window miner, prompt/schema builder, arm runners, differ+report — driven by a `keld-agent study` cobra command. Arms A/B (Qwen3 via `llama-server`) receive the mined window; the control (GLiNER2) and arm C (`gliner-guard-omni`) receive **production input** (raw target prompt + `enrich.Meta.RecentPrompts`). Scoring is disagreement-only with blinded human adjudication.

**Tech Stack:** Go 1.x (host toolchain), `llama.cpp` (`llama-cpp b10221-1`, `/usr/bin/llama-server`) with OpenAI-style `response_format: json_schema` constrained decoding, the existing GLiNER2 sidecar via `internal/agent/enrich/sidecar`.

**Spec:** `docs/superpowers/specs/2026-08-07-llm-classifier-study-design.md`

## Global Constraints

- **This is a study. No product behavior changes.** Do not modify the daemon, sidecar, publish path, wire format, `enrich.SchemaVersion`, or any label vocabulary.
- **Do not touch `feat/multiturn-context`** or any file it owns (`internal/agent/enrich/eval/mine/`, `internal/agent/enrich/contextpack/`, `internal/agent/enrich/eval/multiturn_*.go`). They do not exist on `main`; they are another agent's in-flight work.
- **Do not modify `internal/agentcli/evalcmd.go`** — that branch also edits it. Register the study command from its own new file.
- **All work happens in worktree** `/home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study` on branch `feat/llm-classify-study`.
- **Privacy: transcripts are read locally and never transmitted.** No network call may carry window or prompt text except to `127.0.0.1` (`llama-server`, the sidecar). No API models.
- **Enums come from `enrich` package vars, never transcribed literals** — `enrich.TaskTypeDefs`, `enrich.DomainDefs`, `enrich.Activities`, `enrich.Personal`, `enrich.Functions`, `enrich.Subcats`.
- **K = 8** context turns preceding the target prompt. Not swept in round 1.
- **N = 200** mined windows.
- **Facets scored:** primary `domain`, `task_type`, `subcategory`; secondary `function_guess`, `activity_type`. `sensitivity`, `sensitivity_spans`, `domain_entities`, `speech_act` are out of scope.
- **Two output tiers.** Prompt tier (compared head-to-head against GLiNER2) and session tier (session `domain`/`function_guess`/`activity_type` — **no control exists**, reported not scored). Never adjudicate a session-tier label against the control.
- **Topic terms are verified, never trusted.** Every emitted term must be found in the source transcript by case-insensitive substring match; unlocatable terms are **dropped**. Record the pass rate.
- **The free-text session summary is a LOCAL-ONLY diagnostic.** It is written to `~/.keld/study/` and never published, never transmitted, never proposed as an output. It exists to judge comprehension.
- `enrich.Run` is `Run(text, source string, meta Meta, m Model, opts ...Option) Profile` (`internal/agent/enrich/pipeline.go:121`) — verified.
- The `~/.keld` accessor is `paths.KeldHome()` (`internal/paths/paths.go:26`) — verified. There is no `paths.Home()`.
- Root registration is `root.AddCommand(...)` inside `NewRootCmd()` at `internal/agentcli/agentcli.go:178`; add beside `newEvalCmd()` at line 222 — verified.
- `go test ./...` must stay green **without** any model or sidecar present — live arms go behind the `llmstudy` build tag, mirroring `//go:build sidecar` in `internal/agent/enrich/eval/sidecar_eval_test.go`.
- Commit after every task.

## File Structure

| File | Responsibility |
|---|---|
| `internal/agent/enrich/llmstudy/window.go` | Transcript JSONL → `Window` (turns, tool lines, code elision, caps) |
| `internal/agent/enrich/llmstudy/window_test.go` | Miner tests over fixture transcripts |
| `internal/agent/enrich/llmstudy/testdata/session.jsonl` | Hand-written fixture transcript |
| `internal/agent/enrich/llmstudy/schema.go` | JSON schema + prompt text, built from `enrich` label vars |
| `internal/agent/enrich/llmstudy/schema_test.go` | Asserts enums match `enrich` exactly |
| `internal/agent/enrich/llmstudy/llama.go` | `llama-server` client, constrained decoding, latency |
| `internal/agent/enrich/llmstudy/llama_test.go` | Client tests against `httptest` server |
| `internal/agent/enrich/llmstudy/arms.go` | `Answer` type; encoder-arm runner via `enrich.Run` |
| `internal/agent/enrich/llmstudy/arms_test.go` | Encoder-arm runner test with a fake `enrich.Model` |
| `internal/agent/enrich/llmstudy/differ.go` | Disagreement extraction + blinded adjudication set |
| `internal/agent/enrich/llmstudy/differ_test.go` | Agreements dropped, provenance hidden, deterministic shuffle |
| `internal/agent/enrich/llmstudy/report.go` | Win/loss/tie, Wilson CI, latency percentiles |
| `internal/agent/enrich/llmstudy/report_test.go` | Wilson CI + tally tests |
| `internal/agent/enrich/llmstudy/live_test.go` | `//go:build llmstudy` end-to-end arm smoke |
| `internal/agentcli/studycmd.go` | `keld-agent study mine\|run\|adjudicate\|report` |
| `internal/agentcli/studycmd_test.go` | Command wiring tests |
| `docs/superpowers/plans/2026-08-07-llm-classifier-study-results.md` | Results (Task 9) |

---

### Task 1: Window miner

**Files:**
- Create: `internal/agent/enrich/llmstudy/window.go`
- Create: `internal/agent/enrich/llmstudy/window_test.go`
- Create: `internal/agent/enrich/llmstudy/testdata/session.jsonl`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `Role` (`RoleUser`/`RoleAssistant`/`RoleTool`), `Turn{Role Role; Text string}`, `Window{SessionID, PromptID, Target string; Turns []Turn; Recent []string}`, `MineOpts{K, PerTurnChars, WindowChars int}`, `DefaultMineOpts() MineOpts`, `Mine(path string, o MineOpts) ([]Window, error)`, `Render(w Window) string`.

**Semantics to implement:**
- `K` counts **context turns preceding the target**; the target is appended as the final user turn. So `len(w.Turns) <= K+1`.
- `Turns` is oldest-first. The target prompt is **last**.
- `Recent` is up to 3 prior **user** texts, **newest-first** — the shape `enrich.Meta.RecentPrompts` expects, so the control arm gets production input.
- Consecutive assistant records merge into one turn (one reply spans several records).
- Tool uses become `RoleTool` turns rendered `Name arg` (e.g. `Edit settings.go`). Tool **results** and argument bodies are never included.
- Fenced code → `[code block, N lines]`.
- `thinking`, `image`, `tool_result` blocks contribute nothing.

- [ ] **Step 1: Write the fixture transcript**

Create `internal/agent/enrich/llmstudy/testdata/session.jsonl` (one JSON object per line, no trailing newline issues):

```jsonl
{"type":"user","uuid":"u1","message":{"role":"user","content":"add retry to the settings poll"}}
{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret reasoning"},{"type":"text","text":"The poll lives in settings.go. Here is the change:\n```go\nfunc x() {\n  return 1\n}\n```\nThat wraps it."}]}}
{"type":"assistant","uuid":"a2","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/home/dg/keld/internal/agent/settings/settings.go"}}]}}
{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","content":"applied 1 edit"}]}}
{"type":"assistant","uuid":"a3","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./internal/agent/settings/ -run TestPoll -v"}}]}}
{"type":"assistant","uuid":"a4","message":{"role":"assistant","content":"Tests green."}}
{"type":"user","uuid":"u2","message":{"role":"user","content":"now do the same for publish"}}
```

- [ ] **Step 2: Write the failing test**

Create `internal/agent/enrich/llmstudy/window_test.go`:

```go
package llmstudy

import (
	"strings"
	"testing"
)

func mineFixture(t *testing.T, k int) []Window {
	t.Helper()
	o := DefaultMineOpts()
	o.K = k
	ws, err := Mine("testdata/session.jsonl", o)
	if err != nil {
		t.Fatalf("Mine: %v", err)
	}
	return ws
}

func TestMineFindsEachUserPromptAsTarget(t *testing.T) {
	ws := mineFixture(t, 8)
	// Two real user prompts; the tool_result-only user record is not a prompt.
	if len(ws) != 2 {
		t.Fatalf("want 2 windows, got %d", len(ws))
	}
	if ws[0].Target != "add retry to the settings poll" {
		t.Errorf("window 0 target = %q", ws[0].Target)
	}
	if ws[1].Target != "now do the same for publish" || ws[1].PromptID != "u2" {
		t.Errorf("window 1 target = %q id = %q", ws[1].Target, ws[1].PromptID)
	}
}

func TestTargetIsLastTurn(t *testing.T) {
	w := mineFixture(t, 8)[1]
	last := w.Turns[len(w.Turns)-1]
	if last.Role != RoleUser || last.Text != w.Target {
		t.Fatalf("last turn = %+v, want user target %q", last, w.Target)
	}
}

func TestCodeIsElided(t *testing.T) {
	w := mineFixture(t, 8)[1]
	got := Render(w)
	if strings.Contains(got, "func x()") {
		t.Errorf("raw code leaked into window:\n%s", got)
	}
	if !strings.Contains(got, "[code block, ") {
		t.Errorf("no elision marker:\n%s", got)
	}
	if !strings.Contains(got, "The poll lives in settings.go.") {
		t.Errorf("prose around the code was dropped:\n%s", got)
	}
}

func TestToolUseRenderedCompactlyAndResultsDropped(t *testing.T) {
	w := mineFixture(t, 8)[1]
	got := Render(w)
	if !strings.Contains(got, "Edit settings.go") {
		t.Errorf("tool_use not rendered as name+basename:\n%s", got)
	}
	if strings.Contains(got, "/home/dg/keld/internal") {
		t.Errorf("absolute path leaked instead of basename:\n%s", got)
	}
	if strings.Contains(got, "applied 1 edit") {
		t.Errorf("tool_result leaked into window:\n%s", got)
	}
}

func TestThinkingDropped(t *testing.T) {
	if got := Render(mineFixture(t, 8)[1]); strings.Contains(got, "secret reasoning") {
		t.Errorf("thinking block leaked:\n%s", got)
	}
}

func TestConsecutiveAssistantRecordsMerge(t *testing.T) {
	w := mineFixture(t, 8)[1]
	// a3 (tool) then a4 (text) are distinct roles, so they must not merge;
	// but a1 text + a2 tool must not merge either. Assert no two adjacent
	// turns share the assistant role.
	for i := 1; i < len(w.Turns); i++ {
		if w.Turns[i].Role == RoleAssistant && w.Turns[i-1].Role == RoleAssistant {
			t.Fatalf("adjacent assistant turns not merged at %d: %+v", i, w.Turns)
		}
	}
}

func TestRecentIsPriorUserPromptsNewestFirst(t *testing.T) {
	w := mineFixture(t, 8)[1]
	if len(w.Recent) != 1 || w.Recent[0] != "add retry to the settings poll" {
		t.Fatalf("Recent = %v", w.Recent)
	}
	for _, r := range w.Recent {
		if r == w.Target {
			t.Fatal("Recent must exclude the target prompt")
		}
	}
}

func TestKBoundsContextTurns(t *testing.T) {
	w := mineFixture(t, 2)[1]
	if len(w.Turns) != 3 { // 2 context + target
		t.Fatalf("K=2 should give 3 turns, got %d: %+v", len(w.Turns), w.Turns)
	}
}

func TestPerTurnCapTruncates(t *testing.T) {
	o := DefaultMineOpts()
	o.PerTurnChars = 20
	ws, err := Mine("testdata/session.jsonl", o)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		for _, tn := range w.Turns {
			if len([]rune(tn.Text)) > 20 {
				t.Fatalf("turn exceeds per-turn cap: %q", tn.Text)
			}
		}
	}
}

func TestMineIsDeterministic(t *testing.T) {
	a, b := mineFixture(t, 8), mineFixture(t, 8)
	if Render(a[1]) != Render(b[1]) {
		t.Fatal("Mine is not deterministic")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -v`
Expected: FAIL — build error, `undefined: DefaultMineOpts`, `undefined: Mine`, etc.

- [ ] **Step 4: Write the implementation**

Create `internal/agent/enrich/llmstudy/window.go`:

```go
// Package llmstudy is an OFFLINE study harness comparing a prompted local LLM
// against the production GLiNER2 backend on classification facets. It is not
// part of the enrichment pipeline and never publishes anything.
//
// Privacy: transcripts are read locally. Window text is sent only to loopback
// backends (llama-server, the GLiNER2 sidecar) and never leaves the machine.
package llmstudy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Role labels a rendered turn.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Turn is one rendered conversational turn.
type Turn struct {
	Role Role   `json:"role"`
	Text string `json:"text"`
}

// Window is the classification input: K context turns then the target prompt.
type Window struct {
	SessionID string   `json:"session_id"`
	PromptID  string   `json:"prompt_id"`
	Target    string   `json:"target"`
	Turns     []Turn   `json:"turns"`  // oldest-first; target is LAST
	Recent    []string `json:"recent"` // prior user prompts, newest-first (production Meta)
}

// MineOpts bounds the window. K counts CONTEXT turns before the target.
type MineOpts struct {
	K            int
	PerTurnChars int
	WindowChars  int
}

// DefaultMineOpts is the round-1 configuration (see the study design: K is fixed).
func DefaultMineOpts() MineOpts {
	return MineOpts{K: 8, PerTurnChars: 1200, WindowChars: 12000}
}

const recentCount = 3

// fence matches a fenced code block, including its language tag.
var fence = regexp.MustCompile("(?s)```[^\n]*\n.*?```")

// elideCode replaces fenced code with a line-count marker: that code was written
// is signal, the code itself is bulk.
func elideCode(s string) string {
	return fence.ReplaceAllStringFunc(s, func(m string) string {
		return "[code block, " + strconv.Itoa(strings.Count(m, "\n")-1) + " lines]"
	})
}

// clip truncates to n runes. n <= 0 means unbounded.
func clip(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// toolArgKeys are the argument names worth rendering, in priority order. Only one
// is emitted, and never its body — a Read of a 2000-line file must not become
// 2000 lines of window.
var toolArgKeys = []string{"file_path", "path", "notebook_path", "command", "pattern", "url", "query"}

// toolLine renders a tool_use as "Name arg". Paths are reduced to their base name
// so absolute paths never enter the window.
func toolLine(name string, input map[string]json.RawMessage) string {
	for _, k := range toolArgKeys {
		raw, ok := input[k]
		if !ok {
			continue
		}
		var v string
		if json.Unmarshal(raw, &v) != nil || v == "" {
			continue
		}
		if k == "file_path" || k == "path" || k == "notebook_path" {
			v = filepath.Base(v)
		}
		return name + " " + clip(v, 80)
	}
	return name
}

// line is a tolerant view of a transcript record. Unknown shapes are skipped.
type line struct {
	Type     string          `json:"type"`
	PromptID string          `json:"promptId"`
	UUID     string          `json:"uuid"`
	SessionID string         `json:"sessionId"`
	Message  json.RawMessage `json:"message"`
}

type msg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type  string                     `json:"type"`
	Text  string                     `json:"text"`
	Name  string                     `json:"name"`
	Input map[string]json.RawMessage `json:"input"`
}

// record is one parsed turn candidate.
type record struct {
	role Role
	text string
	id   string // user records only
}

// parseRecord turns one JSONL line into zero or more records. A single assistant
// message can yield a text record plus tool records.
func parseRecord(l line) []record {
	var m msg
	if json.Unmarshal(l.Message, &m) != nil {
		return nil
	}
	id := l.PromptID
	if id == "" {
		id = l.UUID
	}

	// Content may be a bare string.
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		if l.Type == "user" {
			return []record{{role: RoleUser, text: s, id: id}}
		}
		return []record{{role: RoleAssistant, text: s}}
	}

	var blocks []block
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []record
	var prose strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			prose.WriteString(b.Text)
		case "tool_use":
			out = append(out, record{role: RoleTool, text: toolLine(b.Name, b.Input)})
		}
		// thinking, tool_result, image: contribute nothing.
	}
	if p := strings.TrimSpace(prose.String()); p != "" {
		r := record{role: RoleAssistant, text: p}
		if l.Type == "user" {
			r = record{role: RoleUser, text: p, id: id}
		}
		// Prose precedes any tool calls in the same message.
		out = append([]record{r}, out...)
	}
	return out
}

// Mine reads a transcript and returns one Window per user prompt, oldest-first.
func Mine(path string, o MineOpts) ([]Window, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var recs []record
	sessionID := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20) // transcript lines can be large
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue // tolerate malformed lines
		}
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		if l.SessionID != "" {
			sessionID = l.SessionID
		}
		recs = append(recs, parseRecord(l)...)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	var out []Window
	for i, r := range recs {
		if r.role != RoleUser {
			continue
		}
		out = append(out, buildWindow(sessionID, recs, i, o))
	}
	return out, nil
}

// buildWindow assembles the window whose target is recs[i].
func buildWindow(sessionID string, recs []record, i int, o MineOpts) Window {
	start := i - o.K
	if start < 0 {
		start = 0
	}
	turns := make([]Turn, 0, o.K+1)
	for _, c := range recs[start:i] {
		turns = appendTurn(turns, Turn{Role: c.role, Text: clip(elideCode(c.text), o.PerTurnChars)})
	}
	target := clip(elideCode(recs[i].text), o.PerTurnChars)
	turns = append(turns, Turn{Role: RoleUser, Text: target})

	var recent []string
	for j := i - 1; j >= 0 && len(recent) < recentCount; j-- {
		if recs[j].role == RoleUser {
			recent = append(recent, clip(recs[j].text, o.PerTurnChars))
		}
	}

	w := Window{SessionID: sessionID, PromptID: recs[i].id, Target: target, Turns: turns, Recent: recent}
	trimToWindowCap(&w, o.WindowChars)
	return w
}

// appendTurn merges a turn into the previous one when both are assistant prose:
// one assistant reply spans several transcript records.
func appendTurn(turns []Turn, t Turn) []Turn {
	if n := len(turns); n > 0 && t.Role == RoleAssistant && turns[n-1].Role == RoleAssistant {
		turns[n-1].Text = strings.TrimSpace(turns[n-1].Text + " " + t.Text)
		return turns
	}
	return append(turns, t)
}

// trimToWindowCap drops OLDEST context turns until the rendered window fits.
// The target is never dropped: it is the thing being classified.
func trimToWindowCap(w *Window, cap int) {
	if cap <= 0 {
		return
	}
	for len(w.Turns) > 1 && len([]rune(Render(*w))) > cap {
		w.Turns = w.Turns[1:]
	}
}

// Render formats the window for a prompt. Stable and deterministic.
func Render(w Window) string {
	var b strings.Builder
	for _, t := range w.Turns {
		b.WriteString(string(t.Role))
		b.WriteString(": ")
		b.WriteString(t.Text)
		b.WriteString("\n")
	}
	return b.String()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -v`
Expected: PASS, all 10 tests.

- [ ] **Step 6: Verify the whole repo still builds green**

Run: `go test ./... 2>&1 | tail -20`
Expected: no new failures.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/enrich/llmstudy/
git commit -m "feat(llmstudy): conversational window miner with tool uses and code elision"
```

---

### Task 2: Prompt and JSON schema from the live vocabulary

**Files:**
- Create: `internal/agent/enrich/llmstudy/schema.go`
- Create: `internal/agent/enrich/llmstudy/schema_test.go`

**Interfaces:**
- Consumes: `Window`, `Render` (Task 1).
- Produces: `Facet` constants (`FacetTaskType`, `FacetDomain`, `FacetActivity`, `FacetPersonal`, `FacetFunction`, `FacetSubcategory`), `PrimaryFacets []Facet`, `WaveOneSchema() map[string]any`, `SubcategorySchema(fn string) map[string]any`, `WaveOnePrompt(w Window) string`, `SubcategoryPrompt(w Window, fn string) string`, `defsFor(f Facet) []enrich.LabelDef`.

Wave 1 mirrors the pipeline: five independent facets in one call. Subcategory is a **second** call conditioned on the returned `function_guess`, matching the pipeline's Wave 2.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/llmstudy/schema_test.go`:

```go
package llmstudy

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// enumOf pulls the enum list a schema declares for a property.
func enumOf(t *testing.T, schema map[string]any, prop string) []string {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	p, ok := props[prop].(map[string]any)
	if !ok {
		t.Fatalf("schema has no property %q", prop)
	}
	raw, ok := p["enum"].([]string)
	if !ok {
		t.Fatalf("property %q has no []string enum: %v", prop, p)
	}
	return raw
}

func TestWaveOneEnumsMatchLiveVocabulary(t *testing.T) {
	s := WaveOneSchema()
	cases := []struct {
		prop string
		defs []enrich.LabelDef
	}{
		{"task_type", enrich.TaskTypeDefs},
		{"domain", enrich.DomainDefs},
		{"activity_type", enrich.Activities},
		{"personal", enrich.Personal},
		{"function_guess", enrich.Functions},
	}
	for _, c := range cases {
		got := enumOf(t, s, c.prop)
		if len(got) != len(c.defs) {
			t.Fatalf("%s: enum has %d entries, vocabulary has %d", c.prop, len(got), len(c.defs))
		}
		for i, d := range c.defs {
			if got[i] != d.ID {
				t.Errorf("%s[%d] = %q, want %q", c.prop, i, got[i], d.ID)
			}
		}
	}
}

func TestWaveOneSchemaIsStrict(t *testing.T) {
	s := WaveOneSchema()
	if s["additionalProperties"] != false {
		t.Error("schema must set additionalProperties:false so the model cannot invent fields")
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != 5 {
		t.Fatalf("required = %v, want all 5 wave-1 facets", s["required"])
	}
}

func TestSubcategorySchemaIsConditionedOnFunction(t *testing.T) {
	got := enumOf(t, SubcategorySchema("eng"), "subcategory")
	want := enrich.Subcats["eng"]
	if len(got) != len(want) {
		t.Fatalf("eng subcats: got %d, want %d", len(got), len(want))
	}
	for i, d := range want {
		if got[i] != d.ID {
			t.Errorf("subcategory[%d] = %q, want %q", i, got[i], d.ID)
		}
	}
	// A function with no subcategories must not produce an empty enum.
	if SubcategorySchema("nonexistent") != nil {
		t.Error("unknown function must yield a nil schema, not an empty enum")
	}
}

func TestWaveOnePromptCarriesDescriptionsAndWindow(t *testing.T) {
	w := mineFixture(t, 8)[1]
	p := WaveOnePrompt(w)
	// The readable descriptions are load-bearing: they must reach the model.
	for _, d := range enrich.DomainDefs {
		if !strings.Contains(p, d.Text) {
			t.Errorf("prompt omits domain description %q", d.Text)
		}
	}
	if !strings.Contains(p, Render(w)) {
		t.Error("prompt omits the rendered window")
	}
	if !strings.Contains(p, w.Target) {
		t.Error("prompt must name the target prompt explicitly")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'Schema|Prompt|Enums' -v`
Expected: FAIL — `undefined: WaveOneSchema`.

- [ ] **Step 3: Write the implementation**

Create `internal/agent/enrich/llmstudy/schema.go`:

```go
package llmstudy

import (
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// Facet names a scored classification facet. Values match the JSON property
// names and the report's facet keys.
type Facet string

const (
	FacetTaskType    Facet = "task_type"
	FacetDomain      Facet = "domain"
	FacetActivity    Facet = "activity_type"
	FacetPersonal    Facet = "personal"
	FacetFunction    Facet = "function_guess"
	FacetSubcategory Facet = "subcategory"
)

// waveOneFacets are classified in a single call, mirroring the pipeline's Wave 1.
var waveOneFacets = []Facet{FacetTaskType, FacetDomain, FacetActivity, FacetPersonal, FacetFunction}

// PrimaryFacets are the facets the study is designed to decide on. The others
// are secondary readouts. See the study design's Scope section.
var PrimaryFacets = []Facet{FacetDomain, FacetTaskType, FacetSubcategory}

// defsFor returns the live vocabulary for a facet. Sourced from the enrich
// package so the study can never drift onto a stale taxonomy.
func defsFor(f Facet) []enrich.LabelDef {
	switch f {
	case FacetTaskType:
		return enrich.TaskTypeDefs
	case FacetDomain:
		return enrich.DomainDefs
	case FacetActivity:
		return enrich.Activities
	case FacetPersonal:
		return enrich.Personal
	case FacetFunction:
		return enrich.Functions
	}
	return nil
}

func idsOf(defs []enrich.LabelDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}

// WaveOneSchema is the JSON schema for the five independent facets.
func WaveOneSchema() map[string]any {
	props := map[string]any{}
	req := make([]string, 0, len(waveOneFacets))
	for _, f := range waveOneFacets {
		props[string(f)] = map[string]any{"type": "string", "enum": idsOf(defsFor(f))}
		req = append(req, string(f))
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             req,
		"additionalProperties": false,
	}
}

// SubcategorySchema is the Wave-2 schema for one function's subcategories, or
// nil when the function has none.
func SubcategorySchema(fn string) map[string]any {
	defs, ok := enrich.Subcats[fn]
	if !ok || len(defs) == 0 {
		return nil
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			string(FacetSubcategory): map[string]any{"type": "string", "enum": idsOf(defs)},
		},
		"required":             []string{string(FacetSubcategory)},
		"additionalProperties": false,
	}
}

// labelMenu renders a facet's options as "id — description" lines. The readable
// descriptions are load-bearing in the encoder pipeline; the LLM gets the same
// wording so the taxonomies are genuinely comparable.
func labelMenu(f Facet) string {
	var b strings.Builder
	b.WriteString(string(f))
	b.WriteString(":\n")
	for _, d := range defsFor(f) {
		b.WriteString("  - ")
		b.WriteString(d.ID)
		b.WriteString(" — ")
		b.WriteString(d.Text)
		b.WriteString("\n")
	}
	return b.String()
}

const promptPreamble = `You are classifying one prompt from a conversation between a software engineer and an AI coding assistant.

Below is the recent conversation, oldest first. Lines are prefixed with the speaker: "user:", "assistant:", or "tool:" (a tool the assistant invoked). Generated code has been replaced with a placeholder.

CONVERSATION:
`

const promptRules = `
Classify ONLY the final user turn (repeated below as TARGET PROMPT). The earlier
conversation is context to help you interpret it — do not classify the session as
a whole. If the target prompt is terse ("do it", "commit"), use the conversation
to determine what it refers to.

TARGET PROMPT:
`

// WaveOnePrompt builds the Wave-1 classification prompt.
func WaveOnePrompt(w Window) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	b.WriteString(Render(w))
	b.WriteString(promptRules)
	b.WriteString(w.Target)
	b.WriteString("\n\nChoose exactly one option for each of the following:\n\n")
	for _, f := range waveOneFacets {
		b.WriteString(labelMenu(f))
		b.WriteString("\n")
	}
	b.WriteString("Respond with JSON only.\n")
	return b.String()
}

// SubcategoryPrompt builds the Wave-2 prompt, conditioned on the function id.
func SubcategoryPrompt(w Window, fn string) string {
	defs := enrich.Subcats[fn]
	var b strings.Builder
	b.WriteString(promptPreamble)
	b.WriteString(Render(w))
	b.WriteString(promptRules)
	b.WriteString(w.Target)
	b.WriteString("\n\nThe business function is already determined to be \"")
	b.WriteString(fn)
	b.WriteString("\". Choose exactly one subcategory:\n\n")
	for _, d := range defs {
		b.WriteString("  - ")
		b.WriteString(d.ID)
		b.WriteString(" — ")
		b.WriteString(d.Text)
		b.WriteString("\n")
	}
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/schema.go internal/agent/enrich/llmstudy/schema_test.go
git commit -m "feat(llmstudy): constrained-decoding schema and prompts built from live vocabulary"
```

---

### Task 3: llama-server client

**Files:**
- Create: `internal/agent/enrich/llmstudy/llama.go`
- Create: `internal/agent/enrich/llmstudy/llama_test.go`

**Interfaces:**
- Consumes: `Window`, `WaveOneSchema`, `SubcategorySchema`, `WaveOnePrompt`, `SubcategoryPrompt`, `Facet` (Tasks 1–2).
- Produces: `Answer{Labels map[Facet]string; LatencyMS int64; Valid bool; Err string}`, `Llama{BaseURL string; Timeout time.Duration}`, `NewLlama(baseURL string) *Llama`, `(*Llama).Classify(w Window) Answer`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/llmstudy/llama_test.go`:

```go
package llmstudy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// chatReply wraps content in the OpenAI chat-completions envelope llama-server uses.
func chatReply(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	return string(b)
}

func TestClassifyParsesBothWaves(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			io.WriteString(w, chatReply(`{"task_type":"code_generation","domain":"software","activity_type":"generate","personal":"work","function_guess":"eng"}`))
			return
		}
		io.WriteString(w, chatReply(`{"subcategory":"eng.dev"}`))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	want := map[Facet]string{
		FacetTaskType: "code_generation", FacetDomain: "software",
		FacetActivity: "generate", FacetPersonal: "work",
		FacetFunction: "eng", FacetSubcategory: "eng.dev",
	}
	for f, v := range want {
		if got.Labels[f] != v {
			t.Errorf("%s = %q, want %q", f, got.Labels[f], v)
		}
	}
	if len(bodies) != 2 {
		t.Fatalf("want 2 requests (wave 1 + subcategory), got %d", len(bodies))
	}
	if !strings.Contains(bodies[0], `"json_schema"`) {
		t.Error("wave-1 request did not request constrained decoding")
	}
	if !strings.Contains(bodies[0], `"temperature":0`) {
		t.Error("wave-1 request must be deterministic (temperature 0)")
	}
}

func TestClassifyRejectsOffVocabularyLabel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, chatReply(`{"task_type":"telepathy","domain":"software","activity_type":"generate","personal":"work","function_guess":"eng"}`))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if got.Valid {
		t.Fatal("an off-vocabulary label must invalidate the answer")
	}
	if !strings.Contains(got.Err, "telepathy") {
		t.Errorf("Err should name the offending label, got %q", got.Err)
	}
}

func TestClassifyRecordsLatencyAndSurvivesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if got.Valid {
		t.Fatal("HTTP 500 must not yield a valid answer")
	}
	if got.Err == "" {
		t.Error("Err must be populated on failure")
	}
	if got.LatencyMS < 0 {
		t.Error("LatencyMS must be recorded even on failure")
	}
}

func TestClassifySkipsWaveTwoWhenFunctionHasNoSubcats(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.WriteString(w, chatReply(`{"task_type":"reasoning","domain":"general","activity_type":"analyze","personal":"personal","function_guess":"gen"}`))
	}))
	defer srv.Close()

	got := NewLlama(srv.URL).Classify(mineFixture(t, 8)[1])
	if _, ok := enrichSubcatsHas("gen"); ok {
		t.Skip("gen has subcategories in this vocabulary; test assumption stale")
	}
	if calls != 1 {
		t.Fatalf("want 1 call when the function has no subcategories, got %d", calls)
	}
	if got.Labels[FacetSubcategory] != "" {
		t.Errorf("subcategory should be empty, got %q", got.Labels[FacetSubcategory])
	}
}
```

Add this helper at the bottom of `llama_test.go`:

```go
// enrichSubcatsHas reports whether a function id has subcategories.
func enrichSubcatsHas(fn string) (int, bool) {
	d := SubcategorySchema(fn)
	return 0, d != nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Classify -v`
Expected: FAIL — `undefined: NewLlama`.

- [ ] **Step 3: Write the implementation**

Create `internal/agent/enrich/llmstudy/llama.go`:

```go
package llmstudy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Answer is one arm's labels for one window, plus how it went.
type Answer struct {
	Labels    map[Facet]string `json:"labels"`
	LatencyMS int64            `json:"latency_ms"`
	Valid     bool             `json:"valid"`
	Err       string           `json:"err,omitempty"`
}

// Llama talks to a local llama-server over its OpenAI-compatible endpoint.
// Loopback only: window text never leaves the machine.
type Llama struct {
	BaseURL string
	Timeout time.Duration
	hc      *http.Client
}

// NewLlama returns a client for a llama-server base URL (e.g. http://127.0.0.1:8080).
func NewLlama(baseURL string) *Llama {
	to := 180 * time.Second // generous: CPU prefill on a long window is slow
	return &Llama{BaseURL: baseURL, Timeout: to, hc: &http.Client{Timeout: to}}
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// call issues one constrained-decoding request and unmarshals the JSON content.
func (l *Llama) call(prompt string, schema map[string]any, out any) error {
	body := map[string]any{
		"messages":    []any{map[string]any{"role": "user", "content": prompt}},
		"temperature": 0,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "facets",
				"strict": true,
				"schema": schema,
			},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, l.BaseURL+"/v1/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := l.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llama-server HTTP %d", resp.StatusCode)
	}
	var cr chatResp
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return err
	}
	if len(cr.Choices) == 0 {
		return fmt.Errorf("llama-server returned no choices")
	}
	return json.Unmarshal([]byte(cr.Choices[0].Message.Content), out)
}

// validate confirms a label is in the facet's live vocabulary. Constrained
// decoding should make this impossible to fail; it is checked anyway because a
// silent off-vocabulary label would corrupt the study.
func validate(f Facet, v string) error {
	for _, d := range defsFor(f) {
		if d.ID == v {
			return nil
		}
	}
	return fmt.Errorf("%s: off-vocabulary label %q", f, v)
}

// Classify runs Wave 1 then, when the chosen function has subcategories, Wave 2.
func (l *Llama) Classify(w Window) Answer {
	a := Answer{Labels: map[Facet]string{}}
	start := time.Now()
	defer func() { a.LatencyMS = time.Since(start).Milliseconds() }()

	var one map[string]string
	if err := l.call(WaveOnePrompt(w), WaveOneSchema(), &one); err != nil {
		a.Err = err.Error()
		return a
	}
	for _, f := range waveOneFacets {
		v := one[string(f)]
		if err := validate(f, v); err != nil {
			a.Err = err.Error()
			return a
		}
		a.Labels[f] = v
	}

	fn := a.Labels[FacetFunction]
	schema := SubcategorySchema(fn)
	if schema == nil {
		a.Valid = true
		return a
	}
	var two map[string]string
	if err := l.call(SubcategoryPrompt(w, fn), schema, &two); err != nil {
		a.Err = "subcategory: " + err.Error()
		return a
	}
	a.Labels[FacetSubcategory] = two[string(FacetSubcategory)]
	a.Valid = true
	return a
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/llama.go internal/agent/enrich/llmstudy/llama_test.go
git commit -m "feat(llmstudy): llama-server client with schema-constrained decoding"
```

---

### Task 4: Encoder-arm runner (control and arm C)

**Files:**
- Create: `internal/agent/enrich/llmstudy/arms.go`
- Create: `internal/agent/enrich/llmstudy/arms_test.go`

**Interfaces:**
- Consumes: `Window`, `Answer`, `Facet` (Tasks 1, 3).
- Produces: `EncoderArm{Model enrich.Model; Source string}`, `NewEncoderArm(m enrich.Model) *EncoderArm`, `(*EncoderArm).Classify(w Window) Answer`.

**Critical:** the encoder arms receive **production input** — `w.Target` as the text and `w.Recent` as `enrich.Meta.RecentPrompts` — *never* the rendered window. gliner2 truncates head-keeping while the window puts the target last, so feeding it the window would silently discard the prompt being classified. See the study design's "The control must receive its production input" section.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/llmstudy/arms_test.go`:

```go
package llmstudy

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// fakeModel records the text it was asked to classify and returns the first
// label of every task, so Classify produces a deterministic in-vocabulary answer.
type fakeModel struct{ seen []string }

func (m *fakeModel) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	m.seen = append(m.seen, text)
	out := map[string][]enrich.Ranked{}
	for name, labels := range tasks {
		if len(labels) > 0 {
			out[name] = []enrich.Ranked{{Label: labels[0], Score: 0.9}}
		}
	}
	return out
}

func (m *fakeModel) Entities(text string, labels map[string]string) []enrich.Entity { return nil }

func (m *fakeModel) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	m.seen = append(m.seen, text)
	return enrich.ExtractResult{}
}

func TestEncoderArmReceivesProductionInputNotTheWindow(t *testing.T) {
	w := mineFixture(t, 8)[1]
	fm := &fakeModel{}
	got := NewEncoderArm(fm).Classify(w)

	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	if len(fm.seen) == 0 {
		t.Fatal("model was never called")
	}
	// The rendered window must never reach the encoder: it would head-truncate
	// away the target prompt, which sits last.
	for _, s := range fm.seen {
		if s == Render(w) {
			t.Fatal("encoder arm was fed the rendered window instead of production input")
		}
		if len(s) > 0 && containsToolLine(s) {
			t.Fatalf("encoder arm input contains rendered tool lines: %q", s)
		}
	}
}

func containsToolLine(s string) bool {
	return len(s) > 0 && (indexOf(s, "tool: ") >= 0)
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}

func TestEncoderArmPopulatesScoredFacets(t *testing.T) {
	got := NewEncoderArm(&fakeModel{}).Classify(mineFixture(t, 8)[1])
	for _, f := range []Facet{FacetTaskType, FacetDomain, FacetActivity, FacetPersonal, FacetFunction} {
		if got.Labels[f] == "" {
			t.Errorf("facet %s not populated", f)
		}
	}
}

func TestEncoderArmRecordsLatency(t *testing.T) {
	if got := NewEncoderArm(&fakeModel{}).Classify(mineFixture(t, 8)[1]); got.LatencyMS < 0 {
		t.Error("LatencyMS not recorded")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run EncoderArm -v`
Expected: FAIL — `undefined: NewEncoderArm`.

- [ ] **Step 3: Write the implementation**

Create `internal/agent/enrich/llmstudy/arms.go`:

```go
package llmstudy

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// EncoderArm runs the production enrichment pipeline over a Model backend — used
// for the GLiNER2 control and for the gliner-guard-omni arm.
//
// It deliberately feeds PRODUCTION INPUT (the raw target prompt plus the recent
// user prompts that Meta carries), never the rendered multi-turn window. gliner2
// truncates head-keeping and the window places the target last, so passing the
// window would silently discard the prompt under classification and sandbag the
// control into an artificial loss.
type EncoderArm struct {
	Model  enrich.Model
	Source string
}

// NewEncoderArm wraps a Model as a study arm reporting source "claude_code".
func NewEncoderArm(m enrich.Model) *EncoderArm {
	return &EncoderArm{Model: m, Source: "claude_code"}
}

// Classify runs the real pipeline and projects the Profile onto the study facets.
func (e *EncoderArm) Classify(w Window) Answer {
	a := Answer{Labels: map[Facet]string{}}
	start := time.Now()

	meta := enrich.Meta{Tool: e.Source, RecentPrompts: w.Recent}
	p := enrich.Run(w.Target, e.Source, meta, e.Model)

	a.LatencyMS = time.Since(start).Milliseconds()
	a.Labels[FacetTaskType] = p.TaskType.Value
	a.Labels[FacetDomain] = p.Domain.Value
	a.Labels[FacetActivity] = p.Activity.Value
	a.Labels[FacetPersonal] = p.Personal.Value
	a.Labels[FacetFunction] = p.FunctionGuess.Value
	a.Labels[FacetSubcategory] = p.Subcategory.Value
	a.Valid = true
	return a
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -v`
Expected: PASS. If `enrich.Run`'s signature differs, correct the call — do not change `enrich`.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/arms.go internal/agent/enrich/llmstudy/arms_test.go
git commit -m "feat(llmstudy): encoder arm on production input for control and guard-omni"
```

---

### Task 5: Differ and blinded adjudication set

**Files:**
- Create: `internal/agent/enrich/llmstudy/differ.go`
- Create: `internal/agent/enrich/llmstudy/differ_test.go`

**Interfaces:**
- Consumes: `Window`, `Answer`, `Facet`, `Render` (Tasks 1, 3).
- Produces: `Run{Arm string; Answers []Answer}`, `Item{ID, Facet string; Window string; Target string; Options []Option}`, `Option{Key, Label, Description string}`, `Disagreements(ws []Window, control Run, arms []Run, facets []Facet, seed int64) []Item`, `Key{} ` mapping via `Item.Options[i].Key`, `ProvenanceOf(items []Item) map[string]map[string]string`.

Blinding requirement: `Item` carries **no arm names**. Each `Option.Key` is an opaque `a`/`b`/`c` assigned by a seeded shuffle. `ProvenanceOf` returns the key→arm mapping, written to a **separate** file the adjudicator does not open.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/llmstudy/differ_test.go`:

```go
package llmstudy

import (
	"encoding/json"
	"strings"
	"testing"
)

func ans(labels map[Facet]string) Answer {
	return Answer{Labels: labels, Valid: true}
}

func fixtureRuns() ([]Window, Run, []Run) {
	ws := []Window{
		{PromptID: "p1", Target: "fix the retry loop", Turns: []Turn{{RoleUser, "fix the retry loop"}}},
		{PromptID: "p2", Target: "write a poem", Turns: []Turn{{RoleUser, "write a poem"}}},
	}
	control := Run{Arm: "gliner2", Answers: []Answer{
		ans(map[Facet]string{FacetDomain: "general"}),
		ans(map[Facet]string{FacetDomain: "creative"}),
	}}
	arm := Run{Arm: "qwen3-4b", Answers: []Answer{
		ans(map[Facet]string{FacetDomain: "software"}), // disagrees
		ans(map[Facet]string{FacetDomain: "creative"}), // agrees
	}}
	return ws, control, arm2Runs(arm)
}

func arm2Runs(r Run) []Run { return []Run{r} }

func TestAgreementsAreDiscarded(t *testing.T) {
	ws, control, arms := fixtureRuns()
	items := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	if len(items) != 1 {
		t.Fatalf("want 1 disagreement, got %d: %+v", len(items), items)
	}
	if items[0].ID != "p1" {
		t.Errorf("wrong row kept: %+v", items[0])
	}
}

func TestAdjudicationItemHidesProvenance(t *testing.T) {
	ws, control, arms := fixtureRuns()
	items := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"gliner2", "qwen3-4b", "arm", "control"} {
		if strings.Contains(strings.ToLower(string(b)), leak) {
			t.Errorf("adjudication item leaks provenance %q: %s", leak, b)
		}
	}
}

func TestOptionsCarryReadableDescriptions(t *testing.T) {
	ws, control, arms := fixtureRuns()
	it := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)[0]
	if len(it.Options) != 2 {
		t.Fatalf("want 2 options, got %d", len(it.Options))
	}
	for _, o := range it.Options {
		if o.Description == "" {
			t.Errorf("option %q has no description; the adjudicator needs the label wording", o.Label)
		}
	}
}

func TestShuffleIsDeterministicUnderSeed(t *testing.T) {
	ws, control, arms := fixtureRuns()
	a := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	b := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	if a[0].Options[0].Label != b[0].Options[0].Label {
		t.Fatal("same seed produced different option order")
	}
	c := Disagreements(ws, control, arms, []Facet{FacetDomain}, 8)
	_ = c // a different seed may or may not reorder 2 options; only determinism is asserted
}

func TestProvenanceMapsKeysBackToArms(t *testing.T) {
	ws, control, arms := fixtureRuns()
	items := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	prov := ProvenanceOf(items)
	got := prov["p1:domain"]
	if len(got) != 2 {
		t.Fatalf("provenance for p1:domain = %v", got)
	}
	var sawControl, sawArm bool
	for _, armName := range got {
		if armName == "gliner2" {
			sawControl = true
		}
		if armName == "qwen3-4b" {
			sawArm = true
		}
	}
	if !sawControl || !sawArm {
		t.Errorf("provenance must name both arms, got %v", got)
	}
}

func TestInvalidAnswersAreSkipped(t *testing.T) {
	ws, control, _ := fixtureRuns()
	broken := Run{Arm: "broken", Answers: []Answer{{Valid: false, Err: "boom"}, {Valid: false}}}
	if items := Disagreements(ws, control, []Run{broken}, []Facet{FacetDomain}, 7); len(items) != 0 {
		t.Fatalf("invalid answers must not become adjudication items, got %+v", items)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'Agreement|Adjudication|Options|Shuffle|Provenance|Invalid' -v`
Expected: FAIL — `undefined: Disagreements`.

- [ ] **Step 3: Write the implementation**

Create `internal/agent/enrich/llmstudy/differ.go`:

```go
package llmstudy

import (
	"fmt"
	"math/rand"
	"sort"
)

// Run is one arm's answers, index-aligned with the mined windows.
type Run struct {
	Arm     string   `json:"arm"`
	Answers []Answer `json:"answers"`
}

// Option is one candidate label offered for adjudication. Key is opaque so the
// adjudicator cannot infer which model proposed it.
type Option struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Item is one blinded adjudication question. It carries NO arm identity.
type Item struct {
	ID      string   `json:"id"`
	Facet   string   `json:"facet"`
	Window  string   `json:"window"`
	Target  string   `json:"target"`
	Options []Option `json:"options"`
	Choice  string   `json:"choice"` // filled by the human: an Option.Key, "tie", or "both_wrong"
}

// descFor finds a label's readable description in the live vocabulary.
func descFor(f Facet, id string) string {
	for _, d := range defsFor(f) {
		if d.ID == id {
			return d.Text
		}
	}
	// Subcategory ids live in a per-function map.
	for _, defs := range subcatAll() {
		for _, d := range defs {
			if d.ID == id {
				return d.Text
			}
		}
	}
	return id
}

// itemKey is the stable identity of one adjudication question.
func itemKey(id string, f Facet) string { return id + ":" + string(f) }

// provenance is recorded alongside items but written to a separate file.
var provenanceStore = map[string]map[string]string{}

// Disagreements returns one blinded Item per (window, facet) where at least one
// arm disagrees with the control. Agreements carry no information about which
// model is better and are discarded.
func Disagreements(ws []Window, control Run, arms []Run, facets []Facet, seed int64) []Item {
	rng := rand.New(rand.NewSource(seed))
	provenanceStore = map[string]map[string]string{}
	var items []Item

	for i, w := range ws {
		if i >= len(control.Answers) || !control.Answers[i].Valid {
			continue
		}
		for _, f := range facets {
			cv := control.Answers[i].Labels[f]
			if cv == "" {
				continue
			}
			// Collect distinct labels: control first, then any disagreeing arm.
			byLabel := map[string][]string{cv: {control.Arm}}
			order := []string{cv}
			for _, a := range arms {
				if i >= len(a.Answers) || !a.Answers[i].Valid {
					continue
				}
				av := a.Answers[i].Labels[f]
				if av == "" {
					continue
				}
				if _, seen := byLabel[av]; !seen {
					order = append(order, av)
				}
				byLabel[av] = append(byLabel[av], a.Arm)
			}
			if len(order) < 2 {
				continue // unanimous: nothing to adjudicate
			}

			// Shuffle so option position carries no signal.
			rng.Shuffle(len(order), func(x, y int) { order[x], order[y] = order[y], order[x] })

			opts := make([]Option, 0, len(order))
			prov := map[string]string{}
			for j, label := range order {
				key := string(rune('a' + j))
				opts = append(opts, Option{Key: key, Label: label, Description: descFor(f, label)})
				names := byLabel[label]
				sort.Strings(names)
				prov[key] = joinNames(names)
			}
			k := itemKey(w.PromptID, f)
			provenanceStore[k] = prov
			items = append(items, Item{
				ID: w.PromptID, Facet: string(f),
				Window: Render(w), Target: w.Target, Options: opts,
			})
		}
	}
	return items
}

func joinNames(n []string) string {
	out := ""
	for i, s := range n {
		if i > 0 {
			out += "+"
		}
		out += s
	}
	return out
}

// ProvenanceOf returns itemKey -> optionKey -> arm name(s), for the run that
// produced these items. Write it to a file the adjudicator does not open.
func ProvenanceOf(items []Item) map[string]map[string]string {
	out := make(map[string]map[string]string, len(items))
	for _, it := range items {
		k := itemKey(it.ID, Facet(it.Facet))
		if p, ok := provenanceStore[k]; ok {
			out[k] = p
		} else {
			out[k] = map[string]string{}
		}
	}
	if len(out) != len(items) {
		panic(fmt.Sprintf("provenance/item mismatch: %d vs %d", len(out), len(items)))
	}
	return out
}
```

Add `subcatAll` to `schema.go`:

```go
// subcatAll returns every function's subcategory definitions, for description lookup.
func subcatAll() map[string][]enrich.LabelDef { return enrich.Subcats }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/differ.go internal/agent/enrich/llmstudy/differ_test.go internal/agent/enrich/llmstudy/schema.go
git commit -m "feat(llmstudy): disagreement extraction with blinded adjudication items"
```

---

### Task 6: Report — win/loss/tie with Wilson intervals

**Files:**
- Create: `internal/agent/enrich/llmstudy/report.go`
- Create: `internal/agent/enrich/llmstudy/report_test.go`

**Interfaces:**
- Consumes: `Item`, `Run`, `Answer`, `Facet` (Tasks 3, 5).
- Produces: `Tally{Wins, Losses, Ties, BothWrong int}`, `CI{Lo, Hi float64}`, `Wilson(wins, n int) CI`, `Tallies(items []Item, prov map[string]map[string]string, controlArm string) map[string]map[string]Tally`, `Latency(r Run) (p50, p95, max int64)`, `ValidityRate(r Run) float64`.

`Tallies` returns `facet -> arm -> Tally`, where a win means the human chose the option whose provenance is that arm and **not** the control.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/llmstudy/report_test.go`:

```go
package llmstudy

import (
	"math"
	"testing"
)

func TestWilsonBracketsThePointEstimate(t *testing.T) {
	ci := Wilson(8, 10)
	if !(ci.Lo < 0.8 && ci.Hi > 0.8) {
		t.Fatalf("Wilson(8,10) = %+v, must bracket 0.8", ci)
	}
	if ci.Lo < 0 || ci.Hi > 1 {
		t.Fatalf("Wilson must stay in [0,1]: %+v", ci)
	}
}

func TestWilsonWidensWithSmallN(t *testing.T) {
	small, large := Wilson(8, 10), Wilson(800, 1000)
	if (small.Hi - small.Lo) <= (large.Hi - large.Lo) {
		t.Fatal("a smaller sample must give a wider interval")
	}
}

func TestWilsonZeroSampleIsFullRange(t *testing.T) {
	ci := Wilson(0, 0)
	if ci.Lo != 0 || ci.Hi != 1 {
		t.Fatalf("Wilson(0,0) = %+v, want the full range", ci)
	}
}

func TestTalliesCreditWinsToTheChosenArm(t *testing.T) {
	items := []Item{
		{ID: "p1", Facet: "domain", Options: []Option{{Key: "a", Label: "software"}, {Key: "b", Label: "general"}}, Choice: "a"},
		{ID: "p2", Facet: "domain", Options: []Option{{Key: "a", Label: "general"}, {Key: "b", Label: "software"}}, Choice: "a"},
		{ID: "p3", Facet: "domain", Options: []Option{{Key: "a", Label: "software"}, {Key: "b", Label: "general"}}, Choice: "tie"},
		{ID: "p4", Facet: "domain", Options: []Option{{Key: "a", Label: "software"}, {Key: "b", Label: "general"}}, Choice: "both_wrong"},
	}
	prov := map[string]map[string]string{
		"p1:domain": {"a": "qwen", "b": "gliner2"}, // qwen chosen -> win
		"p2:domain": {"a": "gliner2", "b": "qwen"}, // gliner2 chosen -> loss
		"p3:domain": {"a": "qwen", "b": "gliner2"},
		"p4:domain": {"a": "qwen", "b": "gliner2"},
	}
	got := Tallies(items, prov, "gliner2")["domain"]["qwen"]
	want := Tally{Wins: 1, Losses: 1, Ties: 1, BothWrong: 1}
	if got != want {
		t.Fatalf("Tally = %+v, want %+v", got, want)
	}
}

func TestTalliesIgnoreUnadjudicatedItems(t *testing.T) {
	items := []Item{{ID: "p1", Facet: "domain", Options: []Option{{Key: "a", Label: "x"}, {Key: "b", Label: "y"}}, Choice: ""}}
	prov := map[string]map[string]string{"p1:domain": {"a": "qwen", "b": "gliner2"}}
	if got := Tallies(items, prov, "gliner2")["domain"]["qwen"]; got != (Tally{}) {
		t.Fatalf("unadjudicated item counted: %+v", got)
	}
}

func TestLatencyPercentiles(t *testing.T) {
	r := Run{Answers: []Answer{
		{LatencyMS: 100, Valid: true}, {LatencyMS: 200, Valid: true},
		{LatencyMS: 300, Valid: true}, {LatencyMS: 4000, Valid: true},
	}}
	p50, p95, max := Latency(r)
	if max != 4000 {
		t.Errorf("max = %d, want 4000", max)
	}
	if p50 < 100 || p50 > 300 {
		t.Errorf("p50 = %d, out of range", p50)
	}
	if p95 < p50 {
		t.Errorf("p95 (%d) < p50 (%d)", p95, p50)
	}
}

func TestValidityRate(t *testing.T) {
	r := Run{Answers: []Answer{{Valid: true}, {Valid: false}, {Valid: true}, {Valid: true}}}
	if got := ValidityRate(r); math.Abs(got-0.75) > 1e-9 {
		t.Fatalf("ValidityRate = %v, want 0.75", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'Wilson|Tallies|Latency|Validity' -v`
Expected: FAIL — `undefined: Wilson`.

- [ ] **Step 3: Write the implementation**

Create `internal/agent/enrich/llmstudy/report.go`:

```go
package llmstudy

import (
	"math"
	"sort"
	"strings"
)

// Tally counts adjudicated outcomes for one arm on one facet, versus the control.
type Tally struct {
	Wins      int `json:"wins"`
	Losses    int `json:"losses"`
	Ties      int `json:"ties"`
	BothWrong int `json:"both_wrong"`
}

// Decided is the number of items that produced a win or a loss — the denominator
// for the win rate. Ties and both-wrong are excluded: neither is evidence for a
// model, and both_wrong is evidence about the LABEL VOCABULARY instead.
func (t Tally) Decided() int { return t.Wins + t.Losses }

// CI is a confidence interval on a proportion.
type CI struct {
	Lo float64 `json:"lo"`
	Hi float64 `json:"hi"`
}

// Wilson returns the 95% Wilson score interval for wins/n. It is used instead of
// the normal approximation because n here is small (tens), where the normal
// interval misbehaves near 0 and 1.
func Wilson(wins, n int) CI {
	if n <= 0 {
		return CI{Lo: 0, Hi: 1}
	}
	const z = 1.96
	p := float64(wins) / float64(n)
	nf := float64(n)
	den := 1 + z*z/nf
	centre := (p + z*z/(2*nf)) / den
	half := (z / den) * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	lo, hi := centre-half, centre+half
	return CI{Lo: math.Max(0, lo), Hi: math.Min(1, hi)}
}

// Tallies aggregates human choices into facet -> arm -> Tally, relative to the
// control arm. An item whose Choice is empty is not yet adjudicated and is skipped.
func Tallies(items []Item, prov map[string]map[string]string, controlArm string) map[string]map[string]Tally {
	out := map[string]map[string]Tally{}
	for _, it := range items {
		if it.Choice == "" {
			continue
		}
		p := prov[itemKey(it.ID, Facet(it.Facet))]
		if len(p) == 0 {
			continue
		}
		if out[it.Facet] == nil {
			out[it.Facet] = map[string]Tally{}
		}
		// The arm(s) credited by the human's choice, if any.
		chosen := p[it.Choice]
		for key, arms := range p {
			for _, arm := range strings.Split(arms, "+") {
				if arm == controlArm {
					continue
				}
				t := out[it.Facet][arm]
				switch it.Choice {
				case "tie":
					t.Ties++
				case "both_wrong":
					t.BothWrong++
				default:
					if key == it.Choice && strings.Contains(chosen, arm) {
						t.Wins++
					} else if key != it.Choice {
						// This arm's label was not chosen; the control's was.
						t.Losses++
					}
				}
				out[it.Facet][arm] = t
			}
		}
	}
	return out
}

// Latency returns p50, p95 and max wall-clock over an arm's valid answers.
func Latency(r Run) (p50, p95, max int64) {
	var v []int64
	for _, a := range r.Answers {
		if a.Valid {
			v = append(v, a.LatencyMS)
		}
	}
	if len(v) == 0 {
		return 0, 0, 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	pick := func(q float64) int64 {
		i := int(math.Ceil(q*float64(len(v)))) - 1
		if i < 0 {
			i = 0
		}
		if i >= len(v) {
			i = len(v) - 1
		}
		return v[i]
	}
	return pick(0.50), pick(0.95), v[len(v)-1]
}

// ValidityRate is the share of answers that parsed and validated.
func ValidityRate(r Run) float64 {
	if len(r.Answers) == 0 {
		return 0
	}
	ok := 0
	for _, a := range r.Answers {
		if a.Valid {
			ok++
		}
	}
	return float64(ok) / float64(len(r.Answers))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/report.go internal/agent/enrich/llmstudy/report_test.go
git commit -m "feat(llmstudy): win/loss tallies with Wilson intervals and latency percentiles"
```

---

### Task 7: `keld-agent study` command

**Files:**
- Create: `internal/agentcli/studycmd.go`
- Create: `internal/agentcli/studycmd_test.go`
- Modify: whichever file registers root subcommands — find it with the grep in Step 1. **Do not modify `evalcmd.go`.**

**Interfaces:**
- Consumes: everything from Tasks 1–6.
- Produces: `newStudyCmd() *cobra.Command` with subcommands `mine`, `run`, `adjudicate`, `report`.

Artifacts live under `~/.keld/study/`: `windows.jsonl`, `run-<arm>.json`, `items.json`, `provenance.json`, `report.md`.

- [ ] **Step 1: Find the command registration site**

Run: `grep -rn "newEvalCmd\|AddCommand" internal/agentcli/*.go | head -20`
Note the file and pattern used; you will add `newStudyCmd()` the same way.

- [ ] **Step 2: Write the failing test**

Create `internal/agentcli/studycmd_test.go`:

```go
package agentcli

import "testing"

func TestStudyCmdHasAllSubcommands(t *testing.T) {
	c := newStudyCmd()
	if c.Use != "study" {
		t.Fatalf("Use = %q, want study", c.Use)
	}
	want := map[string]bool{"mine": false, "run": false, "adjudicate": false, "report": false}
	for _, sub := range c.Commands() {
		name := sub.Name()
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q missing", name)
		}
	}
}

func TestStudyCmdIsRegisteredOnRoot(t *testing.T) {
	for _, sub := range NewRootCmd().Commands() {
		if sub.Name() == "study" {
			return
		}
	}
	t.Fatal("study command not registered on root")
}
```

If the root constructor is not named `NewRootCmd`, adjust the second test to the actual name found in Step 1.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/agentcli/ -run Study -v`
Expected: FAIL — `undefined: newStudyCmd`.

- [ ] **Step 4: Write the implementation**

Create `internal/agentcli/studycmd.go`:

```package agentcli

package agentcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/llmstudy"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// studyDir holds the offline study's artifacts. Local only; nothing is published.
func studyDir() string { return filepath.Join(paths.Home(), "study") }

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// newStudyCmd builds `keld-agent study`, the offline LLM-vs-GLiNER2 harness.
func newStudyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "study",
		Short: "Offline prompted-LLM vs GLiNER2 classification study (not a product path).",
	}
	c.AddCommand(newStudyMineCmd(), newStudyRunCmd(), newStudyAdjudicateCmd(), newStudyReportCmd())
	return c
}

func newStudyMineCmd() *cobra.Command {
	var root string
	var n, k, seed int
	c := &cobra.Command{
		Use:   "mine",
		Short: "Mine conversational windows from local transcripts.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if root == "" {
				root = filepath.Join(os.Getenv("HOME"), ".claude", "projects")
			}
			var files []string
			err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil {
					return nil // tolerate unreadable subtrees
				}
				if !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
					files = append(files, p)
				}
				return nil
			})
			if err != nil {
				return err
			}
			sort.Strings(files) // deterministic order
			o := llmstudy.DefaultMineOpts()
			o.K = k
			var all []llmstudy.Window
			for _, f := range files {
				ws, err := llmstudy.Mine(f, o)
				if err != nil {
					continue
				}
				all = append(all, ws...)
			}
			picked := llmstudy.Sample(all, n, int64(seed))
			path := filepath.Join(studyDir(), "windows.json")
			if err := writeJSON(path, picked); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "mined %d windows from %d transcripts, sampled %d -> %s\n",
				len(all), len(files), len(picked), path)
			return nil
		},
	}
	c.Flags().StringVar(&root, "root", "", "transcript root (default ~/.claude/projects)")
	c.Flags().IntVar(&n, "n", 200, "windows to sample")
	c.Flags().IntVar(&k, "k", 8, "context turns before the target")
	c.Flags().IntVar(&seed, "seed", 7, "sampling seed")
	return c
}

func newStudyRunCmd() *cobra.Command {
	var arm, backend string
	c := &cobra.Command{
		Use:   "run",
		Short: "Run one arm over the mined windows.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ws []llmstudy.Window
			if err := readJSON(filepath.Join(studyDir(), "windows.json"), &ws); err != nil {
				return fmt.Errorf("read windows (run `study mine` first): %w", err)
			}
			var cls func(llmstudy.Window) llmstudy.Answer
			switch {
			case strings.HasPrefix(arm, "qwen"):
				cls = llmstudy.NewLlama(backend).Classify
			default:
				sc := sidecar.New(backend, 180*time.Second)
				cls = llmstudy.NewEncoderArm(sc).Classify
			}
			run := llmstudy.Run{Arm: arm, Answers: make([]llmstudy.Answer, 0, len(ws))}
			for i, w := range ws {
				a := cls(w)
				run.Answers = append(run.Answers, a)
				if (i+1)%10 == 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s: %d/%d\n", arm, i+1, len(ws))
				}
			}
			p50, p95, max := llmstudy.Latency(run)
			path := filepath.Join(studyDir(), "run-"+arm+".json")
			if err := writeJSON(path, run); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s: %d answers, validity %.3f, latency p50=%dms p95=%dms max=%dms -> %s\n",
				arm, len(run.Answers), llmstudy.ValidityRate(run), p50, p95, max, path)
			return nil
		},
	}
	c.Flags().StringVar(&arm, "arm", "", "arm name (qwen3-4b, qwen3-1.7b, gliner2, guard-omni)")
	c.Flags().StringVar(&backend, "backend", "http://127.0.0.1:8080", "llama-server or sidecar base URL")
	_ = c.MarkFlagRequired("arm")
	return c
}

func newStudyAdjudicateCmd() *cobra.Command {
	var control string
	var seed int
	c := &cobra.Command{
		Use:   "adjudicate",
		Short: "Build the blinded adjudication set from arm disagreements.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ws []llmstudy.Window
			if err := readJSON(filepath.Join(studyDir(), "windows.json"), &ws); err != nil {
				return err
			}
			entries, err := os.ReadDir(studyDir())
			if err != nil {
				return err
			}
			var ctl llmstudy.Run
			var arms []llmstudy.Run
			names := []string{}
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), "run-") || !strings.HasSuffix(e.Name(), ".json") {
					continue
				}
				names = append(names, e.Name())
			}
			sort.Strings(names) // deterministic arm order
			for _, n := range names {
				var r llmstudy.Run
				if err := readJSON(filepath.Join(studyDir(), n), &r); err != nil {
					return err
				}
				if r.Arm == control {
					ctl = r
					continue
				}
				arms = append(arms, r)
			}
			if ctl.Arm == "" {
				return fmt.Errorf("no run found for control arm %q", control)
			}
			facets := []llmstudy.Facet{
				llmstudy.FacetDomain, llmstudy.FacetTaskType, llmstudy.FacetSubcategory,
				llmstudy.FacetFunction, llmstudy.FacetActivity,
			}
			items := llmstudy.Disagreements(ws, ctl, arms, facets, int64(seed))
			prov := llmstudy.ProvenanceOf(items)
			ip := filepath.Join(studyDir(), "items.json")
			pp := filepath.Join(studyDir(), "provenance.json")
			if err := writeJSON(ip, items); err != nil {
				return err
			}
			if err := writeJSON(pp, prov); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%d disagreements to adjudicate -> %s\nprovenance (do NOT open while adjudicating) -> %s\n",
				len(items), ip, pp)
			return nil
		},
	}
	c.Flags().StringVar(&control, "control", "gliner2", "control arm name")
	c.Flags().IntVar(&seed, "seed", 7, "shuffle seed")
	return c
}

func newStudyReportCmd() *cobra.Command {
	var control string
	c := &cobra.Command{
		Use:   "report",
		Short: "Score adjudicated items into a results table.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var items []llmstudy.Item
			if err := readJSON(filepath.Join(studyDir(), "items.json"), &items); err != nil {
				return err
			}
			var prov map[string]map[string]string
			if err := readJSON(filepath.Join(studyDir(), "provenance.json"), &prov); err != nil {
				return err
			}
			tal := llmstudy.Tallies(items, prov, control)
			var b strings.Builder
			b.WriteString("| facet | arm | wins | losses | ties | both wrong | win rate | 95% CI |\n")
			b.WriteString("|---|---|---:|---:|---:|---:|---:|---|\n")
			facets := make([]string, 0, len(tal))
			for f := range tal {
				facets = append(facets, f)
			}
			sort.Strings(facets)
			for _, f := range facets {
				arms := make([]string, 0, len(tal[f]))
				for a := range tal[f] {
					arms = append(arms, a)
				}
				sort.Strings(arms)
				for _, a := range arms {
					t := tal[f][a]
					ci := llmstudy.Wilson(t.Wins, t.Decided())
					rate := 0.0
					if t.Decided() > 0 {
						rate = float64(t.Wins) / float64(t.Decided())
					}
					fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %.3f | [%.3f, %.3f] |\n",
						f, a, t.Wins, t.Losses, t.Ties, t.BothWrong, rate, ci.Lo, ci.Hi)
				}
			}
			b.WriteString("\nA CI whose lower bound exceeds 0.5 is a win over the control.\n")
			out := b.String()
			path := filepath.Join(studyDir(), "report.md")
			if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			fmt.Fprintf(cmd.OutOrStdout(), "\nwritten -> %s\n", path)
			return nil
		},
	}
	c.Flags().StringVar(&control, "control", "gliner2", "control arm name")
	return c
}

// silence an unused import if enrich is not referenced directly.
var _ = enrich.SchemaVersion
```

Remove the stray first line `package agentcli` in the code block above — the file must start with the doc comment and a single `package agentcli`.

Add `Sample` to `internal/agent/enrich/llmstudy/window.go`:

```go
// Sample deterministically picks up to n windows using a seeded shuffle. Windows
// with no context turns are skipped: they cannot show a context effect.
func Sample(ws []Window, n int, seed int64) []Window {
	var eligible []Window
	for _, w := range ws {
		if len(w.Turns) > 1 {
			eligible = append(eligible, w)
		}
	}
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	if n > 0 && n < len(eligible) {
		eligible = eligible[:n]
	}
	return eligible
}
```

Add `"math/rand"` to `window.go`'s imports.

- [ ] **Step 5: Register the command on root**

Add `newStudyCmd()` to the root command's `AddCommand` call, in the file found in Step 1.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agentcli/ -run Study -v && go test ./... 2>&1 | tail -20`
Expected: PASS, and no new failures repo-wide.

- [ ] **Step 7: Commit**

```bash
git add internal/agentcli/studycmd.go internal/agentcli/studycmd_test.go internal/agent/enrich/llmstudy/window.go
git commit -m "feat(llmstudy): keld-agent study mine/run/adjudicate/report command"
```

---

### Task 8: Runtime setup — fetch GGUFs and prove constrained decoding works

**Files:**
- Create: `internal/agent/enrich/llmstudy/live_test.go`

**Interfaces:**
- Consumes: `NewLlama`, `Window`, `Mine` (Tasks 1, 3).
- Produces: nothing consumed by later tasks; a gate.

- [ ] **Step 1: Fetch the two GGUF models**

`llama-server` can fetch from Hugging Face directly, which avoids a manual download step:

```bash
mkdir -p ~/.keld/models/gguf
# ~2.5 GB and ~1.1 GB respectively.
llama-server --hf-repo unsloth/Qwen3-4B-Instruct-2507-GGUF --hf-file Qwen3-4B-Instruct-2507-Q4_K_M.gguf \
  --port 8080 --ctx-size 8192 --threads 10 --no-warmup &
sleep 60 && curl -s http://127.0.0.1:8080/health
```

If `--hf-repo` is unavailable in this build, download with `curl -L -o ~/.keld/models/gguf/<file>.gguf <resolve URL>` and start with `-m <path>`. Record the exact model files and the command used — the results doc must state them.

- [ ] **Step 2: Verify constrained decoding actually constrains**

```bash
curl -s http://127.0.0.1:8080/v1/chat/completions -H 'Content-Type: application/json' -d '{
  "messages":[{"role":"user","content":"Pick one."}],
  "temperature":0,
  "response_format":{"type":"json_schema","json_schema":{"name":"t","strict":true,
    "schema":{"type":"object","properties":{"domain":{"type":"string","enum":["software","legal"]}},
    "required":["domain"],"additionalProperties":false}}}}' | tail -c 400
```
Expected: content is JSON containing only `software` or `legal`. If the server ignores `response_format`, stop and switch to the `/completion` endpoint with a `grammar` field before continuing — the study depends on this.

- [ ] **Step 3: Write the live smoke test**

Create `internal/agent/enrich/llmstudy/live_test.go`:

```go
//go:build llmstudy

// Live arm smoke test. Requires a running llama-server; skipped otherwise, so
// `go test ./...` stays green without a model present.
//
//	LLAMA_URL=http://127.0.0.1:8080 go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run Live -v
package llmstudy

import (
	"net/http"
	"os"
	"testing"
	"time"
)

func TestLiveLlamaClassifiesFixtureWindow(t *testing.T) {
	url := os.Getenv("LLAMA_URL")
	if url == "" {
		url = "http://127.0.0.1:8080"
	}
	hc := &http.Client{Timeout: 3 * time.Second}
	if _, err := hc.Get(url + "/health"); err != nil {
		t.Skipf("llama-server not reachable at %s: %v", url, err)
	}

	ws, err := Mine("testdata/session.jsonl", DefaultMineOpts())
	if err != nil {
		t.Fatal(err)
	}
	got := NewLlama(url).Classify(ws[len(ws)-1])
	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	t.Logf("labels=%v latency=%dms", got.Labels, got.LatencyMS)
	// The fixture is unambiguously software engineering; a model that misses this
	// is misconfigured (wrong chat template, ignored schema).
	if got.Labels[FacetDomain] != "software" {
		t.Errorf("domain = %q, want software on the fixture window", got.Labels[FacetDomain])
	}
	if got.Labels[FacetFunction] != "eng" {
		t.Errorf("function_guess = %q, want eng", got.Labels[FacetFunction])
	}
}
```

- [ ] **Step 4: Run the live test**

Run: `LLAMA_URL=http://127.0.0.1:8080 go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run Live -v`
Expected: PASS, with the logged latency recorded for the results doc.

- [ ] **Step 5: Confirm the untagged suite is unaffected**

Run: `go test ./... 2>&1 | tail -5`
Expected: no new failures; the live test is not compiled without the tag.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/enrich/llmstudy/live_test.go
git commit -m "test(llmstudy): live llama-server arm smoke behind the llmstudy build tag"
```

---

### Task 9: Two-tier output and verified topic terms

**Files:**
- Modify: `internal/agent/enrich/llmstudy/window.go` (add `SessionDigest`)
- Modify: `internal/agent/enrich/llmstudy/schema.go` (session facets, topics, summary)
- Modify: `internal/agent/enrich/llmstudy/llama.go` (`Answer` gains the new fields)
- Create: `internal/agent/enrich/llmstudy/topics.go`
- Create: `internal/agent/enrich/llmstudy/topics_test.go`
- Modify: `internal/agent/enrich/llmstudy/schema_test.go` (session enum assertions)

**Interfaces:**
- Consumes: everything from Tasks 1–3.
- Produces: `Window.Digest []Turn`, `SessionDigest(recs []record, o MineOpts) []Turn`, `Answer.Session map[Facet]string`, `Answer.Topics []string`, `Answer.RawTopics []string`, `Answer.Summary string`, `VerifyTopics(raw []string, source string) (kept []string, dropped []string)`, `sessionFacets []Facet`.

**Why this is a separate task:** the prompt tier is a head-to-head against a control; the session tier and topic terms have **no control** and are new capabilities. Building them after Task 8 keeps the comparable measurement intact and independently committed.

- [ ] **Step 1: Write the failing topic-verification test**

Create `internal/agent/enrich/llmstudy/topics_test.go`:

```go
package llmstudy

import "testing"

func TestVerifyTopicsKeepsOnlyRealSubstrings(t *testing.T) {
	src := "add retry to the settings poll\nThe poll lives in settings.go."
	kept, dropped := VerifyTopics([]string{"retry", "settings poll", "quantum tunnelling"}, src)
	if len(kept) != 2 {
		t.Fatalf("kept = %v, want 2 real terms", kept)
	}
	if len(dropped) != 1 || dropped[0] != "quantum tunnelling" {
		t.Fatalf("dropped = %v, want the hallucinated term", dropped)
	}
}

func TestVerifyTopicsIsCaseInsensitive() {}

func TestVerifyTopicsCaseInsensitive(t *testing.T) {
	kept, dropped := VerifyTopics([]string{"Settings Poll"}, "add retry to the settings poll")
	if len(kept) != 1 || len(dropped) != 0 {
		t.Fatalf("kept=%v dropped=%v; match must be case-insensitive", kept, dropped)
	}
	// The ORIGINAL casing is preserved: we report what the model said, having
	// proven the text occurs.
	if kept[0] != "Settings Poll" {
		t.Errorf("kept[0] = %q, want the model's original casing", kept[0])
	}
}

func TestVerifyTopicsDropsEmptyAndDuplicates(t *testing.T) {
	kept, _ := VerifyTopics([]string{"retry", "retry", "", "   "}, "add retry now")
	if len(kept) != 1 {
		t.Fatalf("kept = %v, want a single deduped term", kept)
	}
}

func TestVerifyTopicsOnEmptySourceKeepsNothing(t *testing.T) {
	kept, dropped := VerifyTopics([]string{"retry"}, "")
	if len(kept) != 0 || len(dropped) != 1 {
		t.Fatalf("kept=%v dropped=%v; nothing can be verified against empty source", kept, dropped)
	}
}
```

Delete the stray empty `TestVerifyTopicsIsCaseInsensitive() {}` line — it is not a valid test signature and is shown here only to be removed if pasted.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run VerifyTopics -v`
Expected: FAIL — `undefined: VerifyTopics`.

- [ ] **Step 3: Implement verification**

Create `internal/agent/enrich/llmstudy/topics.go`:

```go
package llmstudy

import "strings"

// VerifyTopics filters model-emitted topic terms down to those that literally
// occur in the source transcript, case-insensitively.
//
// This is the deterministic gate that makes an open vocabulary safe: a term the
// model paraphrased or invented cannot be located and is dropped, so a surviving
// term is always text that demonstrably occurred in the conversation. Original
// casing is preserved — we report what the model said, having proven it occurs.
func VerifyTopics(raw []string, source string) (kept, dropped []string) {
	hay := strings.ToLower(source)
	seen := map[string]bool{}
	for _, term := range raw {
		t := strings.TrimSpace(term)
		if t == "" {
			continue
		}
		low := strings.ToLower(t)
		if seen[low] {
			continue
		}
		seen[low] = true
		if hay != "" && strings.Contains(hay, low) {
			kept = append(kept, t)
		} else {
			dropped = append(dropped, t)
		}
	}
	return kept, dropped
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -run VerifyTopics -v`
Expected: PASS.

- [ ] **Step 5: Add the session digest to the miner**

In `window.go`, add a `Digest []Turn` field to `Window` (JSON tag `digest`), and this function:

```go
// digestTurns is the number of turns sampled across the whole session for the
// coarse "what is this session about" view.
const digestTurns = 6

// SessionDigest builds the coarse session view: the opening user prompt (which
// usually states the goal) plus turns sampled evenly across the rest of the
// session, each clipped hard. It answers "what is this session about" where the
// recent window answers "what is being discussed now".
func SessionDigest(recs []record, upto int, o MineOpts) []Turn {
	if upto <= 0 {
		return nil
	}
	const clipTo = 240
	var out []Turn
	// The opening user prompt states the goal; always include it.
	for i := 0; i < upto; i++ {
		if recs[i].role == RoleUser {
			out = append(out, Turn{Role: RoleUser, Text: clip(elideCode(recs[i].text), clipTo)})
			break
		}
	}
	if upto <= 1 {
		return out
	}
	step := upto / digestTurns
	if step < 1 {
		step = 1
	}
	for i := step; i < upto; i += step {
		if recs[i].role == RoleTool {
			continue // tool lines are noise at session scale
		}
		out = appendTurn(out, Turn{Role: recs[i].role, Text: clip(elideCode(recs[i].text), clipTo)})
		if len(out) >= digestTurns {
			break
		}
	}
	return out
}
```

In `buildWindow`, set `w.Digest = SessionDigest(recs, i, o)` before the `trimToWindowCap` call. `trimToWindowCap` must **not** trim the digest — only recent context turns.

Add a test to `window_test.go`:

```go
func TestSessionDigestStartsWithOpeningPromptAndExcludesTools(t *testing.T) {
	w := mineFixture(t, 2)[1] // K=2 so the digest covers turns the window drops
	if len(w.Digest) == 0 {
		t.Fatal("digest is empty")
	}
	if w.Digest[0].Role != RoleUser || w.Digest[0].Text != "add retry to the settings poll" {
		t.Errorf("digest must open with the session's first user prompt, got %+v", w.Digest[0])
	}
	for _, d := range w.Digest {
		if d.Role == RoleTool {
			t.Errorf("digest must exclude tool lines, got %+v", d)
		}
	}
}
```

- [ ] **Step 6: Extend the schema with the session tier, topics and summary**

In `schema.go` add:

```go
// sessionFacets are classified at SESSION scope. There is no control for these —
// GLiNER2 has no notion of a session — so they are reported, never adjudicated.
var sessionFacets = []Facet{FacetDomain, FacetFunction, FacetActivity}

const maxTopics = 6

// WaveOneSchemaV2 adds the session tier, topic terms and the local-only summary.
func WaveOneSchemaV2() map[string]any {
	s := WaveOneSchema()
	props := s["properties"].(map[string]any)
	req := s["required"].([]string)

	sessProps := map[string]any{}
	sessReq := make([]string, 0, len(sessionFacets))
	for _, f := range sessionFacets {
		sessProps[string(f)] = map[string]any{"type": "string", "enum": idsOf(defsFor(f))}
		sessReq = append(sessReq, string(f))
	}
	props["session"] = map[string]any{
		"type": "object", "properties": sessProps,
		"required": sessReq, "additionalProperties": false,
	}
	props["topics"] = map[string]any{
		"type": "array", "maxItems": maxTopics,
		"items": map[string]any{"type": "string"},
	}
	props["summary"] = map[string]any{"type": "string"}

	s["properties"] = props
	s["required"] = append(req, "session", "topics", "summary")
	return s
}
```

Extend `WaveOnePrompt` to render the digest and ask for all three: prefix the rendered digest under a `SESSION SO FAR (compressed):` heading before `CONVERSATION:`, and append instructions for `session`, `topics` (`1-4 word phrases copied VERBATIM from the conversation — do not paraphrase; terms that do not appear verbatim will be discarded`) and `summary` (`one sentence, under 30 words`).

Add to `schema_test.go`:

```go
func TestSchemaV2SessionEnumsMatchVocabularyAndTopicsAreBounded(t *testing.T) {
	s := WaveOneSchemaV2()
	props := s["properties"].(map[string]any)
	sess := props["session"].(map[string]any)
	sp := sess["properties"].(map[string]any)
	dom := sp["domain"].(map[string]any)["enum"].([]string)
	if len(dom) != len(enrich.DomainDefs) {
		t.Fatalf("session domain enum has %d, vocabulary has %d", len(dom), len(enrich.DomainDefs))
	}
	topics := props["topics"].(map[string]any)
	if topics["maxItems"] != maxTopics {
		t.Errorf("topics maxItems = %v, want %d", topics["maxItems"], maxTopics)
	}
}
```

- [ ] **Step 7: Extend `Answer` and `Llama.Classify`**

In `llama.go`, add to `Answer`:

```go
	Session   map[Facet]string `json:"session,omitempty"`
	Topics    []string         `json:"topics,omitempty"`     // verified
	RawTopics []string         `json:"raw_topics,omitempty"` // pre-verification, for pass rate
	Summary   string           `json:"summary,omitempty"`    // LOCAL-ONLY diagnostic; never published
```

Change `Classify` to call `WaveOneSchemaV2()` and decode into a struct with `Session map[string]string`, `Topics []string`, `Summary string` alongside the flat facets, then:

```go
	a.Session = map[Facet]string{}
	for _, f := range sessionFacets {
		v := one.Session[string(f)]
		if err := validate(f, v); err != nil {
			a.Err = "session: " + err.Error()
			return a
		}
		a.Session[f] = v
	}
	a.RawTopics = one.Topics
	a.Topics, _ = VerifyTopics(one.Topics, Render(w)+"\n"+renderDigest(w))
	a.Summary = one.Summary
```

Add `renderDigest(w Window) string` to `window.go`, formatting `w.Digest` exactly as `Render` formats `w.Turns`. Verification must run against digest **and** recent window, since a topic may only appear in the older part of the session.

Update `TestClassifyParsesBothWaves` in `llama_test.go` so its wave-1 stub reply includes `"session"`, `"topics"` and `"summary"` — otherwise strict decoding of required fields fails.

- [ ] **Step 8: Run the full package suite**

Run: `go test ./internal/agent/enrich/llmstudy/ -v && go test ./... 2>&1 | tail -10`
Expected: PASS; no new repo-wide failures.

- [ ] **Step 9: Report topic pass rate**

Add to `report.go`:

```go
// TopicPassRate is the share of emitted topic terms that survived substring
// verification. A low rate means the model is paraphrasing rather than naming
// what is actually in the conversation.
func TopicPassRate(r Run) float64 {
	var raw, kept int
	for _, a := range r.Answers {
		if !a.Valid {
			continue
		}
		raw += len(a.RawTopics)
		kept += len(a.Topics)
	}
	if raw == 0 {
		return 0
	}
	return float64(kept) / float64(raw)
}
```

Add a test asserting `TopicPassRate` on a `Run` with `RawTopics` of 4 and `Topics` of 3 returns 0.75, and that an all-invalid run returns 0.

- [ ] **Step 10: Commit**

```bash
git add internal/agent/enrich/llmstudy/
git commit -m "feat(llmstudy): session tier, verified topic terms, local-only summary"
```

---

### Task 10: Execute the study and write the results doc

**Files:**
- Create: `docs/superpowers/plans/2026-08-07-llm-classifier-study-results.md`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: the results document and a recommendation.

- [ ] **Step 1: Mine the corpus**

```bash
go run ./cmd/keld-agent study mine --n 200 --k 8 --seed 7
```
Expected: reports transcripts scanned, windows found, 200 sampled. If fewer than 200 eligible windows exist, record the actual number — do not silently proceed as if N=200.

- [ ] **Step 2: Run the control arm**

Start the production sidecar, then:
```bash
go run ./cmd/keld-agent study run --arm gliner2 --backend http://127.0.0.1:<sidecar-port>
```
Record validity rate and latency percentiles from the command's output.

- [ ] **Step 3: Run arm A (Qwen3-4B)**

With `llama-server` serving Qwen3-4B on 8080:
```bash
go run ./cmd/keld-agent study run --arm qwen3-4b --backend http://127.0.0.1:8080
```

- [ ] **Step 4: Check the latency kill criterion before continuing**

If arm A's p95 exceeds 30,000 ms, record it and note in the results doc that the arm is **not integrable** at K=8 on this host. Continue the study for its quality signal, but the recommendation must reflect the cost.

- [ ] **Step 5: Run arm B (Qwen3-1.7B)**

Restart `llama-server` with the 1.7B GGUF, then:
```bash
go run ./cmd/keld-agent study run --arm qwen3-1.7b --backend http://127.0.0.1:8080
```

- [ ] **Step 6: Run arm C (gliner-guard-omni)**

Point the sidecar at the guard model and run:
```bash
SIDECAR_MODEL=hivetrace/gliner-guard-omni  # restart the sidecar with this
go run ./cmd/keld-agent study run --arm guard-omni --backend http://127.0.0.1:<sidecar-port>
```
If the model fails to load under the `gliner2` library, record the failure and drop the arm rather than substituting a different model.

- [ ] **Step 7: Build the blinded adjudication set**

```bash
go run ./cmd/keld-agent study adjudicate --control gliner2 --seed 7
```
Expected: an item count (anticipate 40–80 for the primary facets). Report the count.

- [ ] **Step 8: Adjudicate**

Open `~/.keld/study/items.json` and fill each item's `"choice"` with an option `key`, `"tie"`, or `"both_wrong"`. **Do not open `provenance.json`.** This step is the human's; do not fabricate choices under any circumstances.

- [ ] **Step 9: Produce the report**

```bash
go run ./cmd/keld-agent study report --control gliner2
```

- [ ] **Step 10: Write the results document**

Create `docs/superpowers/plans/2026-08-07-llm-classifier-study-results.md` recording:
- exact model files, quantisations, `llama-server` flags, sidecar model, host (20 cores / 30.8 GB), date
- N mined, N adjudicated, per-facet item counts
- the report table (win/loss/tie, win rate, Wilson CI per facet per arm)
- per-arm validity rate, latency p50/p95/max, peak RSS
- **facets where `both_wrong` was common — flagged as a label-vocabulary problem, not a model ranking**
- a recommendation against the pre-registered kill criteria: pursue integration, pursue with a different arm, or stop. **A null result is the finding; state it plainly.**

- [ ] **Step 11: Commit**

```bash
git add docs/superpowers/plans/2026-08-07-llm-classifier-study-results.md
git commit -m "docs(llmstudy): study results and recommendation"
```

---

## Self-Review

**Spec coverage.** Window miner with tool uses + code elision → Task 1. K=8 → Global Constraints + Task 1. Prompt contract with enums read from `labels.go` → Task 2. Arms A/B on `llama.cpp` with constrained decoding → Tasks 3, 8. Control and arm C on production input, with the head-truncation rationale → Task 4. N=200 → Task 7 (`Sample`) + Task 9. Disagreement-only blinded adjudication → Task 5. Win/loss/tie with CIs, latency, validity → Task 6. Kill criteria → Task 9 Steps 4 and 10. Do-not-touch constraints → Global Constraints. Isolation from `feat/multiturn-context` → Global Constraints + Task 7 Step 1 (`evalcmd.go` untouched).

**Known gaps, recorded rather than hidden:**
- **Peak RSS is not instrumented in code.** Tasks 6 and 10 ask for it, but nothing measures it. Capture it externally during Task 10 (e.g. `/usr/bin/time -v` on `llama-server`, or sample `/proc/<pid>/status`) and record the method in the results doc.
- **Session tier and topic terms have no control**, by construction — GLiNER2 cannot produce them. They are reported with verification pass rate and human quality review, never as win/loss. Do not let them leak into the adjudication set.
- **The free-text summary is a diagnostic, not a candidate output.** If the study recommends integration, publishing the summary is explicitly *not* part of that recommendation without a separate privacy design.
- **Topic verification is substring-only.** It proves a term *occurred*; it does not prove the model used it meaningfully. That is what the human quality review is for.
- **Arm C's model may not load** under the `gliner2` library, and its licence is unresolved (HF card 307M/Apache-2.0 vs paper 209M/mmBERT-small/CC BY 4.0). Task 9 Step 6 drops the arm rather than substituting.
- **`Tallies` credits a loss to every non-chosen arm** on a decided item. With multiple arms disagreeing in different directions this is the intended reading (each arm is scored pairwise against the control), but where three arms produce three distinct labels the "loss" attribution is coarse. Report per-arm pairwise counts and note this in the results doc if it arises.

**Type consistency.** `Answer.Labels` is `map[Facet]string` throughout (Tasks 3, 4). `Facet` values match the JSON property names in Task 2's schema and the `Item.Facet` strings in Task 5. `Run{Arm, Answers}` is identical in Tasks 5, 6, 7. `itemKey(id, facet)` is defined once in `differ.go` and used by `report.go`. `defsFor` is defined in `schema.go` and used by `llama.go` (`validate`) and `differ.go` (`descFor`).
