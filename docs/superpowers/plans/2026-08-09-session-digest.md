# Per-Session Work Digest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an LLM-maintained, schema-guaranteed session digest (done / happened / insights / current / why / next / unresolved) with iterative refinement, SQLite snapshot storage, and a pre-registered verification harness.

**Architecture:** A `digest` package inside `internal/agent/enrich/llmstudy` reusing the study's `Llama` client, `Signals`, `Outcomes` and the verbatim gate. `digest(n) = LLM(digest(n-1) + turns since + deterministic counts)` keeps context bounded regardless of session length. Snapshots persist to SQLite following `internal/spool/db.go`'s conventions.

**Tech Stack:** Go (host toolchain), `modernc.org/sqlite` (already a dependency, `CGO_ENABLED=0` enforced by `make crosscheck`), `llama-server` with `response_format: json_schema`.

**Spec:** `docs/superpowers/specs/2026-08-09-session-digest-design.md`

## Global Constraints

- **Task 1 gates everything.** It is verification test 6 — does a budget-fitting model write acceptable prose? Free generation is the class where Qwen3-0.6B mode-collapsed (100% "moderate"). If prose needs the 4B, its **5,192 MB resting** breaks the stated **<= 3 GB** budget and the product decision changes. **Do not tune prompts before Task 1 reports.**
- **Deployable config**, unchanged from the sibling study: `--device none --ctx-size 4096 --parallel 1 --batch-size 256 --ubatch-size 64 --cache-ram 512 --threads 2 --chat-template-kwargs '{"enable_thinking":false}'`. `--cache-ram` and `ctx 4096` are non-negotiable (defaults cost 9 GB; ctx 2048 silently dropped 1.5% of windows).
- **Domain-neutral: no schema field, prompt line, or test may name code, tests, or deploys.** The spine is `function_guess`.
- **`Signals.CodeBlocks` and `Signals.CodeLines` must NOT feed the digest's deterministic facts** — structurally zero for a copywriter, so they would score non-engineering work as trivial.
- **Never `pkill` llama-server.** Kill by recorded PID. Pattern-killing has twice torn down a server under a running job in this workspace, and `pkill -f` also matches the invoking shell.
- **RSS readings require a sustained run.** The plateau needs ~60 s and dozens of requests; short probes are worthless and have produced three wrong conclusions here.
- All work in worktree `/home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study` on branch `feat/llm-classify-study`.
- `go test ./...` must stay green without any model present: live work goes behind the `llmstudy` build tag.
- Commit after every task.

## File Structure

| File | Responsibility |
|---|---|
| `internal/agent/enrich/llmstudy/digest.go` | `Digest` type, schema, create + update prompts, `Llama.Digest*` methods |
| `internal/agent/enrich/llmstudy/digest_test.go` | Schema strictness, prompt content, parse/validate, domain-neutrality guard |
| `internal/agent/enrich/llmstudy/digest_facts.go` | `DigestFacts`: the deterministic counts handed to the model |
| `internal/agent/enrich/llmstudy/digest_facts_test.go` | Fact derivation; excludes code_* |
| `internal/agent/enrich/llmstudy/digest_refine.go` | `Refine` loop: bounded sections, append-only insights, carry-forward |
| `internal/agent/enrich/llmstudy/digest_refine_test.go` | Growth bounds, insight retention, contradiction handling |
| `internal/agent/enrich/llmstudy/digest_check.go` | Verification metrics: identifier gate, rubberstamp check, retention |
| `internal/agent/enrich/llmstudy/digest_check_test.go` | Each metric on synthetic inputs |
| `internal/agent/enrich/llmstudy/digeststore/store.go` | SQLite snapshot store |
| `internal/agent/enrich/llmstudy/digeststore/store_test.go` | Round-trip, ordering, file mode, concurrency |
| `internal/agent/enrich/llmstudy/digest_eval_test.go` | `//go:build llmstudy` — live harness for tests 1-4 and 6 |

---

### Task 1: Model-sizing gate (verification test 6)

**Files:**
- Create: `internal/agent/enrich/llmstudy/digest.go`
- Create: `internal/agent/enrich/llmstudy/digest_test.go`
- Create: `internal/agent/enrich/llmstudy/digest_eval_test.go`

**Interfaces:**
- Consumes: `Llama.call` (unexported, same package), `Window`, `Render`, `Mine`, `MineOpts` from the study.
- Produces: `Digest` struct, `DigestSchema() map[string]any`, `DigestCreatePrompt(sessionLabel string, turns string, facts string) string`, `(*Llama).CreateDigest(sessionLabel, turns, facts string) (Digest, error)`.

**Why first:** this is the load-bearing risk. Everything downstream assumes a budget-fitting model can write usable prose, and that assumption is untested.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/llmstudy/digest_test.go`:

```go
package llmstudy

import (
	"strings"
	"testing"
)

func TestDigestSchemaIsStrictAndComplete(t *testing.T) {
	s := DigestSchema()
	if s["additionalProperties"] != false {
		t.Error("schema must be strict so the model cannot invent sections")
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties")
	}
	want := []string{"done", "happened", "insights", "current", "why", "next", "unresolved"}
	for _, f := range want {
		if _, ok := props[f]; !ok {
			t.Errorf("schema missing required section %q", f)
		}
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != len(want) {
		t.Fatalf("required = %v, want all %d sections", s["required"], len(want))
	}
	// insights and unresolved are lists; the rest are prose.
	for _, f := range []string{"insights", "unresolved"} {
		if props[f].(map[string]any)["type"] != "array" {
			t.Errorf("%s must be an array", f)
		}
	}
	for _, f := range []string{"done", "happened", "current", "why", "next"} {
		if props[f].(map[string]any)["type"] != "string" {
			t.Errorf("%s must be a string", f)
		}
	}
}

// unresolved exists to defeat rubberstamping structurally: a required field the
// model must address means an all-positive report cannot validate.
func TestUnresolvedIsRequired(t *testing.T) {
	req := DigestSchema()["required"].([]string)
	for _, r := range req {
		if r == "unresolved" {
			return
		}
	}
	t.Fatal("unresolved must be required, or an all-positive report validates")
}

// The digest must serve accountants and marketers, not only engineers.
func TestDigestPromptIsDomainNeutral(t *testing.T) {
	p := DigestCreatePrompt("finance / invoicing", "user: reconcile the ledger\n", "turns=4 corrections=0")
	banned := []string{"code", "codebase", "test suite", "deploy", "commit", "repository", "compile"}
	low := strings.ToLower(p)
	for _, b := range banned {
		if strings.Contains(low, b) {
			t.Errorf("prompt mentions %q — not domain-neutral", b)
		}
	}
	for _, want := range []string{"reconcile the ledger", "turns=4", "unresolved"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt omits %q", want)
		}
	}
}

