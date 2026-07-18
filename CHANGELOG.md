# Changelog

All notable changes to **keld-signal** (the Keld client — the `keld` CLI + the
`keld-agent` on-device enrichment daemon + its GLiNER2 sidecar). Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); the project uses
semantic-ish versioning during `0.x`.

## [Unreleased]

## [0.6.0] — 2026-07-18

New enrichment facet **`speech_act`** — a subject-independent, structural signal:
whether the current prompt is a `command`, `question`, `statement`, or `fragment`.

### Added
- **`speech_act` facet (schema v5).** A new Wave1 extractor classifies the current
  prompt (its text only, not the session context) into `command` / `question` /
  `statement` / `fragment`, emitted in the `Profile` and carried on the Atlas
  enrichment payload. Label wording was bakeoff-selected (`command` = "a task to
  carry out" recovers imperatives the model reads as *describing* a task). Measured
  `speech_act` accuracy **0.731** (gold+confound); zero regression on every existing
  facet. Groundwork for a future lever that uses speech-act to disambiguate
  task_type/activity (e.g. a question → answer, not code).
- **Adversarial `s1` eval class + speech-act metrics.** `keld-agent eval` gains
  `speech_act` accuracy (per-mood) and `s1_downstream_baseline` (headroom for the
  conditioning lever), backed by 20 "mood-is-the-trap" rows and `speech_act` labels
  on the full gold set.

### Changed
- **SchemaVersion 4 → 5** — signals the new emitted `speech_act` field to Atlas
  (existing label vocabularies unchanged).

## [0.5.0] — 2026-07-17

Enrichment **classification quality** — the on-device model now labels a session
by the *work being done*, not the *subject the software is about*.

### Changed
- **task_type classifies against readable label descriptions, not bare id
  strings (A6, schema v4).** task_type was the last facet still handed the bare
  vocabulary words (`codegen`, `other`, …), so `other` became an undefined
  catch-all that swallowed genuine engineering work phrased as
  debug/fix/refactor/CI/infra/ops. It now classifies over short discriminative
  descriptions, with codegen framed as **"software engineering"** (not "code
  generation"). Measured on the eval harness: task_type subject-leakage
  **0.625 → 0.062** and gold task_type accuracy **0.580 → 0.696**, with
  function-leakage and false-eng unchanged at **0**. Escape hatch:
  `KELD_ENRICH_TASKTYPE_DESCRIPTIONS=off` restores bare-string classification.
- **SchemaVersion 3 → 4** — signals the task_type derivation change to Atlas
  (label vocabulary unchanged, same as the v3 A0/A4 bump).

## [0.4.0] — 2026-07-17

Enrichment **delivery reliability** — the arc that makes enrichment actually land
on every prompt, always on GLiNER2, without wedging — plus the first
**activity-vs-subject** classification fix.

### Added
- **Activity-vs-subject enrichment fix (A0 + A4, schema v3).** Coding sessions
  that build marketing/finance/etc. software no longer inherit the *subject's*
  business function. `task_type` now reads the session context preamble (A0), and
  `function_guess` for interactive coding tools is derived compositionally as
  `eng` rather than topically (A4, disable with
  `KELD_ENRICH_COMPOSITIONAL_FUNCTION=off`). Function subject-leakage 0.375 → 0.
- **Durable spool.** The hook writes each prompt *pointer* (never text) to an
  on-disk spool when the daemon is unreachable; the daemon drains it on startup
  and a periodic sweep. No more silently-lost enrichments during daemon downtime.

### Fixed
- **Never silently degrade to deterministic.** An idle-evicted or restarting
  sidecar is now waited out (client wake + retry) so every enrichment runs on
  GLiNER2 — instead of falling back to the deterministic backend (which abstains
  on job-category), which had produced "PHI flagged but no job category" results.
- **Enrichment death-spiral / stalled delivery.** The per-job timeout was an
  illusion: it stopped *waiting* but couldn't cancel the work, and sidecar calls
  ran on the daemon-lifetime context, so they retried `503`s forever. Every
  re-spool stacked another set of immortal retry loops until the single-flight
  sidecar saturated and shed everything — no enrichments published. Jobs now run
  under a per-job context that **cancels in-flight sidecar calls on timeout**
  (`sidecar.Client.WithContext` + requests bound to the context), so an abandoned
  attempt is reclaimed instead of leaking.

### Changed
- **Per-job deadline + bounded re-spool.** Each job runs under
  `KELD_ENRICH_JOB_TIMEOUT` (default 30s); on timeout it re-spools for a GLiNER2
  retry, but only up to `KELD_ENRICH_MAX_ATTEMPTS` (default 4) — an exhausted job
  is quarantined to `~/.keld/spool/bad/` rather than retried forever.
- **Idle model-unload default 2m → 10m** (`KELD_SIDECAR_IDLE_UNLOAD_S`), so a
  brief pause doesn't evict the model and pay a reload on the next prompt.

### Env
- `KELD_ENRICH_MAX_ATTEMPTS` (new, default 4) — re-spool cap before quarantine.
- `KELD_ENRICH_JOB_TIMEOUT` (default 30s) and `KELD_SPOOL_MAX` (default 500) —
  now documented in the README.

## [0.3.0-rc.1] — 2026-07-05 (internal prerelease)

First release under the **keld-signal** name. This turns the client into a full
**on-device enrichment agent**, not just a telemetry configurator. Builds are
**unsigned** (Gatekeeper/SmartScreen will warn) and this is a **pre-release** for
internal end-to-end testing.

### Highlights
- **On-device enrichment agent** — every prompt is classified locally into a rich
  `Profile` (task · domain + entities · sensitivity + masked spans · activity ·
  work/personal · business function · subcategory); only the **masked, derived**
  signal is published. Raw prompt text never leaves the machine.
- **Two-wave enrichment pipeline** (schema v2): six independent sweeps + one
  function-conditioned subcategory pass, with repo/branch/recent-prompt
  **context augmentation**.
- **Resource-safe sidecar (good citizen):** governed single-flight inference,
  **memory-pressure + idle model eviction** (returns ~2.6 GB to the OS via
  `malloc_trim`), and **dual CPU throttling** — a rate governor *and* dynamic
  per-inference thread scaling (default ≤50% of cores). Backed by a smoke/soak
  **load-test harness** proving no memory leak and no runaway CPU.
- **Org control plane** — per-org remote enrichment settings, polled from Atlas
  (remote-overrides-local, non-fatal offline).
- **Platform installers (unsigned-first):** macOS `.pkg` (arm64/amd64), Windows
  `keld-setup.exe`, Linux one-liner + frozen sidecar tarball.
- **Observability:** `GET /metrics` on the sidecar (model state, governor
  EWMA/interval/threads, queue depth, lifetime counts).

### Features
- Sidecar: memory-pressure eviction, idle eviction (unload after inactivity,
  reload on demand), dynamic CPU-thread scaling, `/metrics`, single-flight
  `InferenceRunner` with bounded-queue backpressure (503).
- Enrichment: declarative Pass framework, job-category vocabulary (schema v2),
  two-wave pipeline with conditioned subcategory, job-category fields on the wire.
- Daemon: poll org enrichment settings and live-apply `include_entity_text`;
  session-context augmentation from the transcript.
- Load-test harness: corpus, driver, sampler, external CPU/RAM stressors, smoke +
  soak tiers with a CLI.

### Fixes
- Sidecar OOM guard (serial Wave-1 + input cap); real GLiNER2 confidence surfaced
  (was hard 1.0/0.0); Windows UTF-8 startup; PyInstaller `SPECPATH` anchoring;
  first-run model provisioning on a fresh machine; `login` re-auth + stored-server
  targeting; leak metric hardened to `mean_growth`.

### Docs / project
- Reframed the repo as the **Keld client** (enrichment core); overhauled README,
  added `AGENTS.md` + `CLAUDE.md`; documented the sidecar resource-safety
  mechanisms and the enrichment sweep pipeline.
- **Renamed `keld-cli` → `keld-signal`** — Go module path, GitHub repo, and
  install URLs.

### CI / release
- Native installer workflow (freeze sidecar → package → gated sign → attach);
  credential-free unsigned dry-runs via `workflow_dispatch`.
- Prerelease tags (`vX.Y.Z-rc.N`) are flagged as GitHub pre-releases.

### Installing this prerelease
Assets are unsigned and this build is not tagged "Latest". Download the
`.pkg` / `keld-setup.exe` / tarball from this release's assets, or pin the CLI
one-liner:

```bash
KELD_RELEASE_TAG=v0.3.0-rc.1 curl -fsSL https://raw.githubusercontent.com/ncx-ai/keld-signal/main/scripts/install.sh | sh
```

## [0.2.2] and earlier

See the [GitHub releases](https://github.com/ncx-ai/keld-signal/releases) for
history prior to the keld-signal rename.

[0.3.0-rc.1]: https://github.com/ncx-ai/keld-signal/releases/tag/v0.3.0-rc.1
[0.2.2]: https://github.com/ncx-ai/keld-signal/releases/tag/v0.2.2
