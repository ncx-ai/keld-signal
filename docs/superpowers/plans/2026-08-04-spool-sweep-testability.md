# Plan 1D-signal — Make the Daemon Sweep Testable

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the largest coverage gap left by the spool rework: the daemon's startup and sweep path has no test at all.

**Architecture:** No behavior change. The sweep goroutine's body is lifted out of `Run` into a function taking its dependencies explicitly (the two intervals and an emitter), so it can be driven from a test without credentials. Then the interactions that five separate tasks created are actually asserted.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure Go, `CGO_ENABLED=0` across five release targets), standard `testing`.

**Source:** the final whole-branch review of the spool plan, which called this "the highest-value gap by a wide margin."

## Why this is the gap that matters

`daemon.go` around lines 770-870 is where all five spool tasks meet: `ImportLegacy` → `drainSpool` → `serve` ordering, the `KELD_SPOOL_SWEEP` ticker driving `Resync` and the drain, the independent `KELD_SPOOL_GAUGE_INTERVAL` ticker driving `Stats` and the `spool.depth` gauge, and the `Evicted()` delta driving `spool.evicted`. None of it is covered, and **no per-task review could reach it either** — each task's review saw only its own diff, and this region is the seam between them.

It is untestable today for one incidental reason: `Run` requires credentials (`hook.LoadConfig()` must yield a non-empty `Endpoint` and `IngestToken`) before it reaches any of this, so a test cannot drive it and neither could the human smoke test. Lifting the goroutine body out removes that coupling.

## Global Constraints

- **No behavior change.** This is a refactor plus tests. If the refactor changes what the daemon does, it is wrong.
- **`CGO_ENABLED=0`** across linux/darwin amd64+arm64 and windows/amd64 — `make crosscheck` must stay green.
- **The `spool` package must not import `clientevents`** — it exposes stats; the daemon emits.
- **Completeness, not latency, is the SLO.** Deep backlog is a designed steady state; a silent drop is a failure.
- Config is read via `os.Getenv` at point of use; this repo has no central config struct.

---

### Task 1: Lift the sweep loop into a testable function, then test it

**Files:**
- Modify: `internal/agent/daemon/daemon.go` (extract the sweep goroutine's body)
- Test: `internal/agent/daemon/sweep_test.go` (new)

**Interfaces:**
- Produces: an unexported function — shape roughly `runSweep(ctx context.Context, q *queue.Queue, emitter *clientevents.Emitter, sweepIv, gaugeIv time.Duration)` — containing exactly the current `select` loop over the two tickers and `ctx.Done()`. `Run` calls it with the values it computes today. Adjust the signature to whatever the current code actually needs; the requirement is that **every dependency arrives as a parameter** rather than being read from the enclosing closure, so a test can supply them.

- [ ] **Step 1: Read the current sweep block and write down what it does**

Before touching anything, read `daemon.go` from the `ImportLegacy` call through the end of the sweep goroutine and list, in order: what runs once at startup, what each ticker fires, and what state is shared (e.g. the `lastEvicted` counter). Put that list in your report — the tests must assert the behavior that exists, not the behavior you'd expect.

- [ ] **Step 2: Extract, with no behavior change**

Move the goroutine body into the new function. Keep the ordering, the ticker cadences, the `defer Stop()` calls, and the `ctx.Done()` case exactly as they are. `Run` should end up launching `go runSweep(...)` with the same arguments it computes today.

Verify with `go vet ./...` and the existing suite (`go test ./internal/agent/daemon/ -count=1`) that nothing changed before writing a single new test.

- [ ] **Step 3: Write the tests**

Drive `runSweep` directly with short intervals (single-digit milliseconds) and a cancellable context, against a real spool in a `t.TempDir()` `KELD_HOME`. Assert the interactions that no existing test covers:

- **The drain sweep drains.** Seed rows via `spool.Write`, run the sweep, cancel, and assert the queue received them and the spool is empty.
- **The two cadences are independent.** With `sweepIv` much shorter than `gaugeIv`, several drains must occur before a single `spool.depth` gauge is emitted. This is the property Task 3's fix round established deliberately, and nothing pins it — a future edit re-coupling them for "finer visibility" would reintroduce the stale-gauge bug the emitter's coalescing causes.
- **`spool.depth` carries real numbers.** Assert the emitted event's `rows`/`bytes`/`oldest_age_s` fields match the spool's actual state, not just that an event fired.
- **`spool.evicted` fires only on a change.** Drive an eviction (a tiny `KELD_SPOOL_MAX_BYTES` plus writes), and assert the event appears once for that delta and does **not** repeat on subsequent quiet sweeps.
- **`Resync` runs on the sweep.** Insert a row through a second, independent handle to the same database (simulating the hook process), then assert the in-memory total picks it up after a sweep tick — this is the cross-process correction, and it currently has only an indirect test.
- **Cancellation stops it promptly** and does not leak a ticker.

Use the `clientevents` emitter's own inspection surface if it has one; otherwise pass a real emitter and read its ring, or thread a tiny interface. Do **not** change `clientevents` to make testing easier — if it resists inspection, say so in the report and assert what you can.

- [ ] **Step 4: Verify**

`go test ./internal/spool/ ./internal/agent/daemon/ -count=1`, then `-race` on the daemon package (this code has two tickers, a shared counter, and a mutex-guarded byte total — race is the failure mode worth checking), then `go vet ./...` and `make crosscheck`.

- [ ] **Step 5: Commit**

```bash
git commit -m "test(daemon): make the sweep loop testable and test it

daemon.go's startup and sweep path had no coverage at all — the ordering of
ImportLegacy, drainSpool and serve, both tickers, Resync, and the two client
events. It is the seam where five separate spool tasks meet, and no per-task
review could reach it either.

Untestable only incidentally: Run requires credentials before reaching any of
it. The loop body now takes its dependencies as parameters, so a test can
drive it with millisecond intervals and no login.

Pins in particular that the drain and gauge cadences stay independent — a
future edit re-coupling them would reintroduce the stale-gauge bug the
emitter's same-code coalescing causes."
```

---

## Verification

- [ ] `go test ./internal/spool/ ./internal/agent/daemon/ -count=1` green
- [ ] `go test ./internal/agent/daemon/ -race` green
- [ ] `go vet ./...` clean, `make crosscheck` five targets OK
- [ ] The refactor is provably behavior-neutral — the pre-existing daemon tests pass unchanged, and the report states what ran before the new tests were added

## Notes

- Work in the `spool-sweep-tests` worktree.
- Deliberately **not** in scope: the `evictFor`/transaction-sharing rework (bundling the self-eviction race, the import crash-window and the insert-side drift — all narrow, and the crash-window cannot fire on the real upgrade path); the cross-process `busy_timeout` assertion; `Drain`'s bounded snapshot; `quarantineRaw`'s unconditional delete; `secure_delete`/`incremental_vacuum`; and a `?` in `$KELD_HOME`.
