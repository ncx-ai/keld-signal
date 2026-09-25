# Can blocks be mapped to `atv1` activity types? — results, 2026-09-24

**Provenance:** measured 2026-09-24 on one developer machine. Pre-registration
`docs/superpowers/specs/2026-09-24-activity-atv1-preregistration.md` (committed `fde3297`
BEFORE any label); labels `scripts/activity-atv1-hand-labels.txt` (`ea4c6a5`, committed
before any arm ran). Harness `scripts/activity_atv1.py`. Both orderings are provable from
git, which is what makes these numbers falsifiable.

**Question asked:** map a block to the 68 fixed `atv1` activity types (17 verbs x 11
contexts), ranked, on CPU under 3-4 GB.

**Answer: yes for the VERB axis, at accuracy 0.660 against a 0.370 constant (lift +0.290,
coverage 1.00) — from PROSE, with GLiNER2, scoring the axes separately.** Every other
route measured worse, and two were refuted outright.

⚠️ **The context axis is NOT measured here and no claim about it may be read off this
page.** The corpus is 100% software engineering: all 100 gold labels carry context
`general` or `none`. Contexts are Study 4, on external corpora.

## The arms

| arm | wording | accuracy | lift | `code.*` acc | coverage |
|---|---|---|---|---|---|
| **D** deterministic (`action` level -> verb, by precedence) | n/a | 0.452 | **+0.036** | 0.628 | 0.84 |
| **P** prose, factored (17 verbs) | bare | 0.500 | +0.130 | 0.222 | 1.00 |
| **P** prose, factored | gloss | 0.640 | +0.270 | 0.467 | 1.00 |
| **P** prose, factored | **rich** | **0.660** | **+0.290** | **0.600** | 1.00 |
| **H** hierarchical (family -> verb) | gloss | 0.350 | **-0.020** | 0.556 | 1.00 |
| **F** flat 68-way | gloss | 0.480 | +0.110 | 0.667 | 1.00 |
| **A** prose + deterministic facts | rich | 0.640 | +0.270 | 0.511 | 1.00 |

Majority constant = `code.edit` at 0.370. For calibration, this repo's shipped facets
measured `activity_type` 0.670, `domain` 0.683, `task_type` 0.733 — so a 17-verb facet at
0.660 sits inside the range Keld already publishes, over a vocabulary ~3x larger.

## What was refuted

**1. The deterministic path, again — now for `code.*` specifically.** `activity.py` had
already refuted `action` -> the 6-value `Activities` FOUR times (best lift -0.169) on the
grounds that acts record WHAT TOUCHED A FILE while the vocabulary divides on WHAT THE
CHANGE MEANS. The live question was whether `atv1`'s `code.write`/`code.edit`, being nearer
to physical acts, escape that. **They do not: 0.628 against a pre-registered 0.70 bar.**
- The judgement call does not carry it: D1 (`run code`->review) and D2 (`run code`->abstain)
  are IDENTICAL on every headline number.
- The dominant error is the prior study's, reproduced: **`review` -> `research` x16**,
  because a reviewer issues reads and reads swamp the evidence.
- A new error appears one level down: **`code.edit` -> `code.write` x8**. The `create`/`edit`
  acts do not track authoring-vs-changing; a heredoc write and a feature are the same act.

**2. The hierarchical / multi-pass cascade.** H had to beat P by >=5 points to earn 2-3x the
calls. It is **29 points worse and below the constant**. The mechanism is in its confusions
(`review`->`research` x14, `review`->`code.edit` x10): **a wrong family pick is
unrecoverable**, because the verb pass can only choose inside it. A 0.35-accuracy first pass
cannot be rescued by any second pass. ⚠️ This refutes the obvious reading of the CSV's own
`family` column and of the pipeline's existing Wave-1 -> Wave-2 conditioning. Do not
re-propose it without new evidence.

