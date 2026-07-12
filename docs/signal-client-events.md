# Signal Client Events — wire contract

`keld-agent` (the on-device enrichment daemon) emits structured **operational**
events about itself — job retries, sidecar crashes, publish failures, resource
pressure, lifecycle — and batches them to Keld Atlas. This is **not** the
enrichment pipeline: it carries no prompt content, no enrichment `Profile`, no
masked spans. It's operability/observability telemetry for the client fleet —
"is this agent healthy," not "what is this prompt about." This document is the
wire contract: everything Atlas needs to build the ingest route (storage) and
a dashboard, without reading the Go source.

Implementation lives in `internal/agent/clientevents/` (+
`internal/agent/clientevents/resource/` for the resource watcher) and is wired
into `internal/agent/daemon/daemon.go`. The org-governance knobs are in
`internal/agent/settings/client_telemetry.go`.

## Route + auth

```
POST /v1/signal/client-events
x-keld-ingest-token: <ingest token>
Content-Type: application/json
```

Same bearer-less header convention as the existing `/v1/enrichments` publish
and `/v1/enrichment-settings` poll routes — **`x-keld-ingest-token`**, not
`Authorization: Bearer`. `/v1/signal/*` is the namespace for **new**
client↔Atlas protocol routes going forward (this is the first one); the two
pre-existing routes above predate the convention and are not being renamed as
part of this change — migrating them is a separate, coordinated cross-repo
follow-up.

The daemon derives this URL from its configured ingest endpoint by swapping
the trailing `/v1/…` segment for `/v1/signal/client-events` (same pattern the
settings-poll endpoint uses for `/v1/enrichment-settings`).

## Envelope

Each POST body is one **batch**: a schema-versioned array of events plus the
install identity that produced them.

```json
{
  "schema_version": 1,
  "install_id": "a3f9c2e1b8d04f7a9c1e2b3d4f5a6b7c",
  "events": [
    {
      "code": "job.retry_exhausted",
      "severity": "warn",
      "fields": { "attempts": 4, "timeout_s": 30 },
      "corr": {
        "org": "org_123",
        "actor": "dg@keld.co",
        "install_id": "a3f9c2e1b8d04f7a9c1e2b3d4f5a6b7c",
        "run_id": "b7e1...",
        "session_id": "sess_abc",
        "prompt_id": "prompt_xyz",
        "version": "0.4.2",
        "os": "linux",
        "arch": "amd64"
      },
      "ts": "2026-07-12T18:04:22.501Z"
    }
  ]
}
```

- **`schema_version`** (int) — currently `1` (`clientevents.SchemaVersion` in
  `internal/agent/clientevents/event.go`). Bump on any breaking change to the
  `Event`/`Corr` shape or code catalog; Atlas should reject or version-branch
  on an unrecognized value rather than guess.
- **`install_id`** (string) — duplicated at the envelope level and inside each
  event's `corr.install_id` (same value) so a receiver can key/shard on the
  envelope without decoding every event.
- **`events`** — a non-empty array of `Event` (an empty/nil batch is never
  posted — the reporter no-ops instead).

## `Event` shape

| JSON field | Type | Tag | Meaning |
|---|---|---|---|
| `code` | string | `code` | Dotted event code, e.g. `job.retry_exhausted`. See the code catalog below for the full enumerated set. |
| `severity` | string | `severity` | One of `info` \| `warn` \| `error` \| `critical` (see Severities). |
| `fields` | object | `fields,omitempty` | Code-specific structured metadata. Values are numbers, bools, `time.Duration`, strings, or nested objects **only** — see Privacy redaction. Omitted entirely when empty. |
| `corr` | object | `corr` | Correlation metadata, see `Corr` below. |
| `ts` | string (RFC 3339) | `ts` | Event timestamp (Go `time.Time`, marshals as RFC 3339 with fractional seconds, e.g. `2026-07-12T18:04:22.501Z`). |

**Coalescing:** consecutive buffered events with the same `code` + `severity`
collapse into one entry with a `fields.count` incremented on repeat, rather
than growing the batch unboundedly for a hot/repeating condition. A receiver
should treat a missing `count` as `1`.

