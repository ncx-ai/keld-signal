# Design: Signal Client telemetry — capture + transport (sub-project A)

**Date:** 2026-07-12
**Status:** design, pending review
**Parent:** Signal Client monitoring (A: keld-signal capture/transport → B: Atlas
ingest route + store → C: keld-atlas admin dashboard). This spec is **A**.

## Problem

Keld internal admins have no visibility into client-side failures of the
keld-signal daemon + GLiNER2 sidecar: quarantined jobs, worker panics/crashes,
publish/provision/settings failures, sidecar fallback, and — importantly —
**keld-signal consuming unexpectedly high RAM/CPU for a prolonged period** on a
user's machine. Today these are only local `log.Printf` lines that never leave
the device. We need a privacy-safe stream of structured client events posted to
Atlas so admins can spot errors/anomalies and reconstruct "what went wrong and
where to look."

A defines the **client-event contract** and implements capture + transport in
keld-signal, posting to a route B will implement (validated against a local stub
until then). A does not build the Atlas route (B) or the dashboard (C).

## Goals

- Structured, **typed** client events (stable code + severity + small
  privacy-safe fields + correlation) emitted at known anomaly/error sites.
- A **resource dimension**: watch keld-signal's whole process tree
  (daemon + sidecar + worker child) and emit a **sustained-high** RAM/CPU anomaly
  event (alertable) plus low-frequency **gauge** snapshots (for trend lines).
- Full **correlation** for narrative reconstruction: org, actor, stable
  install-id, run-id, session/prompt ids, version, os/arch, timestamps.
- **Governed** per-org via the existing control plane (`client_telemetry`
  toggle + min-severity + sample-rate + resource thresholds), default ON,
  independent of the enrichment toggle.
- **Privacy invariant upheld**: never any raw prompt text — only ids + structured
  metadata; a redaction gate enforced before send (the analogue of enrichment
  masking).
- Durable delivery: batch + periodic flush, `internal/retry` on transient POST
  faults, on-disk spool + drain when offline.

## Non-goals

- The Atlas ingest route / storage / query API (B) and the admin dashboard (C).
- A continuous high-rate metrics/time-series pipeline (chosen against — sparse
  events + low-freq gauges instead).
- Capturing sidecar-*internal* Python events beyond what the daemon observes
  (resource footprint is measured from the daemon by process-tree sampling;
  deeper sidecar-internal events are a later addition).
- Shipping raw `log.Printf` lines (structured events only).

## Event model

```go
type Severity string // "critical" | "error" | "warn" | "info"

type Event struct {
    Code     string         `json:"code"`      // stable dotted id, e.g. "job.quarantined"
    Severity Severity       `json:"severity"`
    Fields   map[string]any `json:"fields,omitempty"` // small, privacy-safe, redacted
    Corr     Corr           `json:"corr"`
    TS       time.Time      `json:"ts"`
}

type Corr struct {
    Org       string `json:"org,omitempty"`
    Actor     string `json:"actor,omitempty"`
    InstallID string `json:"install_id"`          // stable per-install uuid (~/.keld/install-id)
    RunID     string `json:"run_id"`              // per daemon process
    SessionID string `json:"session_id,omitempty"`
    PromptID  string `json:"prompt_id,omitempty"`
    Version   string `json:"version"`
    OS        string `json:"os"`
    Arch      string `json:"arch"`
}
```

**Initial code catalog** (map 1:1 to a code site so "where to look" is the code):
- `job.quarantined`, `job.retry_exhausted`, `job.respool_failed`
- `worker.panic`, `worker.crash`, `sidecar.fallback`
- `publish.failed`, `provision.failed`, `settings.poll_failed`,
  `model.load_failed`, `daemon.start`, `daemon.stop`
- `resource.sustained_high_rss`, `resource.sustained_high_cpu`, `resource.gauge`

**Severity floor** default `warn` (info gauges are exempted from the floor when
gauges are enabled — see governance). Escalation: a resource anomaly starts at
`warn` and escalates to `error`/`critical` the longer it stays elevated.

## Capture — `internal/agent/clientevents`

- `Emit(code string, sev Severity, fields map[string]any)` — thread-safe,
  **non-blocking**: stamps the process-wide `Corr` (org/actor/install_id/run_id/
  version/os/arch set once at daemon start; session/prompt/ts added per event via
  a `WithJob(session, prompt)` variant used inside the worker), runs the redaction
  pass on `fields`, applies the severity floor + sample-rate, and enqueues to a
  **bounded** in-memory ring (drop-oldest + coalesce repeated identical codes with
  a count under flood).
- Instrumented sites call `Emit` **alongside** the existing `log.Printf` — additive,
  no behavior change to the daemon. Initial sites = the anomaly `log.Printf`
  points in `daemon.go` (quarantine, retry-exhaustion, worker recover, publish/
  provision/settings failures, sidecar fallback) + daemon start/stop.
- `install_id`: `paths.InstallIDPath()` → `~/.keld/install-id`; generated once
  (random uuid) and reused; user-only perms.

## Resource watcher

