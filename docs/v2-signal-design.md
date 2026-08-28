# Keld Signal v2 — how it works

For a developer new to this system. It explains what runs, when each thing happens, what is
actually detected, and *why* the design looks the way it does. Reference material lives in
`AGENTS.md`; this is the orientation.

Current at `enrich.SchemaVersion` **16**. Nearly every number below is a measurement with a commit
behind it — where a decision looks arbitrary, it usually isn't, and §8 explains why that matters
more here than in most systems.

---

## 1. The one invariant, first

**Raw prompt text is read on the engineer's machine and never leaves it.**

Everything else is a consequence of that. Keld wants to tell an organisation what its AI-assisted
engineering work consists of — which projects, which languages, whether sensitive data is being
pasted into prompts — without shipping the prompts anywhere. So the analysis happens locally and
only *derived, masked* signal is published.

Concretely, what crosses to Atlas is: label values from closed vocabularies, masked spans (at most
the last four characters of a detected secret), counts, and shares. What never crosses: prompt text,
assistant text, file contents, and the free-text proper nouns extracted during analysis.

---

## 2. Two lanes that do not touch each other

    TELEMETRY   the coding tool -> Atlas directly.       usage, tokens, cost.
                Configured by `keld signal setup`, which writes an OTLP block into
                each tool's own config. No Keld process is involved at runtime.

    ENRICHMENT  the coding tool -> keld-agent -> Atlas.  what the work WAS.
                A local daemon reads transcripts on disk, derives signal, masks, publishes.

They are joined at Atlas by a correlation id: `enrichment.corr_id == tool_event.prompt_id`. That
join is why prompt ids matter so much later in this document — an enrichment whose id does not match
the telemetry's is orphaned, and silently.

---

## 3. Three processes

    keld              the CLI. Detects installed tools, edits their configs, installs the
                      service, authenticates. Single static Go binary, no runtime deps.

    keld-agent        the daemon. Owns capture, the job queue, the enrichment pipeline,
                      masking, and publishing. Go.

    keld-agent-       the analysis + enrichment SERVICE. Python, FastAPI, loopback only.
    sidecar           Owns the reference store and everything derived from transcript
                      content. GLiNER2 is one capability it CAN load; it is not its identity
                      and is not required for it to run.

The sidecar distinction matters and is easy to get wrong: it used to be thought of as "the GLiNER2
sidecar". It is not — it serves the store, `/analyze`, `/pii` and `/ingest` with no model at all.
But **the model is still used**: seven classification facets call it (see §6d). What v2 changed is
that the *service* no longer depends on it, and the two facets that matter most for privacy —
sensitivity and workstreams — no longer touch it.

---

## 4. What happens, in order, when an engineer submits a prompt

**Two triggers feed one queue.** A *hook* (which the tool invokes) and a *transcript watcher*
(which tails the JSONL files tools write to disk). Either produces a **pointer** — transcript path
plus prompt id, **never text** — and the queue de-duplicates the overlap.

    1  TRIGGER      hook fires, or the watcher notices a transcript grew.
                    A pointer is enqueued. If the daemon is down, the hook writes the
                    pointer to an on-disk spool and the daemon drains it on startup.

    2  INGEST       the watcher separately tells the sidecar "this transcript advanced".
       (parallel)   The sidecar parses ONLY the appended bytes from a stored offset and
                    updates its store. This is off the request path deliberately -- see §5.

    3  RESOLVE      the daemon reads the prompt text from the transcript, locally.

    4  ENRICH       a staged pipeline of independent passes runs over the text and over
                    the window of work around it. Each pass has its own 30s deadline.

    5  MASK         enforced Go-side. A detected secret becomes "…4483"; an email keeps
                    only its domain.

    6  PUBLISH      POST to Atlas. Durable: batched, retried, spooled when unreachable.
                    Atlas de-duplicates on a dedup key, so a late retry is harmless.

**Deadlines are per pass, not per job.** This is load-bearing. A job may issue eight or nine
separate analyses; a job-wide budget meant one slow pass discarded every pass that had already
succeeded, and the whole job was retried — the same work redone and re-discarded until the attempt
budget ran out. Bounded per pass, a slow pass costs exactly one facet: the others commit and the
profile publishes as `pipeline_status: "partial"`. Progress is monotonic.

