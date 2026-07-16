# On-demand Sidecar Warmup + Reap-on-start Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the v0.3.8 warmth-gate deadlock (the daemon now *triggers* the on-demand model load instead of waiting for it) and stop leaking orphaned sidecars (reap stale sidecars before spawning).

**Architecture:** The sidecar loads its model on-demand (on the first inference request). So the enrichment Worker, when the model isn't warm, fires a warmup request (via an injected `warmup func`, outside the 30s job deadline) to trigger the load, then runs the real enrichment under the 30s. Separately, `mlBackend` reaps any pre-existing sidecar before spawning, so an orphan from a SIGKILL'd/crashed/reinstalled predecessor is always cleaned up.

**Tech Stack:** Go 1.26, standard library (`net/http`, `os/exec`, `context`, `time`).

## Global Constraints

- **Go 1.26**; module `github.com/ncx-ai/keld-signal`. Toolchain at `/opt/homebrew/bin/go` — prefix every `go` command with `export PATH="/opt/homebrew/bin:$PATH"`.
- **Go-only. Do NOT modify the frozen Python sidecar** (`sidecar/`).
- **Do NOT add `Warmup` to the `enrich.Model` interface.** Inject `warmup func(context.Context) error` into `Worker`, symmetric with the existing `ready func() bool`. A `nil` warmup is a no-op (fall back to the bounded passive wait).
- Warmup is fired only when `ready()` is false; it is bounded by `KELD_ENRICH_WARM_WAIT` and is **not** counted against the 30s job deadline.
- Model-not-ready (warmup fails/times out) ⇒ re-spool **without** incrementing the retry ledger; **never** quarantine on a cold model.
- `KELD_ENRICH_WARM_WAIT` default **90s → 120s** (cold load measured ~54s). `KELD_ENRICH_JOB_TIMEOUT` unchanged (30s, inference only).
- Reap matches the resolved sidecar **binary path** only; best-effort (a no-match exit is ignored). Safe under single-instance launchd/systemd.
- No new external dependencies.

## File Structure

- `internal/agent/enrich/sidecar/client.go` — add `Warmup(ctx) error`.
- `internal/agent/enrich/sidecar/client_test.go` — test `Warmup`.
- `internal/agent/daemon/daemon.go` — Worker `warmup` seam + on-demand trigger; `warmupFunc(m)` wiring; `warmWait()` default 120s; `reapStaleSidecars` call in `mlBackend`.
- `internal/agent/daemon/daemon_test.go` — update Worker call sites; new warmup tests.
- `internal/agent/daemon/reap_unix.go` (new, `//go:build darwin || linux`) — `reapStaleSidecars` + seam.
- `internal/agent/daemon/reap_windows.go` (new, `//go:build windows`) — Windows variant.
- `internal/agent/daemon/reap_unix_test.go` (new, `//go:build darwin || linux`) — seam test.

---

### Task 1: `sidecar.Client.Warmup`

**Files:**
- Modify: `internal/agent/enrich/sidecar/client.go` (add method after `Classify`, ~line 148; add `"errors"` to imports)
- Test: `internal/agent/enrich/sidecar/client_test.go`

**Interfaces:**
- Consumes: existing unexported `(c *Client) post(path string, body any, out any) bool` and `WithContext(ctx)`.
- Produces: `func (c *Client) Warmup(ctx context.Context) error` — returns `nil` once the sidecar answers a trivial `/classify` (model resident); returns `ctx.Err()` if the context ends first; a generic error otherwise.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/enrich/sidecar/client_test.go` (imports needed: `context`, `net/http`, `net/http/httptest`, `sync/atomic`, `testing`, `time`):

```go
func TestWarmupWaitsThrough503ThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/classify" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		// First two calls: still loading (503). Then 200.
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"results":{"task_type":[{"label":"other","confidence":1.0}]}}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second)
	if err := c.Warmup(context.Background()); err != nil {
		t.Fatalf("Warmup returned error: %v", err)
	}
	if calls.Load() < 3 {
		t.Fatalf("expected retries through 503; calls=%d", calls.Load())
	}
}

func TestWarmupReturnsCtxErrOnTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // never becomes ready
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Warmup(ctx); err == nil {
		t.Fatal("expected an error when the model never becomes ready before ctx timeout")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/sidecar/ -run TestWarmup -v`
Expected: FAIL to compile — `c.Warmup undefined`.

- [ ] **Step 3: Write minimal implementation**

Add `"errors"` to the import block in `internal/agent/enrich/sidecar/client.go`, then add after `Classify`:

```go
// Warmup triggers and awaits the sidecar's on-demand model load by issuing a
// trivial /classify bound to ctx. The sidecar loads the model only when it
// receives an inference request, so this is the request that starts the load;
// post() waits+retries through the 503/reload window until the sidecar answers.
// Returns nil once the model is resident, ctx.Err() if ctx ends first, or a
// generic error on a non-retryable failure. The result is discarded.
func (c *Client) Warmup(ctx context.Context) error {
	var r extractResp
	if c.WithContext(ctx).post("/classify", struct {
		Text  string              `json:"text"`
		Tasks map[string][]string `json:"tasks"`
	}{"warmup", map[string][]string{"task_type": {"other"}}}, &r) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("sidecar warmup failed")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/sidecar/ -run TestWarmup -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/sidecar/client.go internal/agent/enrich/sidecar/client_test.go
git commit -m "feat(sidecar): add Warmup to trigger+await the on-demand model load"
```

---

### Task 2: Worker on-demand warmup seam

**Files:**
- Modify: `internal/agent/daemon/daemon.go` — `Worker` signature + wait/defer block (~lines 60-85); add `warmupFunc(m)`; change `warmWait()` default; update the production `Worker(...)` call site (~line 513).
- Modify: `internal/agent/daemon/daemon_test.go` — add the new `warmup` arg to every `Worker(...)` call; add three new tests.

**Interfaces:**
- Consumes: `sidecar.Client.Warmup` (Task 1); existing `ready func() bool`, `waitWarm`, `warmWait`, `spool.Write`, `pointerFromJob`, `retryLedger`, `withJobCtx`.
- Produces: `Worker(ctx, q, m, pub, actor, includeEntityText, ready, warmup func(context.Context) error, emitter, ra)` — new `warmup` param after `ready`. `func warmupFunc(m enrich.Model) func(context.Context) error` — returns a warmup closure for the sidecar client, or `nil` for other models.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agent/daemon/daemon_test.go` (reuse the existing `fakeSender`, `sampleInlineJob`, `enrichtest.NewFake()`, `quarantineCount`, `spoolCount`, `waitFor` helpers from the file; `sync/atomic` is available):

```go
// Cold model: Worker must call warmup (which loads the model → ready flips
// true), then process and publish, with the retry ledger untouched.
func TestWorkerWarmupLoadsThenPublishes(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ENRICH_WARM_WAIT", "5s")
	var warm atomic.Bool // starts false (cold)
	var warmupCalls atomic.Int32
	warmup := func(context.Context) error { warmupCalls.Add(1); warm.Store(true); return nil }

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("warmup-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), fs, "t@keld.co",
		func() bool { return false }, warm.Load, warmup, nil, nil)

	waitFor(t, time.Second, func() bool { return fs.count() == 1 })
	if warmupCalls.Load() != 1 {
		t.Fatalf("warmup calls = %d, want 1", warmupCalls.Load())
	}
	q.Close()
}

// Warmup never makes it ready (returns error): job defers (re-spool) WITHOUT
// consuming the retry budget — never quarantined, even at a low max-attempts.
func TestWorkerWarmupTimesOutDefersNeverQuarantines(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_ENRICH_WARM_WAIT", "20ms")
	t.Setenv("KELD_ENRICH_MAX_ATTEMPTS", "2")
	warmup := func(context.Context) error { return context.DeadlineExceeded }

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("warmup-fail-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), fs, "t@keld.co",
		func() bool { return false }, func() bool { return false }, warmup, nil, nil)

	time.Sleep(150 * time.Millisecond)
	q.Close()
	if fs.count() != 0 {
		t.Fatalf("nothing should publish; got %d", fs.count())
	}
	if n := quarantineCount(t, home); n != 0 {
		t.Fatalf("model-not-ready must never quarantine; found %d", n)
	}
	if n := spoolCount(t, home); n == 0 {
		t.Fatal("expected the deferred job to be re-spooled")
	}
}

// Already warm: warmup must NOT be called.
func TestWorkerSkipsWarmupWhenReady(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var warmupCalls atomic.Int32
	warmup := func(context.Context) error { warmupCalls.Add(1); return nil }

	q := queue.New(4)
	fs := &fakeSender{}
	q.Offer(sampleInlineJob("already-warm-1"))
	go Worker(context.Background(), q, enrichtest.NewFake(), fs, "t@keld.co",
		func() bool { return false }, func() bool { return true }, warmup, nil, nil)

	waitFor(t, time.Second, func() bool { return fs.count() == 1 })
	if warmupCalls.Load() != 0 {
		t.Fatalf("warmup must not be called when already ready; calls=%d", warmupCalls.Load())
	}
	q.Close()
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run TestWorkerWarmup -v`
Expected: FAIL to compile — `Worker` takes the old arg count (the new `warmup` param doesn't exist yet), and the new call sites pass an extra arg.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/daemon/daemon.go`:

(a) Change the `Worker` signature to add `warmup func(context.Context) error` after `ready func() bool`:

```go
func Worker(ctx context.Context, q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText func() bool, ready func() bool, warmup func(context.Context) error, emitter *clientevents.Emitter, ra *reauther) {
```

(b) Replace the wait/defer block (current v0.3.8 lines ~67-85, from the `ww := warmWait()` line through the `if !warm { ... continue }` block) with:

```go
		if !ready() {
			ww := warmWait()
			warmedInTime := false
			if warmup != nil {
				// Trigger the sidecar's on-demand model load and wait for it,
				// bounded by ww and OUTSIDE the job's inference deadline.
				wctx, wcancel := context.WithTimeout(ctx, ww)
				err := warmup(wctx)
				wcancel()
				warmedInTime = err == nil
			} else {
				// No warmer wired (e.g. tests): bounded passive wait.
				warm, closed := waitWarm(ready, ww, q.Done())
				if closed {
					return // queue closed during the wait; discard in-hand job and exit.
				}
				warmedInTime = warm
			}
			if !warmedInTime {
				// Model not resident in time — DEFER (re-spool) WITHOUT consuming
				// the retry budget; "not ready yet" is never "un-enrichable".
				if err := spool.Write(pointerFromJob(j)); err != nil {
					log.Printf("keld-agent: job %s deferred (model not ready) and re-spool failed: %v", j.Key(), err)
				} else {
					log.Printf("keld-agent: job %s deferred — model not ready after %s, re-spooled", j.Key(), ww)
				}
				continue
			}
		}
```

(The existing warm path — `to := jobTimeout()` onward — is unchanged.)

(c) Change `warmWait()`'s default from `90 * time.Second` to `120 * time.Second` (leave the `KELD_ENRICH_WARM_WAIT` env override intact).

(d) Add the warmup wiring helper (near `withJobCtx`):

```go
// warmupFunc returns a warmup trigger bound to the sidecar client, or nil when
// m is not the sidecar client (nothing to warm — e.g. a test fake or the eval
// model). The daemon passes this to Worker as its warmup seam.
func warmupFunc(m enrich.Model) func(context.Context) error {
	c, ok := m.(*sidecar.Client)
	if !ok {
		return nil
	}
	return c.Warmup
}
```

(e) Update the production `Worker(...)` call site (the `go Worker(...)` in `Run`, ~line 513) to pass `warmupFunc(model)` after the gate:

```go
		go Worker(ctx, q, model, pub, actor, live.IncludeEntityText, gate, warmupFunc(model), emitter, ra)
```

(f) Update every OTHER `Worker(...)` call in `daemon_test.go` (the pre-existing tests) to insert `nil` for the new `warmup` param, immediately after their `ready`/gate argument. (Grep `go Worker(` / `Worker(` in the test file; each existing call gains one `nil`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run TestWorkerWarmup -v`
Expected: PASS (all three new tests).

Then the whole package (existing Worker/gate tests must still pass with their `nil` warmup — they take the passive-wait fallback exactly as before):
Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -v`
Expected: PASS. If any pre-existing test regresses, STOP and report (do not weaken assertions).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/daemon/daemon.go internal/agent/daemon/daemon_test.go
git commit -m "fix(daemon): trigger on-demand sidecar load via injected warmup (fixes warmth-gate deadlock)"
```

---

### Task 3: reap stale sidecars on start

**Files:**
- Create: `internal/agent/daemon/reap_unix.go` (`//go:build darwin || linux`)
- Create: `internal/agent/daemon/reap_windows.go` (`//go:build windows`)
- Create: `internal/agent/daemon/reap_unix_test.go` (`//go:build darwin || linux`)
- Modify: `internal/agent/daemon/daemon.go` — call `reapStaleSidecars(binPath)` in `mlBackend` before spawning.

**Interfaces:**
- Consumes: `binPath` from `sidecarBinPath()` (already resolved in `mlBackend`).
- Produces: `func reapStaleSidecars(binPath string)` (platform-specific); `func reapStaleSidecarsWith(binPath string, run func(name string, args ...string) error)` (unix, seam for tests).

- [ ] **Step 1: Write the failing test**

Create `internal/agent/daemon/reap_unix_test.go`:

```go
//go:build darwin || linux

package daemon

import (
	"reflect"
	"testing"
)

func TestReapStaleSidecarsWithBuildsPkill(t *testing.T) {
	var gotName string
	var gotArgs []string
	reapStaleSidecarsWith("/usr/local/keld/keld-agent-sidecar/keld-agent-sidecar",
		func(name string, args ...string) error { gotName = name; gotArgs = args; return nil })
	if gotName != "pkill" {
		t.Fatalf("name = %q, want pkill", gotName)
	}
	want := []string{"-f", "/usr/local/keld/keld-agent-sidecar/keld-agent-sidecar"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run TestReapStaleSidecars -v`
Expected: FAIL to compile — `undefined: reapStaleSidecarsWith`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/daemon/reap_unix.go`:

```go
//go:build darwin || linux

package daemon

import "os/exec"

// reapStaleSidecars terminates any running sidecar process whose full command
// line matches binPath — an orphan left by a prior daemon that died without
// cleaning up its child (e.g. launchd `kickstart -k` SIGKILL). Under
// single-instance service management any such process is stale, so reaping
// before spawning guarantees exactly one sidecar per daemon. Best-effort: a
// no-match exit from pkill is ignored.
func reapStaleSidecars(binPath string) {
	reapStaleSidecarsWith(binPath, func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	})
}

func reapStaleSidecarsWith(binPath string, run func(name string, args ...string) error) {
	_ = run("pkill", "-f", binPath)
}
```

Create `internal/agent/daemon/reap_windows.go`:

```go
//go:build windows

package daemon

import "os/exec"

// reapStaleSidecars terminates any orphaned sidecar process by image name.
// Best-effort; a no-match exit is ignored. (binPath is unused on Windows —
// taskkill matches by image name.)
func reapStaleSidecars(binPath string) {
	_ = exec.Command("taskkill", "/F", "/IM", "keld-agent-sidecar.exe").Run()
}
```

- [ ] **Step 4: Wire the call site + verify**

In `internal/agent/daemon/daemon.go`, in `mlBackend`, right after the `if !hasBin { ... }` block (before the ephemeral-port allocation), add:

```go
	// Reap any orphaned sidecar from a prior daemon before spawning ours, so a
	// SIGKILL'd/crashed/reinstalled predecessor's child can't accumulate.
	reapStaleSidecars(binPath)
```

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/daemon/ -run TestReapStaleSidecars -v`
Expected: PASS.
Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./... && go vet ./...`
Expected: clean (both unix and the call site compile; `go build` on this darwin host exercises reap_unix.go).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/daemon/reap_unix.go internal/agent/daemon/reap_windows.go internal/agent/daemon/reap_unix_test.go internal/agent/daemon/daemon.go
git commit -m "fix(daemon): reap orphaned sidecars on start so restarts can't leak them"
```

---

### Task 4: full-suite check + live verification + release

**Files:** none (verification + delivery).

- [ ] **Step 1: Format, vet, whole suite**

Run:
```bash
export PATH="/opt/homebrew/bin:$PATH"
gofmt -l internal/    # expect: no output
go vet ./...          # expect: clean
go test ./...         # expect: all ok
```

- [ ] **Step 2: Build + install locally; restart**

Build both binaries, install to the pkg location or swap in, and `keld-agent restart`. Confirm the binary reports the new version once released; for local verification `go build -o /tmp/keld-agent ./cmd/keld-agent` is enough to drive behavior.

- [ ] **Step 3: Verify reap-on-start**

```bash
ps -axo pid,ppid,command | grep keld-agent-sidecar | grep -v grep   # note current sidecar pid(s)
keld-agent restart
sleep 5
ps -axo pid,ppid,command | grep keld-agent-sidecar | grep -v grep   # expect EXACTLY ONE (child of the new daemon)
```
Expected: exactly one sidecar after restart; no PPID=1 orphans accumulate across repeated restarts.

- [ ] **Step 4: Verify cold→warm (deadlock fixed)**

```bash
keld-agent restart                                   # cold model
sleep 3
python3 ~/keld-signal/scripts/send-test-prompt.py "verify warmup: debug the login flow"
# poll ~60-120s:
for i in $(seq 1 40); do sleep 3; keld-agent metrics | python3 -c "import sys,json;d=json.load(sys.stdin);print(d['worker']['state'], d['counts'])"; done
```
Expected: **without any manual `/classify`**, `worker.state` goes `down → spawning → ready` (the daemon's own warmup drove the load), `completed` climbs, the spool drains, and `~/.keld/logs/agent.err.log` shows no `exceeded 30s` and no `quarantined`. A `deferred — model not ready` line may appear only if the load exceeds `KELD_ENRICH_WARM_WAIT` (120s).

- [ ] **Step 5: Push to main + cut release**

```bash
export PATH="/opt/homebrew/bin:$PATH"
git checkout main && git pull --ff-only origin main
git merge --squash fix/sidecar-warmup-and-reap
git commit -m "fix(daemon): on-demand sidecar warmup + reap-on-start (fixes v0.3.8 deadlock + leak)"
git push origin main
git tag -a v0.3.9 -m "release v0.3.9" && git push origin v0.3.9
```
Expected: `main` advances; `v0.3.9` tag pushed → release CI builds and attaches `keld-v0.3.9-arm64.pkg`.

---

## Self-Review

**Spec coverage:**
- A1 `sidecar.Client.Warmup` → Task 1. ✔
- A1 injected `warmup` seam (not on Model interface) → Task 2 (signature + `warmupFunc`). ✔
- A2 Worker triggers warmup when cold, runs inference under 30s, defers-without-attempt otherwise → Task 2 (wait/defer block + tests). ✔
- A3 `KELD_ENRICH_WARM_WAIT` 120s; `KELD_ENRICH_JOB_TIMEOUT` unchanged → Task 2(c). ✔
- B1 `reapStaleSidecars` platform helpers + seam → Task 3. ✔
- B2 call in `mlBackend` before spawn → Task 3 Step 4. ✔
- Edge cases (warmup fail→defer; nil warmup→passive fallback; no stale sidecar→no-op) → Task 2 block + Task 3 (best-effort pkill). ✔
- Testing (Warmup 503→200 + ctx-timeout; Worker cold/never-warm/already-warm; reap seam) → Tasks 1–3. ✔
- Verification + release → Task 4. ✔

**Placeholder scan:** none — every code step is complete; Step 3(f) points the implementer at the mechanical call-site update (add `nil`) with the exact grep, not a vague "handle the rest."

**Type consistency:** `Warmup(ctx context.Context) error` (Task 1) is consumed by `warmupFunc` returning `func(context.Context) error` (Task 2), which is the new `Worker` `warmup` param type (Task 2) and the injected fakes' type in tests (Task 2). `reapStaleSidecars(binPath string)` (Task 3) matches its call in `mlBackend`; `reapStaleSidecarsWith(binPath string, run func(name string, args ...string) error)` matches its test. `warmWait() time.Duration` unchanged signature. Consistent throughout.
