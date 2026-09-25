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

## MASTER METRICS TABLE — kept current as arms land

Last updated 2026-09-24. 100 blind hand labels; majority constant `code.edit` = 0.370.
`code.*` = accuracy restricted to the 45 windows whose gold verb is `code.write`/`code.edit`.

| arm | what it scores | wording | accuracy | lift | `code.*` | coverage |
|---|---|---|---|---|---|---|
| **D** | `action` level -> verb, precedence | n/a | 0.452 | +0.036 | 0.628 | 0.84 |
| **G** | **file extensions -> code/not-code (binary)** | n/a | **0.729** | **+0.247** | n/a | 0.85 |
| **P** | prose -> 17 verbs (factored) | bare | 0.500 | +0.130 | 0.222 | 1.00 |
| **P** | prose -> 17 verbs (factored) | gloss | 0.640 | +0.270 | 0.467 | 1.00 |
| **P** | prose -> 17 verbs (factored) | **rich** | **0.660** | **+0.290** | 0.600 | 1.00 |
| **H** | prose -> 7 families -> verb | gloss | 0.350 | -0.020 | 0.556 | 1.00 |
| **F** | prose -> all 68 ids at once | gloss | 0.480 | +0.110 | 0.667 | 1.00 |
| **F** | prose -> all 68 ids at once | rich | 0.430 | +0.060 | 0.822 (artifact) | 1.00 |
| **A** | prose + raw act counts as hint | rich | 0.640 | +0.270 | 0.511 | 1.00 |
| **T** | prose + 68 ids + **Atlas team stated** | rich | 0.400 | +0.030 | 0.822 (artifact) | 1.00 |

**Arm T cuts FALSE-DOMAIN attribution 11 -> 4 (-64%)** — see its section below; its
accuracy column is contaminated by F's over-prediction and is not the number to read.

**Winner: P/rich — 0.660, +0.290, full coverage.** For calibration, shipped Keld facets
measured `activity_type` 0.670, `domain` 0.683, `task_type` 0.733.

⚠️ `F/rich`'s 0.822 on `code.*` is an ARTIFACT of calling nearly everything `code.edit`;
never read the `code.*` column without the accuracy column beside it.

### Pre-registered bars — adjudication

| bar | verdict |
|---|---|
| 1 — beat the majority constant | **PASS** for P (+0.290). FAIL for H (-0.020). Marginal for D (+0.036). |
| 2 — `code.*` >= 0.70 | **FAIL for every arm** on its merits (best genuine 0.628). |
| 3 — H beats P by >=5; P beats F | **H REFUTED** (29 pts worse). **P beats F by 0.230** at matched wording > 0.160 spread, so factoring is claimed. |
| 4 — wording spread vs arm gaps | Spread 0.160 overall / 0.378 `code.*`. H's refutation survives; the P-vs-F claim was withheld at `gloss` and is made at `rich`. |
| 5 — context, with shuffled control | **NOT RUN** — corpus has no non-engineering work. Study 4. |
| 6 — proportions denominator | **NOT RUN.** |

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

## Addendum, same day — the two arms the first writeup left open

### Arm G: file types, the deterministic signal arm D should have used

Arm D read the `action` level (`edit`, `read`) which says what TOUCHED a file and never which
KIND. Attempt four's frame excludes the ext/lang levels by construction (*"`reconcile` is
deliberately not run"*), so this signal was absent from every arm above. Re-cut the same 100
windows, read file paths from `tool_use` inputs, mapped through the sidecar's own `EXT_LANG`.
Extensions seen: `.tsx` 420, `.py` 376, `.go` 341, `.md` 153.

| code-file share | precision | recall | accuracy |
|---|---|---|---|
| >= 0.01 | 0.562 | **1.000** | 0.624 |
| >= 0.50 | 0.580 | 0.976 | 0.647 |
| >= 0.75 | 0.673 | 0.854 | **0.729** |

Constant 0.482, so **+0.247 — nearly 7x arm D's +0.036. File types are a far better
deterministic signal than action names, and that correction came from the repo owner.**

⚠️ **But the asymmetry is the whole finding. Recall 1.000, precision 0.562.** Every code
window touches code files, so a NEGATIVE verdict is never wrong; but 32 of 73 windows touching
`.go`/`.py`/`.tsx` are NOT code work, because **reviewing code, researching code and planning
code changes all touch code files too**. The extension names the SUBJECT MATTER, not the
ACTIVITY. It can rule the code family OUT with certainty and cannot rule it IN.

