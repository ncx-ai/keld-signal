# The production beat: observed events, one inference, disjoint windows

**Goal, in the project owner's words:** *a developer using Claude Code should be able to look
back and see what was going on at each point in the chat transcript* — with references to the
product, repo, project and other specifics relevant to their company or endeavour.

This supersedes the fused-prompt beat and both arms of the split experiment. It is the first
design on this branch derived from blind measurement rather than from reasoning about the
material.

## What the measurement licenses

A blind per-beat round (46 packets, 10 planted defects all caught, 0 of 230 unevidenced
verdicts) and a blind series round (16 packets, 19 of 20 plants located, 0 of 160 unevidenced,
0 of 6 self-inconsistency) established:

| | Result | Consequence here |
|---|---|---|
| `legible_to_a_manager` | 0 of 36 failed | naming the work already works — keep it |
| `domain_neutral_specificity` | 0 of 36 failed | specifics come from the window — keep |
| `specifics_present` (series) | 0 of 6 failed | **no entity extraction is needed** |
| `followable` (series) | 1 of 6 failed | timelines survive individual bad beats |
| `not_rubberstamping` | **22 of 36 failed** | the one dominant defect |
| `continuous` (series) | 6 of 6 failed | measured on **stale geometry** — see Risks |

The dominant defect has a mechanism, not just a rate. The prompt asks one fused question —
*"what you are working on, and where it has got to"* — so every firing demands a progress claim
whether or not the window supports one. Corroborating: 4 beats were lost with all five ladder
attempts rejected for claiming unobservable progress, i.e. the model has no way to say *this
window does not show how far along this is*.

`beat_progress.go` reported zero such claims because it exempts a **named** subject reaching a
completion word — its docstring says so deliberately — which is the shape of every span the
judges flagged. The check was sound on its own terms and structurally blind to the defect.

## The design

**One inference per beat.** The split's compose pass existed only to keep a prose writer blind
to the window; with a bulleted output there is nothing to compose, so it collapses. The entity
pass is deleted outright — it typed ordinary English (`arrives`, `individual`, `practitioner` as
`person`; `signing`, `latency` as `tool`) and used its `noise` escape hatch 1 time in 400.

**Output shape:**

```
subject:  one line naming what is being worked on
events:   - one observed occurrence per bullet
          - …
```

**No status or progress field exists.** Completion appears only as an observed event — `committed
and pushed, 15/15 tests passing` — which is legitimate because it happened in the window. What is
removed is the invitation to characterise the job as a whole.

**Why bullets rather than prose.** Prose invites closure: a truncated story wants an ending, and
that pull is what produces *"and the reconciliation is complete."* A list has no ending to write.
An honest thin answer also *looks* normal as a list — `- depreciation task assigned, register
named as fa-register.csv` — where in prose the same content reads as a failure to answer.

**Model the empty answer; do not prohibit the false one.** The prompt carries a worked example of
a window in which nothing completed. It carries **no** forbidden-phrase list: when the
stock-opener rule was reworded to name forbidden phrasings, those openings went 2 → 4. Prompts
summon what they name.

## The corpus rule

Three requirements, and each of them was violated by the first production sweep before it was
written down here.

**1. The worked examples come from real transcripts that are held out of the eval corpus.** The
first set was invented, and two of the three were invented out of `finance-close` — a
hand-authored session that was itself being scored. `fa-register.csv` stood in the instructions
and in that session's window at the same time, so a quarter of the material was judged after the
model had been shown something very close to its answer, and the anchoring guard was inert on
those beats: a term copied out of the instructions occurs in the window too, and anchors exactly
as if it had been read from the evidence. The prompt's own rule — *"Nothing in these instructions
is subject matter"* — exists to prevent this and cannot enforce itself.

The hold-out is **checked mechanically, not intended** (`TestBeatExamplesAreHeldOut`): no
example's subject line, and no strong identifier or capitalised term any example uses, may occur
anywhere in any eval session's window or measured record — real and synthetic sessions alike.
Ordinary English is deliberately outside the check; requiring "written" or "picked" to be absent
from a twelve-session corpus is impossible, and measures on this branch that reached for ordinary
English have all measured English. What contaminates a run is a **name** shared between the
instructions and the material, because a name is what the anchoring guard would then accept.

The hold-out earns something beyond hygiene: it makes **instruction copying measurable**. Because
no example name occurs in any window, a beat carrying one cannot have read it. The first
rebalanced sweep found three such beats — near-verbatim copies of the worked examples, on windows
whose subject matter is itself prompt work — which the per-entry anchoring guard passed, and could
only pass, since a copied entry still carries ordinary words the window contains. Against an
example drawn from the corpus that beat would have read as correctly anchored. The artifact now
counts it.

**2. The eval corpus is real-majority.** The first sweep ran four sessions, two of them
hand-authored, so its headline figures rested substantially on short, clean, invented material.
The difficulty is in the real transcripts — long tool outputs, pasted code, interruptions,
corrections, a user redirecting mid-task — and a figure averaged over both populations describes
neither. Twelve real transcripts is the floor. Session **id** is not identity, either: two of the first
twelve selected turned out to be the same conversation under two ids (byte-identical windows and
records), so a later sweep should dedupe on window content.

**3. Synthetic sessions are a labelled minority check.** `finance-close` and `marketing-launch`
stay, because the requirement is that this works for accountants and marketers as well as for
codegen and the pinned snapshot holds only Claude Code transcripts. They are marked SYNTHETIC on
the session record, in the artifact header and in every tally, and **every figure is reported
three times: overall, real, synthetic.** They are a control read against the real majority, never
half the evidence.

