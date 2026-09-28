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

⚠️ **TWO CORPORA, NOT ONE. The tables below are NOT comparable and must never be pooled.**

| | frame A — engineering | frame B — colleague transcripts |
|---|---|---|
| unit | 60-minute WINDOWS | 20-minute BLOCKS (the shipped cutter) |
| labels | single-label | multi-label, prominence-ordered |
| n | 100 | 60 |
| constant | `code.edit` 0.370 | verb 0.233 / family 0.317 |
| `text.*` gold | **zero** | 13 blocks |
| verbs with support | 7 of 17 | 9 of 17 |

Frame A is everything down to the 2026-09-27 sections. **Frame B is the 2026-09-28 section
at the end, and it is where the verb/family answer lives.** Re-labelling A at block scale is
owed work, not done.

### Frame A — engineering windows

Last updated 2026-09-24. 100 blind hand labels; majority constant `code.edit` = 0.370.
`code.*` = accuracy restricted to the 45 windows whose gold verb is `code.write`/`code.edit`.

| arm | what it scores | wording | accuracy | lift | `code.*` | coverage |
|---|---|---|---|---|---|---|
| **D** | `action` level -> verb, precedence | n/a | 0.452 | +0.036 | 0.628 | 0.84 |
| **G** | **file extensions -> code/not-code (binary)** | n/a | **0.729** | **+0.247** | n/a | 0.85 |
| **P** | prose -> 17 verbs (factored) | bare | 0.500 | +0.130 | 0.222 | 1.00 |
| **P** | prose -> 17 verbs (factored) | gloss | 0.640 | +0.270 | 0.467 | 1.00 |
| **P** | prose -> 17 verbs (factored) | rich | 0.660 | +0.290 | 0.600 | 1.00 |
| **P** | prose -> 17 verbs (factored) | **docs** | **0.700** | **+0.330** | **0.644** | 1.00 |
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

### Frame B — 60 real blocks, verb and family (2026-09-28)

| axis | accuracy | constant | lift | the number that decides it |
|---|---|---|---|---|
| VERB top-1 vs gold primary | 0.350 | 0.233 | +0.117 | `text.transform` R **0.091** on 11 blocks |
| FAMILY, direct 7-way | 0.383 | 0.317 | +0.067 | `language` R **0.000** on 13 blocks |
| FAMILY, derived via verb | **0.417** | 0.317 | +0.100 | same |

**Clears bar 1 and is still not shippable** — the lift is carried by `code.edit`/`plan`/
`review`, and the `language` family is never predicted once. Full per-class tables, the
read-swamping diagnosis and the F-derived > F-direct inversion are in the 2026-09-28 section
at the end of this file.

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

## ⚠️ 2026-09-25 — the 0.660 DOES NOT TRANSFER off engineering work

John's product/marketing session (7 windows, multi-label gold,
`scripts/activity-atv1-john-labels.txt`) is the first non-engineering ground truth. The
measured arm was re-run on it.

| framing | result |
|---|---|
| top1 == gold primary | **0/7 = 0.000** |
| top1 in gold set (multi-label) | 1/7 = 0.143 |
| mean gold-set recall in the ranked list | **0.190** |

**Across 45 independent sub-window calls on document-authoring work, the model proposed a
`text.*` verb ZERO times.** Not `text.transform` (5 of 7 gold primaries), not `text.create`,
not `transcribe`. It answered `extract` or `other` nearly everywhere.

**Why this was not visible before.** The 100-window engineering frame is 37% `code.edit`,
28% `review`, and contains **zero** `text.*` gold. The 0.660 was therefore measured on a
population that excludes the verbs this session is made of. ⚠️ **A headline accuracy is only
a claim about the label distribution it was measured on**, and this is what that costs.

**Windowing is NOT the cause.** The single-call arm truncates at 2000 chars because GLiNER2
has 512 positions, and J05/J06 lost ~50% of their text that way. Cutting into 400-char
sub-windows and ranking by share — the design the single call structurally cannot express —
produced the SAME 0/7. The truncation was a real confound and a red herring for this failure.

**Prime suspect: wording, and it is testable.** The `rich` description for `text.transform`
reads "rewriting, translating or reformatting PROSE that already exists". John's work is
editing a SLIDE DECK — reordering slides, fixing arrow labels, splicing slide XML. The wording
study already measured a **0.378** swing on `code.*` from description changes alone, larger
than any arm gap in this study, so "describe `text.*` in terms of documents, decks and pages
rather than prose" is a cheap hypothesis rather than an excuse. **Untested as of this note.**

⚠️ **n = 7 windows, one session, one person.** No rate here is stable. The 45-call
zero-proposal observation is the durable part; the 0/7 is not a rate.

**Consequence for the spec** (`2026-09-25-block-activity-type-design.md`): its headline number
is scoped to engineering work and must not be read as a general claim. The spec is NOT ready
to implement until either the wording hypothesis is tested or the claim is narrowed in writing.

**Vocabulary finding from the same labelling:** `text.create` has a `sales` context;
`text.transform` does NOT (financial/general/legal/marketing/media/medical/scientific only).
John's session creates a customer deck once and revises it five times, so identical work is
labelable `sales` at creation and only `general` afterwards. That asymmetry is in `atv1`, not
in the labelling.


## 2026-09-25 — RECOVERED: two fixes took the transfer failure to the best result measured

The 0/7 on John's session was substantially SELF-INFLICTED. Two defects, both found by the
repo owner, and fixing them improved BOTH corpora.

