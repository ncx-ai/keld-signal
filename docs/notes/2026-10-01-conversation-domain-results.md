# Head-of-conversation domain recognition on WildChat — results

**Date:** 2026-10-01
**Study:** `docs/superpowers/specs/2026-10-01-conversation-domain-design.md` (design, bar and
limits, all pre-registered) · ledger `.superpowers/sdd/2026-10-01-conversation-domain/progress.md`
**Status:** measured, reported. **The gate is open and is the repo owner's** — §9 lists the
options and decides nothing.

## The result: EVERY ARM FAILS THE BAR. This is the sixth measured negative on this question.

The bar was committed before the arms existed: **precision ≥ 80% AND accuracy ≥ majority
baseline + 20 points, on the RANDOM stratum**. Nothing cleared it.

| arm | RANDOM precision | RANDOM accuracy (baseline 46.7%) | verdict |
|---|---|---|---|
| keyword | 44.8% | 50.0% (+3.3) | FAIL both halves |
| gliner_7 | 30.0% | 30.0% (**−16.7**) | FAIL both halves |
| gliner_8 | **69.6%** | **68.3% (+21.7)** | accuracy PASS, **precision FAIL by 10.4 points** |
| union | 50.0% | 58.3% (+11.7) | FAIL both halves |
| shuffled (noise floor) | 19.0% | 28.3% (−18.3) | — |

`gliner_8` is the only arm to clear either half. Its precision is 69.6%, not 80%. The bar is
conjunctive and was written that way on purpose: an arm that is right about seven of ten
conversations it names a domain for is not something to route work on.

Design §1 lists five prior failed routes to this same question (GLiNER2 on prompts, GLiNER2 on
paths, hand-written keyword lists, agent self-report via hook, document-type recognition).
**This is the sixth.** Recording a sixth negative is the main thing this note is for; the
positive in §2 is secondary to it.

## The one real positive, and it is not about the model

`gliner_7` and `gliner_8` are **the same GLiNER2-large model over the same seven domain labels
on the same text**. The only difference is that `gliner_8` carries an eighth, null label — "a
hobby, school or everyday personal conversation, not anyone's professional work" — competing in
the same ranking (the `attribution.py` `NULL_DOC` idiom), so it can answer `none`.

**That one label is worth +38.3 accuracy points on the random stratum: 68.3% against 30.0%.**
It is the largest effect this study measured, larger than the gap between any two different
methods in it. `gliner_7` scores **below the majority baseline in both strata** (−16.7 random,
−6.7 candidate): a 1.9 GB model is worse than a constant that always answers "none".

⚠️ **And the effect is entirely abstention, not better naming — say it that way or it will be
misread.** Counting only the conversations where the arm names a domain and is right, on the
random stratum: `gliner_7` gets **18 of 60**, `gliner_8` gets **16 of 60**. The null label made
the model slightly *worse* at naming domains and bought all +23 of its net correct answers (41
vs 18 of 60) from the 28 `none` rows, 25 of which it now gets right. The same holds on
candidates (18 → 17 named-correct, +11 from `none`).

So the measured finding is narrow and real: **a forced-choice label set makes a classifier
confidently mislabel every input that belongs to none of its classes, and the cost is unbounded
in the fraction of such inputs.** Here that fraction was 46.7% of the random stratum, and it
cost a 38-point swing.

**What that implies for the shipped facets — an implication, not a measurement.** Nothing in
this study scored `ml_backend:"auto"`'s facets; this is a reading of their vocabularies
(`internal/agent/enrich/labels.go`) against the effect measured above:

- `activity_type` (6 labels) and `personal` (2 labels) are **forced choice with no way to
  decline** — the `gliner_7` shape exactly.
- `domain`'s escape is `general` = "a trivial everyday request (weather, time, jokes, personal
  chat)", deliberately **narrowed** so it stops being a magnet. That is an escape for triviality,
  not for "this is nobody's professional work" — a different thing from the null label that
  bought the 38 points here.
- `task_type`'s `general` and `function_guess`'s `gen` are broader escapes and are closer to it.

Whether any of those facets pays the `gliner_7` penalty on real traffic is **unmeasured**. The
cheap check is the one this study ran: add a null label, score the pair, keep the contrast.

