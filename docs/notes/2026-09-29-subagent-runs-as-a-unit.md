# Subagent runs: the structure, and the experiments it makes possible

**Date:** 2026-09-29. **Status:** structure MEASURED on two corpora; experiments PROPOSED,
none run. Nothing here is a result about characterisation quality.

## Terminology, from the data rather than invented

Claude Code writes each subagent dispatch to **its own transcript file**, `agent-<agentId>.jsonl`.
Every record in it carries `isSidechain: true`, its own `agentId` (matching the filename), and
the **parent `sessionId`**. Call the unit a **subagent run**, identified by `agentId`.

⚠️ `sourceToolAssistantUUID` looks like a link to the dispatching call and **is not** — it
resolves inside the run's OWN file, 120/120 times, and never in the parent. The parent linkage
is `sessionId` plus time ordering.

## The structure, measured on both corpora

| | CORPUS A | CORPUS B |
|---|---|---|
| session files | 218 | 59 |
| **subagent runs** | **187** (46% of files) | **290** (83% of files) |
| first record is `type: user` | 187/187 | 282/290 |
| brief length, median | 2,990 chars | 4,670 chars |
| duration, median | 2.6 min | 7.2 min |
| **runs fitting inside one 20-min block** | **97%** | **80%** |
| requests/run, median (p90) | 13 (41) | 29 (118) |
| briefs opening `"You are …"` | 160 (86%) | 221 (76%) |

**Zero sidechain records appear inside session files** — subagent work is entirely separate,
which is why it is invisible to anything that reads a session transcript alone.

## Why this matters: a run is a semantically delimited unit and a block is not

A 20-minute block is an arbitrary time cut. A subagent run has:

- **a stated GOAL** — the first record is always the brief, and 76–86% of briefs open with an
  explicit `"You are …"` role sentence;
- **a bounded EXECUTION** — median 13 / 29 inference requests;
- **an observable DELIVERABLE** — the final assistant message handed back.

⚠️ **This is the exact deficiency that killed block-level classification.** User text is 7.0%
of a block and the rest is narration, which is why the user-only arm collapsed to 0.140 and
why GLiNER2 hit a +0.117 ceiling on block prose. **A brief is ~100% intent**, authored
deliberately, several thousand characters long. For calibration, Keld's shipped `task_type`
facet measures **0.733 on prompts** — and a brief is a prompt, a long and unusually
well-formed one. The unit that failed had no intent statement in it; this one leads with one.

⚠️ **And the block cutter SPLITS runs**: 20% of corpus B's runs exceed 20 minutes, so their
work is divided across two arbitrary time buckets with no marker saying it was one task.

## Proposed experiments, in dependency order

**E1 — Can a run be characterised from its BRIEF alone?**
Blind-label a stratified sample of briefs; score a classifier against them. This is the
well-posed version of the question that failed at block level, and it is cheap: the brief is
short, self-contained, and states the task. ⚠️ Pre-register the vocabulary and commit labels
before any arm runs, as with every prior study here.

**E2 — Does the brief PREDICT the run's actual activity mix?** *(the valuable one)*
We already have the deterministic per-request classifier, so each run's true activity
distribution is computable with no labelling at all. The question is whether the brief
predicts it. If it does, work can be characterised **at dispatch time, before it runs** —
which is a routing capability that does not exist today, and a far cheaper place to route
than per request. The gold needs no human: it is the run's own measured distribution.

**E3 — Does a SESSION have a coherent characterisation, or is it inherently a mixture?**
Worth knowing before any session-level surface is designed. The honest prior from this
project is that it is a mixture — which would make the session a container to be reported as
a distribution, never a labelled thing.

**E4 — Is the run a better published unit than the block for agentic work?**
Runs are 46% / 83% of transcript files and carry 41.5% / 70.6% of all inference requests.
A unit with a goal and a deliverable beats an arbitrary time cut, and E2 would supply the
evidence either way.

## Caveats to carry in

- **Both corpora use subagent-driven workflows heavily.** The `"You are …"` brief shape is
  partly a skill convention; a team that dispatches subagents ad hoc would have terser briefs.
  A third corpus is owed here as much as it is for the request classifier.
- **8 of corpus B's 290 runs do not start with a `user` record.** Small, but it means "the
  first record is the brief" is a strong regularity, not an invariant, and code must not
  assume it.

---

# E2, run 2026-09-29: the brief predicts whether a run will write code

## ⚠️ First, the obvious target was DEAD

"Predict the run's dominant activity from its brief" has a majority constant of **86% (corpus
A) / 89% (corpus B)** — almost every delegated run is retrieval-dominant. There is essentially
nothing to beat, so that question was abandoned before any arm ran.