One consequence is worth stating rather than hiding: with the examples now drawn from the only
material there is, **all three are engineering sessions**. Whether a non-technical session still
gets a legible beat is therefore something the run *measures* rather than something the prompt
steers, which is what the synthetic pair is now for.

## The guard: verbatim anchoring

Every bullet must contain at least one term appearing **verbatim in its own window or in the
measured record**. This is substring presence — a fact, not a heuristic encoding a judgement, and
it is the standard the project owner set: reliable, not string matching that pretends to judge.

A bullet that fails is **dropped, and the drop is marked** (`AGENTS.md`: dropping must be
visible). The beat is not failed — one unanchored bullet is not a reason to lose the rest.

This replaces `beat_progress.go`'s named-subject carve-out, which is the hole that let the defect
read as zero.

## Windows: disjoint, contiguous, holes marked

Stride equals window. No overlap. Three reasons:

1. **Overlap's justification is retired.** It existed to give `ChangedSubject` shared ground;
   that check fired on 41 of 42 refinements and then flagged 0 of 46 packets, because it measured
   window adjacency rather than subject change. Nothing in this design compares a beat to its
   predecessor.
2. **Overlap costs coverage, our one measured deficit.** 23.7% of each window is currently
   re-read. Coverage sits at 56% of turns (7,926 of 14,154), with 6,228 turns read by no beat.
   Reclaiming that quarter is the cheapest available improvement.
3. **Duplication produces restatement and the suppression is inert.** `beatsRestate` has
   discarded 0 of 70.

Holes remain **marked** in the window, never silent. Full coverage is refuted at ctx 8192: the
largest uncovered stride is 52,148 runes ≈ 14,200 tokens. Reaching 100% requires firing an extra
beat whenever a stride hits the bound — **285 beats instead of 137, 2.1× the inference** — and is
deliberately not taken. Marked holes tell a reader where not to trust the timeline.

Disjoint windows also make the timeline trivially indexable: each beat owns turns N through M,
with no ambiguity about which beat reports an event.

## Prerequisite

`subjectTokens` splits on commas, so `1,400.00` is stored as `400.00` in the block labelled
**measured — authoritative**. Six such fragments hold slots in the finance session. Any beat
anchoring against the record can inherit it. This lands before the first production sweep.

Two sibling leaks are recorded but not fixed here: the `omitted`-turns notice reaching `Subjects`
exactly as `assistant` did, and the collapsed-tool-run marker `x100` bypassing the DF gate via
`strongIdentifier`'s digit rule.

## What the first review round must report

- **Every count split real versus synthetic** — beats asked, generated, failed, and the
  **attempt-count distribution** rather than a median. First-attempt-every-time is the figure
  most likely to move on messier material, and a spread cannot show the one beat that took five.
- **Entries lost to the beat cap, shown.** The cap drops whole trailing entries and marks the
  drop, so the loss is visible but it is still content loss and must be counted, not summarised.
- **Seam misreads** — a bullet anchored only to the record and not to its own window is the
  signature of an event whose antecedent fell on the other side of a boundary. This is the
  measurable cost of dropping overlap. If it is low, disjoint is simply better; if high, the fix
  is a small marked context prefix, not a general overlap.
- Turn coverage in counts, with and without the hole markers.
- Bullets dropped by the anchoring guard, with the bullets shown.
- Series-level `continuous` and `followable`, blind, against a fresh timeline round.
- Generation failures with attempt counts, shown not summarised.

## Risks and what is not established

**The continuity failure was measured on geometry that has since been replaced.** Contiguous
windows landed at `66042cc`, but two beat-generating harnesses still passed the old `K=12`
classification window, and one of them produced the timelines the series round judged. So 6-of-6
`continuous` bears on the *previous* geometry. The series-level effect of this design is
**unmeasured**, and a fresh timeline round is the only way to know.

**Three timelines.** Every series figure rests on three real sessions, each appearing four to six
times. Direction is clear; magnitudes are not established.

**Non-engineering coverage is synthetic, and now so are the examples' blind spots.** The only
non-engineering material available is the two hand-authored sessions, and they are now a labelled
minority (see The corpus rule) rather than half the evidence. The worked examples are all
engineering, since they must come from real held-out transcripts and the snapshot holds nothing
else, so nothing in the prompt steers a non-technical answer any more. That is the honest
position, and it makes the synthetic pair a measurement rather than a decoration — but two
sessions is two sessions, and a domain-neutrality claim cannot rest on them.

**The first production sweep is retracted as a baseline.** Its examples were contaminated and its
corpus was half synthetic, so its figures — including `19 of 19 on the first attempt` and `0 of 69
anchoring firings` — describe neither population and are not comparable with anything measured
after this section was written.

**Report-tier prompt headroom is 4 runes** (13,996 of 14,000). Nothing here widens it, but any
future change to the beat ladder lands there.

## Tasks

1. **Comma split** — `subjectTokens` must not fracture a number on its thousands separator.
   Revert-and-fail test; name the other subject routes that share `subjectTokenRune`.
2. **Disjoint windows** — stride equals window in the beat windower; holes still marked and
   charged to the budget (the hole marker was previously written into the window and charged to
   nobody, putting five real corpus windows over `BeatWindowChars`). Report coverage in counts.
3. **The beat pass** — new prompt and schema (`subject`, `events`), the worked empty example, no
   forbidden-phrase list, temperature ladder on retries. Delete the entity and compose passes.
4. **Verbatim anchoring** — per-bullet check against window ∪ record, offending bullet dropped
   with a visible marker, count exposed to the sweep.
5. **Retire** `beat_progress.go`'s named-subject rule as the honesty check, and stop treating
   `ChangedSubject` / `beatsRestate` as signals. Keep the fact-class measures.
6. **Generate and review** — both a per-beat and a fresh series round, blind, with the
   calibration sets carried over.