## The predicted circularity DID NOT APPEAR, and the spec was wrong about it

Design §3 is the study's central safeguard. It predicted the keyword arm would "look strong on
CANDIDATES and weak on RANDOM", because the 60 candidates were selected **by a keyword net**,
and called that contrast "a finding, not a defect".

**The measurement says the opposite.** Keyword scored **−1.7 against baseline on CANDIDATES** and
**+3.3 on RANDOM**. It did not beat the majority baseline on the stratum its own method selected.
The predicted contrast is absent and its sign is reversed.

**Most likely explanation — an explanation, not a measurement.** The §1 selection net and the
arm's word lists are *different vocabularies*, kept deliberately different (ledger Ruling 3):
the net selects on phrases (`cold email`, `balance sheet`, `terms of service`), the arm scores
on its own single-word sets (`prospect`, `ledger`). The arm therefore never inherited the
selector's advantage, which is what §3 feared. Two other candidate explanations are untested:
the candidate stratum's truth is only 36.7% `none` against the random stratum's 46.7%, so
abstention pays less there; and 36.7% of candidates were labelled `none` by a human anyway (§5),
so the net's own precision is low enough that matching it would not have scored well.

Whatever the cause, the safeguard was not exercised, so **this study does not demonstrate that
the two-strata design detects selector-learning** — it demonstrates that no arm did enough
selector-learning to be caught.

## The control behaved

`shuffled_of_union` — the union's own predictions permuted (seed 20261001), preserving its
marginal distribution and abstention rate while destroying row alignment — scored **−18.3 against
baseline in both strata** (19.0% precision random, 12.0% candidate). The pipeline separates signal
from noise, so the numbers above are measuring something. A study whose shuffled control had not
separated would be reporting nothing at all.

## The frame and the labels

- **59,857 conversations scanned** in WildChat `shard0.parquet`; 30,223 non-English skipped,
  leaving 29,634 English.
- **Candidate pools** from a whole-conversation keyword scan: medical 1,019, marketing 715, legal
  521, sales 378, hr 288, financial 171. These run 2–3× the §1 estimate because §1's scan read
  user turns only and truncated at 6,000 chars.
- **120 sampled**: 60 CANDIDATE (10 per domain) + 60 RANDOM (no filter), disjoint, ids shuffled,
  stratum hidden from the labeller.
- **35 of 60 candidates (58.3%) carry their selecting keyword only after the first turn** — a
  first-turn-only scan would have missed well over half of them.

Labels (blind subagent, committed before any arm existed, 15 spot-checked by the repo owner with
0 individual corrections; one labelling *rule* was overturned — job-seeker-side work is `none`,
not `hr` — and applied per-conversation, 8 flipped, 1 kept):

| stratum | n | distribution | baseline (`none`) |
|---|---|---|---|
| candidate | 60 | none 22, engineering 11, other 10, marketing 6, financial 5, medical 3, legal 1, sales 1, hr 1 | 36.7% |
| random | 60 | none 28, engineering 14, other 12, medical 3, marketing 2, financial 1 | 46.7% |
| ALL | 120 | none 50, engineering 25, other 22, marketing 8, financial 6, medical 6, legal 1, sales 1, hr 1 | 41.7% |

**The selector's two error rates, which are findings in their own right:** 22 of 60 candidates
(36.7%) were labelled `none` by a human — the keyword net's false-positive rate — and 20 of 60
randoms carried a real in-vocabulary domain, which is what a keyword net misses. Strata are
frames, not labels.

`other` (22 of 120) is **inexpressible by every arm** and is scored wrong for all of them
equally. The `acc(expr)` column below reports accuracy over the 98 rows where the truth is
expressible, so the vocabulary's cost is visible rather than hidden.

## Full score table

Precision is over named domains only; abstention counts as a prediction of `none` and as wrong
wherever truth is not `none`.

