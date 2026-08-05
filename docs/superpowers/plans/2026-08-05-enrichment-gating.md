# Enrichment Gating — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `KELD_ENRICH_GATE_ENABLED` is on, skip the semantic Wave1/Wave2 passes on content-free turns (approval/continue follow-ups), while always running `sensitivity` (governance) and `speech_act` (the gate signal). Gated turns publish a minimal profile with `pipeline_status="gated"`.

**Architecture:** All changes are in `internal/agent/enrich`. Gate signal = a no-model length/approval pre-filter OR `speech_act == "fragment"`. Mark always-run passes with an **optional capability interface** `alwaysRunner` (idiomatic here — mirrors `ContextModel`/`MultiLabelModel`/`DescribedLabelModel`), so only `sensitivity` + `speech_act` change; every other extractor is untouched. `enrich.Run` runs the always-run passes first, evaluates the gate, then conditionally runs the gated passes + Wave2.

**Tech Stack:** Go. Tests: `go test ./internal/agent/enrich/...` (fast, in-process fakes — NOT the sidecar, NOT a heavy web suite).

## Global Constraints

- **Default ON** (flipped post-validation; built default-off first — Task-code snippets below show the original default-off form): `KELD_ENRICH_GATE_ENABLED` unset ⇒ gating ON; set `0`/`false`/`off`/`no` ⇒ every pass runs (pre-flip behavior).
- **Governance is never gated:** `sensitivity` (+ its deterministic `creddetect`) runs on every turn.
- **Gate on `fragment` only — never `statement`** (validated: corrections are statements).
- **Preserve pipeline invariants:** passes still run one-at-a-time (no goroutine fan-out — sidecar memory safety), results buffered-then-committed per group, per-pass timeout isolation (`runStageBounded`) unchanged. The gate only *removes* passes from the run.
- Success status string is **`"enriched"`** (existing), failure `"partial"` (existing); the new gated value is **`"gated"`**.
- Env resolved via a helper mirroring `taskTypeDescriptionsEnabled()` in `a6_tasktype.go` (no `os.Getenv` scattered).
- Follow the repo's superpowers workflow (AGENTS.md); no ad-hoc edits.

## File Structure

- `internal/agent/enrich/gate.go` — CREATE: `gateEnabled()`, `prefilterContentFree()`, the `APPROVAL` lexicon, `gateMaxTokens()`, the `alwaysRunner` interface, `speechActFragment()`.
- `internal/agent/enrich/gate_test.go` — CREATE: unit tests for the above.
- `internal/agent/enrich/extractors.go` — MODIFY: add `AlwaysRun() bool` to `SensitivityExtractor` and `SpeechActExtractor`.
- `internal/agent/enrich/pipeline.go` — MODIFY: restructure `Run`'s Wave1 section to partition always-run vs gated, evaluate the gate, conditionally run gated + Wave2, set `"gated"` status.
- `internal/agent/enrich/pipeline_test.go` — MODIFY: add gating tests with a call-counting fake model.

---

### Task 1: Gate primitives — env flag, pre-filter, always-run marker

**Files:**
- Create: `internal/agent/enrich/gate.go`
- Create: `internal/agent/enrich/gate_test.go`
- Modify: `internal/agent/enrich/extractors.go` (add `AlwaysRun()` to two extractors)

**Interfaces:**
- Produces: `gateEnabled() bool`; `prefilterContentFree(text string) bool`; `alwaysRunner` interface (`AlwaysRun() bool`); `speechActFragment(ctx *JobContext) bool`; `SensitivityExtractor.AlwaysRun()`/`SpeechActExtractor.AlwaysRun()` returning true.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/enrich/gate_test.go`:

```go
package enrich

import (
	"testing"
)

func TestGateEnabledDefaultsOff(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "")
	if gateEnabled() {
		t.Fatal("gate must default OFF when unset")
	}
	for _, on := range []string{"1", "true", "TRUE", "on"} {
		t.Setenv("KELD_ENRICH_GATE_ENABLED", on)
		if !gateEnabled() {
			t.Fatalf("gate should be ON for %q", on)
		}
	}
	for _, off := range []string{"0", "false", "off", "no"} {
		t.Setenv("KELD_ENRICH_GATE_ENABLED", off)
		if gateEnabled() {
			t.Fatalf("gate should be OFF for %q", off)
		}
	}
}

