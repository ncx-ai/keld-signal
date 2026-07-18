# Confidence-stratified accuracy (calibration eval) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Report per-facet accuracy stratified by GLiNER2's returned confidence — reliability bins + ECE — via `keld-agent eval --calibration`, with no pipeline change.

**Architecture:** The eval's `Pred` gains a per-facet confidence map (populated from the Profile's `Labeled.Confidence`, currently discarded). A `Calibration` function buckets predictions into fixed-width confidence bins and computes per-bin accuracy + the facet's Expected Calibration Error. `keld-agent eval --calibration` prints per-facet reliability tables, grouping pure-classifier facets separately from the two rule-influenced ones (`sensitivity`, `function_guess`).

**Tech Stack:** Go 1.26 (`export PATH="/opt/homebrew/bin:$PATH"`); the eval harness; the GLiNER2 sidecar for the final measurement only.

**Spec:** `docs/superpowers/specs/2026-07-18-confidence-calibration-eval-design.md`.

## Global Constraints

- Go only. `export PATH="/opt/homebrew/bin:$PATH"` before any `go` command; `gofmt -l .` must be empty before commit (CI gate).
- NO pipeline change — only `internal/agent/enrich/eval/` and `internal/agentcli/evalcmd.go`.
- ECE = Σ over non-empty bins `(n_bin/N)·|accuracy_bin − meanConf_bin|`, N = predictions with a non-empty gold label for the facet.
- Fixed-width bins, default 10 (`[0,.1)…[.9,1]`); the top bin is closed `[.9,1.0]`.
- Pure-classifier facets: `task_type`, `domain`, `activity_type`, `personal`, `speech_act`, `subcategory`. Rule-influenced (report separately, labeled): `sensitivity`, `function_guess`.
- Additive only: existing accuracy scoring, metrics, and default `eval` output must be unchanged.

---

### Task 1: Capture confidence in `Pred` + the `Calibration` metric

**Files:**
- Modify: `internal/agent/enrich/eval/eval.go` (`Pred.Conf`, populate in both `RunModel*`, `Calibration`, `Reliability`)
- Create: `internal/agent/enrich/eval/calibration_test.go`

**Interfaces:**
- Consumes: `enrich.Run` → `Profile` (each facet `Labeled` has `.Value` + `.Confidence`); `fieldOf(pred, facet)` / `fieldOf(gold, facet)`.
- Produces:
  - `Pred.Conf map[string]float64` (facet → top-label confidence).
  - `type Bin struct { Lo, Hi float64; Count int; MeanConf, Accuracy float64 }`.
  - `type Reliability struct { Facet string; Bins []Bin; N int; ECE float64 }`.
  - `func Calibration(gold []GoldRow, pred []Pred, facet string, nbins int) Reliability`.

- [ ] **Step 1: Write the failing test** (`calibration_test.go`)

