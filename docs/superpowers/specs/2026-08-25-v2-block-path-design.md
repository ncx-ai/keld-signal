# The v2 block path

**Status:** design. Nothing below is built. It replaces the "decorate `/analyze`" approach, which
was wrong and was reverted.

## The rule this document exists to enforce

**v2 is a PATH, not a parameter.** The same principle the Atlas spec states for its side. v2 code
lives in its own modules, is reachable by its own entry point, and can be deleted or promoted
wholesale without unpicking v1.

⚠️ **What went wrong before, so it is not repeated.** The block cutter was built correctly, then
bolted onto v1 as an additive `block` key on `/analyze` — an endpoint that still characterises a
60-MINUTE WINDOW ANCHORED TO A PROMPT. Every published workstream stayed v1; the block rode along as
metadata. Adding `covers` and a tick wiring on top made a stepping stone look like a destination.
Threading v2 through a v1 function is the failure mode. Don't.

## What v2 is

    a transcript -> blocks of work -> one characterisation per block

That is the whole model. No prompt anchor, no look-back window, no gap-filling.

**Blocks tile active time**, so there are no gaps to fill and coverage is 100% of activity by
construction. The tick exists to patch holes left by prompt-anchored windows; under this model those
holes do not exist. The 55.0% → 99.5% coverage figure is a v1 statistic and does not motivate
anything here.

## What stays per-message, and why

**`sensitivity` only** — credential detection and PII. Those genuinely apply to one message at a
time, they are latency-sensitive, and they read text. Everything else is a property of a span of
work, not of a sentence.

So the daemon ends up with two independent producers:

| | trigger | reads | publishes |
|---|---|---|---|
| per-message | a prompt arrives | prompt text, on-device | `sensitivity` |
| **block** | a block closes | stored coordinates only | everything else |

## When is a block CLOSED

The only genuinely new concept. A block may be emitted once nothing can still change it:

    closed(b)  ==  b.end <= watermark                      # the store has ingested the whole span
               and ( activity exists after b.end           # something later proves b.end is real
                     or now - b.end >= IDLE_SECONDS )      # or enough silence has settled it

⚠️ **The disjunction is load-bearing and an earlier draft got it wrong.** That draft required
`now - last_activity >= IDLE` for EVERY block, which would emit nothing at all during an active
session — a full working day would produce its first block only after the person stopped. Blocks
must close continuously as work moves past them.

The reasoning: a `budget` or `idle` cut is final the moment it is reached, because later activity
starts a NEW block and cannot alter this one. So any block with activity after it is settled
immediately. The ONLY provisional block is the trailing one, whose end is "where the data currently
stops" rather than a real boundary — and one idle threshold of silence settles that.

**Latency, stated because the UI depends on it:** a block is emitted at most
`IDLE_SECONDS + emitter interval` after its own end for the trailing case, and roughly one emitter
interval in the common case. Mid-session blocks are NOT delayed by the idle wait.

**Emission is idempotent.** A block's identity is `(session, block.start)`, deterministic and
immutable. Re-emitting is an upsert, so a crash mid-batch costs nothing.

⚠️ **This replaces the tick's frontier, and it is much weaker on purpose.** The frontier had to
reason about which FUTURE PROMPTS might sweep over a moment, because a prompt's window reached
backwards. A block reaches nowhere: it is a span with a determined end. Nothing later can overlap it.
Do not port `frontier()` / `tail_closed()` into this path — they solve a problem this model does not
have.

## ⚠️ RAW ACTIVITY KEEPS ARRIVING LIVE. Blocks are a LATER LAYER ON TOP.

Telemetry and enrichment are separate lanes and always have been: the hook posts usage telemetry
straight to Atlas with no daemon involvement, so tool events and token usage appear in the app as
they happen. **Nothing in this design may delay that, and nothing may make the app wait for a block
before showing work.**

The consequence is that the timeline has a permanently uncharacterised leading edge — the most
recent stretch, up to one idle threshold plus an emitter interval, always has activity with no block
attached yet. That is inherent, not a defect.