func TestPrefilterContentFree(t *testing.T) {
	contentFree := []string{
		"ok", "yes", "do that", "ok, do that", "go ahead", "yes please",
		"continue", "lgtm", "sounds good, proceed", "perfect, ship it",
		"sure", "yep that works", "thanks!", "hmm", "wait", "one sec",
	}
	substantive := []string{
		"Add a rate limiter to the /login endpoint",
		"No, use a dictionary instead of a list",
		"Why is this test failing?",
		"Use tabs, not spaces",
		"Change the variable name to userCount",
		"deploy the staging build now please",   // 6 tokens → over cap even though wordy-approval
	}
	for _, p := range contentFree {
		if !prefilterContentFree(p) {
			t.Errorf("expected content-free: %q", p)
		}
	}
	for _, p := range substantive {
		if prefilterContentFree(p) {
			t.Errorf("expected substantive (not gated): %q", p)
		}
	}
	if prefilterContentFree("") {
		t.Error("empty string must not be content-free-gated")
	}
}

func TestAlwaysRunMarkers(t *testing.T) {
	always := func(ex Extractor) bool {
		ar, ok := ex.(alwaysRunner)
		return ok && ar.AlwaysRun()
	}
	if !always(SensitivityExtractor{}) {
		t.Error("sensitivity must be always-run")
	}
	if !always(SpeechActExtractor{}) {
		t.Error("speech_act must be always-run")
	}
	for _, ex := range []Extractor{TaskTypeExtractor{}, DomainEntitiesExtractor{},
		passExtractor{Pass{Name: "activity_type", Labels: Activities}}, funcGuessExtractor{}} {
		if always(ex) {
			t.Errorf("%s must NOT be always-run (it's gated)", ex.Name())
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/dg/keld/keld-signal-worktrees/enrichment-gating && go test ./internal/agent/enrich/ -run 'TestGate|TestPrefilter|TestAlwaysRun' 2>&1 | tail -20`
Expected: FAIL — undefined `gateEnabled`/`prefilterContentFree`/`alwaysRunner`/`AlwaysRun`.

- [ ] **Step 3: Create `gate.go`**

```go
package enrich

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// gateEnabled reports whether enrichment gating is on. Default OFF (unset/empty
// ⇒ every pass runs, today's behavior); "1"/"true"/"on"/"yes" (case-insensitive)
// turn it on. Mirrors taskTypeDescriptionsEnabled() in a6_tasktype.go.
func gateEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("KELD_ENRICH_GATE_ENABLED"))) {
	case "1", "true", "on", "yes":
		return true
	default:
		return false
	}
}

