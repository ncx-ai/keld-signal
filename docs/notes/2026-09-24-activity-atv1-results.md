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
