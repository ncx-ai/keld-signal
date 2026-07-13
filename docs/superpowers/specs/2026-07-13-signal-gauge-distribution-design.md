# Design: Signal resource gauge — interval distribution (sub-project D)

**Date:** 2026-07-13
**Status:** design (decisions locked via brainstorm), pending review
**Repos:** keld-cli (A: watcher + wire contract) → keld-atlas (B: `resource_series`;
C: admin-web resource chart)
**Parent:** Signal Client monitoring. A/B/C shipped; this refines the resource gauge.

## Problem

The `resource.gauge` today emits a **single instantaneous sample every 5 minutes**
(`clientevents/resource/watcher.go` `Poll()`: the gauge branch emits `s.RSSMB` /
`s.CPUPct` at the 5-min boundary). The watcher already samples the process tree every
~15s (those sub-samples feed sustained-anomaly detection) but **discards them for the
gauge**. So the trend charts show one point-in-time value per 5 min: a spike between
marks is invisible, and there's no sense of typical vs peak vs variability. For
capacity/health monitoring we want **min / max / mean / std** of RAM and CPU over each
interval, not an instantaneous poke.

## Decision (locked)

Aggregate the sub-samples the watcher already takes into a **per-interval
distribution**, tighten sampling slightly, and carry the distribution end-to-end
(client → Atlas → chart).

- **Gauge payload** (per metric, whole process tree): `min`, `max`, `mean`, `std`,
  `last`, plus `n` (sample count) and `window_s` (actual elapsed seconds of the
  interval).
- **Cadence:** sub-sample every **10s** (was 15s); emit a gauge every **5 min** (~30
  samples/gauge). Still ~288 gauges/install/day — no volume increase, far richer rows.
- **Storage/scale (#3, decided separately):** unchanged — single `signal_client_events`
  table + the new ts-oriented indexes + retention; a server-side rollup (the interval
  stats merge cleanly) is the documented future scale lever, not built here.

## Non-goals

- No change to sustained-high **anomaly** detection (`pollTrack`) — it keeps using the
  instantaneous per-tick value; this spec only changes the **gauge**.
- No per-process (daemon/sidecar/worker) distribution — the tree total only (the
  sampler's role labels are still best-effort daemon/others). A `proc_tree` snapshot
  from the last sample is retained for context.
- No new storage backend / rollup job (future).

## A — keld-cli: watcher + wire contract

**Watcher (`internal/agent/clientevents/resource/watcher.go`).** Add a per-metric
running accumulator (RSS, CPU): `n`, `sum`, `sumsq`, `min`, `max`, `last`. Each `Poll()`
folds the current sample into both accumulators (in addition to today's `pollTrack`
anomaly logic, which is unchanged). When the gauge cadence fires, compute the stats and
emit, then reset the accumulators:

```
"rss":       {"min":…, "max":…, "mean":…, "std":…, "last":…}   // map[string]any of float64
"cpu":       {"min":…, "max":…, "mean":…, "std":…, "last":…}
"n":         <int samples in the interval>
"window_s":  <float seconds since last gauge>
"proc_tree": treeAsAny(lastSample.Tree)
```
- `mean = sum/n`; `std = sqrt(max(0, sumsq/n − mean²))` (population; `0` when `n<2`).
- The nested `rss`/`cpu` MUST be `map[string]any` (float64 values) so the Go-side
  redaction gate passes them (a `map[string]float64` is dropped as an unknown type —
  established in the watcher's `treeAsAny` lesson).
- **First poll** (`lastGaugeAt` zero): emit a baseline gauge immediately from the single
  sample (`n=1`, `std=0`, min=max=mean=last), then reset — preserves today's
  "a gauge appears promptly" behavior.
- Accumulators are folded inside `Poll()` (still pure/deterministic — sampler+clock
  injected), so `watcher_test.go` can assert the distribution with a scripted sampler.

**Sample interval default** (`daemon.go`): `sampleInterval` default **10s** (was 15s);
env `KELD_CLIENTEVENTS_SAMPLE` override unchanged.

**Schema version** (`internal/agent/clientevents/event.go`): bump
`SchemaVersion` **1 → 2** (the gauge wire shape changed — contract-affecting).

**Wire contract doc** (`docs/signal-client-events.md`): update the `resource.gauge`
fields section to the new nested distribution shape; note `schema_version` is now `2`
and that gauges from older clients carry the old flat `{rss_mb, cpu_pct}` (consumers
must tolerate both — see B).

## B — keld-atlas: `resource_series` (services/api)

`resource_series` + the `ResourcePoint` admin schema return the distribution.
`ResourcePoint` gains: `rss_mean, rss_min, rss_max, rss_std, cpu_mean, cpu_min,
cpu_max, cpu_std` (all `float | None`) alongside `ts`. (Drop or keep the legacy
`rss_mb`/`cpu_pct`? — replace with the `*_mean` etc.; C is the only consumer and moves
with it.)

**Backward compatibility (required):** old gauges already in the DB have the flat
`{rss_mb, cpu_pct}` shape. `resource_series` normalizes BOTH:
- new nested gauge (`fields.rss` is a dict) → read min/max/mean/std/last;
- old flat gauge (`fields.rss_mb` present, no `rss` dict) → `mean=min=max=rss_mb`,
  `std=None`.
Reuse the existing `_num()` coercion so a malformed value → `None`, never a 500.
Ingest (B) stores `fields` opaquely, so **no ingest/storage/migration change** — only
the read helper + schema + tests.

## C — keld-atlas admin-web: resource chart

`components/signal/resource-chart.tsx` + the `ResourcePoint` type in `lib/admin.ts`
move to the distribution shape. The chart plots, per metric (RSS in GB on the left
axis, CPU % on the right):
- a **mean line**, and
- a shaded **min–max band** (recharts `Area` between min and max, low opacity, using
  the existing `--keld-chart-*` tokens),
- **std** surfaced in the tooltip.
Null-safe (missing band → just the mean line; `connectNulls={false}` gaps). Empty state
unchanged. Optionally update the dev seed (`app.seed_signal`) to emit new-shape gauges
so the band renders locally.

## Testing

- **A:** `watcher_test.go` — a scripted sampler feeding several sub-samples across a
  gauge interval; assert the emitted gauge's `rss`/`cpu` min/max/mean/std/last/`n`/
  `window_s` match hand-computed values; first-poll baseline (`n=1`, `std=0`); reset
  between intervals (second interval's stats don't include the first's samples).
  `event_test.go`: `SchemaVersion == 2`.
- **B:** `resource_series` returns the distribution for a new-shape gauge; **and**
  normalizes an old flat gauge (mean=rss_mb, std None); malformed value → None.
- **C:** chart maps points → mean+band series; renders (jsdom-safe, no pixel asserts);
  old-shape/None band → mean-only without throwing.

## Rollout / compatibility notes

- `schema_version` 2 is informational on the wire; Atlas ingest is lenient and stores
  any shape, so a v1 client and a v2 client coexist — the read path tolerates both.
- No DB migration (fields is JSONB). No breaking change to any route.
- Sequence: **A (keld-cli) first** (ships the new gauge + contract), then **B + C
  (keld-atlas)** consume it. Each merges to its repo's `main` locally.

## Decomposition (→ plans)

**keld-cli (A):** 1) watcher accumulator + new gauge shape + `SchemaVersion` 2 + sample
default 10s + tests; 2) wire-contract doc.
**keld-atlas (B+C):** 3) `resource_series` + `ResourcePoint` distribution + old/new
tolerance + tests; 4) admin-web chart mean+band + type + tests (+ optional seed shape).