// gateMaxTokens is the pre-filter's token cap (default 5). Override with
// KELD_ENRICH_GATE_MAX_TOKENS.
func gateMaxTokens() int {
	if v := strings.TrimSpace(os.Getenv("KELD_ENRICH_GATE_MAX_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

// approvalLexicon: the closed set of tokens a content-free approval/continue
// turn is built from. Deliberately narrow — a miss only wastes compute (the turn
// gets enriched anyway); it must never contain a token that appears in real
// requests. See docs/superpowers/specs/2026-08-05-enrichment-gating-design.md.
var approvalLexicon = map[string]struct{}{}

func init() {
	for _, w := range strings.Fields(
		"ok okay yes yep yeah sure go ahead do that it this continue proceed " +
			"lgtm thanks thank you perfect sounds good ship please now cool great " +
			"nice hmm wait sec one works fine done") {
		approvalLexicon[w] = struct{}{}
	}
}

var alphaToken = regexp.MustCompile(`[a-z]+`)

// prefilterContentFree reports whether text is a short approval/continue turn
// answerable with NO model call: 1..gateMaxTokens() alpha tokens, every one in
// the approval lexicon. Model-free and conservative by design.
func prefilterContentFree(text string) bool {
	toks := alphaToken.FindAllString(strings.ToLower(text), -1)
	if len(toks) == 0 || len(toks) > gateMaxTokens() {
		return false
	}
	for _, t := range toks {
		if _, ok := approvalLexicon[t]; !ok {
			return false
		}
	}
	return true
}

// alwaysRunner is an OPTIONAL Extractor capability (mirrors ContextModel /
// MultiLabelModel): a pass that must run on EVERY turn regardless of the gate.
// Only governance (sensitivity) + the gate signal (speech_act) implement it;
// every other pass is gated. Absence ⇒ gated.
type alwaysRunner interface {
	AlwaysRun() bool
}

// speechActFragment reports whether the committed speech_act result is
// "fragment" (a short follow-up/acknowledgement) — the model half of the gate.
func speechActFragment(ctx *JobContext) bool {
	out := ctx.Get("speech_act")
	if out == nil {
		return false
	}
	if l, ok := out["speech_act"].(Labeled); ok {
		return l.Value == "fragment"
	}
	return false
}
```

- [ ] **Step 4: Add `AlwaysRun()` markers in `extractors.go`**

Right after `SensitivityExtractor`'s existing `Version()` method, add:

```go
func (SensitivityExtractor) AlwaysRun() bool { return true }
```

Right after `SpeechActExtractor`'s `Version()` method (in `speechact.go` — that's where the type lives; put the method beside its siblings there), add:

```go
func (SpeechActExtractor) AlwaysRun() bool { return true }
```

(No other extractor implements `AlwaysRun` — they are gated by default via the absent-⇒-gated rule.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/dg/keld/keld-signal-worktrees/enrichment-gating && go test ./internal/agent/enrich/ -run 'TestGate|TestPrefilter|TestAlwaysRun' 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd /home/dg/keld/keld-signal-worktrees/enrichment-gating
git add internal/agent/enrich/gate.go internal/agent/enrich/gate_test.go internal/agent/enrich/extractors.go internal/agent/enrich/speechact.go
git commit -m "feat(enrich): gate primitives — env flag, content-free pre-filter, always-run marker"
```

---

### Task 2: Wire the gate into the pipeline

**Files:**
- Modify: `internal/agent/enrich/pipeline.go` (`Run`)
- Test: `internal/agent/enrich/pipeline_test.go`

**Interfaces:**
- Consumes: `gateEnabled`, `prefilterContentFree`, `alwaysRunner`, `speechActFragment` (Task 1).
- Produces: gated behavior in `Run` + `PipelineStatus == "gated"`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/enrich/pipeline_test.go` a call-counting fake and tests. The fake records which classify tasks / entity+extract calls happen and returns a configurable `speech_act` label:

```go
// countingModel records which passes hit the model and lets a test force the
// speech_act label. Method-per-pass: task_type/activity_type/personal/
// function_guess/subcategory/speech_act → Classify; sensitivity + domain_entities
// → Entities/Extract. (Verify against the extractors when wiring.)
type countingModel struct {
	speechAct    string   // label returned for the "speech_act" task
	classifyHits []string // task names passed to Classify
	entityHits   int
	extractHits  int
}

func (m *countingModel) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	out := map[string][]enrich.Ranked{}
	for task, labels := range tasks {
		m.classifyHits = append(m.classifyHits, task)
		lab := ""
		if len(labels) > 0 {
			lab = labels[0]
		}
		if task == "speech_act" && m.speechAct != "" {
			lab = m.speechAct
		}
		out[task] = []enrich.Ranked{{Label: lab, Confidence: 0.9}}
	}
	return out
}
func (m *countingModel) Entities(text string, labels map[string]string) []enrich.Entity {
	m.entityHits++
	return nil
}
func (m *countingModel) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	m.extractHits++
	return enrich.ExtractResult{}
}

func hit(hits []string, task string) bool {
	for _, h := range hits {
		if h == task {
			return true
		}
	}
	return false
}
```

Note: this fake lives in `pipeline_test.go` (package `enrich`), so it can call unexported helpers; the existing tests use `enrichtest.NewFake()` from an external test package — this one is internal by design so it can assert on gate internals. Adjust the `enrich.` qualifier to bare types since it's in-package (`Ranked`, `Entity`, `ExtractResult`, `Model`).

Tests (in-package, so drop the `enrich.` prefix on types):

```go
func TestGateSkipsSemanticPassesOnPrefilteredTurn(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "1")
	m := &countingModel{}
	p := Run("ok, do that", "claude_code", Meta{}, m)
	if p.PipelineStatus != "gated" {
		t.Fatalf("status = %q, want gated", p.PipelineStatus)
	}
	for _, gated := range []string{"task_type", "activity_type", "personal", "function_guess", "subcategory"} {
		if hit(m.classifyHits, gated) {
			t.Errorf("gated pass %q must not hit the model", gated)
		}
	}
	// governance + gate signal still ran
	if m.entityHits == 0 && m.extractHits == 0 {
		t.Error("sensitivity (governance) must always run")
	}
	if p.Sensitivity.Producer == "" && !hit(m.classifyHits, "speech_act") {
		t.Error("speech_act (gate signal) must always run")
	}
	// gated semantic fields are empty
	if p.TaskType.Value != "" || p.Activity.Value != "" {
		t.Error("gated turn must leave semantic fields empty")
	}
}