**Defect 1 — malformed sub-windows (violated this repo's own convention).** AGENTS.md:
*"Never cut text mid-sentence. Any text read as language — a prompt, a generated report, a
conversation window handed to a model — is bounded at a logical delimiter: a sentence end, a
line break, a turn boundary, an entry boundary."* What was actually fed:
- the HUMAN-READABLE views were used as model input, so their `[... N chars omitted]` markers
  leaked into **16 of 45 sub-windows (36%)**. One began literally `"172 chars omitted] apply
  all three point 2:"`.
- the splitter `(?<=[.!?])\s+` treated list enumerators `1.` `2.` `5.` as sentence ends,
  shredding a numbered technical procedure into fragments.
- role markers were stripped, running user prompts into assistant prose with no boundary.

Fixed by cutting on TURN boundaries from the transcript directly, splitting a long turn only at
real sentence ends (`(?<![0-9A-Z])[.!?]+\s+(?=[A-Z"'(\[])`, which ignores enumerators and
initials), and never emitting a truncation marker. Result: 37 sub-windows, **0 markers**.

**Defect 2 — the word "prose".** `rich` described `text.transform` as *"reformatting PROSE that
already exists"*. John's work is editing a SLIDE DECK. Across 37 CLEAN calls the model proposed
a `text.*` verb **zero times**, answering `extract` instead. The `docs` style widens text.* to
"document, deck, page, report, spec" and narrows `extract` to "pulling structured fields into a
list or table. NOT discussing or explaining what a document says".

### The numbers

| arm | John's 7 (primary) | John's gold-set recall | Engineering 100 | `code.*` |
|---|---|---|---|---|
| malformed + `rich` | 0/7 | 0.190 | 0.660 | 0.600 |
| logical boundaries + `rich` | 0/7 | 0.310 | 0.660 | 0.600 |
| **logical boundaries + `docs`** | **3/7 (0.429)** | **0.476** | **0.700** | **0.644** |

**`docs` is a strict improvement on BOTH corpora — it is not a trade.** 0.700 exceeds the
shipped `activity_type` (0.670) and `domain` (0.683) facets. ⚠️ The change was made on
PRINCIPLE (a deck, a page and a spec are text artifacts; "prose" wrongly excluded them) and
then regression-checked on the engineering set, rather than tuned against John's 7 — which
would have been fitting noise at n=7.

⚠️ **This does not retract the transfer warning, it relocates it.** A headline accuracy is
still only a claim about the label distribution it was measured on. What changed is that the
gap was a description defect rather than a model limit — and that was only findable because a
second, differently-shaped corpus existed.

**Live, untested (2026-09-25):** branch the label set on Atlas team membership
(engineering vs other) — John's J06 predicted `code.edit` 50% on deck work and a team branch
would suppress it; and open-ended extraction mapped into the atv1 space by embedding, which
would remove the label-wording dependency this section is entirely about (measured worth:
0.378 on `code.*`, and the difference between 0/7 and 3/7 here).

## 2026-09-25 — team on the VERB axis: a measured category error

**Prediction recorded BEFORE the numbers were read:** team information will not help the verb
axis, because a verb names WHAT WAS PRODUCED and team names WHAT ENDS IT SERVED. An image is
`image.create` whoever made it. Expected: T1 (prior) ≈ baseline; T2 (hard branch) worse.

| engineering 100 | primary |
|---|---|
| baseline (`docs`, no team info) | **0.700** |
| T1 — team stated in the prompt | 0.710 (+0.010, noise) |
| **T2 — label set BRANCHED by team** | **0.670 (-0.030)** |
| T1 + T2 | 0.700 |

**Confirmed.** Inert as a prior, harmful as a branch.

⚠️ **T2 is unsound in principle as well as in measurement, and this corpus proves it.** The
non-engineering branch drops `code.write`/`code.edit` — but John, who is not an engineer,
wrote **23 `.js` files and a `.py`** to generate his deck. A hard exclusion keyed on team makes
the correct label UNREACHABLE. Do not implement a team branch on the verb axis.

### The principle this establishes

**Evidence must inform the axis it is actually about.**

| evidence | its axis | measured |
|---|---|---|
| file extensions | **modality** | 41/41 on code; recall 1.000 |
| act counts | operation — too coarse to separate intent | refuted 5x |
| team membership | **context / domain** | false domains 11 -> 4 (-64%) |
| prose | the verb, and the 9 non-modality operations | 0.700 |

Act counts and team both fail on the verb axis for the SAME reason: they are facts about a
different question. Team already worked where it belongs — on context.

## 2026-09-25 — the three-source design

`scripts/activity_atv1_threesource.py` + `_run.py`. Modality from WRITE-side file evidence
PROPOSES candidates at one sub-window's weight; prose votes the verb; team primes the context;
everything snaps to a legal `atv1` id and ranks by share.

**John's 7 (team=Product):** primary 3/7 = 0.429, in-set 0.429, **recall 0.476 -> 0.548**,
with **11 candidates added by modality**.

**Deterministic proposals raise RECALL without displacing the top-1** — which is what
"propose, never gate" is supposed to do, and it is the first measured support for the
decomposition. `SendUserFile.files` is included, which `paths.PATH_INPUTS` does not cover; on
John's session that is the only way the delivered `.pptx` is visible at all.

⚠️ **A LEADING-PROMPT DEFECT was found and fixed mid-run.** The first team prior read "they
work in product and **marketing**" — `marketing` is a taxonomy value, so the context pass
returned `marketing` almost everywhere. That is encoding the answer, not supplying a prior.
Restated as "someone on the **Product** team", contexts snapped back to `general` and matched
gold on J01/J03/J04/J05. **State the team's NAME, never a context value.**

⚠️ **Context scoring on John's set is UNRESOLVABLE and no context number should be quoted from
it.** Gold is `general` on the revision windows only because `text.transform` has no `sales`
context. The work genuinely is customer-facing, so a `marketing` or `financial` prediction
there is not cleanly wrong. The vocabulary gap, not the model, makes the comparison
meaningless.

## 2026-09-25 — free-form extraction + embedding into the atv1 space

Motivation: label WORDING is worth 0.378 on `code.*` and the whole 0/7 -> 3/7 on John's set, so
a closed label set makes the answer a function of OUR prose. This asks GLiNER2 to extract what
the work is in the TEXT'S own words (no atv1 vocabulary in the prompt), then maps the spans
into the atv1 space with Qwen3-Embedding-0.6B against the 68 descriptions plus a NULL doc —
`/attribute`'s measured discipline. `scripts/activity_atv1_freeform.py`.

| John's 7 | closed labels (three-source) | free-form + embedding |
|---|---|---|
| primary | **0.429** | 0.000 |
| in-set | **0.429** | 0.143 |
| **recall** | 0.548 | **0.714** |

**Best recall measured anywhere; worst top-1.** The right labels are IN the ranked list and the
ranking cannot find them.

⚠️ **THE NULL DOC IS INERT: 515 of 515 spans beat it.** `/attribute`'s rule — a candidate
attributes only by BEATING "nothing" in the same ranking, measured at 92% on 61 blocks — **does
not transfer to short spans.** A two-word noun (`"slides"`) is closer to SOME atv1 description
than to "no particular activity" every time, so nothing is ever rejected and every extracted
noun casts a vote. **A guard that never fires is not a guard**; do not carry the NULL_DOC
pattern to short-span matching without re-measuring whether it rejects anything.

⚠️ **A design choice loaded this.** Both `activity` AND `artifact` were extracted, so most
spans are nouns (`whiteboard`, `pptx skill`, `system map`) that cannot rank a verb well. A
verb-phrase-only extraction is UNTESTED and is the obvious next variant.

### What the composition evidence now says

**Propose broadly, rank narrowly.** Three sources have now been measured on the same gold:

| source | effect on recall | effect on top-1 |
|---|---|---|
| modality from file evidence | 0.476 -> 0.548 | none |
| free-form + embedding | -> **0.714** | destroys it (0.000) |
| closed-label prose pass | — | **the only thing that ranks** (0.429 John / 0.700 eng) |

So free-form belongs as a CANDIDATE GENERATOR feeding the closed-label ranker, not as a
replacement for it — the same "propose, never gate" shape that made modality work. ⚠️ Untested
as a combination; n=7 throughout.

## 2026-09-25 — the GENERAL TYPE OF WORK classifies cleanly (and better than the verb)

Question asked on its own rather than as half of an activity type: can we say whether work is
marketing, sales, engineering, legal...? `scripts/activity_domain_probe.py`.

**Answerable without per-window labelling**, because two corpora with DIFFERENT known domains
exist and the comparison between them is its own control: if both produce the same
distribution it does not work. `atv1`'s context list has no `engineering` value, so this uses a
9-value business-function vocabulary matching the question as asked.

| ENGINEERING — 40 windows | | JOHN — 7 windows | |
|---|---|---|---|
| engineering | **48%** | sales | **71%** |
| product | **45%** | product | 14% |
| finance | 5% | general | 14% |
| general | 2% | engineering | **0%** |
| sales / marketing | **0%** | | |

**The corpora separate COMPLETELY on the axis that matters.** The engineering corpus never
produces `sales` or `marketing`; John's never produces `engineering`.

**This is a stronger signal than the fine-grained verb**, and the reason is structural: nine
business functions barely overlap, where 17 verbs contain genuinely confusable pairs
(`review`/`research`). Same model, same prose, better-separated question. It is consistent with
`gliner2.5-base-v1` acing a CONTEXT smoke test while scoring below the constant on the 17-way
verb task.

Three readings worth keeping:
- **The 48/45 engineering-vs-product split is probably not an error.** Many of those windows
  genuinely are product work — specs, plans, design decisions, reviews. ⚠️ Unprovable without
  per-window labels; stated as a reading, not a finding.
- **`sales` 71% for John agrees with the one context label assigned by hand** (`text.create.sales`
  on the deck-creation window) — a small independent agreement.
- **`finance` appears in both** (5% / 16% of sub-windows) and is defensible in both: John's deck
  is inference-spend economics, and the engineering corpus contains real seat-cost, billing and
  capex work.

⚠️ **What this does NOT establish.** `legal`, `medical`, `support` and `operations` appear in
NEITHER corpus as ground truth, so this says only that the model does not hallucinate them
here — never that it finds them when they are real. Ground truth is CORPUS-level and therefore
coarse. John's side is 7 windows.

## 2026-09-27 — SYNTHETIC transcripts cannot validate the domain classifier

No real legal/medical/finance/support/operations transcript exists anywhere on this machine
(`john-projects` in the frozen corpus is the SAME deck session already used). So six sessions
per domain were written synthetically — **by TWO independent generators from ONE brief**,
because the confound was stated in advance: if one model writes the data and another classifies
it, the test may measure whether two models share a prior about what "legal work" sounds like.
Running both makes that confound MEASURABLE instead of invisible.

`scripts/activity_domain_synth.py`. Transcripts under `/tmp/claude-1000/synth_{fable,sonnet}/`
(not committed — synthetic, and regenerable from the brief in this note's commit message).

| | Fable | Sonnet |
|---|---|---|
| correct | 2/6 | 3/6 |
| **generator agreement** | **2/6** | |

```
eng_billing   fable=finance      sonnet=engineering   DIVERGE
legal         fable=finance      sonnet=legal         DIVERGE
operations    fable=sales        sonnet=marketing     DIVERGE
support       fable=support      sonnet=engineering   DIVERGE
finance       fable=finance      sonnet=finance       agree
medical       fable=general      sonnet=general       agree
```

**THE VERDICT IS ON THE METHOD, NOT THE CLASSIFIER.** The generators disagree on two thirds of
sessions, so the label depends more on WHO WROTE THE TRANSCRIPT than on the domain. ⚠️ **Had
only one generator been run, either "2/6 — does not work" or "3/6 — promising" would have been
reported, and both would have been artifacts.** Do not use single-generator synthetic data to
validate this facet.

⚠️ **THE HARD NEGATIVE FAILED ON ONE GENERATOR: `eng_billing` -> `finance` at 75% (Fable).**
Every turn in that session is `pytest`, `alembic`, `Edit`, `gh pr checks` — unambiguously
engineering — and it read as finance because it discusses accruals and GL codes. Sonnet's
version of the same brief scored `engineering` 100%. **The vocabulary-vs-activity confusion is
now DEMONSTRATED rather than hypothetical, and whether it fires is luck.**

**One finding survives the disagreement, and only one:** `medical` -> `general` on BOTH
generators. Clinical-ops work reads as generic document work to this classifier, or the
`medical` label description is bad. It is the single cross-generator signal here and the only
result worth following up.

### What this does NOT overturn

The real-corpus result stands on its own evidence: 40 engineering windows -> engineering+product
93% with 0% sales/marketing; 7 John windows -> sales 71% with 0% engineering. That is REAL data
with REAL ground truth and two corpora that separate completely.

⚠️ **What is now blocked is EXTENDING it.** Legal, medical, support and operations cannot be
covered by synthesis. And the `eng_billing` failure gives a concrete reason to re-examine the
**5% `finance`** seen in the engineering windows — that corpus contains real billing, seat-cost
and capex work, which is exactly the shape that just fooled one generator's hard negative.

## 2026-09-27 — Is there a public dataset of professional domain work? NO. Searched, recorded.

After synthesis failed the generator-agreement check, the remaining option was real public data.
**WildChat** (`allenai/WildChat-1M`, ungated, 838k conversations; 4.8M version also ungated) is
the best available: real people, real work, no awareness it would ever be classified, so the
generator confound that sank synthesis is absent by construction.

**One 230 MB shard = 59,857 real English conversations** was downloaded and filtered locally
(`scripts/wildchat_filter.py`; the HF search API 500s constantly on this dataset, so shard +
local filter is the reliable route). Filter: >=2 distinct professional terms for a domain AND
>=4 turns AND >=800 chars — deliberately loose.

**It returned 28 candidates. 0.05% of the corpus.**
`legal 2 · medical 5 · finance 2 · support 5 · marketing 9 · sales 6`

All 28 were then read BLIND and labelled. What they actually are:

| what they really are | n | examples |
|---|---|---|
| students / coursework | 8 | lease-accounting textbook problems, an accounting exam, a thesis lit review, a security-cert quiz |
| job seekers | 3 | writing a sales CV, interview answers |
| consumer questions | 3 | "what is a non disclosure agreement?"; anxiety about signing an NDA with no expiry |
| content generation | ~5 | print-ad ideas, SMART objectives, cold outreach emails |
| **engineering in domain clothing** | 2 | R scraping clinical-trial registries; an Ansible/Liquibase failure |
| **genuine professional work** | **~1** | isolating a VPlex performance issue |

⚠️ **PUBLIC CHAT DATA CONTAINS DOMAIN VOCABULARY IN ABUNDANCE AND DOMAIN WORK ALMOST NOT AT
ALL.** Two "finance" hits were homework problems about lease accounting — maximum finance
vocabulary, zero finance work. This is exactly the vocabulary-vs-activity confusion that fooled
the synthetic hard negative, occurring naturally in the wild.

**The reason is structural and will not be fixed by looking harder.** Professional legal,
medical and finance work with AI happens INSIDE organisations, on enterprise tools, under
confidentiality. The closest thing found is Microsoft's study of ~105,000 enterprise M365
Copilot conversations WITH INDUSTRY LABELS (arXiv 2605.23958) — a research paper, not a
download. The right data exists and is behind an enterprise wall.

### The one thing worth keeping

**W004** (R code scraping clinical-trial registries) and **W025** (an Ansible/Liquibase
deployment failure) are NATURALLY-OCCURRING HARD NEGATIVES: real engineering work dense in
medical and infrastructure vocabulary, written by people with no idea it would be classified.
**These are worth more than any synthetic hard negative**, because they test the
vocabulary-vs-activity confusion on real text. Two is not a corpus, but it is two more than
existed before.

### Consequence for the domain facet

The real-corpus result stands (engineering 93% / John 71%, clean separation). **It cannot be
extended to legal, medical, finance or support by ANY route currently available** — not
synthesis (generators disagree 2/3 of the time), not public chat data (the work is not in it).
Validating those domains requires transcripts from people doing that work, i.e. real Keld
users in those functions. ⚠️ **Until then, a published domain facet is measured on engineering
and product only, and must say so.**

## 2026-09-28 — the REFRAME to verb+family, and a gold set thrown away for truncation

Two things happened here and only one of them is a result. Recording both, because the
discarded one cost more.

### The reframe

Repo owner, 2026-09-28: *"we should reframe the investigation we are doing so that we
primarily focus not on the domain or 'context' as it is called in the activity types
spreadsheet but just on verb and family detection. We will approach 'context'/domain
separately later."*

That follows directly from the two refutations above it — synthesis failed its
generator-agreement control (two generators agreed on 2 of 6 dimensions) and public chat
data does not contain the work (28 candidates in 59,857 real WildChat conversations, of
which ~1 was genuine professional work). **Domain is not measurable on any corpus in
hand**, so it is deferred rather than guessed at.

### The corpus, and why it is not the engineering one

A colleague's real Claude Code transcripts (`~/keld/john-projects`). The engineering frame
this doc has been scoring against has **zero `text.*` gold and support for 7 of 17 verbs**,
so it structurally cannot measure the language or understanding families. This corpus
carries real Notion document work, a publishing pipeline, and lint/review passes alongside
real engineering.

- **146 of 218 files EXCLUDED** as observer sessions — agent meta-transcripts whose turns
  are `[MESSAGE FROM NON-USER SOURCE]` / `<observed_from_primary_session>`. Rule: drop a
  file if >20% of its turns carry the marker. Not human work.
- **Sampling is STRATIFIED, not random** — 30 `keld`, 20 `keld-website`, 10 rest.
  `keld-website` is oversampled deliberately; a proportional draw is ~85% code/agentic and
  could not measure the family axis at all. ⚠️ **The label distribution is therefore NOT
  the population's and no base rate may be read off it.**
- **Blocks are cut by the SHIPPED rule** — 20-minute budget, 15-minute idle (`IDLE_BINS=3`
  over 300s bins) — after the repo owner caught that cost had been measured on blocks while
  accuracy was measured on 60-minute windows. ⚠️ **The engineering gold set is 60-minute
  windows and single-label; the two are NOT poolable.** Re-labelling that set at block scale
  is owed work, not done.

### ⚠️ The first 60 labels were thrown away, and the cause was a rune-count cut

The blind-view generator capped each turn at 700/400 runes with `[... N chars omitted]`
markers. So **the labeller read 131k chars while the model would have scored 333k** — a
label made from 39% of the input is not an answer key for that input. 60 labels were
assigned, then discarded unscored.

**This is the THIRD instance of the same defect class in this project**, each in a new
place:

| # | Where | Consequence |
|---|---|---|
| 1 | Truncation markers fed to GLiNER2 | 36% of sub-windows carried `[... N chars omitted]`; recall 0.190 |
| 2 | Splitter treated `1.` / `2.` as sentence ends | numbered procedures shredded mid-item; fixing both took recall 0.190 → 0.310 |
| 3 | Blind-view turns capped at 700/400 runes | 60 labels discarded before scoring |

Each time the *consequence* was noticed and reasoned about instead of the *cause* being
removed. AGENTS.md already carried the rule ("Never cut text mid-sentence... a conversation
window handed to a model"). `bound()` is now deleted rather than tuned, and the fix is
verified by equality rather than inspection: `view text 333k  turn text 333k  identical:
True`, `markers in views: 0`.

The superseded labels are **kept in `scripts/verb-family-hand-labels.txt` under a warning
header** rather than deleted, and the scorer now cuts at an explicit `# LIVE LABELS` marker
rather than relying on later-wins dict assignment — which would have silently scored the
wrong set the moment anyone reordered the file.

⚠️ **V-ids do not correspond between the two sets.** The frame was regenerated after the fix
and more windows clear the minimum-length filter untruncated, so the sample itself differs.

### What is being scored

60 blocks, multi-label, prominence-ordered, committed to git **before** any arm ran — the
ordering is provable from `git log`, which is what makes the numbers falsifiable.

Two derivations of FAMILY, because they can disagree:

- **F-direct** — one 7-way call against family descriptions.
- **F-derived** — ask for the verb (17-way), map to family via the CSV.

FAMILY is a different question from the refuted hierarchical cascade (0.350, below constant).
That arm failed because a wrong family pick is **unrecoverable** when the verb pass can only
choose inside it. Nothing about that says a 7-way family label is a bad *output* — and the
domain probe above showed coarser, better-separated vocabularies classify markedly better.

Results land in the next section when the run finishes.

### Method note: GPU

Quality numbers now run on GPU (`map_location="cuda"`), after a CPU run was killed at 21
minutes having produced nothing readable. CPU timing is only measured when CPU cost *is* the
measurement — the on-device budget question — and that is a separate arm with its own
conditions.

## 2026-09-28 — RESULT: verb and family on 60 real BLOCKS. The language family scores ZERO.

Run: GPU (`map_location="cuda"`), 60 blocks, untruncated views byte-identical to the
labeller's. Labels committed at `79b279c` **before** the arm ran. Wording is `DOCS` — the
style that won the engineering frame at 0.700.

| axis | what | accuracy | constant | lift |
|---|---|---|---|---|
| **VERB** | top-1 == gold **primary** | **0.350** | 0.233 | **+0.117** |
| VERB | top-1 anywhere in the gold set | 0.517 | — | — |
| VERB | gold-set recall (multi-label) | 0.503 | — | — |
| **FAMILY** | **F-direct** — one 7-way call | **0.383** | 0.317 | **+0.067** |
| **FAMILY** | **F-derived** — 17-way verb, mapped up | **0.417** | 0.317 | **+0.100** |

Every arm clears **bar 1** (beat the constant). That is the whole of the good news, and the
per-class tables are what the aggregate is hiding.

### ⚠️ `language` has 13 gold blocks and ZERO predictions

FAMILY, direct 7-way call:

| family | gold | pred | hit | P | R |
|---|---|---|---|---|---|
| agentic | 19 | 22 | 10 | 0.455 | 0.526 |
| code | 16 | 24 | 10 | 0.417 | **0.625** |
| **language** | **13** | **0** | **0** | **0.000** | **0.000** |
| understanding | 9 | 11 | 3 | 0.273 | 0.333 |
| fallback | 3 | 2 | 0 | 0.000 | 0.000 |
| media | 0 | 1 | 0 | — | — |

VERB, top-1 against gold primary:

| verb | gold | pred | hit | P | R |
|---|---|---|---|---|---|
| code.edit | 14 | **26** | 10 | 0.385 | **0.714** |
| **text.transform** | **11** | **2** | 1 | 0.500 | **0.091** |
| research | 10 | 5 | 1 | 0.200 | **0.100** |
| review | 9 | 8 | 5 | 0.625 | 0.556 |
| plan | 5 | 6 | 3 | 0.500 | 0.600 |
| **converse** | **4** | **0** | 0 | — | **0.000** |
| other | 3 | **12** | 1 | 0.083 | 0.333 |
| **code.write** | **2** | **0** | 0 | — | **0.000** |
| **text.create** | **2** | **0** | 0 | — | **0.000** |
| text.summarize | 0 | 1 | 0 | — | — |

**The aggregate is carried entirely by `code.edit` (R 0.714), `plan` (0.600) and `review`
(0.556).** Those are three classes a deterministic signal can already reach — a coding tool
editing files. **Every class this corpus was selected to measure scores at or near zero:**
`text.create` 0.000, `text.transform` 0.091, `converse` 0.000, and the whole `language`
family 0.000 on 13 real blocks.

This corpus was oversampled 20/60 toward `keld-website` specifically so the language family
would be reachable. It is reachable by a human labeller — 13 blocks of Notion document
editing, lint-and-republish, and doc restructuring. **GLiNER2 called none of them language.**

### Read-swamping, in a third place

`code.edit` is predicted **26 times against 14 gold**; `other` **12 against 3**. The model
reads the *mechanism* — file paths, commits, PR numbers, test counts, all of which appear in
a Notion-publishing block because the publisher is a repo — and answers `code.edit`. The
labelling rubric's rule 3 names this exactly ("Judge by the ARTIFACT and INTENT, not the
mechanism. Writing a script to publish a doc is publishing, not coding, IF the doc is the
point"), and the model cannot make that distinction.

That is the same shape as Amendment 1's read-swamping (tool events rolled up by dominance
gave `researching` 95.4%) and the naive act-count augmentation, now reached through prose
rather than through counts. **Three different routes, one failure.**

### The one genuinely new finding: F-derived BEATS F-direct

**0.417 via the 17-way verb call, against 0.383 asking the 7-way question directly.**

This inverts what the domain probe suggested — that coarser, better-separated vocabularies
classify markedly better (the general-type-of-work arm reached 0.700 on 6 classes). It does
**not** rehabilitate the hierarchical cascade, which is a different mechanism and stays
refuted at 0.350: **H constrains the verb pass to the family already chosen**, so a wrong
family is unrecoverable. Deriving runs the unconstrained 17-way call and maps *up*, where a
wrong verb inside the right family still lands correctly. The gap is 2 blocks of 60, so it
is directional, not established.

### Confusions

    verb    research->other 6  review->code.edit 3  research->code.edit 3
            text.transform->code.edit 3  text.transform->plan 2
    family  agentic->code 6  understanding->code 6  language->agentic 6
            language->understanding 5  code->agentic 4

`language` does not fail toward one wrong answer — it disperses into `agentic` (6) and
`understanding` (5). The model is not mistaking document work for a specific other thing;
it does not represent the category at all on this text.

### Adjudication

| bar | verdict |
|---|---|
| 1 — beat the majority constant | **PASS, marginally.** verb +0.117, family +0.067 / +0.100. |
| per-class honesty (not a pre-registered bar, but the one that decides this) | **FAIL.** `language` R = 0.000 on 13 blocks; `text.*` R = 0.091 pooled. |

**Bar 1 is necessary, not sufficient, and this is the case that shows why.** An arm can beat
the constant while being blind to a third of the taxonomy, because the constant is itself a
code-heavy corpus. A facet that publishes `code.edit` for document work is worse than one
that publishes nothing, and this project's standing rule — never let a check that did not run
publish a confident negative — applies to a class that is never predicted just as much.

**So the family axis is NOT shippable from GLiNER2 prose classification, and the reason is
specific rather than general:** the `DOCS` wording that fixed `text.*` on the engineering
frame (0.660 -> 0.700) **does not transfer to this corpus**. That is the second time a
wording fix has been corpus-local. It is the live alternative to abandoning the axis, and it
is a wording study on THIS corpus, not another arm.

### What was NOT measured

- **No shuffled-label control was run on this frame.** Bar 5's discipline applies here too:
  without it, `code`'s 0.625 could be register rather than content. It is owed before any
  positive claim about the code family.
- The engineering 100-window gold set is single-label and cut at 60 minutes. It is **not
  poolable** with these 60 multi-label blocks and was not pooled.

## 2026-09-28 — TWO refutations: coarsening the vocabulary, and feeding the model user text

Both arms were run to test proposals made in the same session. Both proposals were wrong.
Recording them because each closes a direction that looks obviously right from the outside.

### Refutation 1 — paring the vocabulary down buys NOTHING

Retrospective remap of the SAME 60 predictions into six candidate vocabularies, 17 classes
down to 2. Zero extra inference: this isolates exactly how much error is confusion between
classes a merge would join.

| vocabulary | k | accuracy | constant | **lift** |
|---|---|---|---|---|
| atv1 verbs (as scored) | 17 | 0.350 | 0.233 | **+0.117** |
| generate/transform/analyze/explore/decide/other | 6 | 0.417 | 0.417 | **+0.000** |
| as above, plan->generate, converse alone | 6 | 0.400 | 0.417 | **−0.017** |
| explore folded into analyze | 5 | 0.433 | 0.417 | **+0.017** |
| produce / comprehend / reason / other | 4 | 0.467 | 0.483 | **−0.017** |
| artifact vs no-artifact | 2 | 0.617 | 0.517 | **+0.100** |
| atv1 family (control) | 7 | 0.417 | 0.317 | +0.100 |

**Accuracy climbs 0.350 -> 0.617 as classes merge and the majority constant climbs exactly as
fast.** Every merged vocabulary lands at lift ~0; the two coarsest are BELOW their constant.

⚠️ **This is the trap a coarser taxonomy is designed to walk into**, and the standing rule
catches it: a facet scoring below a constant is strictly worse than publishing nothing. The
68-entry list is not the failure and a 5-entry list is not the fix.

**The distinction that survives:** merging classes moves the constant with it, so it cannot
help. Narrowing the REACHABLE SET PER BLOCK from other evidence is a different operation — it
cuts effective k on each instance without changing the published vocabulary, so the constant
does not move. Fine vocabulary + narrow per-block candidate set is therefore still live;
coarse vocabulary is not.

### Refutation 2 — user text is 7% of the input and contributes NOTHING

The hypothesis: block prose is 93% assistant narration (paths, SHAs, PR numbers, test counts),
which is the lexical material driving `code.edit` over-prediction. The shipped `task_type`
facet measures 0.733 on PROMPTS, so feed the model prompts.

Three arms, same 60 blocks, same committed labels, only the input differs:

| arm | input | scored | abstained | acc | in-set | const | **lift** |
|---|---|---|---|---|---|---|---|
| **U** | user turns only | 50 | **10** | **0.140** | 0.260 | 0.260 | **−0.120** |
| **A** | assistant turns only | 60 | 0 | 0.350 | **0.550** | 0.233 | **+0.117** |
| **L** | all turns | 60 | 0 | 0.350 | 0.517 | 0.233 | **+0.117** |

**A ≡ L.** Identical accuracy, and assistant-only is slightly BETTER on in-set (0.550 vs
0.517). The 7% of user text is not being read at all — removing it changes nothing.

**U is catastrophic**, and the mechanism is the corpus, not the model: it predicted `other`
**32 times in 50 blocks**. Median user turn is **47.5 chars**; **44% are under 40 chars** —
`yes`, `1`, `add that`, `publish it`, `Continue from where you left off.` In an agentic
transcript the user turn is a STEERING token, not a statement of intent. 10 of 60 blocks have
no user turn at all.

⚠️ **This reproduces a finding already in the record on a different question and I should have
weighted it.** Project attribution measured user text alone at **28%** of 61 labelled blocks
against **92%** whole-block mean-pooled, with **24 of 25 blocks having no user text** being
agent continuations. Two independent measurements, two different questions, one conclusion:
**user text alone is the wrong unit for any BLOCK-level question on agentic transcripts.**

### What the two refutations jointly establish

Fourteen measurements now bracket GLiNER2 on block prose: 6 vocabularies x granularity, 3
input variants, 2 family derivations. **Every one lands in +0.000 to +0.117.** Against that,
the deterministic file-extension arm (G) measured **+0.247**.

That is a ceiling for *this model on this task*, robust to vocabulary and to input selection.
It says nothing about other model classes — GLiNER2 is a bi-encoder trained on
GPT-4o-annotated news/law/wiki/pubmed/arxiv with **zero developer text**, and whether a small
generative model reading the same prose clears the ceiling is UNMEASURED and open.

**Consequence for any fusion design:** the model half must NOT be specified as
`P(intent | user text)` — that is arm U, the worst result on record. If a fusion is built, the
model reads assistant prose and carries the SMALLER weight, because the deterministic side has
twice its measured lift.

### Also confirmed, from the earlier arms — fuse at the SCORE level, never in the prompt

Arm A (2026-09-25) already combined the two sources by feeding raw act counts into the prompt
as a hint: **0.640 against 0.660 for prose alone at matched `rich` wording.** Augmentation
through the prompt HURT — the same read-swamping, injected deliberately. Any combination must
happen over scores, after both passes, never as text the model reads.

The three-source arm did it correctly and is the only positive evidence for decomposition so
far: modality proposes candidates at one sub-window's weight, **recall 0.476 -> 0.548 with 11
candidates added and top-1 never displaced** — but n=7. That is the arm worth re-running at
n=60, and `propose, never gate` is its load-bearing rule.

## 2026-09-28 — THE UNIT WAS THE PROBLEM. Per-request, deterministic, macro F1 0.824.

Repo owner, twice, emphatically: *"WE WANT TO KNOW THE ACTIVITY TYPES ***INSIDE*** BLOCKS, NOT
UNIQUELY ATTRIBUTE ONE TYPE TO A BLOCK"*, and the routing unit is **per-request**.

Every arm above scored one top-1 label per 20-minute block against a hand-assigned "primary".
That was wrong twice over: a router substitutes a REQUEST, and the asked-for output was always
a DISTRIBUTION over what a block contains. The requests are grouped by `requestId` — thinking,
text and tool_use blocks of one inference share it and share one `usage`.

### The population, 298 transcripts / 8,193 requests

| routing class | %req | %out tok | %in tok | med out | med in | %think |
|---|---|---|---|---|---|---|
| operate | 31.7% | 26.9% | 39.7% | 410 | 179,554 | 25.7% |
| retrieve | 29.3% | 16.2% | 19.0% | 241 | 75,778 | 32.9% |
| author_code | 12.1% | 15.0% | 9.4% | 576 | 94,952 | 26.8% |
| author_prose | 8.7% | 18.1% | 11.8% | 776 | 208,498 | 18.1% |
| synthesize | 7.2% | 14.1% | 8.8% | 942 | 132,390 | 19.8% |
| verify | 5.1% | 3.4% | 5.2% | 189 | 96,178 | 20.0% |
| acknowledge | 2.9% | 0.7% | 1.7% | 232 | 65,592 | 12.2% |
| delegate | 2.9% | 5.6% | 4.3% | 1,395 | 274,381 | 21.8% |

**41.5% of requests are subagent sidechain** — included, and they were in the hand-labelled
block prose too (`verb_family_frame.load()` has no `isSidechain` filter), so labels and
requests cover the same population. 44% of subagent tool calls are Read/Edit/Write.

⚠️ **atv1's verbs were abandoned here, measured rather than assumed:** 38.8% of requests fall
to `other` under them — git, docker, mkdir, cp, rm, curl, running scripts, killing servers.
atv1 names knowledge work; the request population is dominated by mechanical operations. The
eight classes above assert **full coverage** instead; there is no `other` bucket.

⚠️ **INPUT:OUTPUT IS 235:1** — median 113,286 in, 403 out, 1.71 billion against 7.3 million
corpus-wide. Choosing a model by activity type optimizes the **0.4%** of token flow that is
generation. Whatever the routing taxonomy ends up being, the money is in the context.

### Result: 120 blind hand-labelled requests

Stratified 15/class (a proportional draw gives single digits for the classes that decide
routing quality, so **no base rate may be read off the labels**). Sample committed at `e1df5e6`
and labels at the commit after, both before scoring.

| class | gold | pred | hit | prec | rec | F1 |
|---|---|---|---|---|---|---|
| delegate | 15 | 15 | 15 | **1.000** | **1.000** | **1.000** |
| author_code | 18 | 15 | 15 | 1.000 | 0.833 | 0.909 |
| synthesize | 18 | 15 | 15 | 1.000 | 0.833 | 0.909 |
| acknowledge | 12 | 15 | 12 | 0.800 | 1.000 | 0.889 |
| retrieve | 18 | 15 | 14 | 0.933 | 0.778 | 0.848 |
| verify | 15 | 15 | 12 | 0.800 | 0.800 | 0.800 |
| author_prose | 14 | 15 | 11 | 0.733 | 0.786 | 0.759 |
| **operate** | 10 | 15 | 6 | **0.400** | 0.600 | **0.480** |

**macro F1 0.824**, 100/120 agreement, **with no model at all.** Against the block-level arm on
the same corpus: top-1 0.350 against a 0.233 constant.

Population-weighted (per-class precision x that class's real share): **expected accuracy on a
random request 0.751**. `operate` alone costs 0.19 of it — lifting its precision to 0.80 takes
the estimate to **0.878**, and it is the single highest-leverage fix in the whole project.

Confusions are almost entirely *into* `operate`: `retrieve->operate` 4, `verify->operate` 2,
`author_code->operate` 2, plus `operate->author_prose` 3. It is the residual and it absorbs
everything the Bash rules do not name.

### ⚠️ THE LIMITATION THAT BOUNDS THIS NUMBER

**I wrote the classifier, then labelled the sample.** The blind file carried no class, the order
was shuffled, the key was held back and the sample was committed first — but knowing the rules
means the labels may reproduce the classifier's logic rather than judge independently.
**0.824 is therefore an upper bound, not an estimate.** A second labeller who has not seen
`request_route_vocab.py` is the fix, and it is owed before this number is quoted anywhere.

Two rule edges are where that bias would bite hardest, both written down during labelling:
an authored program in a tool argument is `author_code` whatever the program does; a real
commit message or PR body is `author_prose` while `git add` plus a one-liner is `operate`.

### What a block publishes

Not one label — the mix, in both calls and output tokens, which disagree and should both ship:

    V047   55 requests, 42,007 output tokens
      operate      50.9% of calls / 37.9% of output
      author_code  27.3%          / 45.1%
      retrieve     10.9%          / 11.8%

    V043   53 requests, 68,413 output tokens
      operate      47.2% / 42.4%      author_prose  9.4% / 25.8%
      retrieve     18.9% /  5.5%      synthesize    3.8% / 14.2%
      author_code  17.0% / 10.8%

`author_prose` is 9.4% of V043's calls and **25.8% of its output tokens**. A router reading
call-share alone would under-weight it by a factor of nearly three.

### Two data defects found by reading examples, not by checking totals

1. **Session forks copy history.** 6.8% of requestIds appear in more than one transcript file,
   so the collector concatenated every copy — one Bash call counted three times, narration
   tripled. 1.15x overall, 2-3x on affected requests. A global check for duplicate tool_use
   block IDs returned **0.0%**, because the duplicates are across files. Only reading a sampled
   record showed it. Fixed by deduping on record `uuid`.
2. **`cd <path> &&` prefixes 56.7% of Bash calls** and hid the real command from a rule that
   read the first token, so `cd` looked like the dominant activity. And **a Bash heredoc is
   usually authored PROSE, not code** — 24.9% of Bash calls carry one, median 945 chars against
   178, and the bulk are `git commit -m "$(cat <<EOF...)"` and `gh pr create --body`.

### 2026-09-28 — the `operate` fix, and the HOLDOUT that checks it

Nine mechanism fixes, derived by reading all 20 errors rather than guessing. The largest is
general and would have kept biting: **every shape regex ran over the whole command string,
heredoc bodies included**, so a 16,000-token implementation plan written with
`cat > plan.md <<'PLAN'` classified as `verify` — the word "test" appeared in its PROSE — and
a commit message mentioning pytest did the same. `classify_bash` now splits the command
skeleton from its heredoc bodies and matches only the skeleton.

The rest, each with the error count it closed:

| fix | closed |
|---|---|
| `strip_lead` also strips `echo "=== header ==="` and `export`/`source` hops, not only `cd` | 3 |
| prose-only splits on STRUCTURE, not token count (three real reports sat at 247/374/376) | 3 |
| `VERIFY` accepts flags between runner and `test` (`npm --prefix X test`) | 2 |
| `CODE_CMD` matches `python3 - <<PY` / `python3 << EOF`, which `-c` matching missed | 2 |
| a commit is `author_prose` only when there IS a message (heredoc/`$()`, or `-m` with a newline or >120 chars) | 2 |
| `SIDE_FX` beats a leading retrieve — `git status && … && docker compose up --build` is a deploy | 1 |
| `javascript_tool` / `evaluate_script` are `author_code` | 1 |
| `SendUserFile` DELIVERS an artifact; its caption is a sentence, not a document | 1 |
| `git commit -F -` matches as well as `-m` | 1 |

⚠️ **Also fixed: the scorer read the class recorded in the key file AT SAMPLING TIME**, so the
first re-run silently scored the OLD classifier and reported no change at all. A green
"0.833, unchanged" that was measuring nothing. It now re-derives from `requests.json`.

#### Fitted vs held out

| | fitted (the same 120) | **HOLDOUT (80 fresh, disjoint)** |
|---|---|---|
| agreement | 111/120 = 0.925 | **71/80 = 0.887** |
| macro F1 | 0.916 | **0.881** |
| population-weighted expected accuracy | 0.903 | **0.856** |

Before the fix, the same population-weighted figure was **0.751**. So the honest gain is
**0.751 → 0.856**, and the generalisation gap (0.903 fitted vs 0.856 held out) is **0.047** —
real, small, and reported rather than absorbed.

Holdout per class:

| class | prec | rec | F1 |
|---|---|---|---|
| author_code | 1.000 | 1.000 | **1.000** |
| delegate | 1.000 | 1.000 | **1.000** |
| synthesize | 1.000 | 0.833 | 0.909 |
| author_prose | 0.900 | 0.900 | 0.900 |
| acknowledge | 0.800 | 1.000 | 0.889 |
| retrieve | 1.000 | 0.769 | 0.870 |
| verify | 0.900 | 0.818 | 0.857 |
| **operate** | **0.500** | 0.833 | **0.625** |

`operate` improved (precision 0.400 → 0.500) but is **still the weak class and still the
residual** — `retrieve->operate` 3, `verify->operate` 2 are 5 of the 9 remaining errors. Its
holdout n is 6, so that 0.500 carries little confidence either way. It is the next fix, not a
solved problem.

⚠️ **The labeller bias is unchanged and still bounds both numbers.** I wrote the classifier and
labelled both sets. The holdout removes the fitting problem, not the bias one — a second
labeller who has not seen `request_route_vocab.py` is still owed. Two holdout labels (H069,
H079) are cases where I bent my own rule 2 and said so in the labels file rather than quietly
picking whichever side scored better; a third (H066) is the same ambiguity from the other
direction.