| arm | stratum | named | precision | accuracy | acc(expr) | baseline | margin |
|---|---|---|---|---|---|---|---|
| keyword | candidate | 42/60 | 35.7% | 35.0% | 42.0% | 36.7% | −1.7 |
| keyword | random | 29/60 | 44.8% | 50.0% | 62.5% | 46.7% | +3.3 |
| gliner_7 | candidate | 60/60 | 30.0% | 30.0% | 36.0% | 36.7% | −6.7 |
| gliner_7 | random | 60/60 | 30.0% | 30.0% | 37.5% | 46.7% | −16.7 |
| gliner_8 | candidate | 42/60 | 40.5% | 46.7% | 56.0% | 36.7% | +10.0 |
| gliner_8 | random | 23/60 | 69.6% | 68.3% | 85.4% | 46.7% | +21.7 |
| union | candidate | 54/60 | 38.9% | 40.0% | 48.0% | 36.7% | +3.3 |
| union | random | 38/60 | 50.0% | 58.3% | 72.9% | 46.7% | +11.7 |
| shuffled | candidate | 50/60 | 12.0% | 18.3% | 22.0% | 36.7% | −18.3 |
| shuffled | random | 42/60 | 19.0% | 28.3% | 35.4% | 46.7% | −18.3 |

ALL-stratum figures exist in the scorer output (`task-4-report.md`) and are **reference only**;
pooling the strata is what §3 forbids.

`union` (keyword, else gliner_8) is worse than `gliner_8` alone on both strata — on random it
names 38 domains against gliner_8's 23, and those 15 extra answers cost 10 points of accuracy and
19.6 of precision. On the two prior
spikes the union was the best arm (+9 on prompts, +12 on paths); here it is not.

## Disclosures

Every one of these was recorded during execution, before any score existed. They are listed
together, not scattered as caveats, because their cumulative weight is the reader's to judge.

**(a) Blindness was imperfect, and it was the controller's fault.** The design rests on the arm
author never seeing the labels. It never saw a per-row label or any accuracy — but the Task 2
commit message states the **full aggregate label distribution and the 41.7% baseline**, readable
from git by anyone in the worktree. The arm author confirms its first command was
`git show --stat HEAD`, which printed exactly that, **before it wrote any arm**; the controller's
own Task 3 dispatch additionally handed it the "72 of 120 are none-or-other" figure outright. The
author states it did not use the distribution to choose vocabulary and cannot rule out influence,
which is the only honest answer available. The one argument that cuts in the study's favour: the
leaked aggregate says `legal = 1 of 120`, which argues *against* expanding the legal word list,
and the legal expansion happened anyway — so that edit cannot have been steered by it. The null
label is the one place where knowing "`none` is the plurality" could have helped; it was added on
the controller's instruction, on an argument from `attribution.py`'s `NULL_DOC`, not from the
count. **Report this as a deviation to be judged, not as an objection that has been disposed of.**
The generalisable lesson: a commit message is part of the worktree, and pre-registration ordering
protects against reading the data while doing nothing about metadata the controller volunteers.

**(b) The keyword arm was edited three times after pre-registration.** All three landed before any
score existed, provable from the commit graph, but three is three:

1. **Plural matching + legal vocabulary.** The arm had predicted `legal` zero times. Plural
   tolerance (`contracts`, `clauses` never hit) and an obvious legal word list were added once,
   from first principles, with no accuracy visible.
2. **Phrase matching — a parser bug.** `KWSET` was built with `w.split()`, so phrases written as
   phrases were shredded into unigrams: `power of attorney` → {power, of, attorney},
   `terms of service` → {terms, service}, likewise `cover letter`, `open rate`, `landing page`,
   `cold email`, `offer letter`. **The arm being measured was not the arm that was designed** — it
   was matching on some of the commonest words in English. The fix changed the matcher only, not
   one word of vocabulary. Evidence it was a bug and not a trim: **keyword `legal` predictions fell
   20 → 4** on the fix; 16 of the 20 were unigram noise.
3. **`strip_code` ordering.** `window()` ran before `strip_code`, so a code fence cut at character
   4,000 left an unclosed fence and the tail's code words were scored.

A prior study's hand-written keyword lists scored 52% on text their own author wrote and 17% on
real data. This arm's lists have now been touched three times after construction. It scored +3.3
on random, so nothing here rests on it — but had it scored well, that history would be the first
thing to raise.

