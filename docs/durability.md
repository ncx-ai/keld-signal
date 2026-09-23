# Durability — what Signal holds, what it replays, what it loses

Signal **collects always and pairs to send**
(`internal/agent/daemon/pairing.go`, the file header). Every collector is
constructed and started the moment the daemon runs; only the delivery half waits
for `~/.keld/hook.json` (`daemon/senders.go` · `startSenders`). A sender handed
an empty endpoint must HOLD — spool the batch, keep the cursor, re-spool the
pointer — and the three ways that is said are `publish.ErrNotPaired`,
`clientevents.ErrNotPaired` and `settings.ErrNotPaired`.

This document answers exactly three questions per lane: **what is buffered
where, what is replayed when, what is lost when.** Every claim cites the file
and symbol that makes it true. A claim with no citation was deleted rather than
softened.

Two losses are stated first because they are the ones a reader would otherwise
assume away:

- ⚠️ **A host-side transcript mirror observation made while unpaired is gone.**
  `promptlog` has no spool of its own and never gets one
  (`internal/agent/promptlog/promptlog.go` · `NewPending`).
- ⚠️ **Client events emitted before the reporter starts live only in a bounded,
  coalescing in-memory ring**, and on an unpaired machine that is the whole
  pre-pairing period (`internal/agent/clientevents/emitter.go` · `Emitter.insert`;
  `daemon/daemon.go` builds it with capacity 1024).

"Not paired" is not "Send to Atlas off". The second is a person saying *don't*,
and discarding is the honest answer to it (`daemon/localonly.go` ·
`localOnlySender`; `clientevents/transport.go` · `NewTransport`'s empty-endpoint
branch). Everything below describes the first.

---

## Usage telemetry

Two producers, and only one of them is durable.

**The loopback OTLP proxy.** AI tools POST to `127.0.0.1:14318`
(`internal/agent/teleproxy` · `Addr`), which binds before the pairing exists and
therefore resolves its Atlas endpoints per forward
(`daemon/teleproxy.go` · `startTelemetryProxy`; `teleproxy/proxy.go` ·
`NewPending`). **Buffered:** one file per batch under
`~/.keld/spool/telemetry/logs` and `~/.keld/spool/telemetry/metrics` —
separate directories so a poison metrics batch cannot block logs
(`paths.TelemetrySpoolDir`; `teleproxy/proxy.go` · `newProxy`), written by
`clientevents/transport.go` · `Transport.spool`. The tool is answered `202`
before Atlas is asked at all (`teleproxy/proxy.go` · `receive`), so delivery is
the spool's job and never the editor's. **Replayed:** every minute by
`daemon/teleproxy.go` · `telemetryDrainInterval` → `Proxy.DrainSpools` →
`Transport.DrainSpool`, oldest first; a drain **stops** on a rejection (401/403),
**ends the sweep** on unavailable (net/5xx), and **continues past** a refused
payload (other 4xx). While unpaired `DrainSpool` returns `ErrNotPaired` without
touching a file, because its own poison branch deletes and would classify "no
address" as a bad payload. **Lost:** the oldest batch past
`clientevents/reporter.go` · `defaultMaxSpool` (256 files per route), dropped by
`Transport.enforceSpoolCap`; a batch Atlas refuses with a non-auth 4xx, deleted
by `DrainSpool` so one bad body cannot block the good ones behind it; a body over
`teleproxy/proxy.go` · `maxBody` (4 MiB), answered `413` to the tool rather than
truncated; and trace exports, which are accepted and thrown away by design
(`teleproxy/proxy.go` · `Proxy.TracesDropped`).

**The host-side transcript mirror.** For a tool whose own egress cannot reach
Atlas, the daemon mirrors its transcript as OTLP logs and metrics
(`internal/agent/promptlog` · `Telemetry.Observe`). **Buffered: nothing.**
**Replayed: nothing.** ⚠️ **Lost:** any post that does not land — while unpaired
the endpoint resolves to `""` and the post is skipped
(`promptlog/promptlog.go` · `doPost`), and a post that fails for any other reason
is logged to the debug log and dropped on the same line. `NewPending`'s comment
states this and bounds it: a machine is unpaired only until somebody finishes
signing in, and a post that fails against a reachable Atlas is one record, not a
batch. Giving this path a spool would be a new durable queue, which was
deliberately not added. ⚠️ **Since the OTLP switch shipped off by default this
lane carries EVERY tool's usage, not Cowork's alone** (`promptlog.SourcesFor` is
the complement of `tool_otlp`), so the loss is no longer bounded by one tool's
share. **Counted, since 2026-09-21:** every undelivered post increments
`Telemetry.Dropped()` under a closed reason — `not_paired`, `unreachable`,
`rejected` (4xx), `unavailable` (5xx) — and the daemon emits ONE
`telemetry.mirror_dropped` client event per reason per run carrying the count
(`daemon/mirrordrop.go`), so a machine that is quietly losing usage says so
within one prompt of starting to. Counted is not recovered: the records are
still gone.

## Enrichment

