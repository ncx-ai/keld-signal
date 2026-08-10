# Session story rollup — design

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
| no recency anchor | 97.4% | 4.1% | 14.3% |
| anchor, wording v1 | 88.3% | 10.2% | **7.1%** |
| anchor, wording v2 | 90.9% | 2.0% | 14.3% |

Every configuration trades currency against durability, because the only two options the
design offers are *keep the prose verbatim* or *let the model rewrite it freely*. It also
forced the prompt budget to 11,000 characters and `ctx` to 6144.

## The design

Two grains, and a compression step between them.

- **Specific actions** — the recent conversation window, in detail. What was just done.
- **The session story** — the accumulated account of the session, coarsening as it grows.

The register to aim for is how an engineering lead or PM describes work: *"the work has been
on X, Y and Z; specifically A, B and C."* Coarse framing, carrying a few concrete instances.

### The invariant: store full, feed compressed

This is what makes re-summarisation safe, and it is not optional.

- **Stored** — the full digest. The record, and what a reader is shown. Never regenerated
  from a compression.
- **Fed to the next generation** — a short paraphrase of it, plus the deterministic list of
  named specifics.

Repeated re-summarisation erodes the most valuable content; that finding stands. It is
acceptable here only because the lossy path is the *prompt*, never the artifact. A digest
is compressed to produce context and is otherwise left alone.

### The paraphrase (`story`)

Each refinement emits a paraphrase of the report it just produced: one short paragraph or a
handful of bullets answering only

- what the work has been about, at the grain of themes rather than actions
- where it now stands
- what it is heading toward

It carries **no** `insights`, `unresolved`, or `structure` prose. Those either live in code
(insight merging), are diffed deterministically (open items), or are cumulative content the
paraphrase has no business rewriting.

It is a `story` field on the refinement schema, alongside `retired` and `closed`, and like
them kept **off** `Digest` because it is machinery rather than report content. Produced in
the same call, so it costs no extra inference, and **persisted with its snapshot** — the
store gains a `story` column, so the paraphrase that was actually used is recoverable
rather than re-derived.

### The story ladder — several paraphrases, not one

Feeding only the latest paraphrase makes each generation able to see exactly one step back.
That is a chain, and it is the telephone failure listed under Risks: drift compounds because
no generation can see far enough to notice it. Understanding the *larger* picture of a
session means consulting condensed digests **sampled across the session**, not just the most
recent one.

So the refinement receives a chronological ladder of stored paraphrases:

- **The first** always. It is where the work started, and it is what lets the account say what
  the current work grew out of. Dropping it is how a session loses its own origin.
- **The most recent, in full** (≤600 runes). The immediate handover.
- **Evenly spaced entries between**, clipped short (≤140 runes each), so the trajectory is
  visible without paying full price for every step.
- Capped at **8 entries total**; beyond that the middles are resampled, not appended.

Dense at the recent end, sparse toward the start — the same shape `SessionDigest` already
uses for turns, for the same reason.

Worst case that is 600 + 7×140 ≈ 1,580 runes, against the 4,742 the embedded digest cost.
The ladder is therefore *cheaper* than what it replaces while covering the whole session
rather than one step of it.

**This is also the drift bound.** Specifics are re-anchored each round from the stored digest
rather than from the previous paraphrase, the coarse session view is re-derived from the
transcript, and now the framing itself is checkable against the session's own earlier
framings. Drift becomes visible to the generation that would otherwise compound it.

### The specifics half

`Identifiers(prev)` already extracts the previous report's named specifics and hands them
back as a retain-list. That is the "specifically A, B and C" half of the sentence, and it is
what keeps compression from losing concrete anchors: the framing may be paraphrased, the
named things may not disappear.

So the refinement prompt carries:

```
THE STORY SO FAR, oldest first:
  [1]  <= 140 runes   where the work started
  [3]  <= 140 runes   sampled
  [5]  <= 140 runes   sampled
  [6]  <= 600 runes   most recent, in full
SPECIFICS ALREADY REPORTED: <deterministic list>
MEASURED CONTEXT:        counts, project, tool profile
WHOLE SESSION, sampled:  coarse view, start to now
NEW PART:                recent window in detail
```

`CarryForward` is deleted. Nothing embeds the prior digest's prose.

### What this removes

- **The no-shrink rule.** It contradicts deliberate compression and must go.
- **`CarryForward`** and the priority scheme that decided which sections yield prompt budget.
- **Most of the budget pressure.** The carried report drops from ~4,700 characters to ~600
  plus the retain-list, which is the entire reason the prompt budget went to 11,000 and `ctx`
  to 6144. Both can come back down; measure rather than assume.

## Metric consequences

**T4 must change.** It currently measures whether *prose* survives refinement, which under
this design should legitimately change. It becomes: do the **named specifics** survive
(`RetainedFacts` against the retain-list)? That is what the metric was always trying to
protect; the prose length was an implementation detail standing in for it.

**T11 (synopsis lag) stays** and becomes the primary currency measure. The prediction is that
it improves without an anchor, because the framing is no longer pinned by verbatim prose.
It has to be measured, not assumed — the anchor experiment is exactly the kind of plausible
mechanism that failed.

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

**The ladder can crowd out the detail it exists to frame.** It competes with the recent window
for the same prompt budget, and the window is what the specific-action grain comes from. If a
long session's ladder grows while the window shrinks, the report loses exactly the concreteness
that makes it worth reading. The 8-entry cap is the bound; whether it is the right one should be
measured, not assumed.

**Coarsening may go too far.** "Work was focused on X, Y, Z" is worth reading only if X, Y
and Z are the real subjects. A paraphrase that reaches for generic vocabulary
("infrastructure improvements", "code quality") is worse than the status board it replaces.
The unverified-identifier gate covers the report but not the paraphrase; it should cover both.
