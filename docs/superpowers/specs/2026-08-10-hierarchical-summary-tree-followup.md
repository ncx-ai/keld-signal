# Follow-up: a tree of session summaries

**Status: not scheduled.** A follow-up to
`2026-08-10-session-story-rollup-design.md`, recorded while that design is being
implemented. Nothing here changes the work in flight.

## The idea

Store activity summaries in a tree rather than a flat series. Leaves are beats. A parent
states the main points of its children collectively. Parents have parents, and the root is
the whole session.

## Why it is better than what is being built

The current design samples. `SelectBeats` keeps 12 entries — the first, the subject changes,
the most recent, then even spacing — and **discards the rest**. On a session with 100 beats
that means 88 beats of history are simply not represented, and the omission is silent.

A tree compresses instead of discarding. With a branching factor of 8, those 100 beats become
13 parents and one root; a report reading the 13 parents has *all* 100 beats represented,
coarsely, rather than 12 verbatim and 88 gone. Same prompt budget, no silent loss.

That distinction gets sharper the longer a session runs, which is exactly where the flat
design is weakest — and it is the same weakness measured earlier in this study, where a
synopsis lagged forty windows behind the work because nothing carried the middle.

## It does not reintroduce the chain, and the reason matters

The rollup design removed a chain: a report paraphrasing itself for the next report, where
each generation reads the previous one's output and drift compounds with no floor. A tree
looks superficially similar — a parent reads model output — so the distinction has to be
explicit or someone will collapse them later:

- **Telephone** re-summarises *the same evolving text* repeatedly. Generation N reads
  generation N−1's summary of the same material. Error accumulates without bound because
  nothing is ever re-grounded.
- **A tree** summarises a *fixed, complete set of children exactly once*. A node is computed
  when its children are complete, and then frozen. It is never re-derived, so there is no
  iteration for error to accumulate across.

Depth is bounded and small — 100 beats at branching 8 is depth 3 — so a root is three
compressions from evidence, not a hundred.

## The property this buys: every node has a verifiable source span

Beats carry contiguous ordinals, so any node covers a known contiguous range of the
transcript. That makes a claim at *any* level checkable against the text it summarises.

The flat design cannot do this. A whole-session synopsis has the entire session as its
source, which is too coarse to verify against, which is why the only currency check available
was comparing a synopsis against the measured record — a proxy. In a tree, a level-1 node
covering beats 9–16 is verifiable against precisely those turns, by the same
`UnverifiedIdentifiers` gate already used on reports. **Verification becomes possible at every
level of abstraction rather than only at the leaves.**

## Cost

Parents cost inference, but cheap inference: a parent reads only its children, which are
short by construction. At branching 8 over 100 beats that is 14 extra calls of roughly beat
size — on the order of one report. The root is recomputed rarely, since it only changes when a
whole subtree completes.

## What it generalises to

This is the mechanism the earlier cross-session and per-project rollup was deferred for.
Sessions become children of a project node; the same freeze-once rule and the same
span-verification apply, with the span being a set of sessions rather than a range of turns.
Worth noting because it means the tree is not a session-only idea — it is the shape that makes
the deferred tier affordable.

## Open questions, none blocking

- **Branching factor.** 8 is a guess. It trades node count against how much each parent must
  compress, and it should be measured rather than chosen — the same mistake as reusing an
  insight threshold for beats.
- **Incremental versus rebuild.** When a late beat arrives, does it start a new sibling or
  force its parent to be recomputed? Freeze-once argues for the former, but then the last
  parent is always partial, and a report needs to know that.
- **Whether a report should read parents, leaves, or both.** Probably both: coarse parents for
  the arc, recent leaves in full for the present. That is the same dense-recent, sparse-old
  shape already used for turns and beats, which suggests it is the right instinct rather than a
  coincidence.
- **Whether the root replaces the synopsis section.** If a root node already states what the
  session is about, the report's `synopsis` may be redundant — or the root may simply *be* it,
  computed once rather than rewritten on every refinement. That would retire the lag problem
  instead of mitigating it.
