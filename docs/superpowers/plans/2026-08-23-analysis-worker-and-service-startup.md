# The service starts without GLiNER2 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the daemon treat the sidecar as a general analysis-and-enrichment service that starts and serves without GLiNER2, so `ml_backend:"deterministic"` actually produces workstreams.

**Architecture:** Stop the daemon gating service startup on model provisioning, and start the service in `ml_backend:"deterministic"` so the workstreams pass is wired there. Three Go tasks; no Python changes.

**Deferred (user, 2026-08-23):** moving analysis into a recycled worker child. It is the right fix for the parent's ~619MB spaCy cost and it stays in the spec, but it is memory OPTIMISATION and does not block the service working. Until it lands the parent holds spaCy and the 4096MB budget is not satisfiable with the current ceiling+margin; the guard now says so out loud rather than silently relaxing.

**Tech Stack:** Python 3.12 sidecar (FastAPI, multiprocessing spawn, standalone test scripts — never pytest), Go daemon.

**Spec:** `docs/superpowers/specs/2026-08-23-analysis-service-without-gliner2-design.md`

## Global Constraints

- The sidecar is the client-side **analysis and enrichment service in general**. GLiNER2 is one capability it may load lazily — never its identity, never a precondition for serving.
- **The service must start and serve without GLiNER2, and must never require it to be loaded before running.**
- The parent process **holds no model and its RSS stays flat regardless of uptime** (AGENTS.md). Memory is reclaimed by child process exit — the only cross-platform reset.
- `/analyze` takes COORDINATES (transcript path + prompt id), never text. No span, no offset, no prompt text in any response, log line, or published payload.
- `/analyze` must NOT use the inference single-flight `_dispatch` and must never consume the inference slot.
- Named-terms extraction is **ON by default** (`KELD_TERMS`, user decision). It can contain real person names; it is local-only and forwarded nowhere.
- `KELD_SIDECAR_MEM_BUDGET_MB` is **4096** and is the total for the whole service tree. The hard limit must never sit below `ceiling + KELD_SIDECAR_RSS_HARD_MARGIN_MB`.
- Sidecar tests are **standalone scripts with a `__main__` runner, never pytest**, run with `~/.keld/sidecar-venv/bin/python`.
- Go gates: `go build ./...` and `go test ./...`.
- **Port 33099 may be running a live study — never touch it.** Use another port; do not start the daemon.
- Never `git add -A`, `git checkout`, `git stash`, or `git clean`. Uncommitted user work must survive: `.gitignore`, `internal/agent/daemon/custom_passes*.go`, a `daemon.go` hunk, `scripts/context_value.py`, `scripts/prompt-v9.md`, `scripts/prose_activity.py`.


### Task 1: Separate spawning the service from provisioning the model

**Files:**
- Modify: `internal/agent/daemon/daemon.go` (`mlBackend`, lines ~1070-1125)
- Test: `internal/agent/daemon/daemon_test.go`

**Interfaces:**
- Produces: `sidecarService(ctx context.Context, emitter *clientevents.Emitter) (client *sidecar.Client, sup *Supervisor, healthFn func() bool, ok bool)` — everything `mlBackend` does BEFORE provisioning: binary lookup, `reapStaleSidecars`, ephemeral port, `agentcfg.SetSidecarPort`, client, supervisor. `ok == false` means no service this run (missing binary or port alloc failure); the caller decides what that means.

Pure refactor, no behaviour change. `mlBackend` keeps its exact signature and semantics — it calls `sidecarService` and, on `!ok`, returns `sidecarUnavailable(...)` exactly as today.

- [ ] **Step 1: Write the failing test** — assert `sidecarService` returns a client and supervisor without provisioning, and that `mlBackend`'s existing tests still pass unchanged.
- [ ] **Step 2: Run to verify failure.** Run: `go test ./internal/agent/daemon/ -run TestSidecarService -v`
- [ ] **Step 3: Extract the function.**
- [ ] **Step 4: Run.** Run: `go test ./internal/agent/daemon/ && go build ./...`
- [ ] **Step 5: Commit** — `git commit -m "daemon: spawning the service is not provisioning the model"`

---

### Task 2: Provisioning no longer gates the spawn

**Files:**
- Modify: `internal/agent/daemon/daemon.go` (`mlBackendWithOpts`)
- Test: `internal/agent/daemon/daemon_test.go`

Today `mlBackendWithOpts` starts the supervisor INSIDE the provisioning goroutine:

```go
go func() {
    if err := provision.EnsureModel(...); err != nil { ...; return }
    go opts.sup.Start(ctx)
}()
```

so until a ~1.9 GB download completes there is no service, and therefore no `/analyze`. Start the supervisor immediately and let provisioning run alongside it.