**3. Naive fact augmentation (arm A).** Stating the act counts as a hint — the idiom that
rescued `work_function` when team membership was stated — **made it worse**: 0.640 vs 0.660,
and 0.511 vs 0.600 on `code.*`. Eight windows moved into `research` that prose alone got
right (`code.edit`->`research` x5, `code.write`->`research` x3). **This is Amendment 1's
read-swamping re-entering through the PROMPT rather than the rollup.** It also reproduces
AGENTS.md's existing rule that augmentation is facet-selective (helps `domain`, hurts
`task_type`).
⚠️ **The overall delta (-0.020 on n=100) is noise and must not be quoted as an effect size.**
What is solid is that it did not help and that the errors it added are the predicted ones.
⚠️ **This tests RAW act counts, including `read` and `search` — the two acts already known to
swamp.** Augmentation omitting them, or stating ratios, or stating only discriminative acts
(`test`/`commit`/`build`), is a DIFFERENT experiment and is not refuted by this.

## The wording result, which is larger than every arm gap

| style | accuracy | `code.*` |
|---|---|---|
| bare (id only) | 0.500 | 0.222 |
| gloss (one line) | 0.640 | 0.467 |
| rich (with positive + negative cues) | 0.660 | 0.600 |

**Spread: 0.160 overall, 0.378 on `code.*`.** The `code.*` swing is larger than every arm
gap in the study combined — so **Bar 2's failure was substantially a DESCRIPTION failure,
not a model failure**, and `rich` nearly clears it.

⚠️ **The pre-registered kill switch was written imprecisely and this is recorded rather than
quietly fixed.** Bar 4 said "if the spread exceeds the LARGEST gap between arms". Largest is
P-vs-H at 0.290, so read literally the bar does not fire. But it exists to stop a structure
being chosen on a wording artifact, and the right test is against EACH gap it adjudicates:
- **H's refutation SURVIVES** — 0.290 is ~2x the spread.
- **"factoring beats flat-68" DOES NOT** — that gap is 0.160, EXACTLY the wording spread,
  and F was only ever run at `gloss`. It is not claimed here.

⚠️ **`rich` is not strictly better — it trades errors.** A new confusion appears,
`research` -> `extract` x6, caused by the cue *"pulling structured facts out of content"*
against a corpus half-full of "investigate and report back concrete facts". Fixing one
boundary broke another.

## Stated limits

- **One developer's engineering corpus.** 7 of 17 verbs have any support
  (`code.edit` 37, `review` 28, `research` 18, `code.write` 8, `plan` 6, `converse` 2,
  `other` 1); 10 of 11 contexts have none.
- **`code.*` is the majority of real blocks and the worst-served**: P/rich is best overall
  and 0.600 on code, with most of its errors inside the code family. A 0.660 headline is
  carried by the verbs an engineering fleet exercises LEAST.
- **Gold labels are an agent's, blind, not a domain expert's.**
- **Frame reduced 150 -> 100** (Amendment 1, recorded before labelling): a prefix of the same
  seed-0 ordering, the same n attempt one used. Lowers power, cannot bias.
- **Proportions (bar 6) are NOT measured.** Act-volume denominators are already refuted by
  Amendment 1; a prose-window denominator is proposed but untested.

## Open, with the date opened

- **(2026-09-24) The context axis is entirely unmeasured.** Study 4, external corpora, with
  a shuffled-label control — a number that survives shuffling is measuring register, not
  domain, and is void.
- **(2026-09-24) Discriminative-act augmentation is untested** — arm A refutes only the raw
  form. Named here so the negative result is not over-read.
- **(2026-09-24) F at matched `rich` wording is unrun**, so P-vs-F is open.
- **(2026-09-24) Nothing clears the 0.70 `code.*` bar.** `rich` reaches 0.600. Whether
  further wording work closes it is untested, and there is a live risk of tuning descriptions
  against this 100-window sample until they fit it.
