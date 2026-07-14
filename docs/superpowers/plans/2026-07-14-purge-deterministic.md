# Purge the deterministic backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Remove the deterministic enrichment backend from the daemon runtime + docs. ML-only: enrichment always runs on the GLiNER2 sidecar (queue/spool until ready); `ml_backend:"off"` disables enrichment entirely.

**Spec (full detail + rationale):** `docs/superpowers/specs/2026-07-14-purge-deterministic-design.md` — read it; this plan is the task breakdown.

## Global Constraints
- ML-only: no code path may run enrichment on anything but the GLiNER2 sidecar. When the sidecar isn't ready, the readiness gate stays closed and jobs queue/spool — never degrade.
- `ml_backend:"off"` (local, startup-only) ⇒ enrichment disabled: no enrich Worker, `/enrich` accepts-and-discards (202, no enqueue), no enrichment publish. Telemetry + client-events unaffected.
- The deterministic detection logic survives ONLY as a test-only fake (`internal/agent/enrich/enrichtest`), never referenced by daemon/CLI non-test code.
- Do NOT rename the `sidecar.fallback` client-event (Atlas ingest contract) or bump `enrich.SchemaVersion` (label vocab unchanged). `--json` NDJSON / event payloads unchanged.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- After each task: `go build ./...`, `go test ./...`, `go vet ./...`, `gofmt -l` touched files.

---

## Task 1: `enrichtest.NewFake()` + repoint test call sites (no runtime change yet)
**Files:** create `internal/agent/enrich/enrichtest/enrichtest.go` (+ `enrichtest_test.go`); modify the ~10 test files listed below.
- [ ] Create `internal/agent/enrich/enrichtest/enrichtest.go`: `func NewFake() enrich.Model` carrying the CURRENT body of `internal/agent/enrich/deterministic.go` (rename the type to `fake`; same Classify/Entities/Extract, same patterns/keywords, `luhnValid`). It returns `enrich.Model`. Keep the exact detection semantics (email/api_key/SSN/credit-card-Luhn/phone; codegen/software keyword classification; abstain on activity/personal/function_guess/subcategory) — the dependent tests assert these.
- [ ] Move `deterministic_test.go`'s assertions into `internal/agent/enrich/enrichtest/enrichtest_test.go` (they now test `NewFake()`). (Leave `deterministic.go` in place for now so the build stays green; Task 2 deletes it.)
- [ ] Repoint every `enrich.NewDeterministic()` in TEST files to `enrichtest.NewFake()` (add the import): `internal/agent/enrich/pipeline_test.go`, `extractors_test.go`; `internal/agent/privacy_test.go`; `internal/agent/publish/publish_test.go`; `internal/agent/daemon/daemon_test.go` (lines ~52,94,132,220), `clientevents_wiring_test.go` (~56,89); `internal/localagent/localagent_test.go` (~79); `internal/agent/enrich/eval/runner_test.go`, `sidecar_eval_test.go` (repoint now; Task 4 reworks their assertions). NOTE `enrich/*_test.go` are in package `enrich` → importing `enrichtest` (which imports `enrich`) would cycle: put `enrichtest` in its own package importing `enrich`, and for the in-package `enrich` tests (pipeline_test, extractors_test) either make them external test package (`package enrich_test`) or keep using a local fake — simplest: give `enrich` package tests a tiny local `newFake()` (or move those tests to `enrich_test` and import enrichtest). Implementer picks the cycle-free approach; the daemon/publish/privacy/localagent tests (different packages) import `enrichtest` cleanly.
- [ ] `go test ./...` green (same detections, no runtime change). Commit `test(enrich): add enrichtest.NewFake test double; repoint tests off NewDeterministic`.

