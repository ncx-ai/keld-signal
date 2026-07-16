# Warm-gated Enrichment Deadline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the enrichment job deadline cover inference only — never model-load — by gating each job on the sidecar being *warm* (model resident now), so cold starts defer instead of timing out.

**Architecture:** Split the conflated readiness signal in two. Liveness (`Supervisor.Ready()`, latched off `/health`) is unchanged and still decides whether the Worker runs. A new non-latching *warmth* signal — sidecar `/metrics` `worker.state == "ready"` — gates when a job's clock starts. The Worker waits (bounded) for warmth before starting the fixed 30s inference deadline; if warmth never arrives in time, the job re-spools **without** consuming its retry budget.

**Tech Stack:** Go 1.26, standard library only (`net/http`, `encoding/json`, `sync/atomic`, `time`).

## Global Constraints

- **Go 1.26**; module `github.com/ncx-ai/keld-signal`. Toolchain at `/opt/homebrew/bin/go` — prefix every `go` command with `export PATH="/opt/homebrew/bin:$PATH"`.
- **Go-only. Do NOT modify the frozen Python sidecar** (`sidecar/`). The warmth signal is read from the sidecar's existing `/metrics` `worker.state` field.
- Warmth = `worker.state == "ready"`; any fetch/parse error ⇒ **not warm** (never falsely start the clock on a cold model).
- Readiness used for the per-job gate must be **non-latching** (reflect current state), unlike `Supervisor.Ready()`.
- Deadline is **static, inference-only**: `KELD_ENRICH_JOB_TIMEOUT` (default 30s) starts only after warmth. New knob `KELD_ENRICH_WARM_WAIT` (Go duration, default **90s**) bounds the per-job warm-wait.
- Model-not-ready ⇒ re-spool **without** incrementing the retry ledger; it must **never** drive quarantine. Only warm-but-slow inference consumes the retry budget.
- No new external dependencies.

## File Structure

- `internal/agent/enrich/sidecar/client.go` — add `WorkerReady(ctx) bool` (reads `/metrics`).
- `internal/agent/enrich/sidecar/client_test.go` — test `WorkerReady`.
- `internal/agent/daemon/warmgate.go` — new: non-latching warmth poller.
- `internal/agent/daemon/warmgate_test.go` — new: poller test.
- `internal/agent/daemon/daemon.go` — Worker bounded warm-wait; `warmWait()`; `waitWarm()`; wire warm gate into `mlBackendWithOpts`.
- `internal/agent/daemon/daemon_test.go` — new Worker warm-wait tests; reconcile gate/stub tests.

---

### Task 1: `client.WorkerReady` — read model-resident state from `/metrics`

**Files:**
- Modify: `internal/agent/enrich/sidecar/client.go` (add method after `Healthy`, ~line 164)
- Test: `internal/agent/enrich/sidecar/client_test.go`

**Interfaces:**
- Consumes: existing `Client{ base string; hc *http.Client }`.
- Produces: `func (c *Client) WorkerReady(ctx context.Context) bool` — true iff `GET /metrics` returns 200 with JSON `worker.state == "ready"`; false on any error.

- [ ] **Step 1: Write the failing test**

Create/append to `internal/agent/enrich/sidecar/client_test.go`:

