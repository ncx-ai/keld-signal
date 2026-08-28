# Session story rollup — design

> ## ⚠️ STATUS: BUILT AND MEASURED. Its central claim is REFUTED.
>
> This document is the design as *proposed*. It was implemented in full and measured end to
> end. **The measurement is in
> `docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md` Part 7, which is
> the authority wherever the two disagree.**
>
> **Headline: fact retention 94.9% → 50.0%** (56.2% with the recency anchor off), against a
> ≥90% threshold. Removing the no-shrink rule on the theory that beats plus a deterministic
> retain-list would substitute for embedding the previous report's prose **does not work as
> built**. The beat series itself is accurate and cheap; the retention substitution is not.
>
> Individual claims below carry ⚠️ markers where they were refuted or are unestablished. In
> summary:
>
> | claim | status |
> |---|---|
> | the retain-list "cannot drift the way a paraphrase can" | ⚠️ true and beside the point — it is *dropped*, not drifted (161 of 240) |
> | beats "supply a usable form of" the turning-point signal | ⚠️ refuted — `ChangedSubject` is 100% and demonstrably wrong |
> | story-vs-record is "the first currency check that does not depend on judgement" | ⚠️ refuted — T12 15.7%, all 11 flags accurate; unusable as shipped |
> | "both can come back down" (prompt budget, `ctx`) | ⚠️ refuted — both went **up**, 11,000→14,000 and 6144→8192 |
> | "beats that say nothing are not stored" | ⚠️ unexercised — 0 of 70 discarded, comparator never within 0.3 of firing |
> | T11 (synopsis lag) becomes "the primary currency measure" | ⚠️ not established — the check cannot detect the failure it scores |
> | the lag table at the top of "The problem this replaces" | ⚠️ **invalid** — see the note there |
> | beats are cheap, dense, chain-free, and read as an accurate trajectory | ✅ confirmed |
> | the record's model-dependent fields must be reported as absent | ✅ confirmed and realized (`Populated()` omits focus) |

**Scope: one interactive session.** No cross-session or per-project tier. The digest store
stays keyed by session id, as built.

## The problem this replaces

The refine loop embeds the previous digest **whole** into the next prompt — up to 4,742
characters — and instructs the model that its report "must not become shorter or less
specific". That rule exists for a measured reason: instrumenting retention showed the model
recompressing sections on refinement (`done` shrinking 860 → 306 runes, taking two named
specifics with it), and forbidding it was what fixed fact retention.

But suppressing compression is why the report cannot move on. Measured over 14 stratified
sessions:

| configuration | fact retention | fabricated blockers | synopsis lag |
|---|---:|---:|---:|
| no recency anchor | 97.4% | 4.1% | ~~14.3%~~ ⚠️ |
| anchor, wording v1 | 88.3% | 10.2% | ~~**7.1%**~~ ⚠️ |
| anchor, wording v2 | 90.9% | 2.0% | ~~14.3%~~ ⚠️ |

⚠️ **The synopsis-lag column is INVALID and this document is its only home.** All three figures
carry two independent defects, both found later in the branch:
>
> 1. **The abstention denominator.** `SynopsisLag` returns `lag=false` both when it judges a
>    synopsis current and when it *abstains* for want of opening evidence. These rates were
>    computed over all refinements, so every abstention was scored as a pass — and abstention
>    is not rare: measured on the same corpus later, 17 of 42 and 13 of 42 (40% and 31%).
> 2. **The untrimmed-token bug.** `distinctiveTerms` stored the raw token with trailing
>    punctuation attached, so a sentence-final subject could not match its own record. That
>    *suppressed* `earlyHits`, pushing verdicts below `minLagEvidence` and abstaining on
>    synopses that were genuinely lagging — so the true rates were likely **higher**, not lower.
>
> A third problem makes the column unusable even once those are fixed: `distinctiveToken`
> admits ordinary English, so a synopsis matching an unrelated window on two common words is
> certified current (Part 7, T11). **Do not use 14.3% / 7.1% / 14.3% as a baseline for
> anything.** The fact-retention and fabricated-blocker columns are unaffected and remain the
> evidence for gating the anchor.