### `Corr` shape

| JSON field | Type | Tag | Meaning |
|---|---|---|---|
| `org` | string | `org,omitempty` | Org id, when known (resolved from the local auth token at daemon startup). |
| `actor` | string | `actor,omitempty` | Signed-in principal (user), when known. |
| `install_id` | string | `install_id` | Stable per-machine id — see Stable install id below. |
| `run_id` | string | `run_id` | Fresh id generated once per daemon process start (distinguishes restarts on the same install). |
| `session_id` | string | `session_id,omitempty` | Present only on job-scoped events (stamped via `Emitter.WithJob`); absent on daemon-level events (lifecycle, resource, settings-poll). |
| `prompt_id` | string | `prompt_id,omitempty` | Present only on job-scoped events, alongside `session_id`. |
| `version` | string | `version` | Client (CLI/daemon) version string. |
| `os` | string | `os` | `runtime.GOOS` (`linux`, `darwin`, `windows`). |
| `arch` | string | `arch` | `runtime.GOARCH` (`amd64`, `arm64`). |

`org`/`actor` are best-effort (empty string if the local auth token can't be
loaded at startup) — do not treat their absence as anomalous.

## Severities

`info` < `warn` < `error` < `critical` (ordinal, used for the floor check
below). No other values are ever emitted.

**Severity floor.** The org-governed `min_severity` setting (default `warn`)
gates most events: anything below the floor is dropped before it's ever
buffered. Two exceptions are **floor-exempt** (always pass through once
telemetry is enabled at all, regardless of `min_severity`):
- the lifecycle events `daemon.start` / `daemon.stop` (so the narrative of
  "was this agent even running" survives a strict floor), and