---

## 5. The reference store — why analysis is a query, not a parse

The naive design re-reads the transcript for every prompt. Measured: a 60-minute window of work
contains a mean of **3.8 prompts** (max 20), so an hour was characterised ~4 times, each costing
**0.79s** on a 90 MB transcript. Nothing persisted, so nothing could ever be compared over time.

v2 ingests each transcript **once, incrementally**, into SQLite at `~/.keld/state/refseries.db`.

    event      the raw facts: (session, timestamp, level, ref, count)
    bin        5-minute rollups of those facts
    ingest     per-file byte offset, size, head hash, and a WATERMARK

Result: **0.79s -> 0.0023s.** Same answer — proven equal on 90 real prompts across 30 transcripts,
0 differing.

Three properties a newcomer will otherwise trip over:

**A tail parse is not automatically a full parse.** Two analyses are retroactive: path
reconciliation resolves prose-mentioned files against accumulated context, and workspace resolution
depends on evidence scattered through the file. Ingesting naively in chunks differed from one pass
by thousands of rows. It is equal *because* the reconciliation state is persisted and recomputed
whole per batch, and a changed workspace answer forces a reparse. Verified across 284 transcripts.

**Never answer past the watermark.** If a window ends after what has been fully ingested, the
service refuses (`503`) and the job retries. Serving a window missing its last few minutes would
publish a confidently wrong attribution, which is worse than publishing nothing.

**Two silent data-corruption bugs lived here and are worth knowing about.** Neither was found by a
test that said what was wrong. `transaction()`'s reentrancy depth was one integer on the Store while
each thread holds its own connection — so a second writer read `depth == 1`, assumed it was nested,
skipped its own `BEGIN IMMEDIATE`, ran in autocommit and held the WAL lock until everyone else timed
out. It surfaced as a 1-in-8 "flaky test", and the real error was *masked* by a secondary exception
from `__exit__`. And the session key was `basename(path)[:8]`, which is not unique — **445 of 500
transcripts collided**, since Claude Code writes subagents as `agent-<hash>.jsonl`. It surfaced
because a study silently reported 550 windows where the truth was 1,022. The key is now
`sha256(abspath)[:16]`; the *display* label keeps the short form deliberately, because the
fixture-identity gate fingerprints it and that gate is checked out at a different path on every
machine.

**Bins are not a fallback for events.** Digests read `event`, not `bin`, because reconciliation must
be re-scoped per window. So pruning old events does not *narrow* a window — it breaks it. Retention
is therefore a 400-day horizon plus a refusal (`410`) rather than a silent shortening.

---

## 6. What is actually detected

### 6a. Workstream dimensions — *what this hour of work was about*

Derived from **tool-call inputs only** (which files were read, which commands ran, which branch was
checked out) — never from model output, never from prose. A closed vocabulary maps each observation
to a level, and each level is rolled up over the window:

    project · branch · model · output_type · language · skill · tooling

A dimension is **attributed** only if one value holds >= 50% of the window's evidence **and** there
are at least 5 observations. Below that it says so rather than guessing. The evidence floor is
derived, not chosen: take the 50% share as the null hypothesis and unanimity over *n* observations
has probability `0.5**n`, which first clears 5% at n = 5.

Note that floor is about **sample size, not duration** — a shorter window does not get a smaller
floor, because that would make an attribution's significance depend on window length while the
published `value` and `share` look identical.

### 6b. Sensitivity — *was concrete sensitive data pasted into a prompt*

This detects **leaked data, not topic**. A prompt discussing healthcare is not `phi`; a prompt
containing an SSN is. Two deterministic sources, no model:

    credentials   gitleaks rule set, pure Go. API keys, tokens, private keys, JWTs.
                  Plus structural detectors where a parser beats a pattern: a URI
                  userinfo password is extracted with `net/url` and is a credential
                  BY DEFINITION, not by guess.

    personal      Microsoft Presidio, in the sidecar. 25 CHECKSUM-VALIDATED recognizers
                  in three tiers: universal (card via Luhn, email, phone via
                  libphonenumber, IBAN, crypto), a default `us` set (SSN, ABA routing,
                  NPI, DEA licence), and 11 opt-in regions via `KELD_PII_REGIONS`.

