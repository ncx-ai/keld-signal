# Signal Client telemetry capture (A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** keld-signal captures structured client events (errors/anomalies + process-tree RAM/CPU sustained-high) with full correlation, redacts to uphold the no-raw-text invariant, and posts them (governed, durable, retried) to `POST /v1/signal/client-events`.

**Architecture:** New `internal/agent/clientevents` (types + redaction + non-blocking `Emit` + reporter) and `internal/agent/clientevents/resource` watcher (gopsutil process-tree sampling + sustained detection). Instrumented `daemon.go` sites call `Emit` alongside existing `log.Printf`. Governance rides the existing settings poll (`client_telemetry` block). Transport reuses `internal/retry` + a disk spool.

**Tech Stack:** Go; `github.com/shirou/gopsutil/v4` (process RSS/CPU); `internal/retry`; `internal/spool` pattern; httptest for tests.

## Global Constraints
- **Privacy invariant:** events carry only ids + structured metadata; NEVER raw prompt text. The redaction gate is enforced Go-side before buffering; unit-tested to prove no free-text escapes.
- Events are **additive** to existing `log.Printf` — no daemon behavior change.
- Governed by control-plane `client_telemetry` (default ON, min_severity `warn`, sample_rate 1.0), independent of the enrichment toggle; forward-compatible (absent settings → defaults).
- Route: `POST /v1/signal/client-events` (the `/v1/signal/*` convention). Wire envelope has `schema_version` (`clientevents.SchemaVersion`).
- `Emit` is non-blocking + bounded (drop-oldest, coalesce repeats) — must never stall the daemon.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

---

## File Structure
- `internal/paths/paths.go` (modify) — `InstallIDPath()`.
- `internal/agent/clientevents/install.go` (+ test) — stable install-id (generate-once).
- `internal/agent/clientevents/event.go` (+ test) — `Event`/`Corr`/`Severity`, `SchemaVersion`, severity ordering.
- `internal/agent/clientevents/redact.go` (+ test) — the redaction gate.
- `internal/agent/clientevents/emitter.go` (+ test) — `Emitter` (Corr, bounded ring, floor+sample, `Emit`/`WithJob`).
- `internal/agent/clientevents/reporter.go` (+ test) — batch/flush/retry/spool/drain, endpoint.
- `internal/agent/clientevents/resource/watcher.go` (+ test) — gopsutil sampler + sustained detection + gauges.
- `internal/agent/settings/*.go` (modify + test) — parse optional `client_telemetry`.
- `internal/agent/daemon/daemon.go` (modify) — init Corr, start watcher+reporter, instrument sites, gate on settings.
- `docs/signal-client-events.md` (new) — the wire contract for B/C.
- `AGENTS.md` (modify) — the feature + the `/v1/signal/*` convention.

---

## Task 1: Stable install-id

**Files:** `internal/paths/paths.go` (modify); create `internal/agent/clientevents/install.go`, `internal/agent/clientevents/install_test.go`.

**Interfaces produced:**
- `paths.InstallIDPath() string` → `~/.keld/install-id`
- `clientevents.InstallID() (string, error)` — read the id, generating+persisting a random uuid (0600) on first call; stable thereafter.

- [ ] **Step 1: failing test** — `install_test.go`: with `KELD_HOME=t.TempDir()`, `InstallID()` returns a non-empty id; a second call returns the SAME id; the file exists at `paths.InstallIDPath()` with 0600.
- [ ] **Step 2: run → fail** (`go test ./internal/agent/clientevents/` — undefined).
- [ ] **Step 3: implement.** `paths.InstallIDPath` = `filepath.Join(KeldHome(), "install-id")`. `InstallID()`: read file → if present return trimmed; else generate `crypto/rand` 16 bytes → hex, `os.MkdirAll(KeldHome,0755)`, `os.WriteFile(path, id, 0600)`, return. Handle concurrent-first-write races benignly (re-read on write error).
- [ ] **Step 4: run → pass.**
- [ ] **Step 5: commit** `feat(clientevents): stable per-install id`.

---

## Task 2: Event types + severity + schema version

**Files:** create `internal/agent/clientevents/event.go`, `event_test.go`.