Every configuration trades currency against durability, because the only two options the
design offers are *keep the prose verbatim* or *let the model rewrite it freely*. It also
forced the prompt budget to 11,000 characters and `ctx` to 6144.

## The design

Three grains, produced at two cadences.

- **Specific actions** — the recent conversation window, in detail. What was just done.
- **Beats** — every few turns, one to three sentences on what the work is about. Cheap, dense,
  each derived independently from its own window. The history of how the work changed.
- **The report** — the full nine-section account, rare and wall-clock bounded, written from the
  beats and the measured record rather than from its own predecessor.

The register to aim for is how an engineering lead or PM describes work: *"the work has been
on X, Y and Z; specifically A, B and C."* Coarse framing, carrying a few concrete instances.

### The invariant: nothing reads a summary of a summary

The stored report is the record and a reader sees it whole; it is never regenerated from a
condensed form. That much was in the first draft as "store full, feed compressed".

The two-tier split makes it stronger, and the stronger form is what the design now rests on:
**no model output is ever input to a later generation of the same kind.** A beat reads a
transcript window and the measured record. A report reads beats, measurements and the
retain-list. Neither reads its own predecessor.

Repeated re-summarisation eroding the most valuable content is a measured finding and it still
stands — this design simply has nowhere for it to happen. The one deliberate exception is the
retain-list, which carries named tokens rather than prose and is independently verifiable
against the transcript.

### Two tiers: cheap beats, expensive reports

The report is expensive because it writes nine sections. Asking "what is the work about right
now" is not — it is one to three sentences. Splitting those makes the frequent operation cheap
enough to run every few turns and the expensive one rare.

- **Beat** — every `KELD_DIGEST_BEAT_TURNS` (default **3**) user turns, one to three sentences
  on what the work is about, derived from the recent window and the measured record. Roughly
  60–90 output tokens against ~2,000 for a report, so twenty beats cost about one report.
- **Report** — the full nine-section digest, bounded by the wall-clock floor, which reads the
  **beat series** rather than any summary of itself.

The beat series *is* the history of how the work changed over time. It is also directly
presentable: a timeline of one-line statements is arguably a better answer to "what happened
over the session" than any single report.

**No model output is ever input to a later generation of the same kind.** A beat reads the
transcript window and the measured record — never another beat. A report reads beats,
measurements, and the retain-list — never an earlier report's prose. This replaces the
"store full, feed compressed" formulation with something stronger: nothing fed to a
generation is a summary of a summary, so there is no chain along which drift can compound and
no need for the drift bounds an earlier draft of this spec had to invent.

The one exception is deliberate and narrow: the **retain-list** is derived from the previous
report. It carries *named tokens*, not prose, and each is independently verifiable against the
transcript, so it cannot drift the way a paraphrase can.

⚠️ **Measured: true, and beside the point.** Nothing in the retain-list drifted. Specifics were
**dropped** instead — 161 of 240 were named in the list the prompt carried, under the explicit
instruction *"each must still appear, unless the new part shows it was wrong"*, and the model
dropped them anyway. And because the list is re-derived from the **previous report only**, the
first drop is permanent: no later retain-list can name it (79 of 240 pairs). Immunity to drift
does not buy immunity to loss, and this exception is where the design's retention failure lives.
The follow-up the measurement points at is a **cumulative** retain-list — a union over all prior
reports — which is a change to *this* clause, not to a cap.

**What a beat is given.** The recent window, plus the session record for framing. Not the
previous beat — that is the chain this design exists to avoid. The record is what stops a beat
describing a local action ("read three CSVs") instead of a subject; with `projects`, `focus`
and `subjects` in front of it, a beat can say what the action was *for*.

**Beats that say nothing are not stored.** A run of acknowledgement turns produces no change
of subject, and a series padded with near-identical beats would bury the moments that matter.
A beat matching the previous one on significant words is dropped, reusing the comparison that
collapses duplicate insights.