Three requirements follow, and they belong to Atlas as much as here:

1. **Live activity renders with no block.** The app shows the work as it arrives, unlabelled.
2. **A block attaches RETROACTIVELY** to activity already on screen, when it arrives. This is why
   the join matters: a block row must be attachable to rows the app has already drawn.
3. **"Not characterised yet" must be visually distinct from "characterised, found nothing."** Same
   principle as `thin` versus `absent` on a dimension: never let not-yet-known render as
   known-to-be-nothing. A pending leading edge that looks like an empty one tells the reader their
   last half hour was idle when it was the busiest part of the day.

## Module layout

Everything new, nothing threaded through an existing function.

**Sidecar**

    app/analysis/blocks.py          cut()                      EXISTS, unchanged
    app/analysis/blockdigest.py     NEW. characterise ONE block span -> the payload
    app/main.py                     NEW endpoint POST /blocks  (the only edit to an existing file)

⚠️ **`covers()` was built, shipped, then deleted (`9d47e71`).** It mapped a block to the prompt
episodes overlapping it — see the contract doc's `covers` section for the argument. Nothing in this
spec's module layout should be read as reintroducing it: `blocks.py` exports `cut()` only.

`blockdigest` composes what already exists and is already span-parameterised — `rollup_window`,
`workstreams.payload`, `dynamics`, `prior`, `effort`. None of those know about prompts; they take
bounds. That is why this is composition rather than new analysis.

⚠️ **`analyze.py` is NOT modified.** It is v1's entry point and stays exactly as it is until v1 is
retired. No `block` key, no `prompts` argument, no shared helper reached across from v2. If
`blockdigest` needs something `analyze.py` has, that thing moves DOWN into a module both can import,
it is not called sideways.

**`POST /blocks`** — request `{path, since_ts, resolved}`, response
`{blocks: [{start, end, start_reason, end_reason, ...facets}], watermark}`.
Coordinates only, same `KELD_ANALYZE_ROOTS` confinement as `/analyze`.

⚠️ **The request no longer takes `prompts`, and the response no longer carries `covers`.** Both are
in this spec's original text below (Open question 3) because this document predates the deletion;
see the contract doc for why: the join `covers` computed is one Atlas owes anyway for cost
attribution, and it shipped resolving no ids at all (`promptId` vs. the store's per-message `uuid`
— different id spaces).

**Go**

    internal/agent/blocks/          NEW package: the emitter + its cursor state
    internal/agent/publish/         gains a block row type; existing types untouched

The emitter is a daemon-side loop asking "which closed blocks have I not emitted for this
transcript", keyed on a per-transcript cursor in `~/.keld/state/`. It is NOT the tick and must not
be built on it.

## What this deletes, eventually

Not in this spec, but named so the direction is clear: when the block path publishes and Atlas reads
it, v1's prompt-anchored characterisation retires — `/analyze`'s workstreams/dynamics/prior/effort,
the tick entirely, and `coverage.py`. The per-message path keeps `sensitivity` and nothing else.
That retirement is a separate, later change; v2 must be shipping and proven first.

## Open questions to settle before the plan

1. **Cadence.** The emitter needs an interval. It is pure latency — a block is emitted at most one
   interval after it closes — so this is a product choice, not a measurement.
2. **Backfill.** On first sight of a transcript, emit history or start forward-only? `KELD_WATCH_BACKFILL`
   is forward-only by default and this should match it.
3. ~~Does `covers` ride the block or sit beside it?~~ **Moot — `covers` is deleted (`9d47e71`).** A
   block carries no prompt-shaped field at all. The mapping this question was about turned out to
   be a second, weaker copy of a time join Atlas already has to build for cost attribution
   (`event.ts ∈ [block.start, block.end)`); that same join answers the display question this
   question was trying to solve, so there was no honest place left for it to ride. See the
   contract doc's `covers` section for the full argument.
4. **One row per block, or one batch per transcript?** Atlas dedups on a key either way; batching is
   a transport decision.
