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

⚠️ **Boundaries are DETECTED WHERE THE BRANCH CHANGES, and cut for length everywhere else.** This
section previously said "detected, not fixed" and treated `budget` as a fallback. Phase 0a measured
that and the proportions are the other way round. Results:
`~/keld/refseries-context/blocks/BLOCK-DETECTION-RESULTS.md`.

`EwmaSizer` (fast 0.3, slow 0.02, threshold 0.2, 60-second observation step) is still promoted from
sizing a slice inside a window to cutting the blocks themselves — nothing beat it and nothing
replaces it. What changed is the claim about what it detects. Scored on the same corpus, the same
detector, the same scoring:

| ground truth | precision | recall |
|---|---|---|
| the BRANCH (or workspace) changed | **86.5%** | **55.8%** |
| ANY published allocation dimension changed | **49.5%** | **7.1%** |
| `FixedSizer(15)`, any-dimension truth | 27.6% | 17.3% |

Those are not in tension. They say something specific and it governs this whole section:
**`EwmaSizer` is a branch-change detector, not a work-change detector.** Against general work
change its recall is 7.1% — *below* a fixed constant's 17.3% — so it misses roughly nineteen work
transitions in twenty.

The 86.5% / 55.8% figure replicates the published 86.4% / 54.8% within a point, on a rebuilt store
with correct session keying and a 28% larger population, so the original measurement was sound on
its own population. The shuffled-truth control is why any of it is believed: relocating every
transition to a random non-empty bin of the same session drops `branch` by 60.9 points, while every
other candidate drops under 20 and `action` scores **-3.5** — better against randomised truth than
real truth, at a 93% firing rate, which is what a volume counter looks like.

**Nothing else detects, and this was tested rather than assumed.** `language`, `output_type`,
`component`, `skill`, `action` and the `branch+language` pair were each scored against ground truth
excluding their own level, against pre-registered bars. All failed. Two near-misses are recorded at
the constant rather than smoothed: `output_type` missed the 20-point shuffle bar by 1.4 points, the
pair by 0.6. The bars were fixed before the run and were not moved. `workspace` has **0**
transitions across 496 sessions, replicated.

### What follows for the reasons, and it is the important part

`start_reason` / `end_reason`, from a closed set, and **always stated**:

| reason | meaning |
|---|---|
| `detected` | the detector fired here. **A claim that the branch changed** — not that the work did. |
| `budget` | no transition found within the maximum duration; cut for length. **Not a claim.** |
| `idle` | activity stopped. See below. |
| `session_start` / `session_end` | the transcript's own edges. |

⚠️ **`budget` is the COMMON boundary, not the exception — including for engineering sessions.** At
7.1% recall against general work change, most real shifts produce no detection, so most blocks end
because they hit their duration cap. The earlier draft of this document worried about
`budget`-everywhere for the non-engineering population; measured, it is the normal case for
everyone. Two consequences:

- **A `budget` edge must be visually and semantically distinct**, and that is now a primary
  requirement rather than a nicety. A consumer that renders the two alike does not merely lose
  nuance — it presents a length cut as a work transition on the majority of blocks.
- **`detected` must not be read as "the work changed here".** It means "the branch changed here",
  which is a narrower and more useful thing to say. Any UI copy, digest line or downstream
  inference that widens it is overstating what was measured.

⚠️ **Do not widen the detection level set hoping for better recall.** Six candidates were tested
and refuted. Improving work-shift detection needs a different mechanism — combining signals rather
than swapping the single level the EWMA reads — and that is its own spec with its own
pre-registration, not a parameter change here.

## Idle

**Idle means no turn of any kind — human OR agent.** A long autonomous run has turns, so it is
never idle: it is cut by `detected` or by `budget`, never skipped. Only a genuine gap in all
activity closes a block without a successor. Coverage is driven by turn presence, not prompt
presence — which is the point, since **only 55.0% of transcript turns fall inside any prompt's
60-minute look-back**, and the gap is worse the more autonomous the agent.

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

## Thin blocks merge forward

A block below the evidence floor cannot attribute anything: `window.MIN_EVIDENCE` is 5, derived
rather than chosen — under the 0.50 share floor read as a null hypothesis, unanimity over *n*
observations has probability `0.5**n`, which first clears 5% at n=5. A 90-second block would
therefore publish with every dimension absent, which is a row a consumer must render and can say
nothing about.

So a block that cannot clear the floor is **merged into its successor**, and the merged block
takes the earlier `start` and the later `end_reason`. Merging forward rather than backward keeps
the operation causal: at the moment a thin block closes, its successor's content is not yet
known, so the merge is deferred until the successor closes — never applied retroactively to a
block already published.

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

Three items remain, bars unchanged and still fixed before their runs:

2. **Maximum block duration** (the `budget` cut). Derived, not chosen: the smallest duration at
   which the share of blocks ending in `budget` stops falling materially. ⚠️ This matters far more
   than when it was written, because `budget` is now the common boundary rather than the fallback —
   this number sets the typical block length for most sessions.
3. **Minimum viable block.** Confirm the merge-forward rule against `MIN_EVIDENCE`: what share of
   blocks merge, and does merging change any published VALUE rather than only its evidence count.
4. **Tiling equality.** A session's blocks must partition their session's activity exactly — every
   turn in exactly one block, no turn in none. Asserted over the corpus, not argued.

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