⚠️ **UNEXERCISED, not confirmed.** 70 beats asked, 70 generated, 70 kept, **0 discarded**. The
cadence is not the reason: the largest overlap any real consecutive pair produced was 0.500
against the 0.800 threshold, so the comparator never came within 0.3 of firing. At a 3-user-turn
cadence on real sessions consecutive beats genuinely differ, so this mechanism is **inert on
this corpus** rather than mis-tuned — and its behaviour on the acknowledgement-run case it was
designed for is not established. It cost 70 generations to discard nothing, which is cheap, but
it is unexercised machinery.

**Turning points become measurable without the classification pipeline.** Comparing a new beat
against the accumulated ones detects a change of subject directly. That is the signal the
recency work was blocked on — it was to come from the EWMA focus in the classification
pipeline, which the digest path does not run — and beats supply a usable form of it here.
The EWMA remains better when available; this is not a replacement, it is an unblocking.

⚠️ **REFUTED.** Beats do not supply a usable form of it. `ChangedSubject` was marked on **70 of
70** beats — the signal is stuck at 1, so `SelectBeats`' preference for subject-changing beats
degenerates to "prefer everything" — and it is demonstrably *wrong*, not merely saturated:
session 1's beats [4] and [5] ("feasibility of running smaller, CPU-only LLMs within a strict
RAM budget" and "the memory and performance footprint of a 0.6B Qwen3 model CPU-only") are
plainly the same subject. This is the *second* mechanism to fail this way — `SubjectShifted`
fired on 41 of 42 refinements — and the shared cause is that **thin token overlap does not
distinguish a change of subject from a rewording of one.** Both use the same 0.8 comparison, so
the comparator that cannot see a restatement cannot see continuity either. The unblocking claim
needs a real subject comparison, not a cheaper trigger for the existing one.

**Sampling for the report.** The beat series is dense, so the report samples it: every beat
that changed the subject, the first, the most recent few, and even spacing to fill the cap.
The same rule the ladder used, applied to a denser and cheaper series.

### The paraphrase (`story`) — dropped

Recorded because the reasoning matters: an earlier draft had each report emit a paraphrase of
itself for the next report to read. Beats make it unnecessary and worse. A transcript-derived
beat is cheaper, denser, and not self-referential, while a paraphrase of a report is model
output feeding the next generation of the same kind — the chain this design removes. The
report's own prose has no remaining reason to reach the next generation, so `story` is gone
and the store needs no column for it.

### The beat series — dense, transcript-derived history

A report reads a chronological selection of stored beats. Because beats are cheap they are
dense — every few turns rather than once an hour — so the series has real resolution: an
eight-hour session yields dozens of beats where hourly reports would yield eight.

Selection, capped at **12 entries**:

- **The first** always. It is where the work started, and what lets an account say what the
  current work grew out of. Dropping it is how a session loses its own origin.
- **Every beat that changed the subject**, which is what a trajectory is made of. Evenly
  spaced samples across a long session mostly capture steady progress.
- **The most recent few**, in full (≤`BeatCap` runes each).
- **Even spacing** to fill whatever the cap leaves, so a steady session still shows its shape.
- Beyond the cap, subject-changing beats are preferred over spacing and the oldest middles
  drop first — never the first entry.

At ≤200 runes per beat that is ~2,400 runes worst case against the 4,742 the embedded report
cost, so the series is cheaper than what it replaces while covering the whole session at far
higher resolution than one step of it.

**There is no drift bound to design, because there is no chain.** Every beat is derived
independently from its own window; none reads another. A report reads beats and measurements,
never an earlier report. The earlier draft of this spec needed three mitigations for
compounding paraphrase drift; the two-tier split removes the mechanism instead of bounding it.

### The session record — a minimal measured spine

The ladder and the window are both *narrative*: one paraphrased by a model, one raw
transcript. Neither is authoritative. So the third input is a small structured record that
spans the whole session, is measured rather than written, and is updated when the thing it
describes actually changes.

This splits the context by **truth-status**, which is the property that matters:

| input | origin | authority |
|---|---|---|
| session record | measured, accumulated | authoritative |
| story ladder | model-paraphrased | indicative |
| recent window | raw transcript | evidence |

