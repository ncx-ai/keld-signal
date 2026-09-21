# Model backends, what a fresh install lands on, and delivery reliability

> **Provenance — split out of `AGENTS.md` on 2026-09-17** (at `67be5f5`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-09-14**.
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *Model backends* states the three modes and the never-degrade rule.
This file carries each mode's full behaviour, the installer's config write, the
block-emitter backfill incidents and the deadline arithmetic.

**Model backends.** `ml_backend` (local, startup-only, `settings.Settings`)
selects one of three modes:
- **`"auto"`/`""` (default)** — Enrichment is **ML-only** for its full facet
  set: there is no deterministic *substitute* for the model's own facets. A
  reloading/evicted/not-yet-provisioned sidecar is waited out, never silently
  swapped for a lower-fidelity stand-in of the same facets — that swap is the
  thing this project forbids. `sidecar/` — HTTP client to the GLiNER2 sidecar
  (`/classify`, `/extract`, `/entities`); the sole `Model` implementation.
  Model provisioning (`provision/`) fetches weights (`hf.go`) into
  `~/.keld/models` **on demand** — see the provisioning gotcha below. Why a
  bundled sidecar over in-process ONNX:
  `docs/keld-agent-p2-onnx-decision.md` (historical — see the superseded note
  at its top).
- **`"deterministic"`** — enrichment stays **on** and `Worker` runs with a
  `nil` `enrich.Model`. This is a *different* set of facets — the ones that
  need no model at all, e.g. credential detection (`CredentialSpans`) and
  regular-format PII detection (`PIISpans` — ssn/credit_card/email; both pure
  Go, no sidecar, no network) and the **workstream dimensions** the sidecar's
  `/analyze` derives from transcript coordinates — not a fallback for the
  model's facets.
  **The analysis service still runs; only the model is never loaded.** The
  sidecar is the client-side analysis-and-enrichment service in general
  (`/analyze`, `/match`, `/vocabulary`, `/classify`, `/extract`) and GLiNER2 is
  one capability it loads lazily on its first inference — so this mode starts
  the service (`deterministicBackend` → `sidecarService` + `go sup.Start`) and
  simply never issues an inference, which means the weights are never loaded
  and never even provisioned. Not starting it was a trap: `analyzerFor` came
  back nil, the workstreams pass never registered, and the mode published a
  single credential-derived facet.
  When a service exists, its readiness gate **polls service health**
  (`/health`), not model warmth — the model never warms here, so a warmth gate
  would hold every job forever, and a trivially-true gate would publish
  workstream-less profiles for every job that landed before the service
  finished starting. It polls in the **background** and the gate reads a cached
  atomic (`serviceHealthGate`, the same `warmGate` mechanism `"auto"` uses for
  warmth) — `Worker` calls the gate per job and `waitWarm` re-calls it every
  ~20ms, so a gate that probed `/health` inline would cost thousands of
  loopback connects per deferred job, and a full client timeout on *every* call
  against a service that accepts TCP but never answers. Waiting there is right: the supervisor is bringing the
  service up, so the work becomes doable shortly, and a service that is present
  but never comes up **wedges** this mode (jobs queue/spool) rather than
  degrading — the same trade `"auto"` makes.
  **When there is no service at all, the gate is trivially true instead.**
  `sidecarService` reports `ok == false` when no sidecar binary is installed
  (`err == nil`) or the loopback port could not be allocated (`err != nil`);
  neither resolves without a daemon restart, so `deterministicBackend` takes
  `noAnalysisService` — one `sidecar.unavailable` event (the two causes stay
  distinguishable via its `reason`/`error` field), an open gate, and a **nil**
  analyzer. Enrichment then runs its remaining model-free facets (credential
  detection) with the workstreams pass simply unregistered — absent from
  `extractor_versions` rather than present-and-failed, and so not a downgrade
  (see `pipeline_status` below). Holding the gate here would wedge the mode
  forever on what is the state of **every** machine before the sidecar tarball
  is fetched. Dropping the facet entirely and reporting it dropped is not the
  substitution never-degrade forbids — nothing lower-fidelity stands in for
  window analysis.
  ⚠️ **This used to carry a known gap — "a service that starts and then
  permanently gives up (supervisor restart cap exhausted) still wedges this
  mode" — and on 2026-09-09 a real machine fell into it overnight, so the gap
  is closed at its cause rather than distinguished.** The supervisor no longer
  gives up: after `maxRestarts` CONSECUTIVE failed starts it **rests** (one
  minute, doubling to thirty) and tries again on its own, a `RequestRestart`
  (the page's Restart button, the health owner's ladder) ends the rest at once,
  and a child that answers `/health` resets the budget — four crashes over a
  month are four recoveries, not a crash loop. `ErrSupervisorStopped` therefore
  means only "Start has not begun or the daemon is shutting down". Two clock
  facts made the night possible and both are fixed in the same commit
  (`supervisor.go` → `defaultStartSleepGap`, `servicehealth.go`'s detector):
  the sidecar's **readiness deadline is measured in time the machine was
  AWAKE** — a wall-clock jump between two health polls re-arms it instead of
  spending it, because macOS wakes for ~2 seconds every 15 minutes and each
  wake used to kill a child as a "failed start" (three of them spent the cap);
  and both sleep detectors compare **wall-clock instants (`Round(0)`)**,
  because on macOS Go's monotonic clock does not advance while the machine
  sleeps, which is why the health owner's own detector logged nothing across a
  night of sleep. A gate that waits on this service therefore waits for a
  bounded rest, never for a human.
  `wireEnrichment` returns the analyzer as its own value (derived from the
  service client, not from the `Model`) and threads it to `process`.
  `Settings.MLEnabled()` is false in this mode;
  `Settings.EnrichmentEnabled()` is true. The pipeline tolerates a nil `Model`:
  `enrich.runStage` skips any `Extractor` that needs a model and hasn't opted
  in via the `modelFreeExtractor` capability (mirroring `alwaysRunner`) —
  `SensitivityExtractor` is the one built-in that implements it, since its
  credential layer runs regardless of the model and its NER half is simply
  skipped when `ctx.Model == nil` — a clean skip, decided before `Run`, rather
  than a nil-interface panic.
  **A skip is NOT a failure.** `runStage` returns a tri-state
  (`passOK`/`passFailed`/`passSkipped`): a pass that needs a Model where there
  structurally is none does not set `anyFailed`, so a deterministic run whose
  every executed pass succeeded publishes `pipeline_status:"enriched"`.
  `"partial"` keeps its one meaning — something that should have worked did
  not (panic, error, pass deadline) — including for a model-free pass that
  errors in this mode. This is `WithWorkstreams`' idiom one level down: don't
  downgrade a profile for a facet the run never had. The thinner facet set
  stays **visible** in the new `Profile.FacetsSkipped` / wire
  `facets_skipped` (omitted when empty, so auto-mode payloads are unchanged) —
  always a subset of the `extractor_versions` keys, since a pass that was
  never registered at all (unwired workstreams — see
  `docs/architecture/window-analysis.md`) is absent from both.
  **A HALF-run pass is neither.** `sensitivity` is `ModelFree`, so it RUNS in
  this mode — and, since it consults no model at all, it runs WHOLE whenever the
  PII scan is available. What still half-runs it is a missing/failed/truncated
  **scan**: only the credential layer is left, so an SSN with no credential
  pattern publishes `sensitivity:"none"`, a confident negative from a check
  nobody performed. A pass therefore declares reduced
  capability per job via the optional `degradedExtractor` capability
  (`Degraded(ctx) bool`, consulted after a successful `Run`, mirroring
  `modelFreeExtractor`); `runStage` reports `passDegraded`, the result commits
  normally, and the pass is named in `Profile.FacetsDegraded` / wire
  `facets_degraded` — a **sibling** of `facets_skipped`, not a member: a
  skipped facet has no value, a degraded one has a real value to be read as
  "from the checks that ran". Both lists are subsets of the
  `extractor_versions` keys (pinned by a test), both are omitted when empty,
  and neither moves `pipeline_status`. The sensitivity **vocabulary** is
  unchanged — `"none"` plus the marker is the honest pair, and a new
  `"unknown"` label would be a contract break for no extra information — so
  that work bumped nothing. (The dynamics block below took `SchemaVersion`
  7 → 8 when it began publishing; the current value is stated once, in AGENTS.md, at
  the `labels.go` bullet — don't restate it here, it only goes stale twice.)
- **`"off"`** — enrichment is **disabled entirely**: no enrichment worker is
  started and `/enrich` accepts-and-discards (returns 202, never enqueues).
  Telemetry and client-events are unaffected.

⚠️ **WHAT A FRESH INSTALL LANDS ON, AND WHY IT IS NOT THE COMPILED-IN DEFAULT.**
`keld-agent install` writes three keys into `~/.keld/agent-config.json` —
`{"ml_backend": "deterministic", "blocks": true, "attribution": false}` — via
`settings.WriteInstallDefaults`, which MERGES, so an operator's `pii_regions`,
`include_entity_text` and feature toggles survive an installer run. That is v2: the
model-free facet set plus the block emitter, and **no multi-gigabyte model download,
ever**. ⚠️ **`attribution` is written OFF unconditionally, and until 2026-09-09 it
was written as a copy of `blocks` — i.e. ON.** A fresh install therefore switched on
vector attribution (a 1.2 GB text-model download and a pass that reads messages on
the device) for a person who had chosen nothing, for a feature still being built.
It is now a DEVELOPER control on the page — the Developer box, behind seven taps on
the version — and a re-install converges it to off the way `ml_backend` converges,
so the machines `v3.0.0-rc.1` turned it on for are turned back off by the next
install. `KELD_ATTRIBUTION` still wins in both directions.

⚠️ **FIRST SIGHT BACKFILLS, and that default is a REVERSAL.** The block emitter
used to seed its cursor at the transcript's watermark and emit nothing on first
sight — so on a fresh install every block that already existed was unreachable
(measured on this repo's corpus: **24 closed blocks in one transcript, none of
which ever reached Atlas**), plus, permanently, the block each transcript was
mid-way through when first seen. The reasoning was an analogy to
`KELD_WATCH_BACKFILL` — a restart must not "emit a herd of history" — and the
analogy does not hold: the watcher's backfill re-reads whole transcripts from
disk and is unbounded in FILE SIZE, while a block backfill is a query against a
store that already holds the answer, bounded to `maxPerSweep` (24, the sidecar's
own `DEFAULT_MAX_BLOCKS`) per transcript per sweep and drained across sweeps by
the cursor. The pacing that makes it safe was already there. `KELD_BLOCKS_BACKFILL=0`
restores forward-only, and that branch keeps its tests rather than being deleted.
Re-emission is free either way: a block's identity is `(session, block.start)`
and Atlas upserts, so a re-delivered block is not a duplicate.

⚠️ **BACKFILL NEEDED TWO MORE THINGS, AND EACH FAILED SILENTLY WITHOUT THE NEXT.**
The emitter can only cut blocks for a transcript the STORE has ingested, and it
only sees transcripts in its active set, which the watcher's advance signal
fills. So:
1. **First sight has to signal at all.** Under forward-only `scanFile` set a
   first-sighting cursor to EOF and returned EARLY, so `advanced` never fired and
   a transcript entered the active set only when it next GREW — a session that
   ended yesterday could never be backfilled, whatever the toggle said. The two
   paths are now separated at that sighting: the PROMPT path stays forward-only
   (offering every historical prompt for enrichment is a real herd, and the EOF
   cursor is what prevents it — measured, 2 enrichments rather than 2,152 across
   a full fresh-install simulation), while the ingest/blocks signal fires,
   because it carries coordinates only and does not depend on the cursor.
2. **Those signals have to be PACED.** The ingest signal rides a 64-slot,
   path-coalescing queue whose policy is DROP rather than retry — safe for a
   growing transcript because the next signal catches up, and unsafe for a first
   sighting, which has no next signal. Firing all of them at once on a machine
   with **2,152 known transcripts filled the 64 slots and dropped ~2,088
   permanently**, and the dropped ones were exactly the dormant transcripts the
   change existed to reach. First sightings now drain `firstSightPerPoll` (4) per
   poll; four because the real limit is downstream — one serial sender, and a
   first whole-file ingest measured 5.1s on a 90 MB transcript. Measured end to
   end: `parse_state` 8 → 109 in two minutes (~51 transcripts/min, ~13 minutes
   for 683), with system load FALLING (2.41 → 1.13) rather than spiking.
   ⚠️ A refused signal is RETRIED, not dropped: `drainFirstSight` pops an entry
   only when the hook reports it was taken on, so the pacing rate is real
   backpressure rather than a constant guessed against the sidecar's throughput.

The two-key config write is written FIRST in `runInstall`, before login, because `ml_backend` is
read at daemon startup and never re-read — the restart inside `installService`
(`launchctl bootout`+`bootstrap` / `systemctl --user restart` / `schtasks /End`+`/Run`)
is what makes the new mode take effect in the same run. On macOS that is not
academic: the pkg's `postinstall` kickstarts the agent BEFORE opening
`onboard.command`, so a daemon is already running on the old settings by then. A
test pins the order.

The **compiled-in defaults are unchanged** (`ml_backend` zero value = `"auto"`,
`Settings.Blocks` = false) and that is deliberate, not a hedge. Every machine an
installer never writes to — a binary upgraded in place, `go run`, CI, the eval
harness — keeps the full ML facet set, `TestBuiltInPipelineStillDemandsAModel` keeps
passing untouched, and Atlas's Context column keeps rendering `function_guess` /
`subcategory` / `activity_type` for that population. Phase 5 of
`docs/superpowers/specs/2026-08-25-signal-block-pipeline-design.md` (the default
flip) is still unstarted and still gated on Atlas.

⚠️ **`ml_backend` has NO REMOTE OVERRIDE.** It is local and startup-only and nothing
in `agentcfg/` touches it, so Atlas can neither move a machine between modes nor roll
one back. **The installer is the only lever that will ever exist**, which has two
consequences worth stating rather than discovering: a re-install FLIPS an existing
`auto` machine (deliberate — every re-install converges), and an existing fleet's
Atlas Context column therefore empties machine-by-machine at whatever pace people
upgrade, with no server-side brake. If that pace ever needs controlling, the control
is a staged rollout of the installer itself. `--backend auto|deterministic|off` on
`keld-agent install` is the manual path back.

`blocks` likewise has no remote override — an asymmetry with `Remote.Features`, which
CAN turn feature rows off fleet-wide — and `KELD_BLOCKS=0` is what switches a single
machine off without editing JSON. The key exists at all because an env-only toggle is
**unreachable from an installer**: `LaunchAgentPlist` and `SystemdUnit` carry no
environment block and the Windows task is a bare `/TR "<exe>" run`, so there is
nowhere to put `KELD_BLOCKS` that the daemon would see.

⚠️ **`make install-linux` routes through `keld-agent install` too** (`Makefile`'s
`install-service` target), so a dev machine converges on `deterministic` like
everyone else — pass `--backend auto` to keep exercising GLiNER2 locally. And
⚠️ **do not run `keld-agent install` to test the config write**: `KELD_HOME`
isolates `~/.keld` but NOT the service path, which `service.Install` resolves from
`os.UserHomeDir()` — it will rewrite your real unit to point at the `go run` temp
binary and restart it into `failed`.

**Delivery reliability (never degrade, never wedge).** When enrichment is
enabled, it always runs on GLiNER2 — there is no fallback to swap to. A sidecar
that isn't ready yet (not yet provisioned, restarting, mid-recycle, or the
supervisor couldn't bring it up at all) keeps the readiness gate **closed**:
jobs simply queue/spool until the sidecar is ready, they are never processed by
anything else. `ml_backend:"deterministic"` obeys the same rule against a
different definition of ready — the service answering `/health`, since it asks
for no inference — but only when a service actually exists to become ready. With
no sidecar installed at all it runs on without window analysis rather than wait
for something that cannot arrive (see *Model backends* above).

**Deadlines are PER PASS, not per job** (`KELD_ENRICH_PASS_TIMEOUT`, default
30s). Per-pass is the only correct unit: a job issues 8-9 inferences, so a
job-wide budget meant one slow pass discarded *every pass that had already
succeeded* and re-spooled the whole job — the same work redone and re-discarded
until the attempt budget ran out. That amplification kept the sidecar in
permanent burst and was the driver behind the RAM-oscillation incident. Bounded
per pass, a slow pass costs exactly one facet: `runStage` reports it failed, the
other passes commit, and the profile publishes as `pipeline_status:"partial"`.
Progress is monotonic. Each pass deadline is a child of the job context (via
`enrich.WithJobContext`) and is bound to the backend through
`enrich.ContextModel` (`Client.WithModelContext`), so expiry aborts that pass's
in-flight sidecar call instead of leaving an orphan attempt consuming the
single-flight sidecar.

`KELD_ENRICH_JOB_TIMEOUT` (default **5m**) is now only a **wedge backstop** for a
job stuck outside a pass (resolve, publish). It must stay above
`passes x KELD_ENRICH_PASS_TIMEOUT` or it pre-empts the per-pass deadlines and
resurrects the discard-everything failure mode; a unit test pins that invariant.
A job that trips the backstop re-spools, **bounded** by
`KELD_ENRICH_MAX_ATTEMPTS` (default 4) — an exhausted job is
`spool.Quarantine`'d to `spool/bad/` rather than retried forever. Atlas dedups on
`dedup_key`, so a late double-publish from a recovering attempt is harmless.

