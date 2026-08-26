# The block model — the contract between Signal and Atlas

The seam. Signal computes every fact below; Atlas consumes it. This document is the only place
both sides agree, so it defines the object and its invariants and nothing about how either side
implements them. Signal's implementation and Atlas's are separate specs — deliberately, because
the Atlas half contains a schema migration whose cost gets underestimated when it is buried
inside a design discussion about work characterisation.

## What changed, and why the unit moves

v1 answered "what is this prompt about" and published one enrichment per prompt, keyed on
`prompt_id`. v2 asks what work the session is doing, treats the transcript as the corpus, and
runs **no classifiers on individual messages**. The look-back window stopped being a way to
characterise the latest prompt and became the thing being characterised.

So the published unit moves with it. A **block** is a stretch of work; the analysis attaches to
the block and, through it, to every message inside it.

Two consequences, both deliberate:
- **Per-message passes survive only where the message IS the right unit.** `sensitivity` stays
  per-prompt: a credential is in one message, and attributing it to a block would both dilute it
  and misreport where it is.
- **v1 ships deterministic-only.** `ml_backend:"deterministic"` becomes the shipping default. The
  six model-backed facets (`task_type`, `domain`, `activity_type`, `personal`, `function_guess`,
  `subcategory`) are out of the initial release; GLiNER2 is never loaded and the ~1.9 GB weight
  fetch never happens. Nothing in this contract depends on a model.

## A block

A contiguous span of one session's activity, cut at change points.

    block := (session, start, end, start_reason, end_reason, facets, covers)

⚠️ **THERE IS NO DETECTOR. A block ends on LENGTH or on SILENCE, and nothing else.** Two earlier
drafts of this section said otherwise — the first that boundaries are detected rather than fixed,
the second that they are detected at branch changes and cut for length everywhere else. Both are
superseded. Results: `~/keld/refseries-context/blocks/BLOCK-BOUND-2-RESULTS.md` and
`BLOCK-BOUND-2-ABLATION.md`.

The rule, in full:

> **A block is at most 20 minutes of activity. Fifteen minutes of silence ends it, and the silence
> belongs to no block.**

Two constants, both measured, neither tunable by a request: `MAX_BLOCK_MINUTES = 20`,
`IDLE_BINS = 3` (three consecutive empty five-minute bins).

**Why the detector went.** `EwmaSizer` WAS the third terminator, and it won a pre-registered
four-arm comparison as part of arm A′. A post-hoc ablation over the same 496-session corpus then
removed it, at the shipped cap:

| | with detector | ablated |
|---|---|---|
| blocks that attribute something | 95.29% | **96.21%** |
| blocks holding any evidence at all | 99.3% | **100.0%** |
| merge rate | 1.36% | **0.67%** |
| longest block | 20m | 20m |

Every measured number improved, and the second row is the one that matters: **the detector was the
only source of empty blocks.** A detected cut ends a block early, so the block holds less evidence
and is likelier to fall under the attribution floor — it was buying its 4.3% of boundaries by
thinning the blocks around them.

What was given up is the claim that those cuts sat in more MEANINGFUL places. That claim is exactly
what Phase 0a could not establish: scored against any-dimension work change, the detector recalled
**7.1%** against a fixed-interval control's **17.3%** — worse than a constant — and every
alternative level failed its pre-registered bar, `action` scoring **−3.5** (better against shuffled
truth than real truth, at a 93% firing rate, which is what a volume counter looks like).

**Two consequences Atlas depends on:**

1. **The unit is now fully domain-agnostic.** Detection was its only branch-dependent part. A
   session with no repository — a designer, an analyst, anyone outside engineering — produces
   blocks by the identical rule, with nothing missing and nothing degraded. There is no
   engineering/non-engineering split to handle.
2. **Every boundary is arithmetic, and none is a claim about the work.** A consumer can no longer
   present any edge as "the work changed here", because nothing in the system asserts that. This
   REMOVES a requirement the previous draft imposed (rendering `detected` distinctly from
   `budget`); it does not soften one.