**Contents — minimal and bounded.** Every list is capped, or it stops being minimal:

- `projects` — repos or working directories touched (cap 5, by recency)
- `focus` — domain, function, and concentration, from the EWMA the classification pipeline
  already maintains
- `activity` — the activity-type mix over the session
- `subjects` — accumulated subject terms, **only those verified verbatim against the
  transcript**, capped at 12 by frequency
- `counts` — session-spanning turns, tool profile, corrections, corrected turns. Not the
  window-scoped counts used today
- `turning_points` — the `seq` and trigger reason of prior digests, so a focus shift is
  recoverable as a fact rather than inferred from prose

**Update policy.** Not regenerated each turn; each field changes only when its input does.

- Deterministic fields (projects, counts, tool profile, turning points) are recomputed from
  the transcript every window. No model, so this is free.
- `focus` follows the EWMA as classifications arrive.
- `subjects` is a union, gated by the existing verbatim check, then evicted to the cap. Union
  plus verification means a term cannot enter by being plausible.

**The change detection and the regeneration trigger are the same signal.** A focus shift that
updates the record is the same event that `TriggerFocusShift` already fires on. They should
not be two mechanisms — the record changing is *why* a refresh is worth spending.

**Storage.** One mutable row per session, not snapshots, with the `seq` of the digest that
last consumed it. Digests are snapshots because their prose is a record; the session record
is current state and is overwritten.

**This fixes a defect rather than only adding a feature.** `DigestFacts` today is
window-scoped, and its `Topics`/`Entities` fields — the intended session-spanning
anchor — come from `WithEnrichment`, which needs a classification pass the digest path never
makes. They have been empty in every measurement taken. The session record is the right home
for that accumulated view.

**Privacy.** Terms are verified substrings of locally-read text, the same exposure the
existing topic extraction has. No raw prose enters the record.

### The consistency gate this buys

With a measured spine, drift becomes machine-detectable instead of needing a human or a
transcript re-read: if the story claims the work is about one thing while `focus`, `projects`
and `subjects` say another, the report contradicts a measurement.

That is a new threshold — story-versus-record consistency — and it is the first currency
check that does not depend on judgement. It does not replace scoring a late paraphrase
against the transcript, because the record is a coarse proxy, but it is cheap enough to run
on every refinement.

⚠️ **REFUTED as shipped (T12).** It is cheap and it does run on every beat, but it is not a
usable check: **15.7% of 70 flagged against a ≤5% threshold, and all 11 flagged beats are
accurate.** It measures a vocabulary mismatch between two extractors, not consistency. Two
independent causes, neither about beats:
>
> 1. `distinctiveTerms` admits ordinary English (`distinctiveToken`'s unconditional 7-character
>    rule passes *aligning*, *ensuring*, *specifically*, *correctly*), so a beat's "subject
>    terms" are largely gerunds and adverbs.
> 2. `SessionRecord.Subjects` is dominated by **tool names and workflow vocabulary**
>    (`WebSearch`, `TaskUpdate`, `SendMessage`, `dispatching`, `confirm`, `because`), because
>    `Observe` reads the whole delta including tool lines and frequency-ranks it, and agentic
>    sessions are overwhelmingly tool traffic.
>
> The two populations never intersect, so "no grounded term" fires on correct beats. Whether it
> would catch a **genuine** contradiction is unmeasured — none occurred. "Independent of
> judgement" was the right ambition and it is not what was built: an automated check that
> reliably flags correct output is worse than judgement, not better than it.

### The specifics half

`Identifiers(prev)` already extracts the previous report's named specifics and hands them
back as a retain-list. That is the "specifically A, B and C" half of the sentence, and it is
what keeps compression from losing concrete anchors: the framing may be paraphrased, the
named things may not disappear.

So the refinement prompt carries:

