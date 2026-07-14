# Design: purge the deterministic enrichment backend

**Date:** 2026-07-14
**Status:** design (approved), pending spec review
**Repo:** keld-cli (daemon + CLI + docs)

## Problem / directive

Keld Signal runs on-device ML; **there is no deterministic alternative** (product
directive). The pure-Go deterministic backend (`internal/agent/enrich/deterministic.go`)
must be purged from the daemon runtime and the docs. When enrichment is on it must always
run on the GLiNER2 sidecar (queue/spool until ready, never degrade); when the local
`ml_backend` is `"off"` enrichment is disabled entirely (no enrichment worker, no
enrichment publish — telemetry + client-events still flow).

## Current state (from the purge map)

- Runtime is *already* mostly ML-only: `enrich.NewRouter` has **zero** production call
  sites; when ML is on `mlBackend` returns the raw `*sidecar.Client` with a gate that
  never opens on fallback. Deterministic survives at runtime only in three
  `mlBackend` branches — `!MLEnabled()`, no sidecar binary, and the port-alloc-failure
  edge — plus the `--deterministic` diagnostic CLI path.
- `ml_backend` is a **local, startup-only** setting (`~/.keld/agent-config.json`), *not*
  in the Atlas `Remote` doc and never re-read at runtime. So "off = disabled" needs **no
  dynamic-toggle handling** — it's known once at startup.
- Deterministic is also a **test double** in ~10 test files and the **eval baseline**.

## Decisions (locked)

- **`ml_backend:"off"` ⇒ enrichment disabled** (not a lower-fidelity backend). Telemetry
  + client-events unaffected.
- **Sidecar not ready ⇒ queue/spool until ready** (readiness gate stays closed; never
  deterministic). Applies to the not-provisioned window, port-alloc failure, and
  supervisor give-up.
- **Test double ⇒ a test-only fake Model** in a new `internal/agent/enrich/enrichtest`
  package (`NewFake()`), carrying the current deterministic detection logic. NOT wired
  into the daemon or the shipped binary.
- **Eval ⇒ absolute gold-set thresholds** (drop the "beat deterministic" comparison).

## Runtime changes (daemon + CLI)

1. **`internal/agent/enrich/enrichtest/enrichtest.go` (new, test-support):** move the body
   of `deterministic.go` here as an exported `NewFake() enrich.Model` (same
   Classify/Entities/Extract detection: email/api_key/SSN/credit-card(Luhn)/phone +
   codegen/software keyword classification + deliberate abstention on
   activity/personal/function_guess/subcategory). This package exists so unit tests keep
   a real-ish, offline Model; it is imported only from `_test.go` files. (It may live in a
   normal package that only tests import — acceptable; it is never referenced by
   non-test daemon/CLI code.)