```go
package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWorkerReady(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"ready", 200, `{"worker":{"state":"ready"}}`, true},
		{"spawning", 200, `{"worker":{"state":"spawning"}}`, false},
		{"missing", 200, `{"worker":{}}`, false},
		{"malformed", 200, `not json`, false},
		{"http error", 503, `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/metrics" {
					t.Fatalf("unexpected path %q", r.URL.Path)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(srv.URL, time.Second)
			if got := c.WorkerReady(context.Background()); got != tc.want {
				t.Fatalf("WorkerReady = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkerReadyTransportError(t *testing.T) {
	c := New("http://127.0.0.1:1", time.Second) // nothing listening
	if c.WorkerReady(context.Background()) {
		t.Fatal("WorkerReady should be false when the sidecar is unreachable")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/sidecar/ -run TestWorkerReady -v`
Expected: FAIL to compile — `c.WorkerReady undefined`.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/enrich/sidecar/client.go`, add after `Healthy` (the file already imports `context`, `encoding/json`, `net/http`):

```go
// WorkerReady reports whether the sidecar's inference worker has the model
// resident RIGHT NOW (GET /metrics, worker.state == "ready"). Unlike Healthy
// (which only proves the HTTP server is up), this reflects post-idle-kill
// reloads: worker.state is "spawning" while the model reloads. Any transport
// or decode error is treated as not-ready, so a caller never starts a job's
// deadline against a cold model.
func (c *Client) WorkerReady(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/metrics", nil)
	if err != nil {
		return false
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var m struct {
		Worker struct {
			State string `json:"state"`
		} `json:"worker"`
	}
	return json.NewDecoder(resp.Body).Decode(&m) == nil && m.Worker.State == "ready"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/sidecar/ -run TestWorkerReady -v`
Expected: PASS (both tests, all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/sidecar/client.go internal/agent/enrich/sidecar/client_test.go
git commit -m "feat(sidecar): add WorkerReady to read model-resident state from /metrics"
```

---

### Task 2: warm-gate poller (non-latching warmth)

**Files:**
- Create: `internal/agent/daemon/warmgate.go`
- Test: `internal/agent/daemon/warmgate_test.go`

**Interfaces:**
- Consumes: a probe `func(context.Context) bool` (production: `client.WorkerReady`).
- Produces: `type warmGate struct{...}`; `func newWarmGate() *warmGate`; `func (g *warmGate) run(ctx context.Context, ready func(context.Context) bool, interval time.Duration)` (blocks until ctx done — run in a goroutine); `func (g *warmGate) Warm() bool` (cheap atomic read); `const warmPollInterval = 500 * time.Millisecond`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/daemon/warmgate_test.go`:

```go
package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestWarmGateObservesTransition(t *testing.T) {
	var probe atomic.Bool // starts false
	g := newWarmGate()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.run(ctx, func(context.Context) bool { return probe.Load() }, time.Millisecond)

	if g.Warm() {
		t.Fatal("warm should start false")
	}
	probe.Store(true)
	deadline := time.After(2 * time.Second)
	for !g.Warm() {
		select {
		case <-deadline:
			t.Fatal("warm never became true after probe flipped")
		case <-time.After(2 * time.Millisecond):
		}
	}
	probe.Store(false)
	for g.Warm() {
		select {
		case <-deadline:
			t.Fatal("warm never went back to false (non-latching)")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestWarmGateStopsOnCancel(t *testing.T) {
	g := newWarmGate()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { g.run(ctx, func(context.Context) bool { return true }, time.Millisecond); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("run did not return after ctx cancel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run TestWarmGate -v`
Expected: FAIL to compile — `undefined: newWarmGate`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/daemon/warmgate.go`:

```go
package daemon

import (
	"context"
	"sync/atomic"
	"time"
)

// warmPollInterval is how often the warm gate re-checks model-resident state.
// Frequent enough to notice a warm transition quickly, cheap enough for the
// sidecar's /metrics endpoint.
const warmPollInterval = 500 * time.Millisecond

// warmGate holds the latest observed "model resident now" state as a
// non-latching atomic bool. It exists because Supervisor.Ready() latches true
// after the first /health success and never reflects a later idle-kill reload;
// the Worker needs the live state to avoid counting model-load time against a
// job's deadline.
type warmGate struct{ warm atomic.Bool }

func newWarmGate() *warmGate { return &warmGate{} }

// run polls ready on interval, storing each result, until ctx is cancelled.
// Intended to run in its own goroutine.
func (g *warmGate) run(ctx context.Context, ready func(context.Context) bool, interval time.Duration) {
	g.warm.Store(ready(ctx)) // seed immediately so we don't wait a full interval
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.warm.Store(ready(ctx))
		}
	}
}

// Warm reports the most recently observed model-resident state (cheap).
func (g *warmGate) Warm() bool { return g.warm.Load() }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run TestWarmGate -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/daemon/warmgate.go internal/agent/daemon/warmgate_test.go
git commit -m "feat(daemon): non-latching warm-gate poller for model-resident state"
```

---

### Task 3: Worker bounded warm-wait (defer without burning an attempt)

**Files:**
- Modify: `internal/agent/daemon/daemon.go` — Worker wait-loop (lines 67-76); add `warmWait()` (beside `jobTimeout`, ~line 184) and `waitWarm()`.
- Test: `internal/agent/daemon/daemon_test.go` (append)

**Interfaces:**
- Consumes: existing Worker signature `Worker(ctx, q, m, pub, actor, includeEntityText, ready func() bool, emitter, ra)`; existing `spool.Write`, `pointerFromJob`, `jobTimeout`, `retryLedger`.
- Produces: `func warmWait() time.Duration`; `func waitWarm(ready func() bool, bound time.Duration, done <-chan struct{}) (warm, closed bool)`. `ready` here is the cheap gate (e.g. `warmGate.Warm`).

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/daemon/daemon_test.go` (uses existing helpers: `enrichtest.NewFake()`, a fake `Sender`, `queue.New`, `t.Setenv`, `pointerFromJob`/spool via `KELD_HOME`; mirror the setup in the existing `TestWorkerTimesOutAndRespools`/`TestWorkerQuarantinesAfterMaxAttempts`):

```go
// A job must WAIT (not burn an attempt) until warm, then publish. With a tiny
// warm-wait and a gate that flips to true, the job should publish exactly once.
func TestWorkerWaitsForWarmThenPublishes(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ENRICH_WARM_WAIT", "5s")

	var warm atomic.Bool // false until we flip it
	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("warm-wait-1")) // helper used by existing tests
	go Worker(context.Background(), q, enrichtest.NewFake(), fs, "t@keld.co",
		func() bool { return false }, warm.Load, nil, nil)

	time.Sleep(50 * time.Millisecond) // job pulled, waiting for warm
	if fs.count() != 0 {
		t.Fatal("published before warm")
	}
	warm.Store(true)
	waitFor(t, time.Second, func() bool { return fs.count() == 1 })
	q.Close()
}

// If warmth never arrives within KELD_ENRICH_WARM_WAIT, the job is re-spooled
// (deferred) WITHOUT consuming the retry budget — so it is NEVER quarantined,
// no matter how many times it defers.
func TestWorkerDefersWhenNeverWarmNeverQuarantines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_ENRICH_WARM_WAIT", "20ms")
	t.Setenv("KELD_ENRICH_MAX_ATTEMPTS", "2") // low cap: prove defers don't count

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("never-warm-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), fs, "t@keld.co",
		func() bool { return false }, func() bool { return false }, nil, nil)

	// Give it time to defer several times (each defer re-spools to disk).
	time.Sleep(200 * time.Millisecond)
	q.Close()

	if fs.count() != 0 {
		t.Fatalf("nothing should publish while never warm; got %d", fs.count())
	}
	if n := quarantineCount(t, home); n != 0 {
		t.Fatalf("model-not-ready must never quarantine; found %d quarantined", n)
	}
	// A spooled (deferred) pointer should exist — the job was preserved.
	if n := spoolCount(t, home); n == 0 {
		t.Fatal("expected the deferred job to be re-spooled, not lost")
	}
}
```

Notes for the implementer: reuse whatever the existing timeout/quarantine tests use for the fake `Sender` (`fakeSender` with a mutexed counter), the inline-job constructor, and spool/quarantine counting. If a helper does not yet exist, add a minimal one in the test file:

```go
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.After(d)
	for !cond() {
		select {
		case <-deadline:
			t.Fatal("condition not met in time")
		case <-time.After(2 * time.Millisecond):
		}
	}
}
func spoolCount(t *testing.T, home string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(filepath.Join(home, "spool"), func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".json") {
			n++
		}
		return nil
	})
	return n
}
// quarantineCount counts files under the spool quarantine subtree; match the
// path spool.Quarantine writes to (inspect internal/spool).
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run 'TestWorkerWaitsForWarm|TestWorkerDefersWhenNeverWarm' -v`
Expected: FAIL — the current Worker waits forever on a false gate (no bounded defer), so `TestWorkerDefersWhenNeverWarm...` never re-spools and hangs/fails; compile fails if `waitWarm`/`warmWait` are referenced before they exist.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/daemon/daemon.go`, replace the wait loop (current lines 67-76):

```go
		// Wait until the backend is ready. Poll with a short sleep; break out
		// immediately if the queue is closed so shutdown is never blocked.
		for !ready() {
			select {
			case <-q.Done():
				// Queue closed; discard the in-hand job and exit.
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
```

with:

```go
		// Wait until the model is resident (warm) before starting the job's
		// deadline, so model-load time is never counted against it. Bound the
		// wait: if warmth doesn't arrive in time, DEFER the job (re-spool)
		// WITHOUT consuming its retry budget — "not ready yet" is not
		// "un-enrichable", so a cold start must never drive quarantine.
		warm, closed := waitWarm(ready, warmWait(), q.Done())
		if closed {
			return // queue closed during the wait; discard in-hand job and exit.
		}
		if !warm {
			if err := spool.Write(pointerFromJob(j)); err != nil {
				log.Printf("keld-agent: job %s deferred (model not ready) and re-spool failed: %v", j.Key(), err)
			} else {
				log.Printf("keld-agent: job %s deferred — model not ready after %s, re-spooled", j.Key(), warmWait())
			}
			continue
		}
```

Then add, beside `jobTimeout` (after ~line 191):

```go
// warmWait bounds how long the worker waits for the sidecar model to become
// resident before deferring a job (re-spool, no retry attempt consumed).
// Default 90s (a cold model load plus headroom); override with
// KELD_ENRICH_WARM_WAIT (Go duration).
func warmWait() time.Duration {
	if v := os.Getenv("KELD_ENRICH_WARM_WAIT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 90 * time.Second
}

// waitWarm blocks until ready() is true (warm=true), bound elapses
// (warm=false, closed=false), or done is closed (warm=false, closed=true). It
// polls ready on a short interval; ready is expected to be a cheap gate read.
func waitWarm(ready func() bool, bound time.Duration, done <-chan struct{}) (warm, closed bool) {
	if ready() {
		return true, false
	}
	deadline := time.NewTimer(bound)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return false, true
		case <-deadline.C:
			return false, false
		case <-tick.C:
			if ready() {
				return true, false
			}
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run 'TestWorkerWaitsForWarm|TestWorkerDefersWhenNeverWarm' -v`
Expected: PASS.

Then run the whole daemon package to catch regressions in existing Worker/gate tests:
Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -v`
Expected: PASS. In particular `TestWorkerGateExitsOnQueueClose` still passes because the queue-close path returns `closed=true`; existing warm-immediately tests pass because `waitWarm` returns instantly when `ready()` is already true. If any test regresses, STOP and report (do not weaken assertions).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/daemon/daemon.go internal/agent/daemon/daemon_test.go
git commit -m "fix(daemon): bounded warm-wait — defer cold jobs without burning the retry budget"
```

---

### Task 4: wire the warm gate into `mlBackend`

**Files:**
- Modify: `internal/agent/daemon/daemon.go` — `mlBackendWithOpts` (lines 713-741): start a warm-gate poller over `opts.client.WorkerReady` and return `warmGate.Warm` as the gate.
- Modify: `internal/agent/daemon/daemon_test.go` — sidecar stubs used by gate/Worker tests must also serve `/metrics`; gate assertions must drive worker-state.

**Interfaces:**
- Consumes: `client.WorkerReady(ctx)` (Task 1), `warmGate` (Task 2), existing `mlBackendOpts{ sup, client, ... }`.
- Produces: `mlBackendWithOpts` returns `(enrich.Model, func() bool)` where the `func() bool` is now `warmGate.Warm` (model-resident), not `sup.Ready()`.

- [ ] **Step 1: Update tests to express the new gate semantics (write first)**

The gate now opens on `/metrics` `worker.state == "ready"`, not `/health`. Update the sidecar stubs in `daemon_test.go` so any httptest server used for gate/Worker tests also serves `/metrics`:

```go
// add to each httptest sidecar handler used by gate tests:
case "/metrics":
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"worker":{"state":"ready"}}`))
```

- In `TestWorkerWithSidecarStubPublishes` (~line 210) and `TestWorkerWithProvisionedModel...` (~line 321): change the gate the test passes/asserts. Where a test built `gate := func() bool { return sup.Ready() }`, it should now use the gate returned by `mlBackendWithOpts` (which is warm-based); ensure the stub serves `/metrics` "ready" so the gate opens and the job publishes.
- In `TestGateStaysClosedOnProvisionFailure` (~line 378): the assertion `if gate()` must stay closed — with no sidecar serving `/metrics`, `WorkerReady` returns false, so `warmGate.Warm()` stays false. Keep the assertion; it should still hold. Because the warm gate seeds asynchronously, poll briefly instead of reading once:

```go
	// gate must never open when provisioning fails (no sidecar → not warm).
	time.Sleep(100 * time.Millisecond)
	if gate() {
		t.Fatal("gate must stay closed on provision failure — no deterministic fallback")
	}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run 'TestWorkerWithSidecarStub|TestWorkerWithProvisioned|TestGateStaysClosed' -v`
Expected: FAIL — the gate is still `sup.Ready()`, so warm-based stub `/metrics` isn't consulted (provisioned-model test may pass spuriously, but the wiring under test isn't the warm gate yet). This step mainly guards Step 3's change.

- [ ] **Step 3: Wire the warm gate**

In `internal/agent/daemon/daemon.go`, in `mlBackendWithOpts`, replace the gate construction (current line 739):

```go
	gate := func() bool { return opts.sup.Ready() }
	return opts.client, gate
```

with:

```go
	// The per-job gate is model-resident WARMTH (sidecar /metrics
	// worker.state=="ready"), not latched liveness: after an idle-kill the
	// /health server stays up while the worker reloads, and counting that
	// reload against a job's deadline is the death-spiral this fixes. A dead or
	// never-started sidecar simply never reports warm (metrics unreachable), so
	// the gate stays closed — same durable queue/spool behaviour as before.
	wg := newWarmGate()
	go wg.run(ctx, opts.client.WorkerReady, warmPollInterval)
	return opts.client, wg.Warm
```

(Keep `opts.sup` starting exactly as-is for liveness/restart; only the returned gate changes. `provisionFailed` remains set for logging as documented.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -v`
Expected: PASS (whole package). Then:
Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/daemon/daemon.go internal/agent/daemon/daemon_test.go
git commit -m "fix(daemon): gate enrichment jobs on model warmth, not latched liveness"
```

---

### Task 5: full-suite check + live verification on macOS

**Files:** none (verification only).

- [ ] **Step 1: Format, vet, whole suite**

Run:
```bash
export PATH="/opt/homebrew/bin:$PATH"
gofmt -l internal/    # expect: no output
go vet ./...          # expect: clean
go test ./...         # expect: all ok
```

- [ ] **Step 2: Build + install locally, restart**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go build -o /tmp/keld-agent ./cmd/keld-agent && /tmp/keld-agent --version`
Then (foreground, to observe): temporarily point a run at a warm cycle — or install via the normal flow and `keld-agent restart`. Confirm via `keld-agent metrics` the worker goes `spawning → ready`.

- [ ] **Step 3: Observe cold→warm behavior**

Watch `~/.keld/logs/agent.err.log` while enrichment is driven right after a restart (model cold). Expected: jobs log `deferred — model not ready after …, re-spooled` (NOT `exceeded …s, re-spooled`), then once `worker.state:"ready"`, the spool drains and jobs publish. Confirm no `quarantined` lines accrue from cold starts, and `keld-agent metrics` `counts.completed` climbs.

- [ ] **Step 4: Confirm warm enrichment is well under the deadline**

With the model warm, drive one enrichment; confirm it completes far inside `KELD_ENRICH_JOB_TIMEOUT` (30s) and reaches Atlas (no `publish failed`, spool empty).

- [ ] **Step 5: Push branch and open PR**

```bash
export PATH="/opt/homebrew/bin:$PATH"
git push -u origin feat/warm-gated-enrichment-deadline
gh pr create --fill --title "Warm-gated enrichment deadline (Pillar 2)"
```

---

## Self-Review

**Spec coverage:**
- Non-latching warmth from `/metrics worker.state` → Task 1 (`WorkerReady`) + Task 2 (poller). ✔
- Worker gates on warmth, deadline covers inference only → Task 3. ✔
- Bounded warm-wait, re-spool without burning an attempt, never quarantine on not-ready → Task 3 (`waitWarm` + defer path) + its tests. ✔
- Static 30s inference deadline unchanged; new `KELD_ENRICH_WARM_WAIT` default 90s → Task 3 (`warmWait`). ✔
- Warm gate wired into production, liveness unchanged → Task 4. ✔
- Fetch/parse error ⇒ not warm → Task 1 (`WorkerReady` false on error) + Task 2 seed/poll. ✔
- Edge cases (metrics unreachable, dead sidecar, provision failure) → Task 4 stub/gate tests + Task 3 defer path. ✔
- Go-only, no sidecar change → all tasks touch only Go. ✔

**Placeholder scan:** none — every code step has complete code; test-helper stubs (`quarantineCount`) explicitly point at `internal/spool` for the exact path, which the implementer confirms by reading that package (not a logic placeholder).

**Type consistency:** `WorkerReady(ctx context.Context) bool` (Task 1) is consumed as `opts.client.WorkerReady` in Task 4 and as the `ready func(context.Context) bool` arg to `warmGate.run` (Task 2). `warmGate.Warm` is `func() bool`, matching the Worker's `ready func() bool` param (Task 3) and `mlBackendWithOpts`'s `func() bool` return (Task 4). `waitWarm(ready func() bool, bound time.Duration, done <-chan struct{}) (warm, closed bool)` and `warmWait() time.Duration` are defined in Task 3 and used only there. Consistent throughout.
