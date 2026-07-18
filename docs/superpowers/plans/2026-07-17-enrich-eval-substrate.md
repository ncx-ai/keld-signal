# Enrichment Eval Substrate (Phase 0) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the measurement substrate — a live-sidecar `keld-agent eval` runner, a confound eval set, and subject-leakage / false-eng metrics — then capture the BASELINE the A1/A2 experiments will be gated against.

**Architecture:** Extend the existing `internal/agent/enrich/eval` package (which already has `LoadGold`, `RunModel*`, `Score`) with a confound set + two new metrics, and add a `keld-agent eval` cobra subcommand that resolves the live GLiNER2 client via `localagent.ResolveModel` and prints a metric table.

**Tech Stack:** Go 1.26, stdlib + cobra + the existing enrich/eval packages.

## Global Constraints

- **Go 1.26**; module `github.com/ncx-ai/keld-signal`. `export PATH="/opt/homebrew/bin:$PATH"` before any `go`.
- **Go-only; no frozen-sidecar change.** The runner uses the *running* sidecar via `agentcfg.Read()` → `localagent.ResolveModel(info)`.
- **This phase changes no classification behavior** — it only measures. (A1/A2 are later phases.)
- Confound-set classes (exact `class` values): `c1` (eng activity, non-eng subject), `c2` (genuine non-eng), `c3` (context fragment).
- `subject_leakage_rate` = over `c1` rows, fraction with `function_guess != "eng"`; also reported for `task_type` (fraction not matching the row's eng-correct task). `false_eng_rate` = over `c2` rows, fraction with `function_guess == "eng"`.
- Acceptance gate for later phases (recorded here for reference): keep a step only if leakage↓ meaningfully AND base `gold.jsonl` accuracy Δ ≥ 0 AND false-eng Δ ≤ small ε.
- No new external dependencies.

## File Structure

- `internal/agent/enrich/eval/confound.jsonl` — new embedded confound set.
- `internal/agent/enrich/eval/eval.go` — add `Class` to `GoldRow`, `LoadConfound()`, `LeakageRate()`, `FalseEngRate()`.
- `internal/agent/enrich/eval/eval_test.go` — tests for the new loaders/metrics.
- `internal/agentcli/evalcmd.go` — new `keld-agent eval` subcommand.
- `internal/agentcli/agentcli.go` — register the subcommand in `NewRootCmd`.

---

### Task 1: confound set + `Class` field + `LoadConfound`

**Files:**
- Create: `internal/agent/enrich/eval/confound.jsonl`
- Modify: `internal/agent/enrich/eval/eval.go` (add `Class` field to `GoldRow` ~line 22; add embed + `LoadConfound` beside `LoadGold`)
- Test: `internal/agent/enrich/eval/eval_test.go`

**Interfaces:**
- Consumes: existing `GoldRow`, `LoadGold` parsing.
- Produces: `GoldRow.Class string json:"class"`; `func LoadConfound() ([]GoldRow, error)`.

- [ ] **Step 1: Create the confound set**

Create `internal/agent/enrich/eval/confound.jsonl` with these rows verbatim (one JSON object per line). These are the synthetic starter rows; the operator's real anonymized rows are appended later (same shape) before the baseline is treated as final.

```
{"class":"c1","text":"Add a Postgres migration for the campaigns table and wire the SEO metadata fields into the CampaignSerializer.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c1","text":"Debug why the marketing-attribution worker drops events under load; the Kafka consumer lag keeps climbing.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c1","text":"Write unit tests for the invoice-reconciliation service that posts journal entries to the ledger API.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c1","text":"Refactor the patient-intake FHIR parser to stream records instead of loading the whole bundle into memory.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c1","text":"Set up CI to run the contract-clause classifier's eval suite on every PR and fail under 0.9 F1.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c1","text":"The recruiting pipeline's candidate-dedupe query is O(n^2); rewrite it with a hashed join and add an index.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c1","text":"Implement retry-with-backoff in the payments webhook handler and make the PCI redaction happen before logging.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c1","text":"Trace the null-pointer in the sales-forecast dashboard's React chart component when a quota is unset.","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c2","text":"Write three subject-line variants for our Q3 product-launch email campaign targeting mid-market SaaS buyers.","task_type":"codegen","domain":"business","function_guess":"mkt"}
{"class":"c2","text":"Draft the FY26 operating budget narrative explaining the variance in marketing spend vs plan.","task_type":"summarization","domain":"finance","function_guess":"fin"}
{"class":"c2","text":"Review this SaaS master services agreement and flag any auto-renewal or liability-cap clauses we should push back on.","task_type":"reasoning","domain":"legal","function_guess":"legal"}
{"class":"c2","text":"Summarize the candidate's interview feedback and recommend hire / no-hire with reasons.","task_type":"summarization","domain":"business","function_guess":"hr"}
{"class":"c3","text":"now do the same for the other endpoint","recent_prompts":["add request validation to the /users POST handler","write a table test for the validator"],"repo":"acme/api","branch":"feat/validation","project":"Acme API","task_type":"codegen","domain":"software","function_guess":"eng"}
{"class":"c3","text":"ok ship it","recent_prompts":["fix the flaky migration test","rebase onto main and resolve the conflict in schema.sql"],"repo":"acme/api","branch":"feat/db","project":"Acme API","task_type":"codegen","domain":"software","function_guess":"eng"}
```

- [ ] **Step 2: Write the failing test**

Append to `internal/agent/enrich/eval/eval_test.go`:

```go
func TestLoadConfoundParsesClasses(t *testing.T) {
	rows, err := LoadConfound()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 {
		t.Fatalf("confound rows = %d, want >= 10", len(rows))
	}
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Class]++
	}
	for _, c := range []string{"c1", "c2", "c3"} {
		if seen[c] == 0 {
			t.Fatalf("confound set missing class %q", c)
		}
	}
	// c1 rows must be gold-labeled eng (the whole point).
	for _, r := range rows {
		if r.Class == "c1" && r.FunctionGuess != "eng" {
			t.Fatalf("c1 row not gold-eng: %q", r.Text)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run TestLoadConfound -v`
Expected: FAIL to compile — `undefined: LoadConfound`.

- [ ] **Step 4: Implement**

In `eval.go`: add `Class string \`json:"class"\`` to the `GoldRow` struct (after `Text`). Add beside the existing `//go:embed gold.jsonl`:

```go
//go:embed confound.jsonl
var confoundJSONL string

// LoadConfound parses the embedded confound eval set (classes c1/c2/c3).
func LoadConfound() ([]GoldRow, error) { return parseRows(confoundJSONL) }
```

Refactor `LoadGold` to share a parser (extract the scanner loop into `parseRows(s string) ([]GoldRow, error)` and have both `LoadGold` and `LoadConfound` call it).

- [ ] **Step 5: Run test to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run TestLoadConfound -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/enrich/eval/confound.jsonl internal/agent/enrich/eval/eval.go internal/agent/enrich/eval/eval_test.go
git commit -m "feat(eval): add confound eval set (c1/c2/c3) + LoadConfound"
```

---

### Task 2: leakage + false-eng metrics

**Files:**
- Modify: `internal/agent/enrich/eval/eval.go` (add two metric funcs)
- Test: `internal/agent/enrich/eval/eval_test.go`

**Interfaces:**
- Consumes: `GoldRow` (with `Class`), `Pred`.
- Produces: `func LeakageRate(gold []GoldRow, pred []Pred) map[string]float64` (keys `function_guess`, `task_type`); `func FalseEngRate(gold []GoldRow, pred []Pred) float64`.

- [ ] **Step 1: Write the failing test**

```go
func TestLeakageAndFalseEng(t *testing.T) {
	gold := []GoldRow{
		{Class: "c1", FunctionGuess: "eng", TaskType: "codegen"}, // leaked below
		{Class: "c1", FunctionGuess: "eng", TaskType: "codegen"}, // correct below
		{Class: "c2", FunctionGuess: "mkt"},                      // false-eng below
	}
	pred := []Pred{
		{FunctionGuess: "mkt", TaskType: "summarization"}, // c1 leaked (function+task)
		{FunctionGuess: "eng", TaskType: "codegen"},        // c1 correct
		{FunctionGuess: "eng"},                             // c2 → wrongly eng
	}
	lk := LeakageRate(gold, pred)
	if lk["function_guess"] != 0.5 {
		t.Fatalf("function leakage = %v, want 0.5", lk["function_guess"])
	}
	if lk["task_type"] != 0.5 {
		t.Fatalf("task leakage = %v, want 0.5", lk["task_type"])
	}
	if fe := FalseEngRate(gold, pred); fe != 1.0 {
		t.Fatalf("false_eng = %v, want 1.0", fe)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run TestLeakageAndFalseEng -v`
Expected: FAIL to compile — `undefined: LeakageRate`.

- [ ] **Step 3: Implement**

Add to `eval.go`:

```go
// LeakageRate measures subject-matter leakage over c1 rows (engineering activity,
// non-eng subject): the fraction whose predicted facet != the gold eng-correct
// value. Reported for function_guess and task_type. 0 when there are no c1 rows.
func LeakageRate(gold []GoldRow, pred []Pred) map[string]float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var c1, fLeak, tLeak int
	for i := 0; i < n; i++ {
		if gold[i].Class != "c1" {
			continue
		}
		c1++
		if pred[i].FunctionGuess != gold[i].FunctionGuess {
			fLeak++
		}
		if gold[i].TaskType != "" && pred[i].TaskType != gold[i].TaskType {
			tLeak++
		}
	}
	out := map[string]float64{"function_guess": 0, "task_type": 0}
	if c1 > 0 {
		out["function_guess"] = float64(fLeak) / float64(c1)
		out["task_type"] = float64(tLeak) / float64(c1)
	}
	return out
}

// FalseEngRate measures over-correction over c2 rows (genuine non-eng work):
// the fraction wrongly predicted function_guess == "eng". 0 when no c2 rows.
func FalseEngRate(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var c2, wrong int
	for i := 0; i < n; i++ {
		if gold[i].Class != "c2" {
			continue
		}
		c2++
		if pred[i].FunctionGuess == "eng" {
			wrong++
		}
	}
	if c2 == 0 {
		return 0
	}
	return float64(wrong) / float64(c2)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run TestLeakageAndFalseEng -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/eval/eval.go internal/agent/enrich/eval/eval_test.go
git commit -m "feat(eval): add subject-leakage-rate and false-eng-rate metrics"
```

---

### Task 3: `keld-agent eval` subcommand (live sidecar)

**Files:**
- Create: `internal/agentcli/evalcmd.go`
- Modify: `internal/agentcli/agentcli.go` (register in `NewRootCmd`, near `newEnrichCmd`/`newMetricsCmd` at ~line 219-220)

**Interfaces:**
- Consumes: `agentcfg.Read()`, `localagent.ResolveModel`, `eval.LoadGold`, `eval.LoadConfound`, `eval.RunModel`, `eval.RunModelWithContext`, `eval.Score`, `eval.LeakageRate`, `eval.FalseEngRate`.
- Produces: `func newEvalCmd() *cobra.Command`.

- [ ] **Step 1: Implement the command**

Create `internal/agentcli/evalcmd.go`:

```go
package agentcli

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/eval"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/spf13/cobra"
)

// newEvalCmd builds `keld-agent eval`: run the enrichment pipeline over the
// embedded gold set (and, with --confound, the confound set) against the live
// GLiNER2 sidecar and print a per-facet metric table. Local only; never
// publishes. This is the measurement substrate for classification experiments.
func newEvalCmd() *cobra.Command {
	var withContext, withConfound bool
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Score the enrichment pipeline against the gold/confound sets (local; uses the live sidecar).",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, _ := agentcfg.Read()
			model, note, err := localagent.ResolveModel(info)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "keld-agent eval: "+note)

			rows, err := eval.LoadGold()
			if err != nil {
				return err
			}
			if withConfound {
				cf, err := eval.LoadConfound()
				if err != nil {
					return err
				}
				rows = append(rows, cf...)
			}

			var pred []eval.Pred
			if withContext {
				pred = eval.RunModelWithContext(model, rows)
			} else {
				pred = eval.RunModel(model, rows)
			}

			fields := []string{"task_type", "domain", "sensitivity", "activity_type", "function_guess", "subcategory"}
			m := eval.Score(rows, pred, fields)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "context=%v confound=%v rows=%d\n", withContext, withConfound, len(rows))
			for _, f := range fields {
				fmt.Fprintf(out, "  %-15s accuracy=%.3f\n", f, m[f]["accuracy"])
			}
			fmt.Fprintf(out, "  %-15s sensitive_recall=%.3f\n", "sensitivity", m["sensitivity"]["sensitive_recall"])
			if withConfound {
				lk := eval.LeakageRate(rows, pred)
				fmt.Fprintf(out, "  leakage(function_guess)=%.3f  leakage(task_type)=%.3f  false_eng=%.3f\n",
					lk["function_guess"], lk["task_type"], eval.FalseEngRate(rows, pred))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withContext, "context", false, "Feed session context (recent prompts, repo/branch) to the classifier.")
	cmd.Flags().BoolVar(&withConfound, "confound", false, "Include the confound set and report leakage/false-eng metrics.")
	return cmd
}
```

Register it in `NewRootCmd` (agentcli.go), beside the other subcommands:

```go
	root.AddCommand(newEvalCmd())