// The deterministic counts must be presented as binding, or the prose can contradict
// them and rubberstamping becomes unmeasurable.
func TestDigestPromptTreatsFactsAsAuthoritative(t *testing.T) {
	p := DigestCreatePrompt("x", "user: hi\n", "turns=9 corrections=3")
	for _, want := range []string{"must be consistent", "corrections=3"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt must bind the prose to the counts; missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Digest -v`
Expected: FAIL — `undefined: DigestSchema`.

- [ ] **Step 3: Write the implementation**

Create `internal/agent/enrich/llmstudy/digest.go`:

```go
package llmstudy

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DigestSchemaVersion gates the digest's shape. Bump on any field change: stored
// snapshots record it so a refine loop never mixes shapes.
const DigestSchemaVersion = 1

// Digest is a semi-structured report of what a session has been about.
//
// The structure is guaranteed by constrained decoding; the prose inside each field
// is free. That is what "semi-structured" buys: a malformed report is impossible,
// while the writing stays useful.
//
// Unresolved is required for a reason. Rubberstamping — reporting smooth progress on
// work that was corrected and abandoned — thrives when a format has nowhere to put
// failure. A required field the model must address means an all-positive report
// cannot validate, which is a guarantee where "please be honest" is a hope.
type Digest struct {
	Done       string   `json:"done"`
	Happened   string   `json:"happened"`
	Insights   []string `json:"insights"`
	Current    string   `json:"current"`
	Why        string   `json:"why"`
	Next       string   `json:"next"`
	Unresolved []string `json:"unresolved"`
}

// DigestSchema is the JSON schema every digest must satisfy.
func DigestSchema() map[string]any {
	str := map[string]any{"type": "string"}
	list := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":       str,
			"happened":   str,
			"insights":   list,
			"current":    str,
			"why":        str,
			"next":       str,
			"unresolved": list,
		},
		"required":             []string{"done", "happened", "insights", "current", "why", "next", "unresolved"},
		"additionalProperties": false,
	}
}

// digestSections describes each field to the model. Deliberately profession-neutral:
// no mention of code, tests or deploys, because the same digest must serve an
// accountant reconciling ledgers and a marketer drafting a campaign.
const digestSections = `  done        What has been accomplished so far.
  happened    What actually occurred, including anything that went wrong,
              was reversed, or did not work.
  insights    Key thoughts and learnings. One per entry. Only things a reader
              could not infer from the bare facts.
  current     What is being worked on right now.
  why         The reason it is being done.
  next        Where this is going.
  unresolved  What is still open, blocked, or was abandoned. One per entry.
`

const digestRules = `
Rules:
  - Report only what the conversation supports. If something is not stated, do not
    assert it. Absence of evidence is itself worth reporting.
  - The COUNTS below are measured facts and your report must be consistent with
    them. If corrections occurred, the conversation did not go smoothly and the
    report must say so.
  - Name specifics (files, systems, people, amounts) only when they appear in the
    conversation. Do not invent plausible ones.
  - unresolved must be addressed. If genuinely nothing is open, say so explicitly
    rather than leaving it empty for convenience.
`

