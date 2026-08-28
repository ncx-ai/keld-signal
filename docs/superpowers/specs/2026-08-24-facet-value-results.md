# Are the seven model-backed facets worth the model? — four ship, one drops, two cannot be decided

Rules fixed in `PREREGISTRATION.md` before the sidecar was started. Live gliner2-large-v1 on
127.0.0.1:8412, schema v8, 165 gold rows, 2,015 inferences, 17m04s of single-flight inference.
Raw output: `RESULTS-raw.md`. Per-row dump (both arms, every facet): `rows.ndjson`.
Harness: `internal/agent/enrich/eval/facetstudy_test.go` (`-tags sidecar`, asserts nothing).

## The table

| facet | n | baseline (majority) | acc CTX | acc NOCTX | lift | p (one-sided) | top predicted label | ECE | **verdict** |
|---|---|---|---|---|---|---|---|---|---|
| task_type | 161 | 0.143 `code_generation` | **0.733** | 0.733 | **+0.590** | 7e-64 | question_answering 25% | 0.210 | **SHIP** |
| domain | 161 | 0.261 `software` | **0.683** | 0.683 | **+0.422** | 5e-29 | software 28% | 0.069 | **SHIP** |
| activity_type | 103 | 0.243 `generate` | **0.670** | 0.650 | **+0.427** | 7e-20 | converse 23% | 0.205 | **SHIP** |
| speech_act | 164 | 0.713 `command` | **0.695** | 0.695 | **−0.018** | 0.73 | command 50% | 0.239 | **DROP** |
| function_guess | 20 | 0.300 `eng` | 0.700 | 0.600 | +0.400 | 2.6e-4 | eng 35% | 0.155 | RE-MEASURE (n<30) |
| subcategory | 20 | 0.150 `eng.dev` | 0.550 | 0.500 | +0.400 | 3.9e-5 | (abstain) 10% | 0.300 | RE-MEASURE (n<30) |
| personal | **0** | — | — | — | — | — | **work 81%** | — | UNMEASURABLE |

**No facet is degenerate** (rule 3) except `personal`, which has no gold labels to score against.
Three facets clear the baseline by 42-59 points at p < 1e-19. The seven are not one thing and the
"all bad" outcome did not happen.

## The prior negative results do not reproduce, and the reason is the INPUT

Two studies scored this same model at 33% and 37.8% against ~67% majority baselines on an activity
taxonomy. On **user prompts** against **these vocabularies**, `activity_type` scores **0.670 against
a 0.243 baseline** — a +42.7-point lift where the earlier runs were 30 points *below* a constant.

The model did not change. The input did: frame digests and assistant prose fail, first-person user
prompts do not. Those results should be read as scoped to non-prompt inputs, not as a verdict on
GLiNER2 classification. Refuting them on their own ground still stands — nothing here says a digest
can be classified.

## speech_act is the one that fails, and it fails by inventing `statement`

    gold      command 117 (71%)   question 42 (26%)   fragment 4 (2%)   statement 1 (1%)
    predicted command  82 (50%)   question 58 (35%)   statement 22 (13%)  fragment 2 (1%)

    class      support  predicted  recall  precision
    command      117       82       0.650    0.927
    question      42       58       0.857    0.621
    statement      1       22       0.000    0.000
    fragment       4        2       0.500    1.000

`statement` is predicted 22 times and is right **zero** times. Every one of those is an imperative
work request mislabelled as a description, and it is confident about it:

    #78  command → statement @1.000  "Tighten the wording on this executive summary paragraph…"
    #59  command → statement @0.968  "Review this vendor MSA and flag any one-sided indemnification clauses."
    #54  command → statement @0.913  "summarize this 40-minute meeting transcript into decisions and action items"
    #14  command → statement @0.901  "TL;DR this twenty-page product requirements document."

The other arm of the failure is `command → question` x22, all on explain/research imperatives:

    #52  command → question @1.000  "Explain the difference between civil and criminal liability."
    #60  command → question @1.000  "Research whether this data-sharing clause complies with GDPR Article 28."
    #68  command → question @1.000  "Research what competitors are doing for their Q3 marketing campaigns."
    #28  command → question @1.000  "Explain why this distributed locking scheme avoids deadlock."