- all `resource.gauge` snapshots (gauges are periodic health data, not
  alerts — a `warn` floor shouldn't blind Atlas to baseline resource use).

An org can also disable client telemetry entirely (`enabled: false`), or
sample it (`sample_rate`) — see the settings block below.

## Code catalog

Every code actually emitted by the daemon today. `where` is the emitting
package/file for a receiver author who wants to trace the source.

| Code | Severity | Where | What / when |
|---|---|---|---|
| `daemon.start` | info (floor-exempt) | `daemon/daemon.go` | Daemon finished binding its loopback listener and wrote `agent.json`; fires once per process start. `fields.port`. |
| `daemon.stop` | info (floor-exempt) | `daemon/daemon.go` | Daemon's shutdown goroutine observed context cancellation (graceful shutdown beginning). |
| `job.retry_exhausted` | warn | `daemon/daemon.go` (Worker loop) | An enrichment job hit its per-job timeout (`KELD_ENRICH_JOB_TIMEOUT`) enough times to exceed `KELD_ENRICH_MAX_ATTEMPTS`. `fields.attempts`, `fields.timeout_s`. Always followed by a `job.quarantined` event for the same job. |
| `job.quarantined` | warn (normal path) or error (quarantine write failed) | `daemon/daemon.go` | A retry-exhausted job's pointer was moved to `spool.Quarantine` (`~/.keld/spool/bad/`). warn = quarantine write succeeded, `fields.attempts`; error = the quarantine write itself failed (job pointer may be lost), `fields.error`. |
| `job.respool_failed` | error | `daemon/daemon.go` | A timed-out job (not yet exhausted) failed to re-spool to disk for a later retry — the durability guarantee broke for this job. `fields.error`, `fields.timeout_s`. |
| `worker.panic` | error | `daemon/daemon.go` (`process()`'s recovered panic) | The single enrichment worker goroutine panicked mid-job; recovered so the daemon keeps running. `fields.error`. |
| `worker.crash` | warn | `daemon/supervisor.go` | The sidecar child process exited and is being restarted (under the restart cap). `fields.restart`, `fields.max_restarts`, `fields.backoff_s`. |
| `sidecar.fallback` | warn (pre-supervisor) or error (supervisor-level) | `daemon/daemon.go` (port-alloc failure, warn) and `daemon/supervisor.go` (spawn/start failure or restart cap exceeded, error) | The sidecar could not be brought up at all: no ephemeral port, `exec.Cmd` build/start failure, or the restart cap was exceeded. The deterministic model is used as long as this condition holds. `fields.error` (or `fields.restarts` for cap-exceeded). |
| `publish.failed` | error | `daemon/daemon.go` (`process()`) | POSTing a completed enrichment to Atlas (`/v1/enrichments`) failed. `fields.error`. |
| `model.load_failed` | error | `daemon/daemon.go` (`mlBackendWithOpts`) | Model provisioning (`provision.EnsureModel` — fetching/verifying the ~1.9 GB GLiNER2 weights) failed. `fields.error`. **Note:** the original design spec called this `provision.failed`; there is only one provisioning call site in the code, so it was folded into `model.load_failed` rather than kept as a separate code — this doc reflects the actual (single) code. |
| `settings.poll_failed` | warn | `daemon/daemon.go` (`pollSettings`) | A `GET /v1/enrichment-settings` fetch failed (network error, non-2xx, decode error). Non-fatal — the daemon keeps its last-known effective settings. `fields.error`. |
| `resource.sustained_high_rss` | warn / error / critical (escalating), or the same severity on recovery | `clientevents/resource/watcher.go` | RSS across the daemon+sidecar+worker process tree has stayed above `rss_threshold_mb` for at least `sustained_window_s`. See Resource events below. |
| `resource.sustained_high_cpu` | warn / error / critical (escalating), or the same severity on recovery | `clientevents/resource/watcher.go` | Same as above, for summed CPU% across the process tree vs. `cpu_threshold_pct`. |
| `resource.gauge` | info (floor-exempt) | `clientevents/resource/watcher.go` | Periodic baseline resource snapshot, independent of anomaly state. See Resource events below. |

All `fields.error` / `fields.…error` values are the output of
`clientevents.RedactError` (see Privacy redaction) — a short `"<Type>:
<message>"` string, never a raw Go error, and never containing an absolute
path or raw multi-line text. The `<Type>` half always survives (Go type names
carry no user data, so they're always safe to publish, and remain useful for
classification even when the message doesn't survive); the `<message>` half is
conservatively redacted to `<redacted>` when — even after path-stripping — it
still reads as free text (more than a few words) or still contains a
control/format character. A short, simple message (e.g. `connection refused`)
survives verbatim; a prose or prompt-shaped message does not.

## Resource events — semantics

The resource watcher (`internal/agent/clientevents/resource/watcher.go`)
samples the daemon's full process tree (daemon + sidecar + inference worker,
walked via `gopsutil`) on a fixed interval (daemon-side default 15s, not
org-configurable) and drives two independent hysteresis/escalation state
machines — one for RSS, one for CPU — plus a gauge cadence.

**Escalation (edge-triggered, one event per bucket crossing):**
- A track becomes "elevated" the instant its value exceeds its threshold
  (`rss_threshold_mb` / `cpu_threshold_pct`).
- Once continuously elevated for at least `sustained_window_s`, it emits
  **warn**.
- Still elevated at ≥ 2× `sustained_window_s` → escalates to **error**.
- Still elevated at ≥ 4× `sustained_window_s` → escalates to **critical**.
- Each bucket transition emits exactly one event (no re-emission while
  parked in the same bucket).

**Recovery:** the instant a previously-elevated track drops back to/below
threshold, exactly one event fires with `fields.recovered: true` (same field
shape as the anomaly, plus that flag), **at the same severity as the anomaly
it clears** (the track's peak bucket for that episode — e.g. an episode that
escalated to error recovers at error, not info), and the track's state
resets — a fresh elevation starts again at warn. Emitting the recovery at a
fixed `info` severity would mean it gets dropped by the default `warn`
severity floor even though the anomaly that preceded it passed the floor —
leaving the track looking permanently elevated on the dashboard. Using the
same severity as the anomaly means the recovery is delivered if and only if
its anomaly was (no orphan recoveries, no floor bypass).

**Fields** (both `resource.sustained_high_rss` and
`resource.sustained_high_cpu`, anomaly and recovery alike):

| Field | Type | Meaning |
|---|---|---|
| `rss_mb` (RSS code) / `cpu_pct` (CPU code) | number | Current summed value across the process tree. Note: summed `cpu_pct` can exceed 100 on multi-core hosts — each process can independently saturate a core. |
| `threshold` | number | The threshold that was crossed, at time of emission. |
| `elevated_s` | number | Seconds continuously elevated so far. |
| `proc_tree` | object | Per-role breakdown, see below. |
| `recovered` | bool | Present (`true`) only on the recovery event, which carries the same `severity` as the anomaly it clears (see Recovery above). |

**`proc_tree`:** currently a flat object with two keys — `"daemon"` (the root
process only) and `"others"` (every descendant process — sidecar service +
inference worker — summed together). A per-role breakdown (`daemon` /
`sidecar` / `worker` as distinct keys) is aspirational, noted in the code
comments, but not yet implemented; document the actual two-key shape.

**`resource.gauge`:** an unconditional info snapshot emitted every
`gauge_interval_s` (default 300s) regardless of anomaly state, so Atlas has a
steady-state baseline even when nothing is elevated. Only emitted when
`gauges_enabled` is true. Fields: `rss_mb`, `cpu_pct`, `proc_tree` (same shape
as above; no `threshold`/`elevated_s`, since it's not an anomaly).

## `client_telemetry` settings block

Client-events behavior is governed per-org, riding the **existing**
`GET /v1/enrichment-settings` poll (`internal/agent/settings/remote.go`) — it
is **not yet** a `/v1/signal/*` route; the block is just an additional
optional key on the settings document the daemon already polls every
`KELD_SETTINGS_POLL` (default 5 min).

```json
{
  "include_entity_text": false,
  "client_telemetry": {
    "enabled": true,
    "min_severity": "warn",
    "sample_rate": 1.0,
    "gauges_enabled": true,
    "gauge_interval_s": 300,
    "rss_threshold_mb": 4096,
    "cpu_threshold_pct": 150,
    "sustained_window_s": 120
  }
}
```

| Field | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | Master on/off for client-events emission. Client telemetry is **default ON** and independent of enrichment being enabled/disabled. |
| `min_severity` | string | `"warn"` | Severity floor (see Severities); one of `info`/`warn`/`error`/`critical`. |
| `sample_rate` | float | `1.0` | Fraction of (post-floor) events kept, `[0,1]`. `1.0` keeps everything, `0.0` drops everything. |
| `gauges_enabled` | bool | `true` | Whether periodic `resource.gauge` snapshots are emitted at all. |
| `gauge_interval_s` | int | `300` | Seconds between `resource.gauge` snapshots. |
| `rss_threshold_mb` | float | `4096` | RSS (MB, summed across the process tree) above which a track becomes "elevated". |
| `cpu_threshold_pct` | float | `150` | CPU% (summed across the process tree) above which a track becomes "elevated". |
| `sustained_window_s` | int | `120` | Seconds a track must stay continuously elevated before the first (warn) anomaly event fires; also the unit for the 2×/4× escalation to error/critical. |

Every field is optional/nullable on the wire (pointer types Go-side) — an
absent block, or an absent individual field within it, resolves to the
default above. This makes the block **forward-compatible**: an older Atlas
that predates `client_telemetry` entirely, or a newer daemon talking to an
older Atlas, degrades to the defaults rather than breaking.

## Privacy redaction guarantee

Client events carry **operational** metadata only — ids, counts, durations,
enum-like strings — never prompt content. This is enforced Go-side
(`internal/agent/clientevents/redact.go`) before an event is even buffered:

- **Type allow-list.** An event's `fields` map is rebuilt value-by-value: only
  numbers, bools, and `time.Duration` pass through unchanged; nested
  `map[string]any` recurses under the same rules; anything else (slices,
  structs, pointers, `map[string]<non-any>`) is **dropped** rather than risk
  publishing an unvetted shape.
- **String redaction.** Every string value is checked for:
  1. any control or invisible-formatting character (newlines, tabs, zero-width
     joiners, …) → replaced wholesale with `"<redacted>"`;
  2. the whole value being a single absolute path (POSIX, Windows drive, or
     UNC) → reduced to just its basename;
  3. an absolute path embedded among other text → the **entire** value becomes
     `"<redacted>"` (never surgically stripped, to avoid leaking a fragment
     around the redacted path);
  4. otherwise, a free-text cap: longer than 120 bytes or more than 3
     whitespace-separated words → `"<redacted>"`. Short enums/status
     codes/error reasons pass; prose does not.
- **Errors are never raw.** Any error placed into `fields` goes through
  `RedactError`, which collapses it to a single-line, length-capped
  (~200-rune) `"<Type>: <message>"` summary with any embedded absolute path
  surgically replaced by `<path>` first — never a verbatim `%v` of the
  original error, never multi-line text. The message half then gets the SAME
  free-text protection as a plain field value (item 4 above, applied to the
  message only): if it's still more than a few words, still too long, or
  still contains a control/format character after path-stripping, the message
  becomes `"<redacted>"` while the `<Type>` prefix survives. This is what lets
  `RedactError`'s output skip the generic string redaction above when it's
  later placed in `fields` — it's pre-vetted (path-stripped, free-text
  capped) by `RedactError` itself, so it is not run back through the
  whole-value free-text cap (which would otherwise double-redact it: a
  second, unconditional 3-word cap over an already-short `"<Type>: <message>"`
  summary would clobber nearly every non-trivial error into a bare
  `"<redacted>"`, discarding the type information the first pass deliberately
  preserved).

This is the same no-raw-prompt-text invariant the enrichment pipeline upholds
for masked spans — client events simply never touch prompt content in the
first place, so there's nothing to mask; the gate exists to catch incidental
leaks (e.g. a file path or a wrapped I/O error) in operational metadata.

## Offline durability (spool)

The reporter (`internal/agent/clientevents/reporter.go`) buffers events
in-memory and flushes on a timer (default **30s**, `KELD_CLIENTEVENTS_FLUSH`
to override) plus a best-effort final flush on daemon shutdown:

- Transient POST failures (network errors, HTTP 408/429/5xx) are retried via
  `internal/retry`; a batch that still fails after retries is written to an
  on-disk spool directory, **`~/.keld/spool/clientevents/`**
  (`$KELD_HOME/spool/clientevents`), as one JSON file per batch.
- The spool is **bounded** (256 files by default) and **drop-oldest**: once
  over the cap, the oldest spooled batches are deleted to make room for new
  ones — client-events durability trades old data for new under sustained
  Atlas unavailability, it does not grow unbounded.
- Spooled batches are drained (re-POSTed, oldest first) on daemon startup and
  on every subsequent flush tick. A permanent failure (e.g. 400/401) is
  treated as poison and the file is dropped rather than retried forever; a
  transient failure stops the sweep for that tick (tried again next tick)
  rather than spinning.
- Atlas should expect **at-least-once** delivery per event (a batch that
  succeeds on Atlas's side but whose response is lost to a network fault
  before the daemon can process it will be re-posted). There is currently no
  client-side dedup key on individual client events (unlike `/v1/enrichments`,
  which dedups on `dedup_key`) — if exactly-once semantics matter to a
  consumer, dedup on `(install_id, run_id, code, ts)` or similar is an Atlas
  B-side concern.

## Stable identity

- **`install_id`** — a random 32-hex-char id generated once and persisted at
  `~/.keld/install-id` (`internal/paths.InstallIDPath` +
  `clientevents.InstallID()`), reused across daemon restarts. Written
  atomically (temp file + rename) so a torn write can't corrupt it; an
  empty/unreadable file is treated as absent and regenerated.
- **`run_id`** — freshly generated once per daemon process start; distinguishes
  individual restarts of the same install (e.g. for narrative reconstruction
  around a crash-loop).