**Why this is safe, and what to verify:** the warm gate polls `/metrics` (`WorkerReady`), which never spawns a worker, so no inference is attempted before the gate opens. The gate opens only on `worker.state == "ready"`, and `Worker` actively calls `warmup` to trigger the load. With weights absent that warmup fails, the gate stays closed, and jobs queue/spool — the same outcome as today. **Confirm there is no tight retry loop**: `Worker` bounds warmup by `warmWait` per job. Assert that in a test rather than reasoning about it.

- [ ] **Step 1: Write the failing test** — the supervisor starts even when the provisioner blocks indefinitely; the warm gate stays closed while it does.
- [ ] **Step 2: Run to verify failure.**
- [ ] **Step 3: Move `go opts.sup.Start(ctx)` out of the provisioning goroutine.**
- [ ] **Step 4: Run.** Run: `go test ./internal/agent/daemon/ && go build ./...`
- [ ] **Step 5: Commit** — `git commit -m "daemon: the service starts before the model is provisioned"`

---

### Task 3: Deterministic mode starts the service and wires the analyzer

**Files:**
- Modify: `internal/agent/daemon/daemon.go` (`wireEnrichment` ~line 964, its callsite ~line 720, `process` ~line 442)
- Modify: `internal/agent/daemon/workstreams.go` (`analyzerFor` doc comment — its stated rationale becomes false)
- Test: `internal/agent/daemon/daemon_test.go`

**Interfaces:**
- Produces: `wireEnrichment(...) (handler http.Handler, model enrich.Model, analyzer enrich.WorkstreamAnalyzer, gate func() bool, enabled bool)` — one new return value.

`analyzerFor(m enrich.Model)` derives the analyzer from the Model by type assertion, so in deterministic mode (`model == nil`) it is nil and the workstreams pass never registers. The analyzer must therefore be produced by `wireEnrichment` and threaded to `process`, independent of the Model.

Behaviour per mode after this task:

| mode | service | model | analyzer | gate |
|---|---|---|---|---|
| `off` | not started | nil | nil | n/a — discard handler |
| `deterministic` | **started** | nil | **wired** | **service-up** (health), NOT worker warmth |
| `auto` | started | sidecar client | wired | worker warmth (unchanged) |

The deterministic gate is health-based because the worker will never be warm in that mode — a warm gate would hold every job forever. **DECIDED (user): gate on service-up.** A permanently-failing service therefore wedges deterministic mode rather than publishing credential-only profiles; that is the same trade auto mode already accepts, and it is deliberate. Do not silently substitute a trivially-true gate.

- [ ] **Step 1: Write the failing test**

```go
func TestDeterministicModeStartsTheServiceAndWiresTheAnalyzer(t *testing.T) {
    // ml_backend "deterministic": model is nil, analyzer is NOT nil, gate reflects
    // service health rather than being trivially true.
}

func TestDeterministicGateIsClosedUntilTheServiceIsUp(t *testing.T) {
    // A gate that is trivially true would publish workstream-less profiles on the
    // first jobs after startup; that is the thing this decision rejected.
}
```

Match the idiom of the existing `TestWireEnrichment*` tests — read them first.

- [ ] **Step 2: Run to verify failure.** Run: `go test ./internal/agent/daemon/ -run TestDeterministic -v`
- [ ] **Step 3: Implement** — `wireEnrichment` returns the analyzer; deterministic mode calls `sidecarService` and builds a health gate; `process` takes the analyzer instead of deriving it from the Model.
- [ ] **Step 4: Run.** Run: `go test ./... && go build ./... && gofmt -l internal/agent/daemon/`
- [ ] **Step 5: Update AGENTS.md** — the `ml_backend` "deterministic" bullet says "the sidecar is never started". That becomes false. Rewrite it: the service IS started, the model is never loaded because nothing asks for it.
- [ ] **Step 6: Commit** — `git commit -m "daemon: deterministic mode runs the analysis service"`

---

## Not in this plan

- **Analysis in a recycled worker child** (was Phase A). Deferred by the user as memory optimisation. Design is in the spec; it restores the flat-parent invariant and makes the 4GB budget fit.

- **`pipeline_status` overloading.** Deterministic mode still reports `"partial"` on every job because the seven model-dependent passes each count as failures. `"partial"` conflates "a pass failed" with "this mode has no such pass". Needs a `Run` change and a decision on a `facets_skipped` reason.
- **Whether `project`/`branch` may cross to Atlas.** Pending user decision; they are not published today.
- **The hard-limit cliff / budget-unsatisfiable warning.** In flight separately.
- **Codex and Gemini window analysis.** `WorkstreamsEligible` excludes them because `/analyze` resolves prompts by Claude-Code UUID. Supporting them needs per-source prompt resolution in `analyze.py`.