Mean confidence is 0.966 on hits and 0.862 on misses — a 10-point separation that cannot gate
anything. This is the confidently-wrong signature the prose study named, reproduced on the one facet
that fails here.

**Under rule 1 the verdict is DROP.** Worth stating plainly what dropping buys: `speech_act` is one
inference of eight, so removing it saves ~12% of enrichment CPU and **nothing** of the 1.8 GB — the
other facets still need the model. And the label wording is the suspect, not necessarily the facet:
`command`="a task to carry out" and `statement`="a statement describing a situation" are the two
`SpeechActDefs` entries the confusion runs between, and that wording was bakeoff-tuned once before
(0.624→0.731). A vocabulary re-bakeoff is a legitimate alternative to dropping — but the facet as it
ships today is worth less than the constant `command`, and that is the measured fact.

## Where the three shipping facets are actually wrong

`task_type` (0.733): the whole error budget is `reasoning`, recall **0.350** — 8 of 20 reasoning rows
go to `question_answering`.

    #27  reasoning → question_answering @0.998  "If one train leaves at 60 mph and another at 40 mph, when do they meet?"
    #95  reasoning → question_answering @0.564  "Explain why two transactions updating the same row … deadlock in Postgres"
    #97  reasoning → code_generation    @0.987  "This React component re-renders on every keystroke even though I memoized…"

`translation` is 8/8 at precision 1.000; `code_generation` recall 0.957. For the routing use this
matters in a specific way: `reasoning` is the expensive tier, and it leaks *downward* into
`question_answering` (precision 0.500) — the cheap-model direction.

`domain` (0.683): two confusions carry it — `business → software` x10 and `general → business` x9.

    #67  business → software @0.582  "Write a blog post announcing our new product launch next month."
    #73  business → software @0.314  "Write a launch announcement blog post for our new analytics dashboard."
    #9   software → finance  @0.690  "Write a SQL query to find the top 10 customers by revenue this quarter."

`legal` and `medical` are recall 1.000. `business` recall 0.471 is the weak class and it is the
diffuse one `DomainDefs` already flags as hard. Domain has the best calibration in the study
(ECE 0.069, conf 0.785 correct vs 0.525 wrong) — its confidence is a usable filter.

`activity_type` (0.670): `analyze` recall **0.333**, leaking to `converse` x6 and `retrieve` x5.

    #94  analyze → converse @0.957  "Why is this goroutine leaking memory under sustained load…"
    #112 analyze → retrieve @0.958  "We're losing 8% of enterprise customers per quarter … here's the churn data"
    #88  analyze → review   @0.863  "Run the failing integration test suite in CI and bisect which commit…"

`converse` precision 0.417 — it is the magnet. `review` has support 2; nothing can be concluded
about it either way.

## personal cannot be scored at all — the task brief is wrong about this

The brief states `gold.jsonl` carries `personal` labels. It does not: **zero of 165 rows** have one,
and `eval.GoldRow` had no `personal` field before this study added one. `eval.Score` excludes blank
gold from the denominator and reports a field with no labeled rows as accuracy **1.000, vacuously** —
so anyone scoring `personal` through the existing harness gets a perfect score for a facet that was
never measured. That is the most dangerous number in this report and it is the one nobody would have
questioned.

What can be said without labels is the distribution: **`work` 81%, `personal` 18%, abstain 1%**.
Above the 70% degeneracy flag, but on a gold set that is ~all work prompts a high `work` share is
also the *correct* answer, so this is a flag and not a finding. `personal` needs ground truth before
it can be judged. It is the only facet whose verdict is "we do not know".

## function_guess and subcategory: good scores, undecidable by construction

Both score +40 points over baseline at p < 1e-3, and both have **n = 20**. Rule 4 caps them at
RE-MEASURE and that cap was written down before the numbers existed. Two further reasons not to read
them as a pass:

- **2 of the 20 rows are unanswerable by construction.** Rows #70 (`"yes"`) and #72
  (`"looks good, ship it"`) are gold-labelled `eng` / `fin`, and the model-free approval gate
  (`enrich/gate.go`, `prefilterContentFree`) abstains on them by design. Excluding them,
  function_guess is 14/18 = 0.778 and subcategory 11/18 = 0.611. Neither the score nor the
  correction changes the verdict.
