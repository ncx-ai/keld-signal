# Results — can GLiNER2 classify window-scale work?

Pre-registered in `2026-08-22-prose-probe-preregistration.md` (`5980a78`); ground truth committed
before the first model call (`774d799`). 216 calls, study sidecar, 18 threads, `max_len` 768,
production daemon stopped. Repeatability: 0 divergences in 10 repeats.

## The headline

**The digest is inert as input to a bi-encoder. The transcript text is not.** That is the exact
inversion of the LLM result, and the pre-registration said the prior would not transfer.

| arm | accuracy (n=24) | high+med only (n=14) | distinct predictions |
|---|---|---|---|
| `majority` (always `record`) | 62.5% | 50.0% | — |
| `rule` (frame) | **41.7%** | 64.3% | — |
| `G-digest` | 62.5% | 42.9% | **1** |
| `G-text` | **79.2%** | **71.4%** | 2 |
| `G-both` | 66.7% | 42.9% | 3 |

`single_label` and `multi_label` produced identical labels throughout; only the scores differ.

## 1. The digest is a constant classifier

`G-digest` answered `record` on **all 24 gated windows and all 12 controls — 36 of 36**, at
confidence 0.82–0.999. Its 62.5% is the majority baseline reproduced by a model that says the same
word every time, not a classification. Reading the aggregate table alone would have reported it as
"matches the baseline".

A post-hoc probe (labelled as such — not pre-registered) tested whether one boilerplate phrase was
responsible. It is not, and the real mechanism is worse:

| digest variant | distinct predictions | accuracy |
|---|---|---|
| full | `record` only | 62.5% |
| summary sentences only | `record`, `explain` | 45.8% |
| with "recorded/references/evidence" stripped | `record`, `explain` | 41.7% |

Stripping the harness vocabulary does not recover signal — it moves the constant from `record`
toward `explain`, and accuracy falls. **The digest is a uniform genre.** Every digest is the same
kind of text — a deterministic statistical description of an hour — so a bi-encoder asked what kind
of writing this is scores the digest's own register, which is constant by construction, rather than
the work it describes. This is not fixable by rewording the digest.

It is the same class of defect as `programming_language` returning `Bash x354` from our own header,
one level up: the harness's text answered the question.

## 2. The transcript text carries real signal

`G-text` scored **79.2% (19/24)** against a 62.5% majority — +16.7 points, outside the ±8 noise
band the pre-registration set. On the 14 windows labelled high or medium confidence: 71.4% against
that subset's 50% majority, +21.4.

Per class: `record` 14/15, `plan` 5/8, `explain` **0/1**. It predicted 6 `plan`s and 5 were right.

The one unambiguous window in the set — `g29`, an hour auditing and rewriting the Atlas README,
truth `explain`, labelled high confidence — was answered `record` at **0.993** by every arm.

## 3. Combining is worse than text alone, for a measured reason

`G-both` scored 66.7%, below `G-text`'s 79.2%. The cause was recorded in advance: the digest is
438–495 word tokens, the text 1498 median, the budget 768, and truncation is head-keep. So `both`
is *the whole digest plus roughly the first 300 tokens of text* — adding the digest displaces the
input that carries the signal.

## 4. There is no abstention, in either mode

**0 declines out of 12 negative-control windows, on every arm, in both modes**, at median
confidence 0.956–0.998. These are pure coding hours with zero prose evidence, where "what kind of
writing work is this" has no answer. All 36 control calls returned `record`.

`multi_label` with `cls_threshold` 0.5 buys nothing: the sigmoid scores pin near 1, so the
threshold never binds. This needs no ground truth and is the most reliable result in the run.

**The fact gate is therefore load-bearing and cannot be replaced by a confidence threshold.** It is
the "removing the question" finding — seven failed attempts to buy abstention with a label —
reproduced on GLiNER2.

## 5. The frame rule failed, and the comparison it was meant to provide is void

`rule` scored 41.7%, *below* the majority baseline, predicting `plan` for 19 of 24 windows. The
cause is diagnosable: these sessions **read** `docs/superpowers/plans/*.md` while executing a plan,
so the path is in the frame, and the rule read file-presence as subject matter — the `pdf 54%`
error one level up. The rule was written before any score was seen and was not tuned afterwards.

Consequence: "beats the rule" cannot mean anything here. The operative comparison is the 62.5%
majority, which `G-text` clears and `G-digest` does not.

## Verdict against the pre-registered rule

- **Not dead.** `G-text` beats the majority baseline by 16.7 points.
- **`Alive` by the letter, unearned in substance** — it beats `rule`, but `rule` is broken.
- **Compression is worth building.** The pre-registration said: if `G-text` or `G-both` wins by
  more than 5 points, build it. `G-text` beats `G-digest` by 16.7. The signal-bearing input is the
  one that does not fit the budget — text is 1498 median tokens against 768 — so segment-aware
  compression is now the indicated next step rather than a nicety.
- **The gate is justified.** 0/12 declines without it.

## What this does not support

- n=24, of which **10 are low confidence**, because `prose >= 5` selects "touched markdown", not
  "spent the hour writing". Most gated windows are agentic coding sessions that edited a doc.
- `explain` has **n=1**, so the three-class problem is effectively two-class.
- One genuine near-duplicate pair (`g14`/`g20`, 8 shared turns from paired session files), so
  effective n is 23.
- A single labeller who also ran the arms.
- `G-text` never predicts `explain` and may be separating `plan` from `record` on surface
  vocabulary rather than on rhetorical function. Untested.

## Not run

The 2-thread vs 18-thread label-transfer check was dropped on instruction (18 threads is a
development convenience; production runs 2–4). Repeatability at a fixed thread count passed, so
the run is internally consistent; whether the same labels come out at production thread counts is
**unmeasured**.