⚠️ **This is the one collector with nowhere local to put its output.** There is
no on-disk store of finished profiles, so running the pipeline while unpaired
would compute a profile and discard it — the POINTER is the durable form
(`daemon/pairing.go` · `enrichHold`).

**Buffered:** the prompt pointer — transcript path plus prompt id, never text —
in one WAL-mode SQLite database at `~/.keld/spool/spool.db`
(`internal/spool/db.go` · `dbPath`, `schema`), keyed
`UNIQUE(source_id, corr_scheme, corr_id)` so a re-write upserts rather than
duplicates (`spool/spool.go` · `upsertSQL`). Both the hook
(`internal/hook/forward.go`) and the daemon write it. In flight, jobs sit in a
bounded deduping queue of 1024 (`daemon/daemon.go` · `queueCap`); a genuinely
full queue answers `429` and the hook spools
(`internal/agent/ingress/ingress.go`, the `TakenOn` branch).

**Replayed:** `daemon/daemon.go` · `drainEnrichSpool` at startup and on a
periodic sweep, deleting a row only once `queue.Offer(...).TakenOn()` — so a
duplicate is deleted (it can never become acceptable) while real backpressure
keeps the row. While unpaired the worker holds each job straight back into the
spool without consuming an attempt (`daemon/pairing.go` · `holdJob`) and
`drainEnrichSpool` returns early, so the rows are not churned between disk and
queue on every sweep.

**Lost:** a job that trips `KELD_ENRICH_JOB_TIMEOUT` more than
`KELD_ENRICH_MAX_ATTEMPTS` times (default 4, `daemon/daemon.go` · `maxAttempts`)
is moved to `~/.keld/spool/bad/` and never drained again
(`spool/spool.go` · `Quarantine`); a row whose body will not decode is
quarantined the same way so poison cannot block the drain
(`spool/spool.go` · `quarantineRaw`); and the oldest rows are evicted once the
spool passes its byte budget — 256 MB, `KELD_SPOOL_MAX_BYTES`
(`spool/db.go` · `defaultMaxBytes`, `evictFor`), counted for alarms by
`spool.Evicted`.

⚠️ **A publish that FAILS on a paired machine is not re-spooled.** `process`
returns false on a failed `pub.Send` (`daemon/daemon.go`, the `publish.failed`
emit), and the worker's re-spool branch is reached only when the job misses its
deadline — a job that finished and could not publish is dropped. AGENTS.md
records this for the 401-mid-rotation case as a v1 known limitation; the code
does not distinguish 401 from any other publish error, so the limitation is
general. The prompt survives only if the transcript watcher re-offers it later
(the key is marked done by `queue.Complete` **only on a real publish**).

## Blocks

**Buffered:** nothing of the block itself, and that is a decision rather than an
omission (`internal/agent/blocks/emitter.go` · `Emitter.publish`, the
"THERE IS NO SPOOL HERE" note). The evidence is already durable — the sidecar's
reference series at `~/.keld/state/refseries.db` retains raw events for
`KELD_REFSERIES_RETAIN_DAYS` (`sidecar/app/analysis/store.py` ·
`DEFAULT_RETAIN_DAYS`, 400) — so the only thing the emitter keeps is a
per-transcript cursor in its own state file (`blocks/state.go` · `state`,
`blocks/emitter.go` · `StatePath`), written atomically after every sweep that
moved anything.

**Replayed:** the cursor advances only past a batch whose `SendBlocks` succeeded,
stops at the last contiguous success, and never advances past a gap, because a
watermark cannot express a hole (`blocks/emitter.go` · `Emitter.publish`). The
next sweep re-fetches the same ground. Re-delivery is free: a block's identity is
`(session, block.start)` and Atlas upserts. While unpaired `SendBlocks` answers
`publish.ErrNotPaired` and the cursor is simply held
(`daemon/blocks.go` · `startBlockEmitter`; `publish/block.go`). A sidecar that
cannot answer holds the cursor too rather than retiring the transcript
(`blocks/emitter.go`, the `!ans.OK` branch).

**Lost:** the cursor file. Losing it makes every transcript first-sight again —
harmless while `KELD_BLOCKS_BACKFILL` is on (the default), because first sight
then asks from the session's beginning, bounded to `maxPerSweep` (24) rows per
transcript per sweep; with backfill off, first sight seeds at the watermark and
the blocks between the old cursor and now are never emitted
(`blocks/state.go` · `state`'s header; `blocks/emitter.go` · `sweep`'s
first-sight branch). A cursor is also pruned after
`blocks/state.go` · `cursorRetain` (30 days), which is safe only because an entry
stays in the active set until a sweep returns no blocks at all.