**(c) This measures HEAD-OF-CONVERSATION recognition only.** Both the labeller and every arm saw
exactly `text[:4000]`, a raw character cut. **79 of 120 conversations (65.8%) exceed 4,000
characters, and for those the labeller saw a median of 36% of the conversation** (median length
11,108 chars, max 45,364). The truncation is asymmetric by stratum: 51 candidates against 28
randoms. The committed labels are therefore statements about the first 4,000 characters, and the
arms were deliberately given the identical string so an arm could not be scored wrong for finding
real signal the labeller never saw. This overrides the repo's never-cut-mid-sentence rule for this
one case, under the second half of that same rule — labeller and model must see identical text.
**The claim is correspondingly narrower than "whole-conversation domain recognition", which is why
the title of this note says head-of-conversation.** (The 4,000-char window was load-bearing twice:
the first GLiNER run died of CUDA OOM on full-length text.)

**(d) Small n on the rare domains.** The random stratum holds 28 `none`, 14 engineering, 12
`other`, and then **3 medical, 2 marketing, 1 financial**. The candidate stratum has three
single-member classes (`legal`, `sales`, `hr`). Per-domain recall figures exist in
`task-4-report.md` and **no per-domain conclusion about the rare domains is supported by this
sample.** Anything downstream printing a percentage for a class of n=1 is printing one
conversation.

**(e) `gliner_8`'s accuracy is substantially an abstention effect.** It named a domain on only **23
of 60** random conversations (61.7% abstention) and was right on **25 of the 28 `none` rows**. An
arm that is good at recognising the *absence* of professional work is a different and lesser claim
than an arm that is good at naming the domain that is present. **These numbers support the first
claim and not the second** — 16 correct names out of 60 conversations is the second claim's actual
number, and it is below the 18 that the forced-choice arm managed.

**(f) No arm predicts `mode`.** The doing-vs-asking axis is **unmeasured**; only the label
distribution exists, as the floor a future mode classifier must beat: candidate doing 43 / asking
17 (majority 71.7%), random doing 40 / asking 20 (majority 66.7%), ALL 83/37 (69.2%).

## What this does not say — design §8, restated

**Nothing here is evidence that any of this works on Keld's data.** WildChat is ChatGPT chat:
a person typing to a chatbot, median 2 turns, user text dominant. Keld's sources are **agentic
transcripts** — tool calls, long tool output, file contents pasted in, and far more assistant text
than user text. The one thing measured here that most plausibly transfers (the null-label effect)
would transfer as a *vocabulary* lesson, not as a score. Also binding: **English only**, and a
120-conversation sample.

The honest next step after a pass would have been re-measuring on agent transcripts. That still
needs a **non-engineering customer corpus, which does not exist** — the same input that tier B's
0% coverage and the path classifier's one-domain limit are both waiting on.

## The gate — options, not a decision

Pre-registered bar missed by every arm. What the numbers support, and what each path costs:

1. **Accept the negative and stop.** The honest default. A sixth measured negative on
   "can we recover work domain from text" is itself the deliverable, and it is now recorded with
   the method, the bar and the disclosures beside it. Cost: nothing. What it gives up: the §2
   null-label finding goes unexploited.
2. **Re-run with better null-label and label-vocabulary work.** The largest effect measured was a
   vocabulary change, not a method change, and the null label's wording is itself **load-bearing
   and unmeasured** — every other label in this repo's classifiers was wording-tuned by bakeoff,
   this one was written once. Cheap: the frame, the labels, the scorer and the GLiNER cache all
   exist; a re-run is inference plus scoring. Honest risk: the labels are now scored-against, so a
   second pass over the same 120 is no longer pre-registered against them, and a vocabulary sweep
   fitted to this sample is exactly the fitting the ordering discipline exists to prevent — a
   re-run wanting to be believed needs a fresh sample.
3. **Re-measure on agent transcripts.** The only path that answers the question actually asked.
   Blocked on a non-engineering customer corpus that does not exist (§8). Not schedulable today.

⚠️ **And a deployment constraint that bounds options 1 and 2 whichever way they go:** the arms
that did anything here run on **gliner2-large, which needs `ml_backend:"auto"` — and installers
write `"deterministic"`.** Any GLiNER-based answer reaches dev machines and essentially no users.
Design §5 names `gliner2.5-base-v1` (0.77 GB) and `convaiinnovations/laya` (421 MB) as the
untested alternatives for where the fleet actually is; neither has been measured on anything.
