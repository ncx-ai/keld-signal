# Signal gauge distribution — keld-cli (A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** The `keld-agent` resource watcher aggregates its per-tick sub-samples into a per-interval distribution (min/max/mean/std/last, n, window_s) for RSS + CPU and emits that as the `resource.gauge`, instead of one instantaneous sample. Bump the wire `SchemaVersion` to 2 and document it.

**Architecture:** `internal/agent/clientevents/resource/watcher.go` gains a per-metric running accumulator folded in `Poll()`; the gauge branch emits the computed distribution and resets. Anomaly detection (`pollTrack`) is untouched. Sample interval default → 10s. `SchemaVersion` 1→2. Contract doc updated.

**Tech Stack:** Go; existing `clientevents`/`resource` packages; standard testing.

**Spec:** `docs/superpowers/specs/2026-07-13-signal-gauge-distribution-design.md`.

## Global Constraints
- Only the **gauge** changes; `pollTrack` sustained-high anomaly logic is unchanged (still uses the instantaneous per-tick value).
- Nested `rss`/`cpu` gauge fields MUST be `map[string]any` (float64 values) so the redaction gate preserves them (a `map[string]float64` is dropped — cf. `treeAsAny`).
- `Poll()` stays pure/deterministic (sampler + clock injected) — no real I/O added.
- `std` = population std `sqrt(max(0, sumsq/n − mean²))`, `0` when `n<2`. `mean=sum/n`.
- First poll (lastGaugeAt zero) emits a baseline gauge from the single sample (n=1, std=0) then resets — preserves prompt first-gauge.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Test: `go test ./internal/agent/clientevents/...`; build `go build ./...`; `gofmt`.

---

## Task 1: Watcher interval distribution + SchemaVersion + sample default

**Files:** modify `internal/agent/clientevents/resource/watcher.go`, `internal/agent/clientevents/resource/watcher_test.go`, `internal/agent/clientevents/event.go`, `internal/agent/clientevents/event_test.go`, `internal/agent/daemon/daemon.go`.

**Interfaces/behavior produced:** the `resource.gauge` emitGauge payload becomes
`{rss: map[string]any{min,max,mean,std,last}, cpu: {...}, n int, window_s float64, proc_tree}`. `clientevents.SchemaVersion == 2`.

