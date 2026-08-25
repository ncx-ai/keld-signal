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

## Phase 0 — measure, before any of it is built

The bars are in the contract; this is where they run. **No block cutting merges until Phase 0
reports.**

**0a. Which levels detect.** The detector reads `branch` only, because that is where transitions
were observable — `workspace` has **zero** across 51 sessions. Test `skill`, `component`,
`output_type`, `language` and `action` beside it on the same population (25 sessions / 111
transitions / 1,966 windows), scored identically: precision, recall, median detection distance,
against both the `FixedSizer` control and the shuffled-truth control. A level ships as a detection
input only if it beats fixed on both metrics **and** drops ≥ 20 points under shuffled truth.

⚠️ The stakes are the non-engineering population. A session with no branch activity — John's
Cowork session, any analyst — returns `budget` on every boundary if nothing else detects. That is
a valid outcome and must be *reported*, not discovered later.

**0b. Maximum block duration.** Derived: the smallest cap at which the share of `budget`-ended
blocks stops falling materially. Not a round number chosen for looking reasonable.

**0c. The merge rule.** What share of blocks fall under `MIN_EVIDENCE` and merge forward, and —
the question that matters — does merging change any published VALUE, or only evidence counts?

**0d. Tiling equality.** Every turn in exactly one block, no turn in none, asserted over the
frozen corpus. This is the invariant the whole attribution model rests on, so it is a test, not a
measurement.

Harness: `scripts/`, results durable in `~/keld/refseries-context/blocks/`, never in `docs/`.

## Phase 1 — cut blocks (sidecar)

New module `sidecar/app/analysis/blocks.py`. Pure functions over stored rows, no I/O, mirroring
`dynamics.py`/`prior.py`.

    cut(store, session, from_ts, to_ts, levels, max_minutes, min_evidence) -> [Block]

- Encodes the detection levels as `dynamics.EwmaSizer` already does — a per-bucket novelty share
  on a 60-second observation step — and takes every rising edge of `fast - slow` as a cut, not
  just the last one. That is the single behavioural change to the detector: it currently returns
  ONE cut (the slice start) inside a window; blocks need all of them across a span.
- Emits contiguous blocks with `start_reason`/`end_reason` from the closed set.
- `idle` closes a block when no turn of any kind appears for the idle threshold.
- A block under `min_evidence` merges into its successor, taking the earlier `start` and the later
  `end_reason`. Deferred until the successor closes, so it is never retroactive.

**What this retires.** `coverage.py` exists because v1 windows were prompt-anchored and left gaps:
`covered(prompt_ts, span)` computes which regions a prompt's look-back reached and `gaps()` finds
the rest. Under tiling there are no gaps — blocks cover all activity — so both retire along with
`plan()`'s fixed-span windows.

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