```

- [ ] **Step 2: Build + smoke (no live sidecar needed to compile)**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./... && go vet ./...`
Expected: clean.
Run: `go run ./cmd/keld-agent eval --help`
Expected: help text shows `--context` and `--confound` flags.

- [ ] **Step 3: Commit**

```bash
git add internal/agentcli/evalcmd.go internal/agentcli/agentcli.go
git commit -m "feat(agentcli): add 'keld-agent eval' live-sidecar scoring command"
```

---

### Task 4: capture the BASELINE (live sidecar)

**Files:** none (measurement).

- [ ] **Step 1: Full-suite check**

Run: `export PATH="/opt/homebrew/bin:$PATH"; gofmt -l internal/; go vet ./...; go test ./...`
Expected: clean / all pass.

- [ ] **Step 2: Build + ensure the sidecar is warm**

Run: `go build -o /tmp/keld-agent-exp ./cmd/keld-agent`
Ensure `keld-agent` is running and the sidecar is `ready` (see `keld-agent metrics`); warm it if cold (drive one enrichment) so the eval isn't dominated by a cold load.

- [ ] **Step 3: Run the baseline four ways and record**

Run each and save the output to `docs/superpowers/plans/eval-baseline.txt`:
```
/tmp/keld-agent-exp eval --confound
/tmp/keld-agent-exp eval --confound --context
```
Expected: per-facet accuracy + `leakage(function_guess)`, `leakage(task_type)`, `false_eng`. This is the reference the A1/A2 phases are gated against. Note in the file the sidecar/model version and date.

- [ ] **Step 4: Commit the baseline record**

```bash
git add docs/superpowers/plans/eval-baseline.txt
git commit -m "docs(eval): record baseline metrics (pre-A1/A2)"
```

---

## Self-Review

**Spec coverage:** Phase-0 items from the spec — eval runner (Task 3), confound set c1/c2/c3 (Task 1), leakage + false-eng metrics (Task 2), baseline (Task 4). A1/A2 are explicitly later phases, not in this plan. ✔

**Placeholder scan:** none — confound rows are provided verbatim (data), metric code is complete, command code is complete, commands have expected output. The operator's real rows are an additive data step noted in Global Constraints, not a code placeholder. ✔

**Type consistency:** `GoldRow.Class` (Task 1) is read by `LeakageRate`/`FalseEngRate` (Task 2) and populated from `confound.jsonl`. `LoadConfound() ([]GoldRow, error)`, `LeakageRate(...) map[string]float64`, `FalseEngRate(...) float64` are defined in eval and consumed by `newEvalCmd` (Task 3) with matching signatures. `eval.RunModel`/`RunModelWithContext`/`Score` are pre-existing and used as-is. ✔