⚠️ **`EwmaSizer` is NOT deleted from the codebase.** It keeps its separate, measured, shipped use
sizing the dynamics slice. Only the block cutter stopped consulting it.

⚠️ **Do not reintroduce a detector hoping for better boundaries.** Six candidate levels were tested
and refuted, and the one that survived was then measured to make things worse. Better work-shift
detection needs a different mechanism — distributional statistics over the tool-call mix rather
than novelty on a single categorical level — and that is its own spec with its own
pre-registration and an unsolved ground-truth problem, not a parameter change here.

### What follows for the reasons, and it is the important part

`start_reason` / `end_reason`, from a closed set of **four**, and **always stated**:

| reason | meaning |
|---|---|
| `budget` | the 20-minute cap was reached; cut for length. **Not a claim about the work.** |
| `idle` | activity stopped for 15 minutes. A claim there was NO work, not that it changed. |
| `session_start` / `session_end` | the transcript's own edges. |

⚠️ **`detected` is gone from this set.** Anything Atlas built to render it should be removed rather
than left dormant — a reason that can never arrive is a branch nobody will ever see exercised.

⚠️ **`budget` is the plurality boundary** — 48.5% of blocks at the shipped cap, against
`session_end` 33.0% and `idle` 18.5%. The distinction that still matters is `budget` versus `idle`:
one says "we had to cut somewhere", the other says "the person stopped". A consumer that renders
them alike turns a lunch break into a work transition.

## Idle

**Idle means no turn of any kind — human OR agent — for 15 minutes.** A long autonomous run has
turns, so it is never idle: it is cut by `budget`, never skipped. Only a genuine gap in all
activity closes a block. Coverage is driven by turn presence, not prompt presence — which is the
point, since **only 55.0% of transcript turns fall inside any prompt's 60-minute look-back**, and
the gap is worse the more autonomous the agent.

⚠️ **The silence itself belongs to NO BLOCK.** This is the single most consequential thing in this
document for a consumer, and it is what makes the unit work at all. Before idle was modelled, a
plain 20-minute cap attributed **29.8%** of blocks; with it, **95.3%** — because three quarters of
the old blocks were empty tiles laid over overnight and weekend silence. A timeline must therefore
render gaps between blocks as *nothing*, not as an unlabelled block.

The threshold was swept: at caps of 20–30 minutes, attribution holds between 93.4% and 95.4% across
idle thresholds of 10, 15 and 30 minutes. Nothing hinges on the exact value.

## Tiling — the invariant, and its exact limit

⚠️ **Blocks tile WITHIN a session. They do not tile across sessions, and cannot.**

Within one session blocks are contiguous, non-overlapping and gapless except at `idle`. That is
required: a block is the unit messages are attributed to, so overlapping blocks would put a
message in several with conflicting answers. This is a real change from the v1 window, which
overlapped ~12× on purpose (60-minute span, 50-minute stride — measured to halve the distance
from a real transition to a window edge, 22 min → 12 min). Correct for characterising a prompt;
incoherent as an attribution unit.

Across sessions there is no relationship whatsoever. A block is cut from one transcript's own
change detection, so two users' blocks overlap arbitrarily — one person's 09:30–10:15 sits inside
another's 09:12–10:04 and neither knows the other exists. **There is no global timeline.**

Two things follow that a consumer must not get wrong:
- **A block never spans people.** A session belongs to one actor, so a repo's time splits cleanly
  by person with no apportionment and no double-counting.
- **Adjacency in a merged, multi-session list implies nothing.** Any presentation that groups by
  block must sort by block; grouping headers over a time-ordered multi-session feed assert an
  ownership that is false.

## Thin blocks are published UNATTRIBUTED — there is no merge rule

⚠️ **This section previously specified merging forward. Do not build that.** It was measured and it
is harmful.