func TestGateSkipsOnSpeechActFragment(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "1")
	m := &countingModel{speechAct: "fragment"}
	// A non-prefiltered input so ONLY the speech_act==fragment branch can gate it.
	p := Run("well, alright then I suppose", "claude_code", Meta{}, m)
	if p.PipelineStatus != "gated" {
		t.Fatalf("status = %q, want gated (speech_act fragment)", p.PipelineStatus)
	}
	if hit(m.classifyHits, "task_type") {
		t.Error("task_type must be skipped when gated on fragment")
	}
}

func TestGateRunsAllPassesOnSubstantiveTurn(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "1")
	m := &countingModel{speechAct: "command"}
	p := Run("Add a rate limiter to the login endpoint", "claude_code", Meta{}, m)
	if p.PipelineStatus == "gated" {
		t.Fatal("substantive turn must not be gated")
	}
	if !hit(m.classifyHits, "task_type") {
		t.Error("task_type must run on a substantive turn")
	}
}

func TestGateOffRunsEverything(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "") // default off
	m := &countingModel{}
	p := Run("ok", "claude_code", Meta{}, m)
	if p.PipelineStatus == "gated" {
		t.Fatal("gate disabled: nothing should be gated even for 'ok'")
	}
	if !hit(m.classifyHits, "task_type") {
		t.Error("gate disabled: task_type must still run")
	}
}
```

(When wiring, confirm which model method each pass calls — `sensitivity` and `domain_entities` may both use `Entities`/`Extract`; the assertions above only require that *some* governance call happened and that the named gated *Classify* tasks did not. If `speech_act` uses `ClassifyDescribed`, add that method to `countingModel` recording into `classifyHits["speech_act"]`. Verify against `classifyLabeled` and the sidecar client before finalizing the fake's method set.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/dg/keld/keld-signal-worktrees/enrichment-gating && go test ./internal/agent/enrich/ -run 'TestGate(Skips|Runs|Off)' 2>&1 | tail -25`
Expected: FAIL (status never "gated"; gated passes still hit the model).

- [ ] **Step 3: Restructure `Run`'s wave execution**

In `pipeline.go`, replace the block from `exs := append(Wave1(), cfg.customW1...)` through the `status := "enriched"` assignment with:

```go
	exs := append(Wave1(), cfg.customW1...)

	// Partition Wave1 into always-run (governance + gate signal) and gated
	// (semantic). Wave1 passes are mutually independent, so committing them in
	// two sequential batches preserves order-independence.
	var always, gated []Extractor
	for _, ex := range exs {
		if ar, ok := ex.(alwaysRunner); ok && ar.AlwaysRun() {
			always = append(always, ex)
		} else {
			gated = append(gated, ex)
		}
	}

	anyFailed := false
	commit := func(group []Extractor) {
		type res struct {
			name string
			out  map[string]any
			ok   bool
		}
		results := make([]res, len(group))
		for i, ex := range group {
			out, ok := runStageBounded(ex, ctx, cfg)
			results[i] = res{ex.Name(), out, ok}
		}
		for _, r := range results {
			if !r.ok {
				anyFailed = true
				continue
			}
			ctx.Set(r.name, r.out)
		}
	}

	// Always-run first, so speech_act is committed before the gate decision.
	commit(always)

	// Gate: only when enabled AND the turn is content-free (no-model pre-filter
	// OR speech_act==fragment). Governance/gate passes above already ran.
	gateOff := gateEnabled() && (prefilterContentFree(text) || speechActFragment(ctx))

	ran := append([]Extractor{}, always...)
	wave2 := append(Wave2(), cfg.customW2...)
	if !gateOff {
		commit(gated)
		for _, ex := range wave2 {
			if out, ok := runStageBounded(ex, ctx, cfg); ok {
				ctx.Set(ex.Name(), out)
			} else {
				anyFailed = true
			}
		}
		ran = append(append(ran, gated...), wave2...)
	}

	status := "enriched"
	switch {
	case gateOff:
		status = "gated"
	case anyFailed:
		status = "partial"
	}
```