- [ ] **Step 1: Write failing tests.**
  - `event_test.go`: assert `SchemaVersion == 2` (update the existing `==1` assertion).
  - `watcher_test.go`: with a scripted sampler + fake clock, feed several sub-samples within one gauge interval and assert the emitted gauge distribution. Example: gauge interval 300s, clock advancing 100s per Poll; samples rss_mb = [100, 300, 200] (cpu = [10,30,20]) across ticks that stay within the interval, then a tick that crosses the 300s boundary triggers the emit. Assert the captured gauge fields:
    - `rss["min"]==100`, `rss["max"]==300`, `rss["mean"]==200`, `rss["last"]==<last sample before emit>`, `rss["std"]≈81.65` (population std of {100,200,300}=sqrt(20000/3)≈81.65), and same-shaped `cpu`.
    - `n == <count>`, `window_s` ≈ elapsed seconds.
    - The nested `rss`/`cpu` are `map[string]any`.
  - Add a **first-poll baseline** test: first Poll emits a gauge with `n==1`, `std==0`, min==max==mean==last==the sample.
  - Add a **reset** test: after an emit, the next interval's gauge stats reflect only the new interval's samples (not the prior interval's).
  Capture the gauge via the `emitGauge` closure appending to a slice (existing test pattern).

- [ ] **Step 2: Run → fail.** `go test ./internal/agent/clientevents/...` → FAIL (SchemaVersion 1≠2; gauge still flat rss_mb/cpu_pct).

- [ ] **Step 3: Implement.**
  - `event.go`: `const SchemaVersion = 2`.
  - `watcher.go`: add a small accumulator type + two fields on `Watcher`:
    ```go
    type acc struct{ n int; sum, sumsq, min, max, last float64 }
    func (a *acc) add(v float64) {
        if a.n == 0 || v < a.min { a.min = v }
        if a.n == 0 || v > a.max { a.max = v }
        a.n++; a.sum += v; a.sumsq += v * v; a.last = v
    }
    func (a *acc) stats() map[string]any {
        mean := 0.0
        if a.n > 0 { mean = a.sum / float64(a.n) }
        varp := 0.0
        if a.n > 1 { varp = a.sumsq/float64(a.n) - mean*mean; if varp < 0 { varp = 0 } }
        return map[string]any{"min": a.min, "max": a.max, "mean": mean,
            "std": math.Sqrt(varp), "last": a.last}
    }
    func (a *acc) reset() { *a = acc{} }
    ```
    Add `rssAcc, cpuAcc acc` and `gaugeStartAt time.Time` to `Watcher`. In `Poll()`, BEFORE the gauge-cadence check, fold the sample: `w.rssAcc.add(s.RSSMB); w.cpuAcc.add(s.CPUPct)`. Change the gauge branch to:
    ```go
    if w.lastGaugeAt.IsZero() || now.Sub(w.lastGaugeAt) >= th.GaugeInterval {
        windowS := 0.0
        if !w.gaugeStartAt.IsZero() { windowS = now.Sub(w.gaugeStartAt).Seconds() }
        w.emitGauge(map[string]any{
            "rss": w.rssAcc.stats(), "cpu": w.cpuAcc.stats(),
            "n": w.rssAcc.n, "window_s": windowS, "proc_tree": treeAsAny(s.Tree),
        })
        w.rssAcc.reset(); w.cpuAcc.reset()
        w.lastGaugeAt = now
        w.gaugeStartAt = now
    }
    ```
    (Add `import "math"`.) Note: fold the sample into the acc BEFORE the emit check so the current sample is included in the interval it closes; the first-poll baseline then has n=1. Reset zeroes the accs for the next interval. `gaugeStartAt` tracks the interval start for `window_s` (0 on first).
  - `daemon.go`: change `sampleInterval := 15 * time.Second` → `10 * time.Second`.

- [ ] **Step 4: Run → pass** (`go test ./internal/agent/clientevents/...`) + `go build ./...` + `gofmt -l internal/agent/clientevents/` empty + `go test ./...` (no regressions — the daemon still compiles/wires the new gauge via emitter.EmitGauge which stores fields opaquely).

- [ ] **Step 5: Commit** `feat(clientevents): resource gauge emits per-interval distribution (min/max/mean/std); schema v2`.

---

## Task 2: Wire-contract doc

**Files:** modify `docs/signal-client-events.md`.

- [ ] **Step 1:** Update the `resource.gauge` fields description (around the "Resource events" section) to the new nested shape: `rss`/`cpu` objects each with `min`/`max`/`mean`/`std`/`last`, plus `n` and `window_s`, plus `proc_tree`. Note `schema_version` is now `2`, and that gauges from pre-v2 clients carry the legacy flat `{rss_mb, cpu_pct}` (Atlas normalizes both — mean=rss_mb, std absent). Update the `SchemaVersion`/envelope reference from 1 to 2 where the doc states the current version. Keep the anomaly-event field rows (`rss_mb`/`cpu_pct` on `resource.sustained_high_*`) unchanged — those still carry the instantaneous value.

- [ ] **Step 2: Commit** `docs: signal client-events schema v2 — resource gauge distribution shape`.

---

## Self-Review
- Spec coverage: watcher distribution (T1) ✓; SchemaVersion 2 (T1) ✓; sample default 10s (T1) ✓; contract doc + backward-compat note (T2) ✓. Anomaly path untouched (T1 changes only the gauge branch) ✓.
- Placeholders: none — accumulator + stats + gauge emit are concrete code; tests give hand-computed expected values.
- Type consistency: `rss`/`cpu` are `map[string]any` (redaction-safe); `n` int, `window_s` float64; `SchemaVersion` const used by the envelope. Gauge consumed downstream (B) reads `fields.rss.{...}` / falls back to `fields.rss_mb`.
- Privacy: gauge carries only numeric stats + proc_tree numbers — passes the redaction gate; no new string/free-text fields.
