# Changelog

All notable changes to **keld-signal** (the Keld client — the `keld` CLI + the
`keld-agent` on-device enrichment daemon + its GLiNER2 sidecar). Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); the project uses
semantic-ish versioning during `0.x`.

## [Unreleased]

Enrichment **delivery reliability** — the arc that makes enrichment actually land
on every prompt, always on GLiNER2, without wedging.

### Added
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
