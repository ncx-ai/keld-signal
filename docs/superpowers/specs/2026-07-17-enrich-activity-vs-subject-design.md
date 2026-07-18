# Reduce subject-matter leakage in enrichment (activity vs. topic)

**Date:** 2026-07-17
**Status:** Approved (design) — experiment, measure-first
**Scope:** Go-only (enrichment pipeline + eval harness). No frozen-sidecar change.

## Problem

Enrichment mislabels the user's **work activity/role** based on the **subject the
prompt discusses**. An engineer using `claude_code` to build marketing/finance/
legal software gets `function_guess` = `mkt`/`fin`/`legal` and skewed
`task_type`, because GLiNER2 is a bi-encoder that scores the prompt against
label *descriptions*, and the `Functions` label texts (`labels.go:77`) describe
**subjects** ("marketing and content: copy, campaigns, SEO"), not **activities**.
The strongest disambiguator — the source tool (`claude_code` ⇒ engineering) — is
only injected as weak preamble text, never as a prior. `domain` already captures
subject, so `function_guess` duplicates/​amplifies it instead of adding the
orthogonal role axis.

## Goal

Reduce **subject-leakage** (engineering-activity prompts misclassified to the
subject's function/task) **without** regressing base classification accuracy or
inflating **false-eng** (genuine non-eng work wrongly forced to engineering).
Every additive step is validated empirically before it's kept.

## Decisions (pinned)

- **A1 mechanism — soft posterior.** For interactive coding tools, combine a
  prior over functions with the model score multiplicatively and renormalize:
  `score'(f) = score(f) × prior(f | tool)`, then pick argmax. Strong non-eng
  evidence can still win; prior strength is a tunable parameter to sweep.
- **A2 — activity-phrased, domain-invariant labels.** Rewrite the `Functions`
  (and, if it helps, `task_type`) label descriptions to describe the user's
  *act* regardless of subject, plus a decoy that routes "building software about
  X" to `eng`.
- **Acceptance gate — strict, no-regression.** Keep an additive step only if, vs.
  the prior checkpoint: confound **leakage-rate drops meaningfully**, base
  `gold.jsonl` per-facet accuracy **does not regress** (Δ ≥ 0 within noise), and
  **false-eng-rate stays flat** (Δ ≤ small ε). Otherwise revert/iterate.
- **Confound set — hybrid.** ~30 realistic synthetic rows authored here + a
  handful of the operator's real (anonymized) mislabeled prompts.

Everything is **flag-gated** so each variant is A/B-measurable and nothing ships
until the numbers justify it.

## Phase 0 — measurement substrate (must land first; produces the baseline)

There is no real-sidecar eval runner today (`eval_test.go` only unit-tests
`Score` with fakes). Build it.

1. **`keld-agent eval` dev subcommand** (`internal/agentcli`): loads a gold file
   (defaults to embedded `gold.jsonl`; `--confound <path>` adds the confound
   set), resolves the **live** GLiNER2 client via `localagent` (reads
   `agent.json`), runs `enrich.Run`/`RunModelWithContext` over each row, and
   prints a per-facet metric table (accuracy, sensitive_recall, and the new
   metrics below). Flags: `--context` (augmented vs baseline), and the
   experiment flags (below) so a single command produces a comparison row.
2. **Confound set** `internal/agent/enrich/eval/confound.jsonl`:
   - **C1** — engineering activity, non-eng subject (building marketing/finance/
     legal/medical software). Gold: `function_guess=eng`, `task_type` per the
     real act (codegen/…), `domain=software`.
   - **C2** — genuine non-eng work (real marketing/finance/legal prompts). Gold:
     the true non-eng function. Guards against A1/A2 over-correction.
   - **C3** — context-dependent fragments ("now fix that") with `recent_prompts`.
   ~30 synthetic + operator's real rows.
3. **New metrics** (`eval.go`):
   - `subject_leakage_rate` = over C1 rows, fraction where `function_guess` or
     `task_type` is pulled to the subject instead of the eng-correct label.
   - `false_eng_rate` = over C2 rows, fraction wrongly predicted `eng`/eng-ish.
4. **BASELINE**: run the substrate over `gold.jsonl` + `confound.jsonl` on the
   current pipeline; record the table. This is the reference every later phase
   is gated against.

## Phase 1 — A1 tool prior (soft posterior), behind `KELD_ENRICH_TOOL_PRIOR`

- A `prior(function | tool)` table: for `claude_code`/`codex`, mass concentrated
  on `eng` (and eng-adjacent `it`/`data`), small floor elsewhere; generic/unknown
  tools ⇒ uniform (no-op). Prior strength tunable (e.g. a temperature/weight).
- Apply multiplicatively in the function classification, renormalize, argmax.
  (Evaluate applying the same idea to `task_type`.)
- Re-run the substrate; **gate** vs baseline. Sweep prior strength to find the
  knee (max leakage↓ before false-eng rises).

## Phase 2 — A2 activity-phrased labels, behind a label-set version flag

- Rewrite `Functions` label texts to activity/role framing with explicit
  domain-invariance; add the "building software about X ⇒ eng" decoy.
- Bump `SchemaVersion` semantics as needed (vocab change is contract-affecting).
- Re-run: **A2 alone** and **A1+A2**; gate each vs the best prior checkpoint.

## Metric definitions (exact)

- Base accuracy: existing per-facet accuracy over `gold.jsonl` (unchanged).
- `subject_leakage_rate` (C1): `mislabeled_to_subject / |C1|`, per facet
  (function_guess, task_type) and combined.
- `false_eng_rate` (C2): `predicted_eng_when_gold_noneng / |C2|`.
- Report all three per variant in one table so tradeoffs are visible.

## Out of scope (later)

- Shipping the winner (its own spec→plan→execute once the bake-off picks one).
- A4–A9 alternatives (compositional function, entity-masking, NLI/​LLM model
  swaps) — future contrasts if A1+A2 plateaus.
- Any change to the frozen Python sidecar.

## Verification

The experiment *is* the verification: each phase's kept/rejected decision is
backed by the metric table from the `keld-agent eval` runner against the live
sidecar. No step is merged on assertion — only on numbers.
