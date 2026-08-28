# Precomputed reference series: ingest once, digest from the store

**Status:** design, awaiting review.

## What is wrong today

`/analyze` is stateless. Each prompt re-reads the whole transcript, slices the 60 minutes ending
at that prompt, rolls it up, and throws everything away. Two consequences, both measured:

- **Redundant work.** A 60-minute window contains a mean of **3.8 user prompts** (max 20 over 370
  windows). Each triggers its own `/analyze` over a near-identical window, so one hour of work is
  parsed ~4x on average and up to 20x. On a real 64 MB transcript one call costs 0.8-1.0s.
- **No dynamics are possible.** Nothing persists, so nothing can be compared. `rollup()` counts one
  window; `dominant()` reports a share within that window; `payload()` emits the seven allocation
  dimensions and five inventory lists. That is the entire measurement. There is no turnover, no
  entropy, no rolling column, no delta, no baseline — the handoff lists exactly these as
  unmeasured, and phase 3 (baselines and lift) was deferred for want of persisted history.

The raw material for dynamics is already being generated — successive prompts produce overlapping
windows — but nothing stores it, and adjacent 60-minute windows minutes apart share >90% of their
mass, so differencing the published rows would measure noise at the edges.

## The change

**Ingest each transcript once, incrementally, into a persistent time series. Generate digests by
querying it.**

    watcher detects transcript grew
        -> sidecar parses ONLY the appended tail (byte offset checkpoint)
        -> raw reference events appended to the store
        -> affected 5-minute bins re-rolled
        -> watermark advanced
    digest request
        -> query bins covering the window, aggregate, done. No transcript read.

Transcripts are append-only JSONL, so a byte offset is a valid resume point. The daemon's watcher
already tails them and knows when one advances; it signals the sidecar rather than the sidecar
polling.

## Storage: native SQLite tables

At `~/.keld/state/refseries.db`, beside the existing `prompt-lengths.json`.

    ingest(path PK, offset, size, head_sha, mtime, watermark_ts, updated_at)
    event(session, ts, level, ref, n, source_line)         -- the raw facts
    bin(session, bin_ts, level, ref, n)                    -- 5-min rollups, PK(session,bin_ts,level,ref)
                                                           -- DEFAULT LEVELS ONLY; sparse by design

**Native tables, not pickled pandas.** Pickles couple the on-disk format to the pandas version,
cannot be queried or migrated, and would put a hard pandas dependency into a package that is
deliberately pandas-free (`analysis/__init__.py`; `window.py` argues `Counter` is the entire
performance case a window needs). Native tables cost nothing here and stay inspectable.

**Store raw events as well as rollups — but precompute rollups ONLY for the built-in, default
dimensions.** `events_for_turns` emits 19 levels; the default payload consumes 12 of them (the 7
ALLOCATION levels — workspace, branch, model, artifact, lang, skill, toolchain — and the 5
INVENTORY levels — tool, exe, service, mcp_tool, term). Those 12 are binned eagerly on ingest,
because they are what every digest asks for.

The other 7, and any dimension invented later, are NOT binned. They do not need to be: the raw
events are retained, so adding a rollup is a backfill query over `event` — no transcript is
re-read, no re-parse, no bashlex, no spaCy. That is the whole reason raw facts are stored, and it
also covers the routine case of a `SchemaVersion` bump changing what a level means.

`bin` is therefore keyed by level and sparse by design; a level's absence means "not precomputed",
not "no evidence". A reader must not infer zero from a missing row.

## Bins are 5 minutes; windows are composed

Fine bins are the unit; any window is an aggregation over the bins it covers. 5 minutes is small
enough for dynamics and, measured below, free. A 60-minute digest is 12 bins.

Bin boundaries are wall-clock aligned. The 50/60 stride finding from the study concerned where a
*reporting window* is placed, not how the underlying series is binned, and does not apply here.

## Retention: keep everything, with a size backstop

Measured over 39 transcripts spanning 28.1 days of one engineer's real work:

    raw reference events   3,882 / day   ~80 B/row   ->  0.11 GB/year   (~9 years per GB)
    5-minute rollups         154 / day   ~60 B/row   ->  negligible

A heavy user at 5x is still ~0.55 GB/year. **There is no time-based retention policy.** Raw events
are kept indefinitely; a size-based backstop (`KELD_REFSERIES_MAX_MB`, default 1024) prunes the
oldest raw events when exceeded, and rollups are never pruned — they are three orders of magnitude
cheaper and they are what the dynamics are computed from.

## Consistency: a watermark, and a retry rather than a wrong answer

Each transcript carries `watermark_ts` — the timestamp through which it is fully ingested. A digest
whose window ends after the watermark is **not** served from partial data: the pass fails and the
job re-spools, the same idiom the daemon already uses for a not-ready backend. Serving a window
that is missing its last few minutes would publish a confidently wrong attribution, which is the
failure mode this project keeps hitting.

Rotation and truncation are detected by `size < offset` or a changed `head_sha`; either triggers a
full reparse of that file rather than a corrupt resume.

## What this unlocks

Dynamics become queries against data already stored, not new extraction work: turnover between
adjacent bins, entropy of a window's distribution, rate of change across an hour, and drift against
this machine's own history — the per-machine baseline that phase 3 needed and could not have.

None of that is specified here beyond making it possible. Adding a dimension is cheap once the
series exists; deciding which dynamics are worth publishing is a separate question that should be
answered with a measurement, not by shipping every rolling column.

## Where it lives

The sidecar. The extraction stack — bashlex command parsing, the levels and vocab tables,
workspace resolution, reconcile — is Python and is most of the value. Re-porting it to Go so the
daemon could own the file would be a large rewrite for no gain. The daemon signals; the sidecar
ingests and serves.

`/analyze` keeps its current request and response contract, so the Go client, the workstreams pass
and the published payload are unchanged. What changes is that it queries instead of parsing.

## Out of scope

- **Which dynamics to publish.** Enabled here, not chosen here.
- **Cross-session or cross-machine aggregation.** The store is per-machine and per-session-file.
- **Analysis in a recycled worker child.** Deferred separately; orthogonal to this.
- **Backfill of existing transcripts.** Ingest is forward-only by default, matching
  `KELD_WATCH_BACKFILL`. A deliberate backfill is a follow-up.