```
SESSION RECORD (measured — authoritative):
  projects, focus + concentration, activity mix, subjects,
  session-spanning counts, turning points
BEATS, oldest first (each derived from its own window — indicative):
  [1]   where the work started
  [7]   subject changed
  [14]  subject changed
  [22]  most recent
  [23]  most recent
SPECIFICS ALREADY REPORTED: <deterministic list>
WHOLE SESSION, sampled:  coarse view, start to now
NEW PART:                recent window in detail  (evidence)
```

The order is deliberate: the measured record comes first, because everything after it is
either indicative or evidence, and a model shown authoritative counts first has been observed
to hold its prose consistent with them.

`CarryForward` is deleted. Nothing embeds the prior digest's prose.

### What this removes

- **The no-shrink rule.** It contradicts deliberate compression and must go.
- **`CarryForward`** and the priority scheme that decided which sections yield prompt budget.
- **Most of the budget pressure.** The carried report drops from ~4,700 characters to ~600
  plus the retain-list, which is the entire reason the prompt budget went to 11,000 and `ctx`
  to 6144. Both can come back down; measure rather than assume.

  ⚠️ **REFUTED — both went UP.** Measured rather than assumed, as this bullet asked, and the
  answer was the opposite: the prompt budget went **11,000 → 14,000** and `ctx` **6144 → 8192**.
  Removing `CarryForward` did free 3,200–4,400 runes, but the instructional tail (5,673 runes,
  41% of the refine prompt) plus the enforced 1,600-rune window floor consume more than it
  returned. Neither can come back down: the full-corpus probe's tightest real window margin is
  **+20 runes at the full 14,000** across 29 sessions / 1,087 steps, and `ctx` 8192 has roughly
  170 tokens of worst-case headroom once a schema-legal nine-section report is allowed for.
  Note the sweep *alone* would have licensed ~11,100 — its 4 steps per session never reach the
  tight case — so the probe, not the sweep, is the authority on the budget.

## Bounding the cost

Two cadences, because the two operations differ in cost by roughly twenty times.

**Beats are turn-based**: every `KELD_DIGEST_BEAT_TURNS` (default 3) user turns. No wall-clock
floor — that is the point of making them cheap. A beat that says nothing new is discarded
rather than stored, so a burst of acknowledgements costs one short generation each and adds
nothing to the series.

**Reports are wall-clock bounded**, and turn-based triggers alone do not bound them: a burst of activity can satisfy `MaxTurns` repeatedly within minutes. So the policy gains
a **wall-clock floor**, `KELD_DIGEST_MIN_INTERVAL`, default **1 hour**.

- The floor applies to every reason, including finalisation. A session that has stopped is not
  going anywhere, so producing its final account an hour later costs nothing.
- **A suppressed reason is deferred, not dropped.** The strongest reason seen during the
  interval is carried and fires when the floor elapses, so a focus shift ten minutes after a
  digest still becomes the cause of the next one rather than being lost to a later
  `volume` trigger.
- **Deferral needs a timer, not only turn-driven evaluation.** If the trigger is consulted
  only when new turns arrive, a session that goes quiet with a pending reason never fires. The
  periodic sweep must re-evaluate.

**The cheap and expensive paths separate cleanly.** The session record is deterministic and
recomputed every window at no model cost, so the authoritative state is always current. Only
the narrative is rate-limited. A reader looking at a 20-minute-old digest still sees
up-to-date counts, projects and focus beside it.

Rough duty cycle at the defaults, from CPU-only decode measured at 27–35 tok/s for this
model: a nine-section report is on the order of 2,000 output tokens, so one to two minutes of
CPU per hour. A beat is 60–90 tokens, so roughly two to three seconds; at one per three user
turns and the observed p50 of 22 user turns per session, that is about seven beats and well
under a minute of CPU for a whole session. Both figures are derived from earlier measurements
rather than measured for these prompts, and should be confirmed.

The beat cadence is the cheaper way to buy resolution: twenty beats cost about one report and
give twenty points of history instead of one.

## Metric consequences

**T4 must change.** It currently measures whether *prose* survives refinement, which under
this design should legitimately change. It becomes: do the **named specifics** survive
(`RetainedFacts` against the retain-list)? That is what the metric was always trying to
protect; the prose length was an implementation detail standing in for it.