```go
package eval

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCalibrationBinsAndECE(t *testing.T) {
	// 4 task_type predictions with hand-chosen confidences and correctness:
	//   conf 0.95 correct, 0.92 wrong  -> bin [0.9,1.0]: n=2, meanConf=0.935, acc=0.5
	//   conf 0.55 correct              -> bin [0.5,0.6): n=1, meanConf=0.55, acc=1.0
	//   conf 0.15 wrong                -> bin [0.1,0.2): n=1, meanConf=0.15, acc=0.0
	gold := []GoldRow{
		{TaskType: "codegen"}, {TaskType: "codegen"}, {TaskType: "codegen"}, {TaskType: "codegen"},
	}
	pred := []Pred{
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.95}},
		{TaskType: "other", Conf: map[string]float64{"task_type": 0.92}},
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.55}},
		{TaskType: "other", Conf: map[string]float64{"task_type": 0.15}},
	}
	r := Calibration(gold, pred, "task_type", 10)
	if r.N != 4 {
		t.Fatalf("N = %d, want 4", r.N)
	}
	// find the top bin [0.9,1.0]
	var top *Bin
	for i := range r.Bins {
		if r.Bins[i].Lo == 0.9 {
			top = &r.Bins[i]
		}
	}
	if top == nil || top.Count != 2 || !approx(top.Accuracy, 0.5) || !approx(top.MeanConf, 0.935) {
		t.Fatalf("top bin wrong: %+v", top)
	}
	// ECE weights each bin by n_bin/N. Top bin has 2 preds (weight 2/4), the other
	// two bins 1 each (weight 1/4):
	//   (2/4)*|0.5-0.935| + (1/4)*|1-0.55| + (1/4)*|0-0.15|
	//   = 0.5*0.435 + 0.25*0.45 + 0.25*0.15 = 0.2175 + 0.1125 + 0.0375 = 0.3675
	if !approx(r.ECE, 0.3675) {
		t.Fatalf("ECE = %.5f, want 0.3675", r.ECE)
	}
}

func TestCalibrationSkipsBlankGold(t *testing.T) {
	gold := []GoldRow{{TaskType: ""}, {TaskType: "codegen"}}
	pred := []Pred{
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.9}},
		{TaskType: "codegen", Conf: map[string]float64{"task_type": 0.9}},
	}
	if r := Calibration(gold, pred, "task_type", 10); r.N != 1 {
		t.Fatalf("blank gold must be excluded: N = %d, want 1", r.N)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run TestCalibration -v`
Expected: FAIL — `undefined: Calibration` / `unknown field Conf`.

- [ ] **Step 3: Add `Pred.Conf` + populate it** (`eval.go`)

Add to the `Pred` struct: `Conf map[string]float64`.

In `RunModel` and `RunModelWithContext`, set `Conf` on each constructed `Pred` from the Profile's confidences:
```go
Conf: map[string]float64{
    "task_type":      p.TaskType.Confidence,
    "domain":         p.Domain.Confidence,
    "sensitivity":    p.Sensitivity.Confidence,
    "activity_type":  p.Activity.Confidence,
    "function_guess": p.FunctionGuess.Confidence,
    "speech_act":     p.SpeechAct.Confidence,
    "subcategory":    p.Subcategory.Confidence,
},
```
(Add this field to BOTH `Pred{...}` literals. Leave all existing string fields as-is.)

- [ ] **Step 4: Add `Bin`, `Reliability`, `Calibration`** (`eval.go`, near the other metrics)

```go
// Bin is one confidence band's reliability stats.
type Bin struct {
	Lo, Hi   float64
	Count    int
	MeanConf float64
	Accuracy float64
}

// Reliability is a facet's confidence-stratified accuracy + calibration error.
type Reliability struct {
	Facet string
	Bins  []Bin // non-empty bins, ascending
	N     int
	ECE   float64
}

// Calibration stratifies a facet's predictions into nbins fixed-width confidence
// bins ([0,1/nbins)…[1-1/nbins,1.0], top bin closed) and computes per-bin accuracy
// + the facet's Expected Calibration Error. Rows with a blank gold label for the
// facet are excluded.
func Calibration(gold []GoldRow, pred []Pred, facet string, nbins int) Reliability {
	if nbins <= 0 {
		nbins = 10
	}
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	type acc struct {
		count   int
		sumConf float64
		correct int
	}
	bins := make([]acc, nbins)
	total := 0
	for i := 0; i < n; i++ {
		g := fieldOf(gold[i], facet)
		if g == "" {
			continue
		}
		c := 0.0
		if pred[i].Conf != nil {
			c = pred[i].Conf[facet]
		}
		b := int(c * float64(nbins))
		if b >= nbins { // c == 1.0 lands in the top bin
			b = nbins - 1
		}
		if b < 0 {
			b = 0
		}
		bins[b].count++
		bins[b].sumConf += c
		if g == fieldOf(pred[i], facet) {
			bins[b].correct++
		}
		total++
	}
	r := Reliability{Facet: facet, N: total}
	if total == 0 {
		return r
	}
	width := 1.0 / float64(nbins)
	for i, a := range bins {
		if a.count == 0 {
			continue
		}
		binAcc := float64(a.correct) / float64(a.count)
		binConf := a.sumConf / float64(a.count)
		hi := float64(i+1) * width
		if i == nbins-1 {
			hi = 1.0
		}
		r.Bins = append(r.Bins, Bin{Lo: float64(i) * width, Hi: hi, Count: a.count, MeanConf: binConf, Accuracy: binAcc})
		r.ECE += float64(a.count) / float64(total) * math.Abs(binAcc-binConf)
	}
	return r
}
```
Add `"math"` to the `eval.go` imports if not present.

