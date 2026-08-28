# The sidecar is a service, not a model runner

**Status:** design, awaiting review. Follows the `/analyze` branch (`c04a4e5..37eebb8`).

## The mistake this corrects

The sidecar has been treated throughout as "the GLiNER2 sidecar". It is not. It is the client-side
**analysis and enrichment service**: `/analyze`, `/match`, `/vocabulary`, `/entities`, `/classify`,
`/extract`. GLiNER2 was the initial driving use-case and is now **one capability the service may
load**, not its identity. Deterministic analysis — digest, stats, workstreams — needs none of it.

**Requirement: the service starts and serves without GLiNER2, and never requires GLiNER2 to be
loaded before it can run.**

## The service already complies. The daemon does not.

Verified in code:

- The GLiNER2 worker is spawned only by `WorkerManager.call()` → `_ensure_up()`. Only the inference
  endpoints reach it.
- `/analyze` calls `analyze_window` directly and never touches the worker.
- `/health` documents this deliberately: "DOWN/SPAWNING/READY all serve (a request spawns the
  worker lazily); only HELD cannot."

Two daemon-side defects break it:

1. **Provisioning gates the spawn.** `mlBackend()` runs `provision.EnsureModel` (~1.9 GB download)
   *before* spawning, with the comment "starting a sidecar against an unprovisioned model dir".
   True for inference; false for the service. Until the download completes there is no service at
   all, so no `/analyze` either.
2. **Deterministic mode never starts the service.** `wireEnrichment` returns early with no sidecar,
   so `analyzerFor(nil)` is nil and the workstreams pass is never registered. The mode built for
   deterministic workstreams produces none.

Consequence today: `claude_code` × `auto` is the only source × mode combination that publishes a
workstream, and `ml_backend:"deterministic"` ships as a trap.

## The change

**One service, started whenever enrichment is enabled.**

1. `wireEnrichment` starts the service for every mode except `"off"` — `auto` and `deterministic`
   alike. Mode decides whether a `Model` is wired, not whether the service exists.
2. **Provisioning moves off the startup path.** Spawn the service first; provision in the
   background. Only model-backed facets wait on the model. In deterministic mode the model is never
   requested, so it is never provisioned and never loaded — by absence of demand, not by a flag.
3. `analyzerFor` is wired from the service client in both modes, so the workstreams pass registers
   in both.
4. Memory follows demand: service ~60 MB, plus ~619 MB when spaCy loads for named terms, plus
   ~2,740 MB only if and when an inference actually runs.

## Readiness — the one real design question

Today a single gate holds all work until the sidecar is ready. AGENTS.md is deliberate about this:
a not-ready backend keeps the gate CLOSED and jobs queue or spool; they are never processed by
something lesser. That rule must survive.

But the gate now has two distinct dependencies:

- **model-backed facets** need the model provisioned AND the worker able to spawn
- **analysis facets** need only the service's HTTP up
- **credential detection** needs nothing at all

Options:

  A. Gate on service-up in deterministic mode; gate on service-up AND model in auto mode.
     Workstreams then work on the very first job. A permanently-failing service wedges
     deterministic mode entirely, even though credential detection could still run.
  B. Trivially-true gate in deterministic mode (today's Task-4 behaviour). Nothing wedges, but the
     first jobs after startup publish `"partial"` with no workstreams until the service comes up.

**DECIDED: A** (user, 2026-08-23). It matches the existing philosophy — do not process work you cannot do
properly — and the wedge risk is the same one auto mode already accepts. B trades a permanent
correctness cost (early jobs silently missing their dimensions) for a failure mode that is already
tolerated elsewhere.

## Analysis runs in a worker child, not the parent

**DECIDED (user, 2026-08-23).** spaCy in the parent costs ~619 MB permanently, because the parent is
never recycled. That breaks the load-bearing claim in AGENTS.md — "the service holds no model and
its own RSS stays flat regardless of uptime" — and makes the config infeasible: parent 620 +
worker drift ceiling 3409 + hard margin 512 = 4540 MB against a 4096 MB budget.

Analysis therefore moves into a **recycled child process**, exactly the way inference already
works. One service still, unchanged from the outside; the parent goes back to ~60 MB flat and
analysis memory becomes reclaimable by process exit — the same cross-platform reset the inference
worker relies on.

    parent      ~60 MB   flat, never recycled, holds nothing
    analysis   ~680 MB   child, recycled, holds spaCy
    gliner2   ~2740 MB   child, recycled, lazy - only if inference is asked for
                         peak ~3480 MB of 4096

Rejected: raising the budget to 4.6 GB (makes the permanent-parent cost official and kills the
flat-RSS claim) and lowering the token ceiling (trades measured model quality for spaCy, and
AGENTS.md is explicit that ceiling values are measured, not estimated).

## Out of scope

- `pipeline_status` overloading. Deterministic mode still reports `"partial"` on every job because
  the seven model-dependent passes each count as failures. That is a `Run` change and a separate
  decision: `"partial"` currently conflates "a pass failed" with "this mode has no such pass".
- The 67 MB of slack now visible above the drift ceiling. Surfaced by the parent-reserve fix;
  it is a budget question, not a wiring one.
- Whether `project`/`branch` may cross to Atlas. Pending user decision.
