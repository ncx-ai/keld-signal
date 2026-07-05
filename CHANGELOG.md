# Changelog

All notable changes to **keld-signal** (the Keld client — the `keld` CLI + the
`keld-agent` on-device enrichment daemon + its GLiNER2 sidecar). Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); the project uses
semantic-ish versioning during `0.x`.

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