- **The CTX arm changes function_guess on 125 of 165 rows** — not because of session context, but
  because `RunModel` leaves `Meta.Tool` empty while `RunModelWithContext` sets `claude_code`, and
  the A4 rule is tool-conditioned. The 10-point CTX-over-NOCTX gap on 20 rows is 2 rows and is
  noise; the *tool prior* is the real lever and this gold set is not built to isolate it.

## Which number is honest — and a limitation of the gold set

**CTX is the honest number** (it is the production path) but on this corpus the two arms are nearly
the same input: `domain` and `speech_act` are byte-identical across arms, `task_type` differs on 7 of
165 rows, `activity_type` on 10. **Only 4 of 165 gold rows carry `recent_prompts` and 3 carry
repo/project** (rows #69-#72), and all four are approval fragments. So:

- This study does **not** measure the value of the context preamble. The existing augmentation check
  in `sidecar_eval_test.go` is measuring almost nothing on this corpus either.
- The four context-bearing rows are the ones the approval gate abstains on, so the gold set's only
  context path is also its only guaranteed-miss path. A gold set that could measure the preamble is
  a prerequisite for any claim about `Meta.Preamble()`.

Other limitations, as pre-registered: the gold set is curated in-house against these same
vocabularies, so every number here is an **optimistic** bound; per-class support is ~10-40 rows so
per-class claims are directional; every facet is over-confident (ECE 0.07-0.30).

## What the July floors would say

`minTaskTypeAcc = 0.50` / `minDomainAcc = 0.40` (`eval/sidecar_eval_test.go`, calibrated 2026-07-14
on 73 rows at task_type 0.580 / domain 0.449):

| floor | value | measured today | verdict |
|---|---|---|---|
| minTaskTypeAcc | 0.50 | **0.733** | PASS, 23 points of slack |
| minDomainAcc | 0.40 | **0.683** | PASS, 28 points of slack |

They are **usable but stale-loose**, and stale in the safe direction: the model scores far *better*
than when they were set (bigger gold set, v6 routing vocabulary, description-scored labels). Both
still sit above their majority baselines (0.143 / 0.261), so they would catch a collapse to a
constant — but they would sleep through a 20-point regression. By this file's own convention
(measured − 0.05) a fresh calibration would put them at ~0.68 / ~0.63. **This study does not move
them.** Re-deriving floors is a decision with its own review, and the gate they live in has not run
in CI since July — a floor nobody runs is not protecting anything regardless of its value.

## Recommendation per facet

| facet | recommendation |
|---|---|
| `task_type` | **SHIP.** +59 points over baseline, the strongest result in the study. Watch `reasoning` recall (0.350) if it is used for tier routing. |
| `domain` | **SHIP.** +42 points, best-calibrated facet, confidence usable as a filter. |
| `activity_type` | **SHIP.** +43 points on user prompts. Explicitly *not* refuted by the two prior digest/prose studies. |
| `speech_act` | **DROP** as it ships — worth less than the constant `command`, invents `statement` 22 times at zero precision, confidently. Re-bakeoff of `SpeechActDefs` (command/statement wording) is the alternative to removal; it is one inference of eight and frees no memory. |
| `function_guess` | **RE-MEASURE** — needs ≥100 labelled rows. Scores well on 20; the tool prior, not the prompt, may be doing the work. |
| `subcategory` | **RE-MEASURE** — 20 rows over a 58-label conditioned vocabulary is ~0.3 rows per label. |
| `personal` | **RE-MEASURE with ground truth that exists.** Currently unmeasurable, and the harness reports it as 1.000 by convention, which is worse than reporting nothing. |

**The model is earned regardless of the speech_act outcome**: `task_type`, `domain` and
`activity_type` each beat their baselines by >40 points on 103-161 labelled rows, and they alone
justify provisioning gliner2-large-v1. The honest split the pre-registration expected is what came
out — three earn it, one does not, three are unmeasured.

This study measured only. No facet, vocabulary, floor or `ml_backend` default was changed.