**Interfaces produced:**
```go
const SchemaVersion = 1
type Severity string
const ( SevInfo Severity = "info"; SevWarn = "warn"; SevError = "error"; SevCritical = "critical" )
func (s Severity) rank() int // info<warn<error<critical
func (s Severity) AtLeast(min Severity) bool
type Corr struct { Org, Actor, InstallID, RunID, SessionID, PromptID, Version, OS, Arch string; /* json tags per spec */ }
type Event struct { Code string; Severity Severity; Fields map[string]any; Corr Corr; TS time.Time /* json tags */ }
```

- [ ] **Step 1: failing test** — severity ordering (`SevError.AtLeast(SevWarn)==true`, `SevInfo.AtLeast(SevWarn)==false`); `Event` JSON round-trip includes `code`/`severity`/`corr`/`ts` and omits empty `fields`; `SchemaVersion==1`.
- [ ] **Step 2: run → fail.**
- [ ] **Step 3: implement** the types + json tags (per the spec's struct) + `rank`/`AtLeast`.
- [ ] **Step 4: run → pass.**
- [ ] **Step 5: commit** `feat(clientevents): event/corr/severity types + schema version`.

---

## Task 3: Redaction gate (privacy invariant)

**Files:** create `internal/agent/clientevents/redact.go`, `redact_test.go`.

**Interfaces produced:** `func redactFields(in map[string]any) map[string]any` — returns a copy where each value is allow-listed: numbers/bools/durations/short enums pass; strings are (a) rejected if they look like free text (contain spaces beyond a small cap OR exceed `maxFieldLen` 120) → replaced with `"<redacted>"`, (b) absolute filesystem paths → basename-only or `"<path>"`; nested maps recurse; unknown types → dropped. `func RedactError(err error) string` — error → a short class/summary (type name + a redacted, length-capped message with paths stripped), never raw multi-line/text.

- [ ] **Step 1: failing test** — an absolute path (`/home/dg/keld/x.json`) → not present verbatim (basename or `<path>`); a long prose string (>120 chars, spaces) → `<redacted>`; numbers/short enums (`"deadline"`, `503`, `4`) pass unchanged; `RedactError(fmt.Errorf("open /home/u/secret.txt: denied"))` contains no `/home/u/secret.txt`. A field literally set to a fake "prompt" sentence is redacted.
- [ ] **Step 2: run → fail.**
- [ ] **Step 3: implement** the allow-list + path stripping (regex for absolute paths `/[^ ]+` and Windows `[A-Za-z]:\\...`; cap length; count spaces). Keep it conservative — when in doubt, redact.
- [ ] **Step 4: run → pass.**
- [ ] **Step 5: commit** `feat(clientevents): privacy redaction gate for event fields`.

---

## Task 4: Emitter (non-blocking bounded ring + gating)

**Files:** create `internal/agent/clientevents/emitter.go`, `emitter_test.go`.

**Interfaces produced:**
```go
type Gate struct { Enabled bool; MinSeverity Severity; SampleRate float64 } // from settings; zero-value = disabled
type Emitter struct { /* corr, ring, gate (atomic), clock, rand */ }
func NewEmitter(base Corr, capacity int) *Emitter
func (e *Emitter) SetGate(g Gate)                       // called by settings poll
func (e *Emitter) Emit(code string, sev Severity, fields map[string]any)
func (e *Emitter) WithJob(session, prompt string) *JobEmitter // Emit stamps SessionID/PromptID
func (e *Emitter) Drain() []Event                        // reporter pulls the batch
```

- [ ] **Step 1: failing test** — `Emit` stamps base Corr + `ts`; below-`MinSeverity` events dropped; `Enabled=false` → dropped; `SampleRate=0` → dropped, `1.0` → kept; ring caps at capacity (drop-oldest); `Emit` runs redaction (a path field is redacted in the drained event); `WithJob` stamps session/prompt; `Emit` never blocks (fill past capacity in a tight loop returns promptly). Use an injected clock; `Info` gauges bypass the floor via a dedicated `EmitGauge`/exemption (see note).
- [ ] **Step 2: run → fail.**
- [ ] **Step 3: implement.** Ring = mutex + slice (drop-oldest on overflow) + coalesce (if the last buffered event has the same `code`+`severity`, increment a `Fields["count"]` instead of appending). Gate stored via `atomic.Value`. `Emit` applies floor (unless the event is a gauge — provide `EmitGauge(fields)` that is exempt from `MinSeverity` but still honors `Enabled`+`SampleRate`), sample (rand), redaction, then enqueue. Never blocks (bounded, non-blocking mutex section only).
- [ ] **Step 4: run → pass.**
- [ ] **Step 5: commit** `feat(clientevents): non-blocking bounded emitter with governance gate`.

---

## Task 5: Reporter (batch → retry-POST → spool/drain)

**Files:** create `internal/agent/clientevents/reporter.go`, `reporter_test.go`.

**Interfaces:**
- Consumes: `Emitter.Drain`, `retry.Do`, an injected HTTP `poster` + a spool dir.
- Produces: `type Reporter struct{...}`; `NewReporter(endpoint, token, installID string, drain func() []Event, spoolDir string) *Reporter`; `Run(ctx, interval)`; `flush(ctx) error`; wire envelope `{schema_version, install_id, events}`.

- [ ] **Step 1: failing test** — `flush` posts the drained batch as the envelope (schema_version + install_id + events) with the bearer token, to the endpoint (httptest); a transient 503 is retried via `retry.Do` (inject a fast policy); on persistent failure the batch is spooled to `spoolDir` (files present); `drainSpool` re-posts spooled batches on success and deletes them; empty drain → no POST. No real sleeps.
- [ ] **Step 2: run → fail.**
- [ ] **Step 3: implement.** `flush`: `events := drain(); if len==0 return nil`; marshal envelope; `retry.Do(ctx, policy, func() error { POST; non-2xx → retry.HTTPStatus(code) })`; on final error → write the envelope JSON to `spoolDir/<ts>-<rand>.json`. `Run` loops on a ticker calling `flush` + a periodic `drainSpool`; drains spool on start. Endpoint passed in (daemon derives it — Task 7). Bounded spool (cap file count; drop-oldest).
- [ ] **Step 4: run → pass.**
- [ ] **Step 5: commit** `feat(clientevents): batching reporter with retry + offline spool`.

---

## Task 6: Resource watcher (gopsutil process-tree, sustained detection + gauges)

**Files:** `go.mod`/`go.sum` (add gopsutil); create `internal/agent/clientevents/resource/watcher.go`, `watcher_test.go`.

**Interfaces:**
- Produces: `type Sample struct{ RSSMB, CPUPct float64; Tree map[string]float64 }`; `type Sampler func() Sample`; `type Thresholds struct{ RSSMB, CPUPct float64; SustainedWindow, GaugeInterval time.Duration }`; `type Watcher struct{...}`; `NewWatcher(daemonPID int, emit func(code string, sev Severity, fields map[string]any), emitGauge func(fields map[string]any), th Thresholds, sampler Sampler, clock func() time.Time) *Watcher`; `Poll()` (one tick — pure-ish, testable); `Run(ctx, interval)`.
- The **production sampler** uses `gopsutil/v4/process`: from the daemon pid, collect self + descendants (sidecar + worker), sum `MemoryInfo().RSS` and `CPUPercent()`.

- [ ] **Step 1: failing test** (inject a scripted `Sampler` + fake clock): RSS below threshold → no anomaly; RSS above threshold but < window → no anomaly yet; above threshold ≥ window → one `resource.sustained_high_rss` with `{rss_mb, threshold, elevated_s}`; staying elevated longer → re-emit at escalated severity (warn→error); dropping below → one recovered `info` event + reset (hysteresis, no flap); a gauge emitted every `GaugeInterval`. Same for CPU.
- [ ] **Step 2: run → fail.**
- [ ] **Step 3: implement** `Poll()` state machine (track `elevatedSince`, `lastGauge`, `lastSeverity`); severity escalation by `elevated_s` thresholds; hysteresis (require a below-threshold sample to clear). Add the gopsutil-backed production `Sampler` (`go get github.com/shirou/gopsutil/v4`). Keep `Poll` free of real I/O (sampler injected) so tests are deterministic.
- [ ] **Step 4: run → pass** + `go build ./...`.
- [ ] **Step 5: commit** `feat(clientevents): process-tree resource watcher (sustained-high + gauges)`.

---

## Task 7: Settings `client_telemetry` + daemon wiring

**Files:** modify `internal/agent/settings/*.go` (+ test), `internal/agent/daemon/daemon.go`; instrument the anomaly sites.

- [ ] **Step 1: failing tests** — (a) settings: a response with a `client_telemetry` block parses into the typed struct; ABSENT → defaults (enabled, warn, 1.0, gauges on, default thresholds) — forward-compatible. (b) daemon: a small test that the instrumented site emits (e.g. calling the quarantine path with a fake emitter records a `job.quarantined` event). Keep daemon tests light — assert the emit calls via an injected emitter interface.
- [ ] **Step 2: run → fail.**
- [ ] **Step 3: implement.**
  - `settings`: add an optional `ClientTelemetry *ClientTelemetry` field to the settings shape with a `WithDefaults()` that fills defaults when nil/partial; map it to `clientevents.Gate` + `resource.Thresholds`.
  - `daemon.go`: build base `Corr` (org, actor, `clientevents.InstallID()`, a per-run `run_id`, `version.CLI`, `runtime.GOOS/GOARCH`); construct the `Emitter`, `Reporter` (endpoint via a new `signalClientEventsEndpoint(cfg.Endpoint)` → `/v1/signal/client-events`), and `resource.Watcher` (daemon pid = `os.Getpid()`); start `Run` goroutines under the daemon ctx. Wire the settings poll to call `emitter.SetGate(...)` + update watcher thresholds on each poll. At each anomaly `log.Printf` site, add an `emitter.Emit(code, sev, fields)` with redaction-safe fields (use `clientevents.RedactError` for error values). Emit `daemon.start`/`daemon.stop`. In the worker, use `emitter.WithJob(session, prompt)` for job-scoped events.
- [ ] **Step 4: run → full suite** `go test ./...` (0 fail) + `go build ./...`.
- [ ] **Step 5: commit** `feat(daemon): wire client-events emitter, reporter, resource watcher + settings gate`.

---

## Task 8: Contract docs

**Files:** create `docs/signal-client-events.md`; modify `AGENTS.md`.

- [ ] **Step 1:** `docs/signal-client-events.md` — the wire contract B/C build against: the envelope (`schema_version`, `install_id`, `events[]`), the `Event`/`Corr` shape, the code catalog + severities, the `client_telemetry` settings block, and the `/v1/signal/*` route convention. `AGENTS.md`: a bullet under the enrichment/architecture section describing client-events telemetry + the route convention + the privacy redaction gate.
- [ ] **Step 2: commit** `docs: signal client-events wire contract (interface for Atlas B/C)`.

---

## Task 9: Live verification (stub sink)

Not a code task — validate against a local stub.
- [ ] Build + run a stub `POST /v1/signal/client-events` (httptest binary or `scripts/`), point the daemon at it (`KELD_*` endpoint), trigger an anomaly (e.g. `make send-test-prompt` against a killed sidecar) + a forced-high-RSS condition (low `rss_threshold_mb` via settings/env), and assert the posted batch shape + redaction + correlation + a `resource.sustained_high_rss` event. Confirm `Emit` is non-blocking under load (daemon stays responsive).

---

## Self-Review
- Spec coverage: install-id (T1) ✓; event/severity/schema (T2) ✓; redaction invariant (T3) ✓; non-blocking bounded emitter + gate (T4) ✓; reporter batch/retry/spool (T5) ✓; resource watcher sustained+gauges via gopsutil (T6) ✓; settings gate + daemon wiring + instrumented sites (T7) ✓; contract docs (T8) ✓; live stub (T9) ✓. `/v1/signal/*` convention (T7 endpoint + T8 docs) ✓.
- Placeholders: none — core-logic code is concrete; wiring task names exact files + the endpoint helper.
- Type consistency: `Severity`/`Corr`/`Event`/`SchemaVersion` (T2) used by emitter (T4)/reporter (T5)/watcher (T6)/daemon (T7); `Gate` (T4) fed by settings (T7); `redactFields`/`RedactError` (T3) used by emitter (T4) + daemon sites (T7). Reporter envelope `{schema_version, install_id, events}` matches T8 docs.
- Privacy: redaction is its own task (T3), enforced in `Emit` (T4), and re-checked at daemon sites via `RedactError` (T7); T9 asserts no leak live.