⚠️ **And the negative gate is INERT on this corpus — measured, fires 0 times.** Every window
the model called `code.*` did touch code files; all 6 cross-family errors are code-file-rich
windows that are really `review`/`research`. The gate needs non-code work to exist before it
can fire, and this corpus has none. **It is expected to matter on a mixed fleet and provably
does nothing on an all-engineering one.** Do not implement it on the strength of this corpus;
do not discard it either.

### Arm F at matched wording — Bar 3 resolves, and the first context number

`F/rich`: accuracy 0.430, lift +0.060, `code.*` **0.822**.

⚠️ **That 0.822 is the only arm to clear Bar 2 and it is an ARTIFACT.** It is reached by
calling nearly everything `code.edit` (`review`->`code.edit` x16, `research`->`code.edit` x13,
`plan`->`code.edit` x4), which inflates code recall while tanking overall accuracy to below
arm D. **A `code.*` figure earned by over-predicting code means nothing**; Bar 2 must never be
read without the overall-accuracy column beside it.

**Bar 3 RESOLVES.** At matched `rich` wording P beats F by **0.230**, which exceeds the 0.160
wording spread. The claim withheld in the first writeup — *factoring the axes beats scoring all
68 at once* — is now made. At `gloss` the gap was 0.160, exactly the spread, and it was right
to withhold it.

**FIRST MEASURED CONTEXT NUMBER, and it is a warning.** `F/rich` assigned
**`review.legal` to 11 of 100 windows** — up from 5 at `gloss` — on a corpus containing **zero
legal work**. A ~11% false-domain rate that RISES as descriptions get richer. This is the
strongest evidence yet that the context axis over-fires when offered, and it argues for gating
context on verb rather than scoring all 68 ids together. ⚠️ It is a false-POSITIVE rate only;
it says nothing about whether real contexts would be found, which remains Study 4.


### Arm T: Atlas team membership stated as a prior (2026-09-24)

Proposed by the repo owner. Precedent is in this study's own window #25, where stating team
membership as a fact fixed `work_function` on all three windows after it had *"failed in every
previous formulation"*. Prompt prefix: `"The person doing this work is on the Engineering team. "`
Base arm is F (flat 68), because it is the ONLY arm that scores contexts at all.

| | accuracy | lift | false-domain attributions |
|---|---|---|---|
| F/rich | 0.430 | +0.060 | **11** (`review.general` -> `review.legal`) |
| T/rich | 0.400 | +0.030 | **4** |

**The prior does the job it was aimed at: a 64% cut in hallucinated domains from one sentence
of Atlas metadata.** That is the context axis's precision problem, and it is the failure most
likely to embarrass a published facet — attributing someone's engineering work to `legal`.

⚠️ **But it TRADES an error rather than removing one.** `review`->`code.edit` went 16 -> 22 and
`research`->`code.edit` 13 -> 16: stating "Engineering team" makes the model answer *engineering
thing* more often, which suppresses false `legal` and amplifies over-prediction of the team's
own default. Net accuracy -0.030 — noise at n=100, but directionally negative.

⚠️ **This measures the FALSE-POSITIVE half only, and that is the half this corpus can see.**
Whether stating "Marketing team" helps FIND marketing work is unmeasurable here and is exactly
where the benefit would be. Same shape as arm G's negative gate: the mechanism is sound, the
corpus cannot adjudicate it.

⚠️ **The base arm is the study's WORST (F, 0.430).** The false-domain finding is about the
context axis and stands on its own, but the accuracy number is contaminated by F's
over-prediction of `code.edit` and must not be compared against P/rich's 0.660. **The
production shape to test is P (factored) plus a team prior on the CONTEXT pass only** — which
P does not currently have, and which is the natural next experiment.

**Open (2026-09-24):** team-as-prior on a factored context pass, measured on a corpus
containing more than one team. Until then this is promising and unadjudicated.

## GLiNER2.5 (released 2026-09-24) — inventory and comparison

Six variants shipped the day this study ran. **There is no `large` in the 2.5 line**; the
large-backbone slot is filled by a classification-specialised fine-tune.