A block below the evidence floor cannot attribute anything: `window.MIN_EVIDENCE` is 5, derived
rather than chosen — under the 0.50 share floor read as a null hypothesis, unanimity over *n*
observations has probability `0.5**n`, which first clears 5% at n=5.

The bar for the whole bound comparison was written as *"the merge rule becomes unnecessary at ≥ 95%
attributable"*, and the shipped rule clears it at **96.2%**, with a residual thin population of
**0.67%**. Round 1 also measured what merging costs when it does fire: it changes a published VALUE
**88.6%** of the time, against a pre-registered 5% bar. Folding a thin block into its neighbour does
not fill in a blank — it silently changes the neighbour's answer.

So a thin block **publishes as itself, with its dimensions absent**. That is an honest blank, and
the alternative was measured to be worse. Atlas renders such a row as a block with a span and no
characterisation; it must not hide it, and it must not attach the neighbour's facets to it.

## `covers` — the mapping Atlas cannot compute

Atlas groups activity under the preceding user prompt. Signal's unit is a block. Neither contains
the other: a long autonomous episode spans many blocks; a rapid back-and-forth puts many episodes
in one block.

**That mismatch is information, not something to flatten.** An episode spanning four blocks means
the agent's work changed character mid-run — exactly what the detector detects — and a sequence of
blocks is strictly more informative than one label over the whole run. Many episodes in one block
means those prompts were all the same work.

⚠️ **Only Signal can compute this mapping.** The daemon owns the human-prompt filter
(`watch/filter.go`); the store's `prompt` index holds every user- AND assistant-shaped turn — ~260
rows for one real session's 14 human prompts — so anything planning against the store alone
swallows the session. Nothing downstream can tell a human prompt from an agent turn.

So the block carries it:

    covers: [{prompt_id, from, to, complete}]

The episode segments inside this block: the anchoring human prompt, the portion of its episode
falling in this block, and whether the episode ended here or continues into the next. An episode
view collects the blocks naming its `prompt_id`; a timeline view walks blocks. `complete: false`
is what lets a UI show continuation rather than implying the work stopped.

## The published rows

**`WindowEnrichment` becomes the primary row.** It already exists (`publish/window.go`) and is
already the right shape — a window row with no text facets — built as an inert secondary under
`KELD_TICK`. It carries the block: span, boundary reasons, `covers`, and the deterministic facets
(8 allocation dimensions including `repo`, all 9 inventories, `inventory_omitted`, `dynamics`,
`prior`, `effort`).

**The per-prompt `Enrichment` shrinks to `sensitivity`** plus correlation and the pipeline
metadata. Everything else it carried today moves to the block.

**The tick becomes the primary trigger.** Its frontier rule is unchanged and is what makes this
safe: never emit above `min(watermark, now - span)`. Time `t` below that can only be covered by a
prompt in `(t, t+span]`, all of which have arrived, so the covered set there is final and an
emitted block can never later overlap a new one. Not a margin — exact, and replayed against
randomised incremental prompt streams. Coverage goes **55.0% → 99.5%**.

## What Atlas has to change

Named here because the contract is unusable without it; specified in the Atlas document.
The chosen presentation is a **block rail plus a detail pane** — grouping and ordering agree by
construction there, since the rail sorts by session.

- `uq_enrichment_corr (org_id, source_id, corr_scheme, corr_id)` allows one row per correlation
  id and upserts `ON CONFLICT DO UPDATE`. A block row needs its own key.
- `corr_scheme == "prompt_id"` is hardcoded in 9+ read paths. A block row is invisible
  product-wide until each is touched.
- There is no temporal join anywhere — the predicate is string equality on
  `corr_id == prompt_id`. A block join needs `event_ts BETWEEN start AND end`, which is *cheaper*
  (`tool_events` is range-partitioned on `event_ts`) and does not exist.