⚠️ **With Send to Atlas OFF the block is discarded on purpose, and a separate
path exists to recover it.** `localOnlySender` reports success so the cursor
advances, and `daemon/republish.go` · `captureOnCut` writes the block's own
marshalled JSON into `unsent_payloads` in `ledger.db`
(`internal/agent/ledger/unsent.go` · `SaveUnsentPayload`) so a later run with
Atlas on republishes exactly that payload without re-cutting anything
(`daemon/republish.go` · `startRepublisher`). A payload Atlas refuses **on its
own** five times (`ledger.UnsentRefusalLimit`, spread over about an hour and a
quarter by `republishInterval`'s doubling backoff) is held aside, kept, and never
offered again. This path is wired only when Atlas is off
(`daemon/daemon.go`, the `if !set.AtlasEnabled()` branch around `onCut`) — it is
not the unpaired story.

## Feature rows

Off by default and registered only under `ml_backend:"deterministic"`
(`daemon/features.go` · `startFeatureEmitter`). Two independent toggles:
`KELD_FEATURES` collects, `KELD_FEATURES_PUBLISH` sends.

**Buffered:** in memory first — `internal/agent/features/emitter.go` ·
`bufferCapacity` (512 rows, about 700 KB, roughly 2.7 days of one user's
production) — then on disk under `~/.keld/spool/features/`
(`paths.FeaturesSpoolDir`, a directory of its own so two paths cannot re-post
each other's bodies to each other's routes), 256 batch files, drop-oldest
(`clientevents/reporter.go` · `defaultMaxSpool`).

⚠️ **The cursor advances on BUFFERING, not on delivery** — the opposite of the
block emitter's rule (`features/state.go` · `entry.Cursor`). What makes that safe
is backpressure, not optimism: a sweep never takes more rows than the buffer has
room for (`features/emitter.go` · `Emitter.room`, `sweepOne`), so a full buffer
stops the sweep taking rather than dropping what it took.

**Replayed:** `features/reporter.go` · `Reporter.Run` drains the spool at startup
and flushes every `DefaultFlush` (2 minutes), with one best-effort final flush on
shutdown. While unpaired `Deliver` spools the chunk and answers `ErrNotPaired`
(`clientevents/transport.go` · `NewPendingTransport`), and `DrainSpool` is a
no-op rather than a delete.

**Lost:** with collection on and publishing off, `Flush` **drains and discards**
— deliberately, so a buffer nobody empties cannot wedge the emitter's
backpressure (`features/reporter.go` · `Reporter.Flush`, the gate check after the
drain). ⚠️ **A flush stops at the first failing chunk and drops the rest of what
it drained** (`Reporter.Flush`'s header states this): the rows are already out of
the buffer and only the transport can preserve them, so the chunk it could not
deliver is spooled and the chunks behind it are gone rather than pushed into a
spool that is already evicting. On an unpaired machine every chunk fails, so a
flush carrying more than `features/emitter.go` · `batchRows` (64) rows keeps 64
and loses the remainder — reachable when several transcripts are active inside
one flush interval, since `maxPerSweep` is 64 per transcript per sweep. Beyond
that: the oldest spool file past 256, a row that will not marshal (skipped and
counted), and the cursor file, which loses the rows between the old cursor and
the watermark exactly as the block cursor does (`features/state.go` · `state`'s
header).

## Client events

The daemon's operational events about itself — job retries, quarantines, sidecar
crashes, publish failures, lifecycle (`docs/signal-client-events.md`).

**Buffered:** an in-memory ring of 1024 (`daemon/daemon.go` ·
`clientevents.NewEmitter(base, 1024)`) which **coalesces** a consecutive repeat
of the same code and severity into a `count` field on the previous entry and
drops the **oldest** past capacity (`clientevents/emitter.go` · `Emitter.insert`).
Then, once a batch has been attempted, one file per batch under
`~/.keld/spool/clientevents/` (`paths.ClientEventsSpoolDir`), 256 files,
drop-oldest.

**Replayed:** `clientevents/reporter.go` · `Reporter.Run` drains the spool at
startup, then flushes and drains on each tick. ⚠️ **The reporter is started by
`startSenders`, which is the one thing in the process that waits for the
pairing** (`daemon/daemon.go`, the goroutine around `awaitConfig`), so on an
unpaired machine nothing drains the ring until the pairing lands.

**Lost:** events past 1024 ring entries before the reporter starts — the oldest
go first, and coalescing means a burst of one code costs one slot but keeps only
the first entry's fields plus a count. A batch that fails permanently (a 400, or
anything `retry.IsTransient` and `IsAuthRejection` both decline) is dropped
rather than spooled (`clientevents/transport.go` · `Transport.Deliver`) —
re-posting it would never succeed. With Send to Atlas off the reporter is built
with an empty endpoint and **discards** without spooling
(`clientevents/transport.go` · `NewTransport`'s empty-endpoint branch), because
operational events about a machine nobody is collecting from have nowhere to go
and a spool for them could never drain.

---

## What the page says

The Today pane prints one sentence beside the health strip whenever a health
cell is not `ok` (`internal/agent/ui/app.js` · `durabilityNote`). It is this,
verbatim, and a test pins the two copies together
(`ui/e2e/durability.spec.ts`, `internal/agent/ui/test/durability.test.js`):

<!-- page-copy:durability -->
> Work is recorded on this machine first and delivered when Atlas can be reached, so a red badge here usually means late rather than lost.
<!-- /page-copy:durability -->

"Usually" is doing real work in that sentence. The lanes above name the cases
where it is not true.