| model | weights | backbone | tagged for |
|---|---|---|---|
| `gliner2.5-small-v1` | **0.30 GB** | deberta-v3-xsmall | NER + classification |
| `gliner2.5-base-v1` | **0.77 GB** | deberta-v3-base | NER + classification |
| `gliner2.5-multi-v1` | 1.15 GB | mdeberta-v3-base | multilingual |
| `GLiNER2.5-Decide` | 1.95 GB | deberta-v3-large | **classification / intent / sentiment** |
| `GLiNER2.5-Decide-1B` | — | — | classification |
| `gliner2-large-v1` (**current**) | 1.95 GB | deberta-v3-large | NER + classification |

**`GLiNER2.5-Decide` is a fine-tune of the exact model Keld ships** (`base_model:
fastino/gliner2-large-v1`) specialised for label-set classification — which is what every
facet in this repo does. Its own card claims **60.2%** on `fast-decisions` against **49.0%**
for the general-purpose large.

⚠️ **It needs `gliner2` 2.0.0.** Version 1.3.2 (the sidecar's) cannot load it: the 2.5 configs
carry `"attn_implementation": "sdpa"`, which DebertaV2 does not support, and 1.3.2 raises
rather than falling back. 2.0.0 warns and falls back to `eager`. Installed to a THROWAWAY
target dir on `PYTHONPATH` for this benchmark, per the `piibench` convention — **the sidecar's
own dependency was not touched.** Adopting 2.5 in production is a dependency upgrade with its
own blast radius (the sidecar's `/classify`, `/extract`, `/entities` all ride this library).

⚠️ **Library version is confounded with model.** All three arms below are therefore re-run
under 2.0.0, INCLUDING the current model, so the comparison isolates the weights.

### RESULT: 2.5 does NOT beat what Keld already ships

Arm P, rich wording, all under lib 2.0.0.

| model | size | accuracy | lift | `code.*` |
|---|---|---|---|---|
| **`gliner2-large-v1` (current)** | 1.95 GB | **0.660** | **+0.290** | 0.600 |
| `GLiNER2.5-Decide` | 1.95 GB | 0.520 | +0.150 | 0.489 |
| `gliner2.5-base-v1` | 0.77 GB | **0.340** | **-0.030** | 0.089 |
| `gliner2.5-small-v1` | 0.30 GB | not run — 1/3 on smoke | | |

**CONTROL HOLDS: the current model scores 0.660 under BOTH libraries** (1.3.2 and 2.0.0), so
the library upgrade is not a confound and these differences are the weights.

**`GLiNER2.5-Decide` is 14 points WORSE than the model it was fine-tuned from**, despite being
the classification-specialised variant and despite claiming +11 over the general large on its
own `fast-decisions` benchmark. That suite is intent/routing/sentiment over 17 domains; it does
not transfer to "what kind of work is this window". Its signature is abstention —
`research`->`other` x13 — plus oddities like `code.edit`->`audio.create` x2.

⚠️ **`gliner2.5-base-v1` went 3/3 on a three-sentence CONTEXT smoke test at 0.99 confidence,
then scored BELOW THE MAJORITY CONSTANT on the real 17-way verb task (0.340, lift -0.030,
`code.*` 0.089).** This is the same trap recorded in
`2026-09-24-term-extraction-bakeoff.md` — a smoke test proves the MECHANISM, never a RATE —
re-encountered the same day. Stopping at the smoke test would have recommended a model that is
worse than always answering `code.edit`.

⚠️ **`gliner2.5-base-v1` and `-small-v1` are a DIFFERENT ARCHITECTURE** (`boundary` /
`BoundaryExtractor`, not `span` / `SpanExtractor`) and load only through `AutoExtractor`.
`GLiNER2.from_pretrained` raises a misleading `max_width` AttributeError on them. Patching
`max_width` into the config to force a load builds the WRONG model class — do not do it.
`GLiNER2.5-Decide` IS a span model, so its number above was produced by the correct path.

**Live thread, untested:** `base-v1` aced the CONTEXT smoke test while failing the VERB task,
and those are different problems (11 well-separated categories vs 17 overlapping ones). Since
the winning design scores the axes SEPARATELY, a 0.77 GB model on the context pass with the
large on verbs is reachable. The paragraph above is the reason not to believe it without a
full run.
