# Confidence-stratified accuracy (calibration eval instrument)

**Date:** 2026-07-18
**Status:** Approved (brainstorm) — ready for implementation planning
**Scope:** Go-only, eval harness only. Report per-facet accuracy **stratified by
GLiNER2's returned confidence** — reliability bins + Expected Calibration Error
(ECE) — so we can tell whether the confidence score is trustworthy and where
errors sit on the confidence axis. **No pipeline change.** This is the measurement
instrument for the task_type focus and the prerequisite for a future abstain lever.

## Motivation

Every facet is reported as a single accuracy number (task_type 0.696, …). That
hides the question that matters: **is GLiNER2's confidence calibrated?** Its
confidence is a bi-encoder sigmoid score, not a guaranteed probability — a "0.95"
might mean 95%-likely-correct or might mean the model is simply always confident.
Stratifying accuracy by confidence answers this and unlocks:
1. A **reliability view**: if `≥0.9` predictions are ~0.97 accurate and `<0.7` are
   near-random, then "0.696" means "excellent when confident, guessing on a tail" —
   a completely different problem than "mediocre classifier."
2. The **evidence for an abstain lever (Lever F, next spec)**: you cannot pick an
   abstain threshold until the reliability curve shows the score means something.
3. It **sharpens the task_type work**: see *where* task_type errors live on the
   confidence axis before deciding classifier-fix vs. abstain.

The data already exists — `Labeled.Confidence` holds GLiNER2's returned score for
every facet prediction; the eval discards it today. So this is a harness upgrade,
not a pipeline change.

## Goals

1. Per-facet **reliability table** (confidence bin → count, mean confidence,
   accuracy) + per-facet **ECE**, printed by `keld-agent eval --calibration`.
2. Zero change to the enrichment pipeline or any existing metric/output.
3. Honest handling of facets whose confidence is **rule-forced** (not GLiNER's).

## Non-goals

- **Abstain / thresholding** (Lever F) — the next spec, designed from this output.
- **Entity-span calibration** (sensitivity spans have per-span confidence) — later.
- Any change to `Labeled`, the extractors, or the pipeline.

## Design

### Capture the confidence in the eval

`Pred` (`eval.go`) currently stores only label strings. Add a per-facet confidence
map:
```go
type Pred struct {
    …existing string fields…
    Conf map[string]float64 // facet name -> top-label confidence
}
```
`RunModel` / `RunModelWithContext` populate it from the Profile's `Labeled.Confidence`
(e.g. `Conf["task_type"] = p.TaskType.Confidence`) for each facet. Purely additive —
existing accuracy scoring is untouched.

### The calibration metric

`Calibration(gold, pred, facet, nbins) → Reliability` where `Reliability` holds, per
non-empty bin: `[lo,hi)`, `count`, `meanConf`, `accuracy`; plus the facet's **ECE**
= Σ over bins `(n_bin/N)·|accuracy_bin − meanConf_bin|` (N = predictions with a
non-empty gold label for that facet). Fixed-width bins, default **10** (`[0,.1)…[.9,1]`).
A prediction counts in the bin its top-label confidence falls in; correctness is the
existing `fieldOf` gold==pred check. Rows with a blank gold label for the facet are
excluded (same convention as `Score`).

### Reporting

`keld-agent eval --calibration` prints, per facet, the reliability table + ECE, over
the gold set with context (the clean accuracy reference); `--confound` extends the
set. Default output unchanged (new flag only).

### Rule-forced-confidence handling (the wrinkle)

Two facets have confidence **forced to 1.0 by rules, not GLiNER**: `sensitivity`
(hard entity rule + creddetect, `extractors.go`) and `function_guess` (A4
coding-tool → `eng`@1.0, `a4_compositional.go`). Their calibration would measure the
rules, not the model. Handling, without touching the pipeline:
- The **primary calibration set is the pure-classifier facets**: `task_type`,
  `domain`, `activity_type`, `personal`, `speech_act`, `subcategory`.
- `sensitivity` and `function_guess` are reported **separately and labeled
  "rule-influenced"** (their tables will show a spike in the top bin from the
  forced 1.0s — that's expected and honest, not a bug). This is documentation +
  facet grouping, not a pipeline signal.

## Success criteria

- `keld-agent eval --calibration` prints a per-facet reliability table + ECE.
- Numbers produced for the pure-classifier facets (esp. `task_type`) on the gold set.
- `ECE` math verified by a unit test with hand-computed bins.
- No change to any existing metric, output (without the flag), or the pipeline.

## Rollout

Build the metric + flag, unit-test the bin/ECE math, then a controller-run
`--calibration` pass to produce the actual reliability curves (esp. task_type) —
which becomes the input to the task_type-improvement decision and the future
abstain-lever spec.