- `Enrichment` has no time bounds, only the point `ts` and `received_at`.
- `corr_session_id` is written on every row and never read, joined or indexed — the natural hook
  is half-built and inert.

## Measurement status

**Item 1 is DONE and came back NULL.** Full results:
`~/keld/refseries-context/blocks/BLOCK-DETECTION-RESULTS.md`; harness
`scripts/block_detect_eval.py` (13 tests); pre-registration in the same directory, written before
the run.

`skill`, `component`, `output_type`, `language`, `action` and `branch+language` were each scored
against ground truth excluding their own level. **None passed.** `branch` stays as the sole
detection level, and the finding that matters is recorded in the boundary section above: the
detector detects branch change, not work change, so `budget` is the common boundary.

⚠️ The earlier draft of this section said the non-engineering population "would return every
boundary as `budget`" as though that were the bad case to avoid. Measured, it is the normal case
for every population. The problem is not that some sessions lack `branch` — it is that branch
change is a narrow proxy for work change.

**Items 2-4 are now DONE too.** Results: `BLOCK-BOUND-2-RESULTS.md`, `BLOCK-BOUND-2-ABLATION.md`;
pre-registration `BLOCK-BOUND-2-PREREGISTRATION.md` with four amendments, all written before their
runs; harness `scripts/block_sizing_eval.py`.

2. **Maximum block duration — 20 minutes.** ⚠️ The rule this document proposed for deriving it
   ("the smallest duration at which the `budget` share stops falling materially") was **retired**:
   the curve declines shallowly and monotonically across the whole 10-120 minute range, so that
   rule fires on the first pair it sees and returns a number by proving there is no elbow. Replaced
   by a four-arm comparison against pre-registered bars, won by a plain cap plus idle. Usable range
   15-45 min; at 120 the cap stops bounding anything (50.1% of blocks become whole sessions).
3. **Minimum viable block — the merge rule is NOT BUILT.** See the section above: the 95% bar was
   *defined* as the point at which merging becomes unnecessary, the shipped rule clears it at
   96.2%, and merging was measured to change a published value 88.6% of the time it fires.
4. **Tiling — done, and the invariant is narrower than this document assumed.** Blocks tile the
   ACTIVE part of a session, not the whole span, because idle time belongs to no block. The
   assertion is "every active bin lies in exactly one block", pinned as a test rather than
   measured.

⚠️ **A fifth thing was learned that no item asked for**, and it is the largest single effect in the
whole study: the first round's arms did not implement the idle terminator at all, and a plain
20-minute cap without it attributed **29.8%** of blocks — because 74.6% of its blocks were empty
tiles over silence. Idle is not a refinement of the bound; it is most of the bound.

### The corpus itself had to be rebuilt, and that is worth knowing

Phase 0a's replication check discovered that the frozen store every prior store-derived study used
held **55 of 500 transcripts** — `evidence_floor.transcripts()` keeps only unique session keys, and
under the pre-`bbb74b4` scheme (`basename(path)[:8]`) all 445 `agent-<hash>.jsonl` subagent
transcripts collided and were dropped. Not merged evidence: a silently narrow, non-randomly
selected sample. Rebuilt with `sha256(abspath)[:16]`: 500 transcripts, 496 sessions, 681,857
events, 30s.

The published sizer figure replicates on the rebuilt store within a point, so that measurement was
sound on its own population. **`DYNAMICS-VALUE.md`, which chose `DROPPED_DIMENSIONS` in shipped
code, was computed on the same 55-transcript store and is being re-run.** No dimension stays
dropped on 11%-of-corpus evidence.

## Out of scope

- Cross-machine or org-wide baselines. The store is one machine's; an org baseline is Atlas's.
- The salience block (`docs/superpowers/specs/2026-08-25-repo-history-baseline-design.md`). It
  attaches to a block when it ships and needs nothing here.
- Any routing decision. The block is an input to one; naming a model per reading is its own spec.