Detected entity types roll up to the highest severity present: `ssn`→`phi`,
`credit_card`→`pci`, credential→`secrets`, other identifier→`pii`.

**The gate that makes this usable.** Developer transcripts are full of documentation values.
`4111 1111 1111 1111` passes Luhn; `123-45-6789` is the textbook SSN; `user@example.com` is
RFC-reserved. Without suppressing them, measured precision on real prompts was **~1%, with 24% of
all prompts reporting `pii`**. With the gate, and after dropping free-text name/address detection
(858 spans, approximately zero real names — `JSON` alone accounted for 132), precision is **10/10**
and the false-`pii` rate fell from 240 to 4.5 per thousand prompts.

Two things follow that are easy to misread as gaps: **person and address are not detected** (spaCy's
NER is measurably unfit for this on code-heavy text), and **`proprietary` can never be emitted**
because no detector maps to it.

### 6c. Dynamics — *how the work is changing*

New in v2, and only possible because the store persists history. Each analysis compares a **recent
slice** against a **preceding baseline** and reports, per dimension:

    turnover              share of slice evidence in values absent from the baseline
    decay                 values leaving
    concentration_shift   the dominant value's slice share minus its baseline share
    reading               a STATED conclusion from a closed 7-value vocabulary

The `reading` exists because of a measurement: a 16 KB window document scored **worse than nothing**
for answering questions about the work (-3.3), while a short digest carrying the *same facts* scored
**+36.7** — because the digest stated a conclusion and the document left the reader to divide two
numbers. So the conclusion ships beside the numbers, and the unlabelled remainder does not ship
at all.

**Where the slice boundary goes was decided by experiment, not preference.** A fixed 15-minute slice
was compared against an EWMA of novelty and against `river`'s ADWIN, PageHinkley and KSWIN, scored
against deterministic ground truth (a real transition = the dominant branch changing). The EWMA won
by **+74.6 precision / +27.0 recall**, and `river` does not ship — its defaults structurally cannot
fire inside a 60-observation budget.

The control is what makes that believable: relocating every transition to a random position collapses
the EWMA from 86.4% to 24.1%, while every *fixed* sizer barely moves (11.9 -> 10.9) — a constant
offset was never a detector — and ADWIN scores *better* on shuffled truth than real, i.e. carries no
signal at all.

Half the candidate dynamics were then **dropped on their distributions**: `project` was identically
zero across all 2,180 comparable windows (a transcript is scoped to one project directory), and
`model` reported a change **0 times in 2,702 windows**.

### 6d. Physical acts — *what was done, as an inventory rather than a label*

    read · search · edit · create · transform · convert a document · run code ·
    query a database · test · build · commit · version control · fetch · publish ·
    deliver a file · delegate · install · manage files · run a service · apply a skill  (22 total)

Published as `inventory.physical_acts`. Measured live: `read 162, search 107, edit 63, test 44,
version control 33, commit 28, run code 14, create 6, build 5, manage files 3`.

**It is an inventory, not an allocation, and that was measured rather than assumed.** As a
single-winner dimension it covers only **18.5%** of windows — but almost none of that is missing
data: the level fires in **97.8%** of windows at a median of 34 observations, more evidence than
`output_type` (10) or `language` (9), both of which ship. The top act's median share is **0.403**,
and a window carries a median of **7** distinct acts (max 16). No floor recovers it; a 0.30 floor
still yields 0.612.

An hour of real work reads *and* edits *and* tests. "Which act owns this hour" is the wrong
question, which is why this joins `named_terms` (97% coverage / 19% dominance) on the inventory
side rather than the allocation side. It is also the only inventory dimension with **no top-N cut**:
the vocabulary is closed at 22, and 4 of 10 sampled windows carry 14–15 distinct acts — above the
12 the open-vocabulary dimensions are truncated to.

Unlike `named_terms`, it **is** published to Atlas. The difference is verified in code, not asserted:
`action` is written at exactly two sites, both inside the `tool_use` branch, from a tool name and
from shell argv, each through `action_for`, whose every return is a literal or a closed-table lookup.
No transcript fragment can occupy the level.