| dominant class per run | CORPUS A | CORPUS B |
|---|---|---|
| retrieve | 161/187 (86%) | 258/290 (89%) |
| author_code | 18 | 11 |
| operate / verify / author_prose | 8 | 21 |

**That is itself a finding:** delegated work is overwhelmingly reading. Runs still differ —
median distance from the pooled mix is 0.30–0.36 — but not in their argmax.

## The target that is both balanced and routing-actionable

**"Will this run write any code?"** — constant **54% (A) / 60% (B)**, and it decides the one
thing a router needs: whether the run requires a code-capable model.

## Design: fit on corpus A, test on corpus B

Cross-person by construction, so there is no holdout to contaminate and no way to tune on the
test set. **The gold needs no labelling** — it is the deterministic per-request classifier's
own output for that run.

## Result

| arm | corpus | accuracy | constant | **lift** | P | R |
|---|---|---|---|---|---|---|
| lexical rule (in-sample) | A | 0.888 | 0.540 | +0.348 | 0.93 | 0.85 |
| **lexical rule (out-of-sample)** | **B** | **0.845** | **0.600** | **+0.245** | **0.97** | **0.76** |
| one word: `implement` | B | 0.579 | 0.600 | −0.021 | 0.98 | 0.30 |
| **shuffled-label control** | B | **0.276** | 0.600 | **−0.324** | 0.30 | 0.15 |

**The signal transfers across people** (+0.245 on a corpus never seen), **is not a trivial
keyword** (the single strongest word scores below the constant), and **is real rather than
register** (the shuffled control collapses to 0.276, far below chance).

Generalisation gap 0.348 → 0.245, which is real and reported.

⚠️ **The fitted terms looked like a trap and mostly were not.** The strongest negatives on
corpus A are `re-reviewing`, `verdicts`, `out-of-scope`, `critical/important` — the vocabulary
of ONE subagent-driven-development skill. If the rule were learning that skill's phrasing it
would have collapsed on corpus B. It lost 0.10 of lift and kept the rest, so most of the
signal is about the work. Some of it is not, and a third corpus is still owed.

## ⚠️ For routing, the operating point is WRONG as tuned

Precision 0.97 / recall 0.76 means: when it says "this will write code" it is almost always
right, but it **misses a quarter of code-writing runs**. That is the expensive error — a
missed code run routed to a cheap model costs a failed run, while a false positive costs only
over-provisioning on one. The threshold was chosen to maximise accuracy on corpus A; for
routing it must be **re-tuned for recall**, and that is a deliberate choice, not a default.

## What this establishes

Delegated work can be characterised **at dispatch time, before it runs** — from text that is
already written, with no model, at +0.245 over the constant across people. That is a
capability that did not exist, and it is a far cheaper place to route than per request: one
decision covers a median of 13 (A) / 29 (B) inference requests.

**Still open:** E1 (characterise a run's *kind* from its brief, now that "dominant class" is
known to be the wrong target), E3 (sessions), E4 (is the run a better published unit than the
block), and a third corpus for all of it.

---

# E3 + E4, run 2026-09-29: which unit is coherent enough to characterise?

A unit is worth characterising only if its contents are consistent. If it is a grab-bag, any
single label for it is a lie and the honest output is a distribution.

## ⚠️ The naive table was confounded and I nearly reported it