**T11 (synopsis lag) stays** and becomes the primary currency measure. The prediction is that
it improves without an anchor, because the framing is no longer pinned by verbatim prose.
It has to be measured, not assumed — the anchor experiment is exactly the kind of plausible
mechanism that failed.

⚠️ **T11 CANNOT be the primary currency measure, and the prediction is still unmeasured.**
`SynopsisLag` flags only when the synopsis shares **no** distinctive term with the recent
window, and `distinctiveToken` admits any 7+-character word that is not an explicit stopword —
so two ordinary English words are enough to certify a synopsis current. Reproduced: a synopsis
entirely about a ledger reconciliation, measured against a window entirely about dropdown
opacity, returns `recentHits=2` on *"remains"* and *"whether"*. The measured 0.0% in both arms is
therefore near-tautological, and the prediction is neither confirmed nor refuted. The caution in
this paragraph was exactly right and was applied to the wrong risk: the mechanism under
suspicion was the *anchor*, and the one that failed was the *instrument*.

**T4's redefinition, by contrast, worked as intended.** Measuring named specifics against the
retain-list rather than prose length is what made the retention failure legible and localisable
(161 named-and-dropped, 0 cap evictions, 79 already-gone). The metric change is a success even
though what it measures is a failure.

**A story-versus-record consistency threshold becomes possible**, and it is the first currency
check independent of human judgement: a story whose subject contradicts the measured `focus`,
`projects` and `subjects` is wrong against a measurement, not against an opinion.

**A new threshold is needed for fabricated `next`.** Observed in real output: a `next`
inventing schema fields ("tool name, call id, input, output, timestamp") that were never
discussed. T7 only inspects `unresolved`, so nothing catches it.

## What this does not address

- **Usefulness is still unmeasured.** Every threshold here is structural or
  consistency-based. Whether the story reads the way a lead would write it is a judgment
  call needing a reader who did not write the prompts, and this design does not change that.
- **Non-engineering accuracy** remains untested: Cowork is VM-backed and yields no readable
  transcripts, so the only non-technical evidence is hand-authored.
- **Cross-session and per-project rollup** are explicitly out of scope. Worth noting that the
  compression mechanism is the prerequisite: feeding several full session digests into a
  project-level summary would exhaust any budget, while feeding paraphrases would not.

## Risks

**The paraphrase becomes the record by accident.** If any code path regenerates a stored
digest from `story`, the erosion finding applies again in full. The boundary needs a test,
not a convention.

**Compression drifts the framing.** Paraphrasing a paraphrase is telephone. Three things bound
it: the story ladder lets a generation see the session's earlier framings rather than only the
last one, so drift is visible where it would otherwise compound; the retain-list re-anchors
specifics each round from the stored digest, never from a paraphrase; and the coarse session
view is re-derived from the transcript every time. None is a guarantee. Drift should still be
measured directly, by scoring a late paraphrase against the transcript rather than against its
predecessor — a chain of mutually consistent paraphrases can be consistently wrong.

**The session record can go stale in the fields that need a model.** `projects`, counts and
turning points are deterministic and cannot rot. `focus` depends on the EWMA from the
classification pipeline, so on any deployment where classification is unavailable the
authoritative-looking spine is silently partial. It must be explicit about which fields are
populated rather than presenting an absent focus as an empty one — the same failure as
`Topics` reading empty for months because nothing said it needed a pass that never ran.

**The ladder can crowd out the detail it exists to frame.** It competes with the recent window
for the same prompt budget, and the window is what the specific-action grain comes from. If a
long session's ladder grows while the window shrinks, the report loses exactly the concreteness
that makes it worth reading. The 8-entry cap is the bound; whether it is the right one should be
measured, not assumed.

**Coarsening may go too far.** "Work was focused on X, Y, Z" is worth reading only if X, Y
and Z are the real subjects. A paraphrase that reaches for generic vocabulary
("infrastructure improvements", "code quality") is worse than the status board it replaces.
The unverified-identifier gate covers the report but not the paraphrase; it should cover both.