## Task 2: daemon runtime purge (the core)
**Files:** `internal/agent/daemon/daemon.go` (`mlBackend`, `mlBackendWithOpts`, `Run` wiring), `internal/agent/ingress/ingress.go` (accept-and-discard when disabled), `internal/agent/daemon/supervisor.go` (comment), delete `internal/agent/enrich/deterministic.go` + `deterministic_test.go` (moved in T1) + `internal/agent/enrich/router.go` + `router_test.go`.
- [ ] Write failing daemon tests: (a) `ml_backend:"off"` ⇒ enrich Worker not started AND `POST /enrich` returns 202 without enqueue/publish (assert the queue stays empty + Sender never called); (b) extend `TestMLBackendProvisionFailureDoesNotDegradeToDeterministic` / add one asserting port-alloc-failure path returns a closed gate (no publish, job queued/spooled) — never a deterministic publish.
- [ ] `mlBackend`: delete the `deterministic()` closure + all deterministic returns. `!MLEnabled()` → return a disabled signal (e.g. `(nil, nil)` + a bool, or a dedicated sentinel) consumed by `Run` to skip the worker. Port-alloc failure → emit `sidecar.fallback` (SevWarn) + return a permanently-closed gate (`func() bool { return false }`), NO deterministic. Provisioning/supervisor paths unchanged (already gate-closed).
- [ ] `Run`: when enrichment disabled, do not `go Worker(...)`; wire the ingress to accept-and-discard (`/enrich` → 202, no `q.Offer`). Log `keld-agent: enrichment disabled (ml_backend=off)`. When enabled, unchanged.
- [ ] Delete `deterministic.go`, `deterministic_test.go`, `router.go`, `router_test.go`. Update `supervisor.go`'s deterministic-mentioning comments (~line 86) to the no-fallback meaning.
- [ ] `grep -rn "NewDeterministic\|NewRouter" internal/ --include=*.go` → only `enrichtest` + tests (zero runtime). `go build/test/vet` green. Commit `feat(agent): purge deterministic backend from the daemon (sidecar-only; ml off = disabled)`.

## Task 3: CLI + localagent (no deterministic fallback in diagnostics)
**Files:** `internal/localagent/localagent.go` (`ResolveModel`), `internal/localagent/localagent_test.go`, `internal/cli/signalagent.go`, `internal/agentcli/enrichcmd.go`.
- [ ] `ResolveModel`: drop `forceDeterministic`; if no healthy sidecar (`SidecarPort==0`), return a clear error ("keld-agent sidecar not running or still provisioning — ML enrichment unavailable") instead of `NewDeterministic()`. Update the `MetricsURL` "deterministic backend in use" string.
- [ ] Remove the `--deterministic` flag + help text from `keld signal enrich` (`signalagent.go`) and `keld-agent enrich` (`enrichcmd.go`); update their `Long` text (drop "otherwise the deterministic backend").
- [ ] Update `localagent_test.go` (`TestResolveModel`, `TestEnrichJSON`) for the new signature/behavior (use `enrichtest.NewFake()` where a Model is needed, or a running-sidecar stub). `go test ./...` green. Commit `feat(cli): keld/keld-agent enrich require the sidecar (no deterministic)`.

## Task 4: eval — absolute gold thresholds
**Files:** `internal/agent/enrich/eval/runner_test.go`, `sidecar_eval_test.go` (keep `eval.go`).
- [ ] Rework the sidecar eval gate to score against fixed absolute thresholds on the gold set (e.g. `Score(...).sensitive_recall >= T`, task-type accuracy `>= T`) — pick conservative thresholds from a current sidecar run; drop the `sSA <= dSA` deterministic comparison. The deterministic-baseline `TestRunModelOnDeterministicBaseline` becomes a fake-baseline sanity check (using `enrichtest.NewFake()`) or is removed. No deterministic reference remains in the sidecar gate. Commit `test(eval): score the sidecar against absolute gold thresholds (drop deterministic baseline)`.

## Task 5: docs rewrite (ML-only posture)
**Files:** `CLAUDE.md`, `AGENTS.md`, `README.md`, `docs/signal-client-events.md`, `docs/keld-agent-p2-onnx-decision.md`.
- [ ] Rewrite each deterministic-as-fallback mention (exact lines in spec §9) to: sidecar-only; `ml_backend:"off"` ⇒ enrichment disabled; sidecar-not-ready ⇒ queue/spool until ready (never degrade); no deterministic backend exists. For `keld-agent-p2-onnx-decision.md` add a top "**Superseded 2026-07-14: deterministic purged, ML mandatory**" note instead of rewriting the historical rationale. Reword `signal-client-events.md`'s `sidecar.fallback` row (drop "The deterministic model is used…"). Leave dated `docs/superpowers/**` artifacts untouched.
- [ ] `grep -rin "deterministic" CLAUDE.md AGENTS.md README.md docs/signal-client-events.md` → only intentional historical/negative mentions ("no deterministic backend", "superseded"). Commit `docs: purge deterministic backend; document ML-only posture`.

## Self-Review
- Runtime deterministic references removed (T2); test-fake preserves coverage (T1); CLI diagnostics error instead of degrade (T3); eval keeps a real gate w/o deterministic (T4); docs match reality (T5).
- ML-off = disabled wiring (no unbounded spool): ingress accepts-and-discards, worker not started (T2).
- Contract stability: no `sidecar.fallback` rename, no `SchemaVersion` bump, `--json` unchanged.