- A sampler (goroutine, interval default 15s) measures the **keld-signal process
  tree** RSS + CPU%: the `keld-agent` daemon, the sidecar (FastAPI parent), and
  its worker child. (Reuse `gopsutil`? No — avoid a new dep; read `/proc` on
  Linux, `ps`/platform calls elsewhere, or a minimal per-OS probe. Footprint is
  RSS sum + CPU% over the tree.)
- **Sustained detection** (EWMA/threshold-over-window, mirroring the sidecar
  governor + memwatch): when tree RSS > `rss_threshold_mb` OR tree CPU% >
  `cpu_threshold_pct` continuously for ≥ `sustained_window_s`, emit
  `resource.sustained_high_rss` / `_cpu` with `{rss_mb|cpu_pct, threshold,
  elevated_s, proc_tree:{daemon_mb, sidecar_mb, worker_mb, ...}}`; re-emit at
  escalating severity as `elevated_s` grows; one "recovered" info event when it
  drops back. Hysteresis so it doesn't flap.
- **Gauge snapshots**: every `gauge_interval_s` (default 300s) emit
  `resource.gauge {rss_mb, cpu_pct, ...}` at `info` for dashboard trend lines
  (exempt from the severity floor when gauges are enabled).
- Thresholds/window/intervals come from the control-plane `client_telemetry`
  settings (below) with env fallbacks; defaults sized to keld-signal's expected
  footprint (~2.7 GB sidecar baseline → `rss_threshold_mb` ~4096; CPU governed to
  2 cores → `cpu_threshold_pct` ~150 sustained).

## Transport

- A reporter drains the buffer, batches (envelope below), and POSTs to the new
  Atlas route — **A defines the contract**, proposed `POST /v1/client-events`
  (derived from `cfg.Endpoint` the same way `enrichEndpoint`/`settingsEndpoint`
  are), with the ingest token, wrapped in `retry.Do` (transient → backoff,
  permanent → drop-or-spool per status).
- Flush on a timer (default 30s) or when the batch hits N events; flush on
  graceful shutdown.
- **Offline durability**: on unreachable Atlas / persistent failure, spool the
  batch to `~/.keld/spool/clientevents/` (mirror the hook spool) and drain on
  startup + periodic sweep; bounded (cap + drop-oldest).

**Wire envelope + version:**
```json
{ "schema_version": 1, "install_id": "...", "events": [ Event, ... ] }
```
A `clientevents.SchemaVersion` const (bump on contract change) — the interface B
consumes and C renders. Documented in `docs/` as the client-events contract.

## Governance — control-plane `client_telemetry`

- Extend the settings poll (`internal/agent/settings`) to parse an **optional**
  `client_telemetry` block from `GET /v1/enrichment-settings` (forward-compatible:
  absent → defaults, so it works before B implements it):
  ```json
  "client_telemetry": {
    "enabled": true, "min_severity": "warn", "sample_rate": 1.0,
    "gauges_enabled": true, "gauge_interval_s": 300,
    "rss_threshold_mb": 4096, "cpu_threshold_pct": 150, "sustained_window_s": 120
  }
  ```
- Remote overrides local; non-fatal if Atlas unreachable. Default ON; independent
  of the enrichment on/off toggle. When `enabled=false`, emit/send are no-ops.

## Privacy invariant

Events carry only ids + structured metadata. The emitter runs a **redaction
gate** on `fields` before enqueue: reject/redact anything that could be free text
— strip absolute filesystem paths, cap string lengths, and only allow a
documented allow-list of field shapes (numbers, enums, short codes, durations,
HTTP statuses). Error values are reduced to a class/summary, never raw `%v`. This
gate is enforced Go-side before any buffering (the analogue of `enrich/mask.go`),
and unit-tested to prove no raw text escapes.

## Testing (TDD)

- **Emitter**: corr stamping; non-blocking + bounded ring (drop-oldest, coalesce);
  severity floor + sample-rate gating; `install_id` generate-once/reuse.
- **Redaction gate**: absolute paths stripped; over-long strings capped; a field
  carrying a prompt-like string is rejected/redacted — proves the invariant.
- **Resource watcher**: injected sampler + clock → sustained detection fires only
  after the window, escalates, recovers with hysteresis; gauges at the interval;
  thresholds honored from settings.
- **Transport**: batch flush on timer/size; `retry.Do` on transient; spool on
  failure + drain on startup; envelope + `schema_version` round-trip; governance
  gate (`enabled=false` → no send).
- **Live (stub)**: an httptest / `scripts` stub `/v1/client-events` sink; trigger
  an anomaly + a forced-high-RSS condition; assert the posted batch shape +
  redaction + correlation.

## Risks / notes

- Cross-platform process-tree RSS/CPU without a new dep: `/proc` on Linux is easy;
  macOS/Windows need a minimal probe (or accept `gopsutil` — decide in the plan).
  The sidecar already uses `psutil` Python-side, but this watcher is Go-side.
- Volume: gauges every 5 min ≈ 288/install/day + sparse anomalies — bounded and
  governable via `sample_rate`/`gauges_enabled`.
- `client_telemetry` settings + `/v1/client-events` are the **contract with B**;
  keep them versioned and documented so B/C build against a stable interface.