### 6e. File paths — *which parts of the tree were hot*

    files · directories · components

Three inventory dimensions over the same reconciled paths the workspace resolution already
produces, published as a frequency distribution (`[{value, n}, ...]`) rather than a single owner —
"which files were hot this hour" is a distribution question, so allocation is the wrong shape.

The caps are per-level rather than the blanket 12 the other open vocabularies use, and that
difference is measured. Distinct paths per one-hour window, over 165 windows:

    file        p50  8   p90 32   max 54      cap 40
    dir         p50  5   p90 14   max 27      cap 24
    component   p50  3   p90  7   max 17      cap 16

At a cap of 12, a third of all windows would be truncated on `file` alone. Truncation keeps the
top N by count, so a hotspot is never what gets cut — but the tail is what distinguishes an hour
spent in three files from an hour scattered across forty, and dropping it silently makes the
second look like the first. So the cut is reported in a sibling `inventory_omitted` block, which
also covers the six older inventory dimensions that had been truncating silently at 12.

**Why publishing paths is acceptable here** is worth stating, since it is the first dimension
carrying strings lifted from the filesystem. Every value is already workspace-relative —
`reconcile()` resolves each path against the resolved workspace root — and that was verified
rather than assumed: across the full 500-transcript corpus plus a Cowork session, zero absolute
paths, zero home directories, zero `../` escapes, at all three levels. A test pins it. What does
cross the wire is repository *structure* and whatever appears in a filename, which is the same
exposure `branch` already carries.

These dimensions are coding-heavy. A non-engineering session produces three distinct paths in
total, so an empty list here is an accurate answer about the work rather than a failure to
measure it.

### 6f. Effort — *what the window cost in work*

    authored_bytes · authoring_turns · authored_status
    fast_share · gaps · tempo · tempo_status

Two signals that survived a six-candidate sweep of everything the transcript carries and the system
was discarding.

**Diff magnitude** is the byte size of what edits actually wrote. Held at a *fixed edit count*,
per-window byte totals span **22×–87×** from p10 to p90 — windows indistinguishable under
`edit >= 5` differ by two orders of magnitude in bytes authored.

**Tempo** is the share of inter-turn gaps under 5s, which separates long autonomous stretches from
turn-by-turn steering. Its independence is the cleanest of any candidate measured: **r = +0.012**
against log window volume, −0.001 against the published `evidence` count.

Note `tempo` states a conclusion while `authored_bytes` does not. The corpus supplies a defensible
cut point for the gap share (median gap 4.15s; a 5s cut puts median `fast_share` nearest 0.5) and
supplies none for a byte sum — so the byte count publishes with its turn count and refuses to invent
"large" versus "small".

`absent` and `0` stay distinct: a window with costed edits that authored nothing publishes `0`; a
window with no magnitude at all publishes `null`.

### 6g. Classification facets — model-backed, and not running this phase

Six facets classify the prompt text against a closed vocabulary using GLiNER2 in the sidecar.
**None of them runs in this phase** — GLiNER2 is not being shipped, so they publish nothing and are
named in `facets_skipped`:

    task_type · domain · activity_type · personal · function_guess · subcategory

(`speech_act` was removed outright at SchemaVersion 9 — it scored 0.695 against a 0.713 constant,
predicting `statement` 22 times and being right zero times.)

Measured live against the gold set when the model *is* enabled: `task_type` 0.733 vs a 0.143
baseline, `domain` 0.683 vs 0.261, `activity_type` 0.670 vs 0.243. So they work; they are simply not
part of this phase. `personal` has **zero** gold labels and is unmeasurable; `function_guess` and
`subcategory` have n=20.

These are the reason `ml_backend:"auto"` still provisions a ~1.8 GB model. They are also the part of
the system with the weakest evidence behind it — see §9. Classifiers score against readable label
DESCRIPTIONS, not bare ids, because the bi-encoder keys on token overlap; the label wording is
load-bearing.

In `ml_backend:"deterministic"` these seven do not run at all, and the profile publishes the
model-free facets with the others named in `facets_skipped`.

### 6h. The session prior — *what the session looked like before this hour*