// DigestCreatePrompt builds the first-digest prompt.
func DigestCreatePrompt(sessionLabel, turns, facts string) string {
	var b strings.Builder
	b.WriteString("You are writing a short report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	b.WriteString("Session context: ")
	b.WriteString(sessionLabel)
	b.WriteString("\n\nMEASURED COUNTS (authoritative — your report must be consistent with these):\n  ")
	b.WriteString(facts)
	b.WriteString("\n\nCONVERSATION:\n")
	b.WriteString(turns)
	b.WriteString("\nWrite these sections:\n")
	b.WriteString(digestSections)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// CreateDigest produces the first digest for a session.
func (l *Llama) CreateDigest(sessionLabel, turns, facts string) (Digest, error) {
	var d Digest
	if err := l.call(DigestCreatePrompt(sessionLabel, turns, facts), DigestSchema(), &d); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// DigestJSON renders a digest for embedding in a refine prompt.
func DigestJSON(d Digest) string {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\":%q}", err.Error())
	}
	return string(b)
}

// digestElapsed is a tiny helper so callers can time a digest call without
// importing time at every site.
func digestElapsed(start time.Time) int64 { return time.Since(start).Milliseconds() }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -run Digest -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Write the live sizing harness**

Create `internal/agent/enrich/llmstudy/digest_eval_test.go`:

```go
//go:build llmstudy

// Live digest harness. Requires a llama-server.
//
//	DIGEST_URL=http://127.0.0.1:8095 go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run DigestSizing -v -timeout 60m
package llmstudy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDigestSizing is verification test 6: can a budget-fitting model write a
// usable digest at all? Free generation is the capability class where Qwen3-0.6B
// collapsed to a single value, so this must be answered before any prompt tuning.
func TestDigestSizing(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8095"
	}
	root := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no transcripts")
	}

	o := DefaultMineOpts()
	o.K = 12 // a wider window than classification uses: a digest needs more material
	l := NewLlama(url)

	tried, ok := 0, 0
	for _, f := range files {
		if tried >= 5 {
			break
		}
		ws, err := Mine(f, o)
		if err != nil || len(ws) < 6 {
			continue
		}
		w := ws[len(ws)-1]
		sig := Extract(w)
		facts := DigestFactsLine(sig)
		tried++

		d, err := l.CreateDigest("software / engineering", Render(w), facts)
		if err != nil {
			t.Errorf("[%d] call failed: %v", tried, err)
			continue
		}
		problems := ValidateDigest(d)
		if len(problems) > 0 {
			t.Errorf("[%d] malformed: %v", tried, problems)
			continue
		}
		ok++
		t.Logf("--- digest %d (%s) ---", tried, filepath.Base(f))
		t.Logf("  done:       %s", clipLog(d.Done))
		t.Logf("  happened:   %s", clipLog(d.Happened))
		t.Logf("  current:    %s", clipLog(d.Current))
		t.Logf("  why:        %s", clipLog(d.Why))
		t.Logf("  next:       %s", clipLog(d.Next))
		t.Logf("  insights:   %d entries; first: %s", len(d.Insights), clipLog(firstOr(d.Insights)))
		t.Logf("  unresolved: %d entries; first: %s", len(d.Unresolved), clipLog(firstOr(d.Unresolved)))
		t.Logf("  facts given: %s", facts)
	}
	t.Logf("structural validity: %d/%d", ok, tried)
	if tried > 0 && ok != tried {
		t.Errorf("threshold 1 requires 100%% structural validity, got %d/%d", ok, tried)
	}
}

func clipLog(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 220 {
		return s[:220] + "…"
	}
	return s
}

func firstOr(v []string) string {
	if len(v) == 0 {
		return "(none)"
	}
	return v[0]
}
```

- [ ] **Step 6: Add the validator and facts line this harness needs**

Append to `internal/agent/enrich/llmstudy/digest.go`:

```go
// ValidateDigest reports structural problems a schema cannot express: empty prose
// where prose is required, and an unaddressed unresolved list.
//
// The unresolved check accepts an explicit "nothing is open" entry but rejects an
// empty list, because an empty list is exactly what a rubberstamping model produces.
func ValidateDigest(d Digest) []string {
	var p []string
	for _, f := range []struct {
		name, val string
	}{
		{"done", d.Done}, {"happened", d.Happened},
		{"current", d.Current}, {"why", d.Why}, {"next", d.Next},
	} {
		if strings.TrimSpace(f.val) == "" {
			p = append(p, f.name+" is empty")
		}
	}
	if len(d.Unresolved) == 0 {
		p = append(p, "unresolved is empty — it must be addressed explicitly")
	}
	return p
}
```

- [ ] **Step 7: Run the sizing test on the 1.7B, then the 4B**

```bash
S=/tmp/claude-1000/-home-dg-keld-keld-signal/*/scratchpad   # session scratchpad
G=$HOME/.keld/models/gguf
# 1.7B — the budget-fitting candidate
nohup llama-server -m $G/Qwen3-1.7B-Q4_K_M.gguf --ctx-size 4096 --parallel 1 \
  --threads 8 --batch-size 256 --ubatch-size 64 --cache-ram 512 --port 8095 \
  --no-warmup --jinja --device none \
  --chat-template-kwargs '{"enable_thinking":false}' > /tmp/dg17.log 2>&1 &
echo $! > /tmp/dg17.pid
until curl -s -m 2 http://127.0.0.1:8095/health >/dev/null; do sleep 2; done
DIGEST_URL=http://127.0.0.1:8095 go test -tags llmstudy \
  ./internal/agent/enrich/llmstudy/ -run DigestSizing -v -timeout 60m
awk '/^VmHWM/{printf "1.7B peak %d MB\n", int($2/1024)}' /proc/$(cat /tmp/dg17.pid)/status
kill $(cat /tmp/dg17.pid)          # BY PID. Never pkill.
```

Then repeat with `Qwen3-4B-Instruct-2507-Q4_K_M.gguf` on port 8096 as the quality
ceiling. **Read the printed digests.** The gate is not a number — it is whether a
manager could learn anything from them.

- [ ] **Step 8: Record the verdict**

Append a "Digest sizing" section to
`docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md` with: the
structural-validity fraction per model, peak RSS per model, and a plain judgement on
whether the 1.7B prose is usable. **If the 1.7B is not usable, stop and report** —
the budget or the feature scope has to change, and further tasks are premature.

- [ ] **Step 9: Commit**

```bash
git add internal/agent/enrich/llmstudy/digest.go \
        internal/agent/enrich/llmstudy/digest_test.go \
        internal/agent/enrich/llmstudy/digest_eval_test.go \
        docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md
git commit -m "feat(digest): schema, create prompt, and the model-sizing gate"
```

---

### Task 2: Deterministic facts

**Files:**
- Create: `internal/agent/enrich/llmstudy/digest_facts.go`
- Create: `internal/agent/enrich/llmstudy/digest_facts_test.go`

**Interfaces:**
- Consumes: `Signals` and `Extract(Window) Signals` (existing), `Outcome` (existing).
- Produces: `DigestFacts` struct, `FactsFrom(sig Signals, oc []Outcome) DigestFacts`, `DigestFactsLine(sig Signals) string`, `(DigestFacts).Line() string`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import (
	"strings"
	"testing"
)

// Code counts are structurally zero for a copywriter or accountant. Including them
// would score all non-engineering work as trivial, so they must not appear.
func TestDigestFactsExcludeCodeCounts(t *testing.T) {
	sig := Signals{Turns: 9, UserTurns: 3, ToolCalls: 12, ToolVariety: 4,
		Corrections: 2, CodeBlocks: 7, CodeLines: 300, AssistantChars: 5000}
	line := DigestFactsLine(sig)
	for _, banned := range []string{"code_blocks", "code_lines", "300", "=7"} {
		if strings.Contains(line, banned) {
			t.Errorf("facts line leaks engineering-specific count %q: %s", banned, line)
		}
	}
	for _, want := range []string{"turns=9", "user_turns=3", "corrections=2"} {
		if !strings.Contains(line, want) {
			t.Errorf("facts line missing %q: %s", want, line)
		}
	}
}

// A correction count is the anti-rubberstamping lever, so it must always be present
// even when zero — an absent field lets the model assume the happy path.
func TestDigestFactsAlwaysStatesCorrections(t *testing.T) {
	line := DigestFactsLine(Signals{Turns: 4, UserTurns: 1})
	if !strings.Contains(line, "corrections=0") {
		t.Fatalf("corrections must be stated even at zero: %s", line)
	}
}

func TestFactsFromCountsOutcomes(t *testing.T) {
	f := FactsFrom(
		Signals{Turns: 10, UserTurns: 4, ToolCalls: 20, ToolVariety: 5, Corrections: 1},
		[]Outcome{{Corrected: true}, {Corrected: false}, {Terminal: true}},
	)
	if f.CorrectedTurns != 1 {
		t.Errorf("CorrectedTurns = %d, want 1", f.CorrectedTurns)
	}
	if f.Turns != 10 || f.UserTurns != 4 {
		t.Errorf("counts not carried: %+v", f)
	}
	if !strings.Contains(f.Line(), "corrected_turns=1") {
		t.Errorf("Line() omits corrected_turns: %s", f.Line())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run DigestFacts -v`
Expected: FAIL — `undefined: DigestFactsLine`.

- [ ] **Step 3: Write the implementation**

```go
package llmstudy

import "fmt"

// DigestFacts are the measured counts handed to the model as authoritative.
//
// They exist to make rubberstamping detectable: if corrections occurred and the
// prose claims smooth progress, the two disagree and the disagreement is
// machine-checkable. Counts cannot be flattered.
//
// Deliberately excludes CodeBlocks and CodeLines. Those are structurally zero for a
// copywriter or an accountant, so feeding them in would systematically score
// non-engineering work as trivial — the digest must serve every profession.
type DigestFacts struct {
	Turns          int
	UserTurns      int
	ToolCalls      int
	ToolVariety    int
	Corrections    int // user turns that pushed back, within the window
	CorrectedTurns int // turns whose NEXT user message pushed back (from Outcome)
}

// FactsFrom derives the facts from a window's signals and its outcomes.
func FactsFrom(sig Signals, oc []Outcome) DigestFacts {
	f := DigestFacts{
		Turns:       sig.Turns,
		UserTurns:   sig.UserTurns,
		ToolCalls:   sig.ToolCalls,
		ToolVariety: sig.ToolVariety,
		Corrections: sig.Corrections,
	}
	for _, o := range oc {
		if o.Corrected {
			f.CorrectedTurns++
		}
	}
	return f
}

// Line renders the facts for the prompt. Every field is always present — an absent
// count would let the model assume the happy path.
func (f DigestFacts) Line() string {
	return fmt.Sprintf(
		"turns=%d user_turns=%d tool_calls=%d tool_variety=%d corrections=%d corrected_turns=%d",
		f.Turns, f.UserTurns, f.ToolCalls, f.ToolVariety, f.Corrections, f.CorrectedTurns)
}

// DigestFactsLine is the convenience path when only Signals are available.
func DigestFactsLine(sig Signals) string { return FactsFrom(sig, nil).Line() }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'DigestFacts|FactsFrom' -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/digest_facts.go internal/agent/enrich/llmstudy/digest_facts_test.go
git commit -m "feat(digest): deterministic facts, excluding engineering-specific counts"
```

---

### Task 3: Refine loop

**Files:**
- Create: `internal/agent/enrich/llmstudy/digest_refine.go`
- Create: `internal/agent/enrich/llmstudy/digest_refine_test.go`

**Interfaces:**
- Consumes: `Digest`, `DigestSchema`, `DigestJSON`, `ValidateDigest` (Task 1); `DigestFacts` (Task 2); `Llama.call`.
- Produces: `DigestUpdatePrompt(prev Digest, sessionLabel, newTurns, facts string) string`, `(*Llama).RefineDigest(prev Digest, sessionLabel, newTurns, facts string) (Digest, error)`, `MergeInsights(prev, next Digest) Digest`, `CapSections(d Digest, maxProse, maxList int) Digest`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import (
	"strings"
	"testing"
)

// Insights are append-only: repeated re-summarising is what destroys the most
// valuable content, so earlier entries carry forward verbatim.
func TestMergeInsightsIsAppendOnlyAndDeduped(t *testing.T) {
	prev := Digest{Insights: []string{"retry loop was the bottleneck", "staging mirrors prod"}}
	next := Digest{Insights: []string{"staging mirrors prod", "the vendor rate-limits at 100/s"}}
	got := MergeInsights(prev, next)
	if len(got.Insights) != 3 {
		t.Fatalf("want 3 insights (2 prior + 1 new), got %d: %v", len(got.Insights), got.Insights)
	}
	if got.Insights[0] != "retry loop was the bottleneck" {
		t.Errorf("earlier insight must survive verbatim and first, got %q", got.Insights[0])
	}
}

func TestMergeInsightsKeepsUnresolvedFromTheNewDigest(t *testing.T) {
	prev := Digest{Unresolved: []string{"waiting on vendor"}}
	next := Digest{Unresolved: []string{"nothing open"}}
	// Unresolved is CURRENT state, not history: the new answer replaces the old.
	if got := MergeInsights(prev, next); len(got.Unresolved) != 1 || got.Unresolved[0] != "nothing open" {
		t.Fatalf("unresolved must reflect current state, got %v", got.Unresolved)
	}
}

// Unbounded growth would eventually blow the context the refine loop exists to bound.
func TestCapSectionsBoundsProseAndLists(t *testing.T) {
	long := strings.Repeat("word ", 500)
	d := Digest{
		Done: long, Happened: long, Current: long, Why: long, Next: long,
		Insights:   make([]string, 40),
		Unresolved: make([]string, 40),
	}
	for i := range d.Insights {
		d.Insights[i] = "insight"
		d.Unresolved[i] = "open item"
	}
	got := CapSections(d, 400, 12)
	for name, v := range map[string]string{
		"done": got.Done, "happened": got.Happened, "current": got.Current,
		"why": got.Why, "next": got.Next,
	} {
		if len([]rune(v)) > 400 {
			t.Errorf("%s not capped: %d runes", name, len([]rune(v)))
		}
	}
	if len(got.Insights) != 12 || len(got.Unresolved) != 12 {
		t.Errorf("lists not capped: insights=%d unresolved=%d", len(got.Insights), len(got.Unresolved))
	}
}

// Capping insights must drop the OLDEST, since recent ones are likeliest to matter —
// but the test pins the direction so a future change is deliberate.
func TestCapSectionsDropsOldestInsights(t *testing.T) {
	d := Digest{Insights: []string{"first", "second", "third"}}
	got := CapSections(d, 100, 2)
	if len(got.Insights) != 2 || got.Insights[0] != "second" {
		t.Fatalf("want the two most recent, got %v", got.Insights)
	}
}

func TestUpdatePromptCarriesPriorDigestAndDemandsRevision(t *testing.T) {
	prev := Digest{Done: "reconciled March", Insights: []string{"ledger totals disagree"}}
	p := DigestUpdatePrompt(prev, "finance / invoicing", "user: now do April\n", "turns=4 corrections=0")
	for _, want := range []string{"reconciled March", "ledger totals disagree", "now do April", "state what changed"} {
		if !strings.Contains(p, want) {
			t.Errorf("update prompt omits %q", want)
		}
	}
	// Recency bias guard: the prompt must instruct preservation of earlier material.
	if !strings.Contains(p, "unless it is contradicted") {
		t.Error("update prompt must instruct carry-forward of earlier material")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'MergeInsights|CapSections|UpdatePrompt' -v`
Expected: FAIL — `undefined: MergeInsights`.

- [ ] **Step 3: Write the implementation**

```go
package llmstudy

import "strings"

// DigestUpdatePrompt builds the refinement prompt.
//
// Refine loops fail in four known ways and this prompt addresses three of them:
// recency bias (carry earlier material forward unless contradicted), silent
// contradiction (state what changed), and drift (insights are merged in code, not
// re-prosed by the model). The fourth, unbounded growth, is handled by CapSections.
func DigestUpdatePrompt(prev Digest, sessionLabel, newTurns, facts string) string {
	var b strings.Builder
	b.WriteString("You are updating an existing report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	b.WriteString("Session context: ")
	b.WriteString(sessionLabel)
	b.WriteString("\n\nEXISTING REPORT:\n")
	b.WriteString(DigestJSON(prev))
	b.WriteString("\n\nMEASURED COUNTS for the whole session so far (authoritative — your report must be consistent with these):\n  ")
	b.WriteString(facts)
	b.WriteString("\n\nNEW PART OF THE CONVERSATION, since the report above:\n")
	b.WriteString(newTurns)
	b.WriteString("\nProduce the UPDATED report, same sections:\n")
	b.WriteString(digestSections)
	b.WriteString(`
Updating rules:
  - Keep what the existing report says unless it is contradicted by the new part.
  - Where something changed, revise it in place and state what changed.
  - insights: add only genuinely new learnings. Do not restate existing ones.
  - unresolved must describe the CURRENT state: drop what is now closed, add what
    has newly opened.
`)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// RefineDigest produces the next digest, then merges insights and caps growth.
func (l *Llama) RefineDigest(prev Digest, sessionLabel, newTurns, facts string) (Digest, error) {
	var next Digest
	if err := l.call(DigestUpdatePrompt(prev, sessionLabel, newTurns, facts), DigestSchema(), &next); err != nil {
		return Digest{}, err
	}
	return CapSections(MergeInsights(prev, next), DefaultProseCap, DefaultListCap), nil
}

// Default caps. Prose is per-section runes; lists are entry counts.
const (
	DefaultProseCap = 900
	DefaultListCap  = 12
)

// MergeInsights carries prior insights forward verbatim and appends genuinely new
// ones, deduplicated case-insensitively.
//
// This is the drift mitigation, and it is done in code rather than asked of the
// model on purpose: repeated re-summarising is exactly what erodes the most
// valuable content, so the model never gets the chance to re-word an old insight.
// Unresolved is NOT merged — it describes current state, so the new answer wins.
func MergeInsights(prev, next Digest) Digest {
	out := next
	seen := map[string]bool{}
	merged := make([]string, 0, len(prev.Insights)+len(next.Insights))
	add := func(s string) {
		t := strings.TrimSpace(s)
		if t == "" {
			return
		}
		k := strings.ToLower(t)
		if seen[k] {
			return
		}
		seen[k] = true
		merged = append(merged, t)
	}
	for _, s := range prev.Insights {
		add(s)
	}
	for _, s := range next.Insights {
		add(s)
	}
	out.Insights = merged
	return out
}

// CapSections bounds prose length and list size so a long session cannot grow the
// digest past the context the refine loop exists to keep bounded.
func CapSections(d Digest, maxProse, maxList int) Digest {
	d.Done = clip(d.Done, maxProse)
	d.Happened = clip(d.Happened, maxProse)
	d.Current = clip(d.Current, maxProse)
	d.Why = clip(d.Why, maxProse)
	d.Next = clip(d.Next, maxProse)
	d.Insights = tailN(d.Insights, maxList)
	d.Unresolved = tailN(d.Unresolved, maxList)
	return d
}

// tailN keeps the last n entries — the most recent, since older insights have
// already been carried through several refinements and newer ones have not.
func tailN(v []string, n int) []string {
	if n <= 0 || len(v) <= n {
		return v
	}
	return v[len(v)-n:]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'MergeInsights|CapSections|UpdatePrompt' -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/digest_refine.go internal/agent/enrich/llmstudy/digest_refine_test.go
git commit -m "feat(digest): refine loop with append-only insights and bounded growth"
```

---

### Task 4: Verification metrics

**Files:**
- Create: `internal/agent/enrich/llmstudy/digest_check.go`
- Create: `internal/agent/enrich/llmstudy/digest_check_test.go`

**Interfaces:**
- Consumes: `Digest` (Task 1), `DigestFacts` (Task 2), `VerifyTopics` (existing).
- Produces: `Identifiers(d Digest) []string`, `UnverifiedIdentifiers(d Digest, source string) []string`, `LooksRubberstamped(d Digest, f DigestFacts) bool`, `RetainedFacts(before, after Digest, facts []string) int`.

- [ ] **Step 1: Write the failing test**

```go
package llmstudy

import "testing"

// The identifier gate bounds fabricated SPECIFICS. It cannot bound fabricated
// judgements, which is why the spec keeps a human review gate.
func TestUnverifiedIdentifiersFlagsInventedSpecifics(t *testing.T) {
	src := "user: reconcile the March ledger for Northwind\nassistant: opened ledger-2026-03.csv\n"
	d := Digest{
		Done:     "Reconciled the March ledger for Northwind using ledger-2026-03.csv.",
		Happened: "Also cross-checked against Globex and invoice-99812.pdf.",
	}
	bad := UnverifiedIdentifiers(d, src)
	found := map[string]bool{}
	for _, b := range bad {
		found[b] = true
	}
	if !found["Globex"] || !found["invoice-99812.pdf"] {
		t.Errorf("invented specifics not flagged: %v", bad)
	}
	if found["Northwind"] || found["ledger-2026-03.csv"] {
		t.Errorf("real specifics wrongly flagged: %v", bad)
	}
}

// Rubberstamping: corrections occurred, yet the report names no friction anywhere.
func TestLooksRubberstampedWhenFrictionIsOmitted(t *testing.T) {
	facts := DigestFacts{Turns: 20, UserTurns: 8, Corrections: 3, CorrectedTurns: 2}
	glowing := Digest{
		Done:       "Everything was completed smoothly.",
		Happened:   "All steps succeeded on the first attempt.",
		Unresolved: []string{"nothing is open"},
	}
	if !LooksRubberstamped(glowing, facts) {
		t.Error("a glowing report with 3 corrections must be flagged")
	}
	honest := Digest{
		Done:       "Completed the reconciliation.",
		Happened:   "Two attempts were reversed after the totals disagreed.",
		Unresolved: []string{"April still to do"},
	}
	if LooksRubberstamped(honest, facts) {
		t.Error("a report naming the friction must not be flagged")
	}
}

// With no corrections there is nothing to under-report, so nothing is flagged.
func TestLooksRubberstampedIsSilentWithoutCorrections(t *testing.T) {
	facts := DigestFacts{Turns: 6, UserTurns: 2}
	d := Digest{Done: "Done.", Happened: "Went fine.", Unresolved: []string{"nothing open"}}
	if LooksRubberstamped(d, facts) {
		t.Error("no corrections means no rubberstamping to detect")
	}
}

func TestRetainedFactsCountsSurvivors(t *testing.T) {
	before := Digest{Done: "opened ledger-2026-03.csv and contacted Northwind"}
	after := Digest{Done: "contacted Northwind about the totals"}
	got := RetainedFacts(before, after, []string{"ledger-2026-03.csv", "Northwind"})
	if got != 1 {
		t.Fatalf("RetainedFacts = %d, want 1 (Northwind survived, the file did not)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'Unverified|Rubberstamped|RetainedFacts' -v`
Expected: FAIL — `undefined: UnverifiedIdentifiers`.

- [ ] **Step 3: Write the implementation**

```go
package llmstudy

import (
	"regexp"
	"strings"
)

// identifierPat matches the token shapes worth verifying: dotted/hyphenated
// filenames and identifiers, and capitalised proper nouns. Deliberately narrow —
// a broad pattern would flag ordinary prose and drown the signal.
var identifierPat = regexp.MustCompile(`\b[A-Za-z0-9_]+(?:[.-][A-Za-z0-9_]+)+\b|\b[A-Z][a-zA-Z0-9]{2,}\b`)

// digestStopWords are capitalised words that begin sentences or name generic
// concepts, and would otherwise be flagged as invented proper nouns.
var digestStopWords = map[string]bool{
	"The": true, "This": true, "That": true, "These": true, "Those": true,
	"There": true, "Then": true, "They": true, "Their": true, "It": true,
	"Also": true, "After": true, "Before": true, "Both": true, "Everything": true,
	"All": true, "Two": true, "One": true, "Three": true, "Some": true,
	"Completed": true, "Reconciled": true, "Nothing": true, "Next": true,
	"Currently": true, "Still": true, "Work": true, "April": true, "March": true,
}

// Identifiers extracts the candidate specifics from a digest's prose.
func Identifiers(d Digest) []string {
	var b strings.Builder
	for _, s := range []string{d.Done, d.Happened, d.Current, d.Why, d.Next} {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, s := range d.Insights {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	for _, s := range d.Unresolved {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range identifierPat.FindAllString(b.String(), -1) {
		if digestStopWords[m] || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// UnverifiedIdentifiers returns the specifics that do not appear in the source.
//
// This is the same gate that caught 11 of 11 fabrications by Qwen3-0.6B in the
// classification study, where the invented values were lifted from the prompt's own
// examples. It bounds fabricated SPECIFICS only: a fabricated judgement ("the team
// decided to prioritise X") contains no verifiable token and passes untouched.
func UnverifiedIdentifiers(d Digest, source string) []string {
	_, dropped := VerifyTopics(Identifiers(d), source)
	return dropped
}

// frictionWords are the vocabulary a report uses when it admits difficulty.
var frictionWords = []string{
	"revert", "reverse", "reversed", "fail", "failed", "failing", "broke", "broken",
	"wrong", "incorrect", "retry", "retried", "again", "corrected", "correction",
	"disagree", "mismatch", "blocked", "stuck", "abandoned", "unresolved", "issue",
	"problem", "did not", "didn't", "unable", "reworked", "redo", "backtrack",
}

// LooksRubberstamped reports whether a digest claims a clean run despite measured
// corrections.
//
// This is the metric the whole anti-rubberstamping design is judged by, and it is
// only possible because `corrected` is harvested ground truth rather than an
// opinion. With no corrections there is nothing to under-report, so it stays silent.
func LooksRubberstamped(d Digest, f DigestFacts) bool {
	if f.Corrections == 0 && f.CorrectedTurns == 0 {
		return false
	}
	hay := strings.ToLower(strings.Join(append([]string{
		d.Done, d.Happened, d.Current, d.Why, d.Next,
	}, append(d.Insights, d.Unresolved...)...), " "))
	for _, w := range frictionWords {
		if strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// RetainedFacts counts how many of the given facts still appear after a refinement.
// Used by the drift test: inject known facts, refine, measure survival.
func RetainedFacts(before, after Digest, facts []string) int {
	_ = before // present for call-site symmetry; retention is measured on `after`
	hay := strings.ToLower(strings.Join(append([]string{
		after.Done, after.Happened, after.Current, after.Why, after.Next,
	}, append(after.Insights, after.Unresolved...)...), " "))
	n := 0
	for _, f := range facts {
		if strings.Contains(hay, strings.ToLower(f)) {
			n++
		}
	}
	return n
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/ -run 'Unverified|Rubberstamped|RetainedFacts|Identifiers' -v`
Expected: PASS (4 tests). If `TestUnverifiedIdentifiersFlagsInventedSpecifics` fails on a
stop-word choice, add the offending token to `digestStopWords` — the pattern is meant
to be narrow, and tuning it is expected.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/llmstudy/digest_check.go internal/agent/enrich/llmstudy/digest_check_test.go
git commit -m "feat(digest): verification metrics for hallucination, rubberstamping, drift"
```

---

### Task 5: SQLite snapshot store

**Files:**
- Create: `internal/agent/enrich/llmstudy/digeststore/store.go`
- Create: `internal/agent/enrich/llmstudy/digeststore/store_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks (stores JSON bodies as opaque text, so it does not import `llmstudy` and cannot create an import cycle).
- Produces: `Store` type, `Open(path string) (*Store, error)`, `(*Store).Put(rec Record) error`, `(*Store).Latest(sessionID string) (Record, bool, error)`, `(*Store).History(sessionID string) ([]Record, error)`, `(*Store).Close() error`, `Record` struct.

- [ ] **Step 1: Write the failing test**

```go
package digeststore

import (
	"os"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutAndLatestReturnsHighestSeq(t *testing.T) {
	s := openTemp(t)
	for seq := 1; seq <= 3; seq++ {
		if err := s.Put(Record{
			SessionID: "sess-a", Seq: seq, CreatedTS: int64(1000 + seq),
			SchemaVersion: 1, Model: "qwen3-1.7b",
			FromPromptID:  "p1", ToPromptID: "p9", Turns: seq * 4,
			Signals:       `{"turns":4}`, Body: `{"done":"v` + string(rune('0'+seq)) + `"}`,
		}); err != nil {
			t.Fatalf("Put seq %d: %v", seq, err)
		}
	}
	got, ok, err := s.Latest("sess-a")
	if err != nil || !ok {
		t.Fatalf("Latest: ok=%v err=%v", ok, err)
	}
	if got.Seq != 3 {
		t.Errorf("Seq = %d, want 3", got.Seq)
	}
	if got.Body != `{"done":"v3"}` {
		t.Errorf("Body = %s", got.Body)
	}
}

func TestLatestOnUnknownSessionIsNotAnError(t *testing.T) {
	s := openTemp(t)
	_, ok, err := s.Latest("nope")
	if err != nil {
		t.Fatalf("unknown session must not error: %v", err)
	}
	if ok {
		t.Error("ok must be false for an unknown session")
	}
}

// History powers the drift test, so it must come back in refinement order.
func TestHistoryIsOrderedAscending(t *testing.T) {
	s := openTemp(t)
	for _, seq := range []int{3, 1, 2} {
		if err := s.Put(Record{SessionID: "s", Seq: seq, SchemaVersion: 1,
			Model: "m", FromPromptID: "a", ToPromptID: "b", Signals: "{}", Body: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.History("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h[0].Seq != 1 || h[2].Seq != 3 {
		t.Fatalf("History not ascending: %+v", h)
	}
}

func TestPutIsIdempotentOnSessionAndSeq(t *testing.T) {
	s := openTemp(t)
	rec := Record{SessionID: "s", Seq: 1, SchemaVersion: 1, Model: "m",
		FromPromptID: "a", ToPromptID: "b", Signals: "{}", Body: `{"v":1}`}
	if err := s.Put(rec); err != nil {
		t.Fatal(err)
	}
	rec.Body = `{"v":2}`
	if err := s.Put(rec); err != nil {
		t.Fatalf("re-Put must overwrite, not error: %v", err)
	}
	got, _, _ := s.Latest("s")
	if got.Body != `{"v":2}` {
		t.Errorf("Body = %s, want the rewritten value", got.Body)
	}
}

// The digest holds transcript-derived prose, so the file must not be world-readable.
// Mode is set BEFORE sql.Open because SQLite derives the -wal/-shm sidecars' modes
// from the main file at the moment it creates them.
func TestDatabaseFileIsOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "digest.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Put(Record{SessionID: "s", Seq: 1, SchemaVersion: 1, Model: "m",
		FromPromptID: "a", ToPromptID: "b", Signals: "{}", Body: "{}"}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(p + suffix)
		if err != nil {
			continue // sidecar may not exist yet
		}
		if m := fi.Mode().Perm(); m&0o077 != 0 {
			t.Errorf("%s mode is %o, want owner-only", p+suffix, m)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/enrich/llmstudy/digeststore/ -v`
Expected: FAIL — `undefined: Open`.

- [ ] **Step 3: Write the implementation**

```go
// Package digeststore persists session digests to SQLite.
//
// Snapshots only, with the consumed turn range recorded. There is deliberately no
// delta table: the input delta is fully described by FromPromptID..ToPromptID and
// the transcript is already on disk, so replay needs no duplicate copy of the turns.
// Snapshots plus boundaries give replay, audit and drift measurement at a fraction
// of the size.
package digeststore

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS digest (
  session_id     TEXT    NOT NULL,
  seq            INTEGER NOT NULL,
  created_ts     INTEGER NOT NULL,
  schema_version INTEGER NOT NULL,
  model          TEXT    NOT NULL,
  from_prompt_id TEXT    NOT NULL,
  to_prompt_id   TEXT    NOT NULL,
  turns          INTEGER NOT NULL,
  signals        TEXT    NOT NULL,
  body           TEXT    NOT NULL,
  PRIMARY KEY(session_id, seq)
);
CREATE INDEX IF NOT EXISTS ix_digest_session ON digest(session_id, seq DESC);
`

// Record is one digest snapshot.
type Record struct {
	SessionID     string
	Seq           int
	CreatedTS     int64
	SchemaVersion int
	Model         string
	FromPromptID  string
	ToPromptID    string
	Turns         int
	Signals       string // the deterministic facts given to the model, as JSON
	Body          string // the digest, as JSON
}

// Store is a digest database.
type Store struct{ db *sql.DB }

// Open creates or opens the store.
//
// The file is created 0600 BEFORE sql.Open, because SQLite derives the -wal and
// -shm sidecar modes from the main database file's mode at the moment it creates
// them — setting the mode afterwards, or only on the main file, would leave freshly
// written digest prose world-readable in the WAL. This mirrors internal/spool/db.go,
// which learned the same lesson when the spool began holding inline prompt text.
//
// Pragmas ride the DSN rather than a post-open Exec: database/sql transparently
// retires a connection on driver.ErrBadConn and opens a replacement, which would
// silently come up with busy_timeout=0.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One writer: SQLite permits a single writer anyway, so serialising here turns
	// lock contention into queueing instead of SQLITE_BUSY.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Put writes a snapshot, overwriting any existing row for the same (session, seq)
// so a retried refinement is idempotent rather than an error.
func (s *Store) Put(r Record) error {
	if r.SessionID == "" || r.Seq <= 0 {
		return fmt.Errorf("digeststore: session_id required and seq must be >= 1")
	}
	_, err := s.db.Exec(`
INSERT INTO digest (session_id, seq, created_ts, schema_version, model,
                    from_prompt_id, to_prompt_id, turns, signals, body)
VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(session_id, seq) DO UPDATE SET
  created_ts=excluded.created_ts, schema_version=excluded.schema_version,
  model=excluded.model, from_prompt_id=excluded.from_prompt_id,
  to_prompt_id=excluded.to_prompt_id, turns=excluded.turns,
  signals=excluded.signals, body=excluded.body`,
		r.SessionID, r.Seq, r.CreatedTS, r.SchemaVersion, r.Model,
		r.FromPromptID, r.ToPromptID, r.Turns, r.Signals, r.Body)
	return err
}

const selectCols = `session_id, seq, created_ts, schema_version, model,
                    from_prompt_id, to_prompt_id, turns, signals, body`

func scanRec(sc interface{ Scan(...any) error }) (Record, error) {
	var r Record
	err := sc.Scan(&r.SessionID, &r.Seq, &r.CreatedTS, &r.SchemaVersion, &r.Model,
		&r.FromPromptID, &r.ToPromptID, &r.Turns, &r.Signals, &r.Body)
	return r, err
}

// Latest returns the newest snapshot for a session. An unknown session is not an
// error — it is the normal first-digest case.
func (s *Store) Latest(sessionID string) (Record, bool, error) {
	row := s.db.QueryRow(`SELECT `+selectCols+`
FROM digest WHERE session_id = ? ORDER BY seq DESC LIMIT 1`, sessionID)
	r, err := scanRec(row)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return r, true, nil
}

// History returns every snapshot for a session in refinement order, which is what
// the drift measurement replays.
func (s *Store) History(sessionID string) ([]Record, error) {
	rows, err := s.db.Query(`SELECT `+selectCols+`
FROM digest WHERE session_id = ? ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/enrich/llmstudy/digeststore/ -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Verify the static-binary invariant still holds**

Run: `make crosscheck`
Expected: every release target builds with `CGO_ENABLED=0`. `modernc.org/sqlite` is
pure Go, so this must stay green; if it does not, stop — the dependency choice is wrong.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/enrich/llmstudy/digeststore/
git commit -m "feat(digest): SQLite snapshot store following the spool's file-mode discipline"
```

---

### Task 6: Run verification tests 1-4 and report

**Files:**
- Modify: `internal/agent/enrich/llmstudy/digest_eval_test.go`
- Modify: `docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md`

**Interfaces:**
- Consumes: everything from Tasks 1-5.
- Produces: measured numbers against the spec's pre-registered thresholds.

- [ ] **Step 1: Add the refine-loop harness**

Append to `internal/agent/enrich/llmstudy/digest_eval_test.go`:

```go
// TestDigestRefineQuality measures thresholds 1-4 over real sessions:
// structural validity, hallucinated identifiers, rubberstamping, and retention.
func TestDigestRefineQuality(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8095"
	}
	root := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)

	o := DefaultMineOpts()
	o.K = 12
	l := NewLlama(url)

	var (
		digests, malformed        int
		ids, unverified           int
		withCorrections, stamped  int
		retentionNum, retentionDen int
	)
	sessions := 0
	for _, f := range files {
		if sessions >= 8 {
			break
		}
		ws, err := Mine(f, o)
		ocs, err2 := Outcomes(f, o)
		if err != nil || err2 != nil || len(ws) < 20 || len(ws) != len(ocs) {
			continue
		}
		sessions++

		// Refine across four checkpoints, 5 windows apart.
		var cur Digest
		var first Digest
		var injected []string
		for step, idx := range []int{5, 10, 15, 19} {
			w := ws[idx]
			facts := FactsFrom(Extract(w), ocs[:idx+1])
			src := Render(w)
			var d Digest
			if step == 0 {
				d, err = l.CreateDigest("software / engineering", src, facts.Line())
			} else {
				d, err = l.RefineDigest(cur, "software / engineering", src, facts.Line())
			}
			if err != nil {
				t.Logf("session %d step %d: %v", sessions, step, err)
				continue
			}
			digests++
			if p := ValidateDigest(d); len(p) > 0 {
				malformed++
				t.Logf("session %d step %d malformed: %v", sessions, step, p)
			}
			all := Identifiers(d)
			ids += len(all)
			unverified += len(UnverifiedIdentifiers(d, src))
			if facts.Corrections > 0 || facts.CorrectedTurns > 0 {
				withCorrections++
				if LooksRubberstamped(d, facts) {
					stamped++
					t.Logf("session %d step %d RUBBERSTAMPED (corrections=%d): %s",
						sessions, step, facts.Corrections, clipLog(d.Happened))
				}
			}
			if step == 0 {
				first = d
				injected = Identifiers(d)
				if len(injected) > 5 {
					injected = injected[:5]
				}
			}
			cur = d
		}
		if len(injected) > 0 {
			retentionNum += RetainedFacts(first, cur, injected)
			retentionDen += len(injected)
		}
	}

	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return float64(n) / float64(d) * 100
	}
	t.Logf("sessions=%d digests=%d", sessions, digests)
	t.Logf("threshold 1  structural validity: %.1f%% (want 100%%)", pct(digests-malformed, digests))
	t.Logf("threshold 2  unverified identifiers: %.1f%% of %d (want <=2%%)", pct(unverified, ids), ids)
	t.Logf("threshold 3  rubberstamped: %.1f%% of %d correction-bearing (want <=10%%)",
		pct(stamped, withCorrections), withCorrections)
	t.Logf("threshold 4  retention to final snapshot: %.1f%% of %d (want >=90%%)",
		pct(retentionNum, retentionDen), retentionDen)
}
```

- [ ] **Step 2: Run it on the model Task 1 selected**

```bash
G=$HOME/.keld/models/gguf
nohup llama-server -m $G/Qwen3-1.7B-Q4_K_M.gguf --ctx-size 4096 --parallel 1 \
  --threads 8 --batch-size 256 --ubatch-size 64 --cache-ram 512 --port 8095 \
  --no-warmup --jinja --device none \
  --chat-template-kwargs '{"enable_thinking":false}' > /tmp/dg17.log 2>&1 &
echo $! > /tmp/dg17.pid
until curl -s -m 2 http://127.0.0.1:8095/health >/dev/null; do sleep 2; done
DIGEST_URL=http://127.0.0.1:8095 go test -tags llmstudy \
  ./internal/agent/enrich/llmstudy/ -run DigestRefineQuality -v -timeout 90m
awk '/^VmHWM/{printf "peak %d MB\n", int($2/1024)}' /proc/$(cat /tmp/dg17.pid)/status
kill $(cat /tmp/dg17.pid)          # BY PID.
```

- [ ] **Step 3: Record the results honestly**

Append a "Part 4: digest verification" section to
`docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md` with each
threshold, its measured value, and pass/fail. **State misses as findings, not as
reasons to relax the threshold.** If threshold 3 (rubberstamping) fails, that is the
most important result in the section and the prompt needs work before anything ships.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/enrich/llmstudy/digest_eval_test.go \
        docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md
git commit -m "test(digest): measure thresholds 1-4 and record the results"
```

---

## Self-Review

**Spec coverage.** Digest schema with `unresolved` → Task 1. Domain neutrality (no
engineering vocabulary; `code_*` excluded) → Tasks 1 and 2, each with a test.
Deterministic facts as authoritative → Task 2. Iterative refinement with all four
failure-mode mitigations → Task 3 (append-only insights, caps, carry-forward,
state-what-changed). Anti-hallucination identifier gate → Task 4. SQLite snapshots
with the spool's file-mode discipline and no delta table → Task 5. Verification
thresholds 1-4 → Task 6; threshold 6 (model sizing) → Task 1, deliberately first.

**Gaps recorded rather than hidden:**

- **Threshold 5 (usefulness, blind panel) has no task.** It cannot be automated and
  the spec says it must be run by someone other than whoever wrote the prompts. It is
  a human step after Task 6, not an implementation task.
- **Publication (`digest_publish` opt-in) has no task.** The spec settles the policy
  and the `AGENTS.md` rewording, but wiring it into `settings`/`agentcfg` and the
  publish path belongs to the integration work the project owner has already flagged
  as a separate architecture exercise. Building it before the quality numbers exist
  would be premature.
- **Update triggers (every 10 user turns + idle finalisation) have no task.** They are
  daemon wiring, and the same integration exercise owns them. Task 6 exercises the
  refine loop at fixed checkpoints instead, which is what a quality measurement needs.
- `digestElapsed` in Task 1 is defined but unused by these tasks; it exists for the
  integration work's timing needs. Delete it if the integration lands differently.

**Type consistency.** `Digest` fields are identical in Tasks 1, 3 and 4.
`DigestFacts` is produced in Task 2 and consumed by `LooksRubberstamped` in Task 4 and
the harness in Task 6. `Record` is confined to `digeststore` (Task 5), which imports
nothing from `llmstudy`, so no import cycle is possible. `DigestFactsLine(Signals)`
(Task 2) is used by Task 1's harness; `FactsFrom(Signals, []Outcome).Line()` is used
by Task 6's.
