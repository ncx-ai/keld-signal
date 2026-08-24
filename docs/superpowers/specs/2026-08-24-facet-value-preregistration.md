# Are the seven model-backed classification facets worth a 1.8 GB dependency? — pre-registered

Written 2026-08-24, BEFORE any sidecar was started or any model run scored. Decision rules below
are fixed. Results go in `RESULTS.md` (durable copy) and
`docs/superpowers/specs/2026-08-24-facet-value-results.md` (committed).

## Question

Seven facets classify prompt text on GLiNER2:

    task_type · domain · activity_type · personal · function_guess · speech_act · subcategory

They are the entire reason `ml_backend:"auto"` provisions gliner2-large-v1 (~1.8 GB download,
~2.7 GB resident). `SensitivityExtractor` and `WorkstreamsExtractor` are already `ModelFree`, so if
these seven do not earn the model, nothing does.

## Why the bar is set where it is

Two prior measurements on this same model scored a *related* facet far below a constant:

- `docs/superpowers/plans/2026-08-22-window-context-handoff.md` — `activity_type` over frame
  digests: **33%** against a **67%** majority-class guess.
- `~/keld/refseries-context/prose-activity/RESULTS.md` — assistant prose against an activity
  taxonomy: **37.8%** vs a **67.8%** majority baseline, **degenerate at 91% one class**, and
  confidently wrong (0.975 on a clear miss).

Neither classified a **user prompt** against **these** vocabularies, which is what production does.
So they are suggestive, not decisive — hence this study. But they fix the bar: a facet that cannot
beat "always answer the most common label" is not earning the dependency, and an accuracy number
alone cannot be trusted, because degeneracy has already produced a plausible-looking score twice.

## Sample — fixed before scoring

`internal/agent/enrich/eval/gold.jsonl` via `eval.LoadGold()`: **165 rows**. Per facet the
denominator is the rows carrying a non-blank gold label for that facet (`eval.Score` excludes
blanks). Counted from the labels alone, before any model ran:

| facet | labeled n | distinct gold labels | vocab size |
|---|---|---|---|
| task_type | 161 | 10 | 10 |
| domain | 161 | 9 | 9 |
| activity_type | 103 | 6 | 6 |
| speech_act | 164 | 4 | 4 |
| function_guess | **20** | 8 | 12 |
| subcategory | **20** | 17 | ~40 |
| personal | **0** | 0 | 2 |

## Baselines — computed from the gold labels only, stated before seeing any model score

The majority-class rate per facet (the accuracy of a constant answer, no model, no dependency):

| facet | majority label | baseline |
|---|---|---|
| task_type | `code_generation` (23/161; tied with `question_answering`, `text_generation`) | **0.143** |
| domain | `software` (42/161) | **0.261** |
| activity_type | `generate` (25/103) | **0.243** |
| speech_act | `command` (117/164) | **0.713** |
| function_guess | `eng` (6/20) | **0.300** |
| subcategory | `eng.dev` (3/20) | **0.150** |
| personal | — | **not computable (no labels)** |

Note the spread: `task_type` faces a very weak baseline (10 near-balanced classes) and `speech_act`
faces a very strong one (71% `command`). This is stated now so neither can be re-framed later.

## Runs

Both, for every facet, via the existing harness (`eval.RunModel`, `eval.RunModelWithContext`,
`eval.Score` — no second harness is written):

- **NOCTX** — `RunModel`: prompt text only, no `Meta`. Understates production.
- **CTX** — `RunModelWithContext`: each row's session context through `GoldRow.Meta` →
  `Meta.Preamble()`/`PreambleCoding()`, which is what the daemon does.

**CTX is the deciding number** for a shipping judgement, because it is the production path. NOCTX is
reported beside it. If NOCTX is higher, that is reported as a finding about the preamble and the
decision still runs on CTX.

## Decision rules — fixed before the run

Per facet, on the CTX run:

1. **PRIMARY (lift).** Accuracy must beat its majority baseline by **>= 10 accuracy points**.
   Below the baseline, or within 10 points of it, the model is not buying anything a constant
   does not.
2. **SIGNIFICANCE.** The lift must survive a one-sided exact binomial test against the baseline
   rate at **p < 0.05**. This exists because two facets have n = 20, where a 10-point gap is
   1-2 rows.
3. **DEGENERACY.** Any facet whose single most-frequent PREDICTED label exceeds **70%** of its
   predictions is flagged degenerate. A degenerate facet is **disqualified** — it is reporting its
   prior, not the prompt — *unless* the gold distribution is itself that concentrated (only
   `speech_act`, at 71.3% `command`, can invoke this), in which case the degeneracy is reported
   and the facet is judged on rules 1-2 alone.
4. **UNDERPOWERED.** A facet with fewer than 30 labeled rows cannot be decided either way by this
   study. `function_guess` (20) and `subcategory` (20) are underpowered **by construction, stated
   now**: their verdict is capped at "re-measure with better ground truth" regardless of what they
   score. A good score there is not a ship signal and a bad one is not a drop signal.
5. **UNMEASURABLE.** `personal` has **zero** gold labels and the harness has no `personal` field on
   `GoldRow`, so its accuracy cannot be computed at all. Verdict is fixed in advance as
   "unmeasurable — no ground truth exists". The prediction distribution IS still reported (that
   needs no labels), and a degenerate distribution there is reported as evidence, not as an
   accuracy claim.

### Verdicts these rules produce

- **SHIP** — passes 1, 2, and 3, with n >= 30.
- **DROP** — fails rule 1 (at or below baseline + 10), with n >= 30. A facet scoring *below* its
  baseline is worth negative value: a constant is both cheaper and more accurate.
- **RE-MEASURE** — n < 30, or passes 1 but fails 2 (real-looking lift, insufficient evidence).
- **UNMEASURABLE** — `personal`.

### The July floors

`minTaskTypeAcc = 0.50` / `minDomainAcc = 0.40` (`eval/sidecar_eval_test.go`, `-tags sidecar`,
calibrated 2026-07-14 on a 73-row gold set at SchemaVersion 6). This study reports what they would
say against the new numbers and whether they are still usable. It does **not** re-derive or move
them — that is a separate decision.

## Reported per facet, no exceptions

accuracy (CTX and NOCTX) · majority baseline · lift · binomial p · full prediction distribution ·
gold distribution · named misses with the model's actual answer and its confidence.

Named rows are mandatory, not decoration: ~20 defects in this programme surfaced as plausible wrong
numbers and essentially none was caught by reading an aggregate.

## Stated limitations, before the result

- **The gold set is curated in-house**, by the same people who wrote the vocabularies, and each row
  was written to have a clear label. It is therefore an *optimistic* estimate of production
  accuracy. A facet that fails here fails on easy data.
- **165 rows** is small. Per-class support at 10 classes is ~16 rows; per-class claims are
  directional only.
- The gold set predates the current `SchemaVersion` (8) in parts; `task_type` gold values are all
  in the current v6 routing vocabulary and `domain`/`activity_type`/`speech_act` gold values are all
  in the current vocabularies (verified before this run, by set comparison against `labels.go`).
- **A null result is a result.** "These facets do not earn the model" is a finding, and so is "some
  do". An honest split is the most likely outcome and no rule above is tuned to produce one.
- This study **measures only**. It removes, disables and changes nothing.