- [ ] **Step 5: Run to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run TestCalibration -v`
Expected: PASS (both tests; the hand-computed ECE 0.25875 matches).

- [ ] **Step 6: Full eval-package tests + build + gofmt**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ && go build ./... && gofmt -l internal/agent/enrich/eval/`
Expected: PASS, no build errors, gofmt output empty.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/enrich/eval/
git commit -m "feat(eval): capture per-facet confidence + Calibration (reliability bins + ECE)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: `keld-agent eval --calibration` reporting

**Files:**
- Modify: `internal/agentcli/evalcmd.go`

**Interfaces:**
- Consumes: `eval.Calibration(rows, pred, facet, 10)` (Task 1).
- Produces: `--calibration` flag → per-facet reliability tables + ECE, pure-classifier facets first, then a labeled "rule-influenced" group (`sensitivity`, `function_guess`).

- [ ] **Step 1: Add the `--calibration` flag + printing** (`evalcmd.go`)

Add `var withCalibration bool` and `cmd.Flags().BoolVar(&withCalibration, "calibration", false, "Print per-facet accuracy stratified by GLiNER2 confidence (reliability bins + ECE).")`.

After the existing metric printing, add:
```go
	if withCalibration {
		classifier := []string{"task_type", "domain", "activity_type", "personal", "speech_act", "subcategory"}
		ruleInfluenced := []string{"sensitivity", "function_guess"}
		printCal := func(title string, facets []string) {
			fmt.Fprintf(out, "\n== calibration: %s ==\n", title)
			for _, f := range facets {
				r := eval.Calibration(rows, pred, f, 10)
				fmt.Fprintf(out, "  %-15s N=%-3d ECE=%.3f\n", r.Facet, r.N, r.ECE)
				for _, b := range r.Bins {
					fmt.Fprintf(out, "      [%.1f,%.1f) n=%-3d conf=%.3f acc=%.3f\n", b.Lo, b.Hi, b.Count, b.MeanConf, b.Accuracy)
				}
			}
		}
		printCal("classifier facets", classifier)
		printCal("rule-influenced (confidence forced to 1.0 on some rows — reflects rules, not model)", ruleInfluenced)
	}
```
(`rows` and `pred` are the already-computed gold[+confound] rows and predictions from the existing command body — reuse them; do NOT re-run the model.)

- [ ] **Step 2: Build + vet + gofmt**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./... && go vet ./internal/agentcli/ && gofmt -l internal/agentcli/`
Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add internal/agentcli/evalcmd.go
git commit -m "feat(eval): --calibration reports per-facet reliability bins + ECE

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

- [ ] **Step 4: MEASURE (controller-run, live sidecar)** — produce the actual calibration curves

Start the warm 8-thread dev sidecar (HANDOFF recipe), then:
```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
go build -o /tmp/exp-cal ./cmd/keld-agent
/tmp/exp-cal eval --context --calibration          # gold-set reliability per facet
/tmp/exp-cal eval --confound --context --calibration  # extended set
```
Record in `docs/superpowers/HANDOFF.md`: the per-facet ECE and the task_type reliability table (where do its errors sit on the confidence axis?), plus the read on whether GLiNER2 is over/under-confident. This is the input to the task_type-improvement decision and the future abstain-lever (Lever F) spec.

---

## Notes for the implementer
- Do NOT implement an abstain threshold or change the pipeline — this plan only measures. Lever F (abstain) is a separate spec built from these curves.
- The `sensitivity`/`function_guess` tables will show a large top-bin (conf≈1.0) spike from rule-forced predictions — that is expected and is why they are grouped separately and labeled.