A window is characterised on its own, which loses something obvious: a value that barely cleared
the 50% attribution floor looks exactly like a value that is the whole story. The session the
window sits in is a cheap frame of reference that tells them apart, so each enabled dimension
also carries the session's own answer and three ways of comparing:

    prior:  value · share · evidence · status        the session before this window
    agrees      did the window's value match the session's?
    departure   the window's share minus that value's share of the session
    novel       is the window's value one the session had never produced?

It costs no second parse and no inference — it is the same rollup the window already uses, run
over wider bounds, about 1.6 µs per dimension.

**The rule that governs everything else here: the prior is a contrast, never a fallback.** It is
reported *beside* the window's answer and never supplies one the window lacked. If the window
could not attribute a dimension, all three comparisons are null and the dimension stays blank.
Letting a thin window inherit the session's value would buy coverage by turning "we don't know"
into something that reads as confident — the precise failure the evidence floor exists to
prevent, and one this project has already paid for twice (two facets that predicted a label
dozens of times and were right zero times). Note that **45.1% of windows have no prior at all**,
because they are a session's first. That number is the standing temptation to relax the rule;
the block instead says `absent` out loud, since a silently omitted block reads as an oversight
and oversights get "fixed".

**The prior stops at the window's start**, which corrects the design's own wording. "The session
so far" read literally would include the window inside its own prior, and that is not merely
weaker — it is degenerate. `novel` could never fire (0 of 1,022 windows, by construction), a
session's first window would be its own prior at 100% agreement, and every departure shrinks
toward zero the more of the session the window represents. So the range is `[session start,
window start)`: still causal, still a subset of what the daemon knew.

**Four of the seven dimensions carry it** — `branch`, `language`, `output_type`, `skill` —
chosen by measurement over 1,022 windows. `project` and `model` agree with the session 100% of
the time with zero disagreements, so a comparison there would publish a constant.

The `output_type` decision is worth reading, because the first answer was wrong for an
instructive reason. It was excluded on 86.7% agreement — seemingly too predictable to be worth
reporting. But agreement is only defined where *both* the window and the session have an answer,
so it is silent about exactly the case the prior is for: windows with no answer of their own. On
a real non-engineering session the prior supplied `output_type` in 6 of 7 windows the window
itself could not attribute — a deck built in the first hour, with every hour after reading
`absent` while the session reads `presentation`. A metric that cannot see the case a feature
exists for is the wrong metric, not a verdict.

The dimension list is *derived* from the published allocation set rather than written out again.
That is a privacy property, not tidiness: it makes an inventory level structurally impossible to
add here, which is what keeps `named_terms` — the one level read from message text, which has
held real people's names — out of this block by construction.

---

## 7. Three operating modes

`ml_backend`, local and startup-only:

    "auto" (default)   everything above, and the GLiNER2 model loads lazily when a
                       model-backed facet is requested. CORRECTION (measured): the
                       Go pipeline still requests one -- 6 inferences per prompt
                       (task_type, domain, activity_type, personal, function_guess,
                       subcategory, speech_act). See
                       enrich.TestBuiltInPipelineStillDemandsAModel, which fails the
                       day that reaches zero. The weights are fetched ON DEMAND, by
                       the first attempted inference, never at daemon start.
    "deterministic"    enrichment runs; the model is never asked for, so never loads.
                       The service still starts -- it is needed for analysis and PII.
    "off"              no enrichment at all; the intake endpoint accepts and discards.

Enrichment never silently degrades. If the service cannot answer, jobs queue and spool until it can
— they are never handed to a lower-fidelity substitute. The one exception is deliberate: if no
sidecar binary is *installed*, that is "this capability does not exist here" rather than "not ready
yet", so enrichment runs without window analysis instead of wedging forever.

---

## 8. The method — why the code reads the way it does

Nearly every constant in this system has a measurement attached, and the comments record it. That is
not decoration; it is because the failures here are almost never crashes.