2. **Delete** `internal/agent/enrich/deterministic.go`, `deterministic_test.go` (move its
   assertions to `enrichtest` as the fake's own tests), `router.go`, `router_test.go`
   (router is dead in production and only routed to deterministic).

3. **`internal/agent/daemon/daemon.go` `mlBackend`:** remove the `deterministic()`
   closure and all three deterministic returns. New behavior:
   - `!set.MLEnabled()` → return a signal that enrichment is **disabled** (see #4). No
     model, no worker.
   - ML on: provision + supervise the sidecar exactly as today; return `(sidecarClient,
     gate=sup.Ready)`. The port-alloc-failure branch no longer returns deterministic — it
     emits `sidecar.fallback` and returns a **permanently-closed gate** (jobs queue/spool
     until restart), consistent with the supervisor give-up path.

4. **Enrichment-disabled wiring (`Run`):** when `!set.MLEnabled()`, do not start the
   enrich `Worker`, and make the `/enrich` ingress **accept-and-discard** (return `202`,
   do not enqueue) so the hook does not spool pointers that will never be processed
   (bounded: nothing accumulates). Telemetry, client-events, settings poll, spool sweep
   for *other* purposes remain. (Because `ml_backend` is startup-only, this is decided
   once; no runtime flip.) Add a startup log line noting enrichment is disabled.

5. **`internal/localagent/localagent.go` `ResolveModel`:** with no deterministic fallback,
   the `keld signal enrich` / `keld-agent enrich` diagnostic commands must reach the
   sidecar; if `SidecarPort == 0` / no healthy sidecar, return a clear error ("sidecar not
   running — start keld-agent / wait for provisioning") instead of a deterministic Model.
   Remove the `forceDeterministic` param.

6. **Remove the `--deterministic` flag** + its help text from `internal/cli/signalagent.go`
   and `internal/agentcli/enrichcmd.go`.

7. **`sidecar.fallback` client-event:** keep the event name and severities (renaming is an
   Atlas ingest-contract change — out of scope), but update its Go comments
   (`supervisor.go`, `daemon.go`) and the doc to the new meaning: the sidecar could not be
   brought up / gave up for this daemon run; the readiness gate stays closed and jobs
   queue/spool (then quarantine after `KELD_ENRICH_MAX_ATTEMPTS`) until restart — **no
   deterministic fallback**.

## Eval changes

8. **`internal/agent/enrich/eval/`:** rework `runner_test.go` (`TestRunModelOnDeterministicBaseline`)
   and `sidecar_eval_test.go` to score the sidecar against **fixed absolute thresholds**
   on the gold set (e.g. `sensitive_recall >= T`, `task_type_accuracy >= T`) instead of
   `sSA <= dSA` vs deterministic. Keep `eval.go` (`RunModel`/`Score`/`LoadGold`)
   infrastructure. The deterministic-baseline runner test either becomes a fake-baseline
   sanity check (using `enrichtest.NewFake()`) or is removed — implementer's call, but the
   **sidecar** gate must remain and must not reference deterministic.

## Docs changes (rewrite to ML-only posture)

9. Rewrite every deterministic-as-fallback mention (exact lines from the purge map):
   - **CLAUDE.md:26-31** (the "Never silently degrade" bullet) → ML is mandatory; sidecar
     always; when the sidecar can't come up, enrichment queues/spools until it can; ML off
     ⇒ enrichment disabled. No deterministic backend exists.
   - **AGENTS.md:** diagram node (25), the `deterministic.go` bullet (69-72), delivery-
     reliability (78-80), repo-tree line (180), the "permanent zero-dep fallback" line
     (241), provisioning line (303-304). Replace with: sidecar-only; ML off ⇒ disabled;
     not-ready ⇒ queue/spool.
   - **README.md:** 25, 34, 70-71, 82-91, 104, 177-178, 247-250 — same posture.
   - **docs/signal-client-events.md:144** — reword the `sidecar.fallback` row (drop "The
     deterministic model is used…").
   - **docs/keld-agent-p2-onnx-decision.md:59-63, 70-71, 75-76, 87-89** — this is a dated
     decision doc; add a short "**Superseded (2026-07-14):** deterministic backend purged —
     ML is mandatory" note at the top rather than rewriting the historical rationale
     line-by-line.
   - Leave dated `docs/superpowers/**` plan/spec artifacts as history (no edits).
   - Bump `enrich.SchemaVersion`? **No** — the label vocab/wire output is unchanged;
     removing a backend doesn't change the published schema. (Confirm no vocab change.)

## Testing

- New `enrichtest` fake has its own tests (the migrated `deterministic_test.go` assertions).
- All ~10 test files that called `enrich.NewDeterministic()` now call
  `enrichtest.NewFake()`; `go test ./...` stays green (same detections).
- New daemon tests: (a) `ml_backend:"off"` ⇒ Worker not started + `/enrich` returns 202
  without enqueue/publish; (b) sidecar-not-ready ⇒ gate closed, job stays queued/spooled,
  no publish (extend `TestMLBackendProvisionFailureDoesNotDegradeToDeterministic`).
- `grep -rn "NewDeterministic\|NewRouter\|forceDeterministic" internal/` returns **only**
  `enrichtest` + test files (no runtime references); `deterministic.go`/`router.go` gone.
- Eval: the sidecar gate runs against absolute thresholds; no deterministic reference.
- `go build ./...`, `go vet ./...`, `gofmt -l` clean.

## Decomposition (→ plan tasks)

1. `enrichtest.NewFake()` + repoint all test call sites (green build, no runtime change yet).
2. Daemon runtime purge: `mlBackend` sidecar-only + ML-off-disabled wiring + gate-closed-on-not-ready; delete `deterministic.go`/`router.go`; `sidecar.fallback` comments.
3. CLI/localagent: `ResolveModel` sidecar-or-error, remove `--deterministic` flags.
4. Eval: absolute gold thresholds.
5. Docs rewrite.

Tasks 1→2 are ordered (2 deletes what 1 stops depending on). 3/4/5 are independent after 2.
