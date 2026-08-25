# Signal's side of the block model

Implements `2026-08-25-block-model-contract-design.md`. That document defines the object; this one
says what changes in this repo to produce it. Atlas's side is separate
(`2026-08-25-atlas-block-activity-design.md`, Direction C — a block rail plus a detail pane).

Most of what follows is **wiring something already measured**, not inventing. The detector, the
frontier rule, the window row and the tick all exist; they are pointed at the wrong unit.

## ⚠️ Read this first: flipping the default empties Atlas's Context column

`ml_backend:"deterministic"` becoming the default is not a Signal-local change. Measured just now
against a nil `Model`, the skipped set is:

    skipped = [task_type, domain_entities, activity_type, personal, function_guess, subcategory]
    degraded = [sensitivity]

Atlas's activity feed renders exactly three enrichment facets in its Context column —
`function_guess` as **Work**, `subcategory` as **Kind**, `activity_type` as **Activity**. All
three are in that skipped set. **Flip the default before Atlas renders block facets and the
column goes blank on every row.**

So the default flip is the LAST step here, gated on Atlas, and it is called out in Phase 5 rather
than bundled with the pipeline work.

### One of those three can be saved for the cost of a line

`funcGuessExtractor.Run` (`enrich/a4_compositional.go`) returns `{"function_guess": "eng"}`
**structurally** for `claude_code`/`codex`/`gemini_cli` and never touches `ctx.Model`; the
`classifyPass` branch runs only for non-coding tools. It is skipped in deterministic mode purely
because it does not declare the `modelFreeExtractor` capability, so `runStage` drops it before
`Run` is called.

Declare `ModelFree() bool { return true }` on it and have `Run` return an absent value — not an
error — when `ctx.Model == nil` and the tool is not a coding tool. Coding sources keep their
`Work` pill through the transition; non-coding sources report it absent, honestly. This is a
one-file change and it should land early, independent of everything else.

## Phase 0 — MEASURED, 2026-08-25

Ran. Full results in `~/keld/refseries-context/blocks/` (`BLOCK-BOUND-RESULTS.md`,
`BLOCK-BOUND-2-RESULTS.md`); pre-registrations beside them. Summary, because it changed this spec:

**0a. Which levels detect — a pre-registered NULL.** Nothing beat `branch`. On wide ground truth
`branch` itself fails rule 1 (recall 7.1 vs the fixed control's 17.3); `language` 12.9,
`output_type` 18.6, `component` 7.7 and `skill` 17.7 all missed the 20-point shuffle bar, and
`action` scored **−3.5** — better on shuffled truth than on real truth — while firing on 93% of
windows. **`EwmaSizer` is a branch-change detector, not a work-change detector.** Detection stays
`branch`-only and fires on just **32 of 496 sessions**, so on most sessions the bound and the idle
rule decide every boundary. That is the stated, expected outcome, not a defect.

**0b. Maximum block duration — 20 minutes.** ⚠️ The rule this spec proposed ("the smallest cap at
which the `budget`-ended share stops falling materially") was **retired**: the curve declines
shallowly and monotonically (99.3% → 91.4% across 10–120 min), so the rule fires on the first pair
it sees and returns a number by proving there is no elbow. Replaced by a four-arm comparison
against pre-registered bars. **Arm A′ — a plain time cap plus the idle terminator — ships at
n = 20**: 95.3% of blocks attribute something, longest block 0.33 h, 25.7% whole-session, 3.12
blocks per session. Its usable range is 15–45 min; at 120 min its whole-session share reaches 50.1%
and the cap stops bounding anything.

**0b′. The idle terminator, which this spec had left unvalued — 15 minutes.** ⚠️ It is also the
single most consequential finding: the first round's arms did not implement idle at all, and
**arm A without it scored 22.2% only because 74.6% of its blocks were empty tiles over silence**.
Adding it moves the same cap from 29.8% → 95.3% at n = 20. Idle is **≥ 3 consecutive empty
5-minute bins**; swept at 10/15/30 min, attribution holds at 93.4–95.4% across all three, so the
conclusion does not rest on the value.

**0c. The merge rule — NOT BUILT.** The bar was written as "the merge rule becomes unnecessary at
≥ 95% attributable", and A′@20 clears it at 95.3% with a **1.4%** merge rate. Round 1 also measured
that when merging does fire it changes a published VALUE **88.6%** of the time, against a 5% bar.
So a thin block **publishes as unattributed** rather than being folded into a neighbour and
silently changing that neighbour's answer. **Do not implement the merge rule**; the 4.7% of blocks
that cannot attribute are an honest blank, and the alternative was measured to be worse.

**0d. Tiling — over the ACTIVE span, not the whole span.** Idle time belongs to **no block**, which
is the whole point of the terminator. The invariant is therefore: blocks are ordered and disjoint,
and **every active bin lies in exactly one block**. It is a test, not a measurement.

**What no arm could establish.** C′ (turns) was UNMATCHED at every setting — its blocks are far
shorter than any A′ candidate, so no same-size comparison existed. Its raw attribution is the
corpus best (96.2%). Judging turns against minutes needs a control pairing on something other than
duration, which this design does not have. Recorded as a gap, not as a result about turns.

## Phase 1 — cut blocks (sidecar)

New module `sidecar/app/analysis/blocks.py`. Pure functions over stored rows, no I/O, mirroring
`dynamics.py`/`prior.py`. The reference implementation is `scripts/block_sizing_eval.py`'s
`with_idle(bound_time)` — the arm that shipped — and the port must reproduce it.

    cut(store, session, from_ts, to_ts, max_minutes=MAX_BLOCK_MINUTES,
        idle_bins=IDLE_BINS) -> [Block]

    MAX_BLOCK_MINUTES = 20   # measured, 0b
    IDLE_BINS = 3            # 15 minutes, measured, 0b′

- Detection reuses `dynamics.EwmaSizer` unchanged, taking **every** rising edge of `fast - slow`
  rather than only the last. That is the one behavioural change: the sizer currently returns ONE
  cut because it is sizing a slice; blocks need all of them across a span. `fire_indices` already
  computes them, so nothing about the detector is reimplemented and what ships is what was
  measured.
- **Idle splits the span into active segments first**, and the cap and detection run *within* each
  segment. Dead air is in no block.
- Emits blocks with `start_reason`/`end_reason` from a closed set:
  `session_start` / `detected` / `idle` / `budget` / `session_end`.
- **No merge rule** (0c). A block below `MIN_EVIDENCE` publishes unattributed.

⚠️ **One consequence of idle to know:** a detected cut falling inside a silent gap is consumed with
the gap — 66 detected ends against 67 without idle. Correct, since nothing changed during silence,
but it means detected-cut counts are no longer identical across bounds, and that identity was the
strongest evidence the tiling held.

**What this retires.** `coverage.py` exists because v1 windows were prompt-anchored and left gaps:
`covered(prompt_ts, span)` computes which regions a prompt's look-back reached and `gaps()` finds
the rest. Blocks cover all **activity**, so both retire along with `plan()`'s fixed-span windows.
(They cover all activity, not all time — the gaps that remain are precisely the silence, which by
construction has nothing to characterise.)

⚠️ **`frontier()` and `tail_closed()` SURVIVE UNCHANGED and are load-bearing.** Never emit above
`min(watermark, now - span)`: time `t` below that can only be covered by a prompt in `(t, t+span]`,
all of which have arrived, so the covered set there is final and an emitted block can never later
overlap a new one. Exact, not a margin, and replayed against randomised incremental prompt
streams. Deleting it because "blocks tile anyway" would reintroduce double-publish on every
in-flight session.

## Phase 2 — `covers`

The episode mapping, computed where the human-prompt filter lives.

The daemon names the human prompt ids (`watch/filter.go`); the store times them. That split is
already how the tick plans — the store's `prompt` index holds every user- AND assistant-shaped
turn (~260 rows for one session's 14 human prompts), so planning against it swallows the session.
Same division here: the daemon supplies the ids, `blocks.py` intersects them with block spans and
emits `[{prompt_id, from, to, complete}]`.

`complete: false` when the episode continues past the block's end. That is what lets a consumer
render continuation instead of implying the work stopped.

## Phase 3 — the wire (Go)

- `enrich.WindowCharacterisation` gains the span, both boundary reasons and `covers`.
  `WindowRef` already carries start/end; the reasons and `covers` are new.
- `publish.WindowEnrichment` gains the same, and becomes the row that carries the deterministic
  facet set. It already carries all of it — 8 allocation dimensions including `repo`, 9
  inventories, `inventory_omitted`, `dynamics`, `prior`, `effort`.
- `publish.Enrichment` **shrinks to `sensitivity`** plus correlation, `prompt_chars`, and the
  pipeline metadata. Everything else moves to the block row.
- Correlation: the block row keeps its own scheme rather than borrowing a prompt's. Atlas keys
  enrichments `UNIQUE(org_id, source_id, corr_scheme, corr_id)` and upserts
  `ON CONFLICT DO UPDATE` over every column, so a block row under `prompt_id` would **overwrite**
  the anchor prompt's row rather than dedup against it.
- `SchemaVersion` bump; sidecar `SCHEMA` bump.
- The exhaustive wire-shape allowlist in `publish/window_test.go` and its `sampleWindow()` fixture
  both need the new keys, or the check passes vacuously.

## Phase 4 — the tick becomes the trigger

`daemon/tick.go` already has the shape: `tickState.observe/targets/advance/save`, `runTicker`,
`tickOnce`, a per-transcript monotonic cursor in `~/.keld/state/tick.json`, forward-only on first
sight. What changes is that it drives the primary path rather than filling gaps, and its interval
is latency rather than coverage — measured at 99.5% coverage identically at 5/10/20/60 minutes, so
the 10-minute default stands.

`KELD_TICK` flips on. **Nothing safety-relevant waits for a tick:** `sensitivity` keeps its
per-prompt trigger, and this path reads no prompt text at all.

## Phase 5 — the default flip, gated on Atlas

Last. `ml_backend:"deterministic"` becomes the default only once Atlas renders block facets, for
the reason at the top of this document. Consequences, all wanted: GLiNER2 never loads, the ~1.9 GB
weight fetch never happens, and `TestBuiltInPipelineStillDemandsAModel` — which pins the default
pipeline at 5 inferences per prompt — is rewritten to pin the opposite.

## What gets deleted

Stated because deletions are where regressions hide:

- `coverage.covered`, `coverage.gaps`, `coverage.plan`'s fixed-span windowing. **Not**
  `coverage.frontier` / `tail_closed`.
- The per-prompt window facets on `publish.Enrichment` — the fields, not the computation, which
  moves.
- Nothing in `dynamics.py`. The slice/baseline comparison inside a block is still wanted; only the
  sizer's second use (cutting blocks) is added.

## Risks, named

- **Phase 0 returns nothing beyond `branch`.** Then non-engineering sessions are `budget`-cut
  throughout and blocks are effectively fixed-duration for that population. The contract still
  holds and the page still works; the detector's value is just narrower than hoped. Report it.
- **Merging forward could mask a real transition** — a genuine 90-second pivot gets absorbed into
  its successor. That is the trade for not emitting rows that say nothing, and 0c measures how
  often it happens.
- **`sensitivity` degraded in deterministic mode** is a pre-existing condition, not new here: it
  reports `degraded` when the PII scan is unavailable, and never lets a check that did not run
  publish a confident negative. Unchanged by this work, restated so nobody reads the Phase 0
  output as a regression.