**The characteristic failure is a plausible wrong number.** Roughly twenty defects surfaced during
v2 development and essentially none was caught by reading an aggregate table: a folded vocabulary
flattering a half-life, `pdf 54%` for a slide deck built from ten `pdftoppm` calls, a model answering
`2659` to "which ticket?" (it was the window's own event count). The habits that follow:

- **Render specific identifiers, not aggregates.** Name the session, show the matched value.
- **Measure on the real corpus, not the fixture.** Every unmeasured precision claim in v2 was wrong.
  A URI-password detector's first version produced 43 findings across 42 prompts and *one* distinct
  value.
- **Fixtures built from published examples measure the gate, not the detector.** This bit three
  times. Recall read as 0.14 and 0.42 when the true numbers were 1.00 and 0.71.
- **Inject a mutation and confirm the suite bites.** Vacuously-passing tests were found in nearly
  every work unit — including assertions about masking that ran over empty span lists.
- **Post-hoc findings usually fail replication.** A binary split scored +0.218 when spotted inside a
  failed experiment and **+0.061** on a fresh sample — over half the original effect was class
  balance, the baseline having moved 0.538 -> 0.653 on resampling alone. Treat anything discovered
  mid-analysis as a hypothesis, never a result.
- **Correlation with log window volume < 0.5 is the single most informative test here.** Almost every
  attractive candidate turns out to be a size bucket wearing a label: derived routing clusters named
  *size x did-anything-run*; output volume scored +0.552; `interactivity` +0.497; a promising
  authoring split +0.737. Two signals passed it, and both shipped.
- **Score a stability bar against a permutation null, or it means nothing.** A 0.70 split-half
  agreement floor looked reasonable until the same procedure on column-permuted noise cleared it out
  to k=8, matching real data exactly at k=4. The bar sat below its own null's p95.
- **Pre-register the decision rule** before an experiment, and report the null result when it comes.
  `river` was rejected this way; so was GLiNER2 as a classifier (37.8% against a 67.8% majority
  baseline — 30 points *worse* than a constant guess, and degenerate at 91% one class).

---

## 9. Deliberately not done, and what was refuted

**Not done:**

- **GLiNER2 is not shipping this phase.** The plumbing remains and loads lazily; nothing asks, so
  nothing downloads. The six classification facets above go with it.
- **Enrichment cadence is per prompt.** A tick-based cadence is specified and deferred — the store
  removed its efficiency justification, leaving a product decision.
- **Codex gets telemetry but no enrichment.** Both capture paths are dead: the watcher requires an
  `ordinal` field real Codex transcripts do not emit (0 of 26 prompts captured across 14 sessions),
  and its hook events are not prompt submissions. The fix is known, but the prompt id must match what
  Codex's own telemetry emits or the Atlas join orphans every row.
- **Cowork** is supported in code but its transcripts moved inside a VM the host cannot read.
- **`proprietary`** is in the sensitivity vocabulary and structurally unemittable — no detector maps
  to it.

**Refuted, with the measurement, so nobody repeats them:**

| | result |
|---|---|
| `activity_type`, deterministically | **four attempts, closed.** 6-way mapping 0.218 vs a 0.538 constant; derived clustering named size; two binary facets +0.061/+0.060; and a re-run on the *corrected* vocabulary still −0.169 |
| GLiNER2 as an activity classifier | 37.8% vs a 67.8% baseline, degenerate at 91% one class |
| spaCy NER for `person`/`address` | 858 spans, ~zero real names (`JSON` alone accounted for 132) |
| gitleaks as a library | 17/24, **bit-identical** to our 490-line loader, for 204 modules |
| token-weighted attribution | dominant value flips in **0.89%** of slots — and applying it would have silently deleted `MIN_EVIDENCE`, a count floor being meaningless against a token sum |
| output volume | +0.552 against log volume — a tool-call count restated |
| thrashing (consecutive tool errors) | real and concentrated (49 windows hold 29.1% of all errors) but 4.8% prevalence |
| shape+keyword credential detection | 6 candidates in 2,137 prompts, all git SHAs or prose runs, zero credentials |

**Why `activity_type` closed.** The levels record *what physically touched a file*; the vocabulary
divides on *what the change meant*. Implementing a feature and reformatting a document are the same
`Edit`. A vocabulary fix that corrected 96%-wrong `transform` records moved the score 15 points and
did not move the verdict — which is how we know the two problems were independent. The reframe that
*did* work was to stop asking what an edit meant and publish what happened: §6d.