Then change the `versions` map construction to iterate `ran` instead of `exs`+`wave2`:

```go
	versions := map[string]string{}
	for _, ex := range ran {
		versions[ex.Name()] = ex.Version()
	}
```

And change `collectCustom(ctx, cfg.customW1, cfg.customW2)` — leave as-is; gated custom passes were never committed, so `collectCustom` naturally contributes nothing for them (it skips absent stages). The `Profile{...}` assembly is unchanged: gated passes' `ctx.Get(...)` return nil → `labeledFrom` yields empty `Labeled`, exactly the intended empty semantic fields.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/dg/keld/keld-signal-worktrees/enrichment-gating && go test ./internal/agent/enrich/ -run 'TestGate(Skips|Runs|Off)' 2>&1 | tail -25`
Expected: PASS.

- [ ] **Step 5: Run the whole enrich package (regression)**

Run: `cd /home/dg/keld/keld-signal-worktrees/enrichment-gating && go test ./internal/agent/enrich/... 2>&1 | tail -25`
Expected: PASS — the existing pipeline/pass/timeout tests still green (gate default-off preserves behavior). If a pre-existing test asserted a specific `PipelineStatus` on an "ok"-like input WITHOUT setting the env, it stays "enriched"/"partial" (gate off), so no change.

- [ ] **Step 6: Commit**

```bash
cd /home/dg/keld/keld-signal-worktrees/enrichment-gating
git add internal/agent/enrich/pipeline.go internal/agent/enrich/pipeline_test.go
git commit -m "feat(enrich): gate semantic passes on content-free turns (default off)"
```

---

## Self-Review

**Spec coverage:**
- Gate = pre-filter OR speech_act==fragment, only when enabled → Task 2 `gateOff`. ✓
- Always-run sensitivity + speech_act; gated = rest incl. custom + Wave2 → Task 1 markers + Task 2 partition. ✓
- Pre-filter narrow (≤N approval tokens, no model) → Task 1 `prefilterContentFree`. ✓
- Gate on `fragment` only → Task 1 `speechActFragment`. ✓
- `pipeline_status="gated"`; empty semantic fields; publish unchanged → Task 2 (status + natural empty via `labeledFrom`). ✓
- Env flag default-off; `KELD_ENRICH_GATE_ENABLED` / `KELD_ENRICH_GATE_MAX_TOKENS` → Task 1. ✓
- Preserve one-at-a-time + per-pass timeout → Task 2 keeps `runStageBounded`, no fan-out; two sequential commit batches. ✓
- Optional `alwaysRunner` interface (idiomatic, per ContextModel precedent) instead of widening `Extractor` → Task 1 (a refinement over the spec's "add to Extractor interface" wording; same effect, less churn). ✓
- Tests: pre-filter table, AlwaysRun markers, gated-skips, substantive-runs-all, gate-off-runs-all → Tasks 1 & 2. ✓
- O1 default (run speech_act even on pre-filtered turns) → Task 2 runs `always` (incl. speech_act) unconditionally before the gate. ✓

**Placeholder scan:** No TBD/TODO; complete code for gate.go, markers, and the Run restructure. The one flagged verification (which model method each pass calls, for the counting fake) is a concrete "confirm against these files" instruction, not a vague gap.

**Type consistency:** `gateEnabled`/`prefilterContentFree`/`alwaysRunner`/`speechActFragment`/`AlwaysRun` names consistent across Tasks 1→2. Status strings `"enriched"`/`"partial"`/`"gated"` match `pipeline.go`. `ctx.Get("speech_act")["speech_act"].(Labeled)` matches how `SpeechActExtractor.Run` stores it and how `labeledFrom` reads it.