Coherence (share of a unit's requests in its own largest class) is **inversely related to
size**: a one-request unit is coherent 1.0 by arithmetic. Corpus B's sessions scored 0.92 —
on a **median of 4 requests**. The control is a **size-matched shuffle**: reassign every
request to a random unit while preserving each unit's size, then recompute. A real unit must
beat its own shuffled twin.

| unit | CORPUS A gain over shuffle | CORPUS B gain over shuffle |
|---|---|---|
| session | **+0.24** | **+0.41** |
| subagent run | +0.10 | +0.04 |
| **block (20 min)** | **+0.06** | **−0.10** |

## And the gain does not survive size — on any unit

| unit | 1–5 req | 6–20 req | **21+ req** |
|---|---|---|---|
| session (A / B) | +0.08 / +0.29 | **+0.25 / +0.42** | **+0.07 / −0.21** |
| subagent run (A / B) | +0.10 / −0.17 | **+0.20 / +0.20** | **+0.05 / +0.00** |
| block (A / B) | +0.17 / −0.10 | +0.05 / −0.17 | +0.01 / −0.10 |

## Three findings

**1. Above ~20 requests, NO unit is coherent.** Session, block and run all collapse to
+0.07 / +0.05 / +0.01 on corpus A and −0.21 / +0.00 / −0.10 on corpus B. **Any single
activity label for a large unit is a false statement**, whatever the unit is called. This is
independent evidence for the product decision already taken: publish a DISTRIBUTION, never a
label. It was argued from the taxonomy before; it is now measured.

**2. The 20-minute block is the WORST unit at every size, on both corpora** — +0.06 and
−0.10 overall, and never above +0.17 in any band. It is an arbitrary time cut and the data
says so plainly. It remains fine as a *reporting window*; it is not a thing with a kind.

**3. ⚠️ CORRECTION TO THIS FILE'S OWN EARLIER SECTION.** I argued above that a subagent run is
"a semantically delimited unit and a block is not", on the strength of its having a stated
goal. Its goal is stated; its **execution is barely more coherent than a block** — +0.10 / +0.04
overall, +0.05 / +0.00 in the 21+ band. A run knows what it was ASKED to do and still does a
mixture of reading, writing and verifying to get there. E2's positive result stands on its own
terms (the brief predicts whether code gets written, +0.245 across people) and does **not**
license the stronger claim that a run is a homogeneous thing.

## Where a single label WOULD be honest

The 6–20 request band is the only place coherence is real and consistent across both corpora:
sessions **+0.25 / +0.42** and runs **+0.20 / +0.20**. That suggests a size-gated treatment —
label a small unit, distribute a large one — rather than one rule for all.

⚠️ Untested: that band was found by looking, not predicted in advance. Treat it as a
hypothesis for a pre-registered check, not a threshold to ship.

---

# The distributional question, 2026-09-29 — which is the one that mattered

⚠️ **E3/E4 above measured COHERENCE — the share of a unit's requests in its single largest
class. That is a single-label metric, and a single label was never the goal.** The question is
whether a unit's DISTRIBUTION carries information. Re-measured on that basis, the answer
inverts.

## 1. Distributions ARE distinctive, and unlike coherence the signal SURVIVES AT SIZE

Distance of a unit's mix from the corpus average, against a size-matched shuffle of the same
requests (small units are far from any average by arithmetic, so the shuffle is the only
honest baseline):

| unit | 1–5 req | 6–20 req | **21+ req** |
|---|---|---|---|
| session (A / B) | +0.29 / +0.45 | +0.23 / +0.73 | **+0.21 / +0.21** |
| block (A / B) | +0.21 / +0.33 | +0.14 / +0.24 | **+0.14 / +0.15** |
| subagent run (A / B) | +0.07 / −0.02 | +0.05 / +0.08 | **+0.14 / +0.16** |

**Compare with coherence, which collapsed to +0.07 / +0.01 / −0.21 in that same 21+ band.**
The two metrics disagree because they ask different things: a 40/30/20/10 unit is
"incoherent" and perfectly informative. **You cannot label a large unit; you can absolutely
characterise it.**

⚠️ This also corrects "the block is the worst unit" from the section above. It is the worst
thing to LABEL. As a distribution-bearing reporting window it is fine: +0.14 to +0.33.

## 2. Runs come in TWO recurring profiles, and they are the SAME two on both corpora

k-means on the 9-dim mix, `k` chosen where the gap over a shuffled control is widest:

    CORPUS A / subagent run   k=2, gain +0.35
       61%   retrieve 35%  author_code 25%  operate 15%     <- implementing
       39%   retrieve 77%  synthesize  9%  operate  6%     <- investigating

    CORPUS B / subagent run   k=2, gain +0.30
       58%   retrieve 41%  author_code 19%  operate 16%     <- implementing
       42%   retrieve 80%  operate     6%  synthesize 4%    <- investigating

**Two shapes, same shapes, similar proportions, two different people.** And they are the same
split E2 found predictable from the brief at +0.245 across corpora — the profiles ARE
"writes code" versus "does not", arrived at from the other direction.

Sessions also cluster interpretably (corpus A, k=3, gain +0.41): 12% pure reporting
(synthesize 69%), 39% investigation (retrieve 71%), 49% mixed build work.

**Blocks are the most fragmented and the weakest**: k=5 at +0.20 (A) and k=6 at +0.12 (B),
against k=2 at +0.30–0.35 for runs. More profiles, less structure — what an arbitrary time
cut through several activities should look like.

## What this supports building

- **Publish the distribution for every unit** — sessions, blocks and runs all carry real
  distributional signal at every size.
- **Name the two run profiles.** Two stable, cross-person shapes is a product surface
  ("this delegated task was an investigation"), not a table. E2 says the brief predicts which
  one **before the run executes**.
- **Do not label large units.** Coherence says any single label above ~20 requests is false.

⚠️ `k` was chosen by maximising the gap over the shuffled control, which is a selection on the
test statistic. The k=2 run result is robust to that (every k from 2 to 6 gives +0.25 to
+0.35), but the block k=5/k=6 picks are not — treat those profile counts as descriptive.
⚠️ Corpus B sessions could not be clustered: only 13 have ≥6 requests.
