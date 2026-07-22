# Changelog

All notable changes to **keld-signal** (the Keld client — the `keld` CLI + the
`keld-agent` on-device enrichment daemon + its GLiNER2 sidecar). Format loosely
follows [Keep a Changelog](https://keepachangelog.com/); the project uses
semantic-ish versioning during `0.x`.

## [Unreleased]

## [0.11.0] — 2026-07-22

Gemini CLI parity — native OTEL completed. Enrichment via `~/.gemini/tmp/*/chats`
watcher root + Gemini transcript reader (resolves user messages by `id`,
pointer model — no prompt text on disk); telemetry stays native (no host-side emit /
no double-count).

### Added
- **Gemini transcript watcher** (`internal/agent/watch/gemini.go`) tails
  `~/.gemini/tmp/*/chats` to capture prompts with source=gemini, feeding the same
  resolve → enrich → publish pipeline as Claude Code / Cowork / Codex. Pointer-only,
  never text.
- **Gemini TranscriptReader** (`internal/agent/resolve/gemini.go`) implements the
  `TranscriptReader` + `RecentReader` interfaces for Gemini chat transcripts:
  resolves a user message by matching the JSONL line's `id` field
  (promptID = message `id`; pointer model — ids only, no disk-resident
  prompt text). Registered alongside Claude Code, Cowork, and Codex readers.
- **Gemini classified as an interactive coding tool** in the enrichment pipeline.

### Changed
- **Gemini telemetry now native.** Gemini's own OTEL (configured in Task 1) handles
  all gemini telemetry; the host-side promptlog emitter explicitly excludes `gemini`
  to prevent double-counting. Guard test asserts `SourcesFromEnv()` default does
  not include gemini. The keld-managed `~/.gemini/.env` block preserves `GEMINI_API_KEY`
  while adding OTEL auth headers (`x-keld-ingest-token` / `x-keld-actor`) and
  `OTEL_TRACES_EXPORTER=none`, using the base OTLP endpoint (was a broken `/v1/logs?token=` URL).
  Distinguished in Atlas by `service.name:"gemini-cli"`; identity via `user.email`.
- **Gemini hook** — `settings.json hooks.BeforeAgent` → `keld __hook --source gemini`
  (context event only; silent stdout; watcher owns enrichment capture).

## [0.10.0] — 2026-07-21

Codex parity — native OTEL completed. Enrichment via `~/.codex/sessions`
watcher root + rollout TranscriptReader (resolves user_message by session_id#ordinal,
pointer model — no prompt text on disk); telemetry stays native (no host-side emit /
no double-count).

### Added
- **Codex transcript watcher** (`internal/agent/watch/codex.go`) tails
  `~/.codex/sessions` to capture prompts with source=codex, feeding the same
  resolve → enrich → publish pipeline as Claude Code / Cowork. Pointer-only,
  never text.
- **Codex TranscriptReader** (`internal/agent/resolve/codex.go`) implements the
  `TranscriptReader` + `RecentReader` interfaces for Codex rollout transcripts:
  resolves a `user_message` by matching the rollout line's `ordinal` field
  (promptID = `session_id#ordinal`; pointer model — ids only, no disk-resident
  prompt text). Registered alongside Claude Code and Cowork readers.

### Changed
- **Codex telemetry now native.** Codex's own OTEL (configured in Task 1) handles
  all codex telemetry; the host-side promptlog emitter explicitly excludes `codex`
  to prevent double-counting. Guard test asserts `SourcesFromEnv()` default does
  not include codex.

## [0.9.5] — 2026-07-21

Cost is now computed authoritatively in Atlas from exact tokens, not estimated
on-device.

### Changed
- **Dropped client-derived cost** (`cost_usd`/`cost_usd_micros` log attrs +
  `cost.usage` metric). The transcript carries no first-hand cost — only tokens —
  and a client price table is approximate (ignores `service_tier` and the 1h-vs-5m
  cache-write rate split). Instead the `api_request` event now carries the **exact
  token detail** Atlas needs to compute cost correctly: `cache_creation_1h_tokens`,
  `cache_creation_5m_tokens`, and `service_tier`, alongside the existing
  input/output/cache totals. Removed the per-model price table.

## [0.9.4] — 2026-07-21

Telemetry now mirrors the Claude Code CLI's OTLP schema exactly (so token/cost
data actually surfaces in Atlas).

### Fixed
- **Metrics rendered correctly.** `token.usage`/`cost.usage` are now emitted as
  one Sum per name with a datapoint per type, delta temporality, **monotonic=true**
  — matching the captured CLI shape. Previously duplicate-named, single-datapoint,
  non-monotonic sums that Atlas would not surface (so token/cost data appeared
  missing).
- **`api_request`/`assistant_response` now carry the CLI's full attribute set** we
  can reconstruct: `prompt.id` (linked to the turn's user prompt), `effort`,
  `cost_usd` (double) + `cost_usd_micros`, `client_request_id`, `request_id`.

### Added
- **Schema fidelity test** — asserts each emitted log event's attribute keys equal
  the captured CLI oracle minus documented omissions (`prompt`/`response` text =
  privacy; `terminal.type`/`user.id`/`user.account_id`/`duration_ms`/`query_source`/
  `speed` = not reconstructable host-side). Guards against future drift.
- `doubleValue` support in the OTLP attribute encoder (for `cost_usd`).

## [0.9.3] — 2026-07-21

### Fixed
- **Watched telemetry now carries a `tool=<source>` resource attribute** (e.g.
  `tool=cowork`), mirroring Cowork's own native `otelConfig` resourceAttributes.
  Without it, emitted telemetry was indistinguishable from Claude Code CLI traffic
  in Atlas — activity appeared but was not attributable to Cowork. `service.name`
  stays `claude-code` (family recognition); `tool` marks the surface.

## [0.9.2] — 2026-07-21

Full-fidelity Cowork telemetry — watched telemetry now mirrors the Claude Code
CLI's native OTEL, not a single thin event.

### Changed
- **`promptlog` now emits the CLI's full OTEL footprint** for watched sources.
  Grounded in a captured real `claude` OTEL export, the daemon mirrors the
  transcript's events into OTLP **logs** (`user_prompt`, `api_request`,
  `assistant_response`) and **metrics** (`token.usage`, `cost.usage`) at
  `/v1/logs` + `/v1/metrics`, with resource attrs (`service.name=claude-code`,
  version, os/arch) and the **Anthropic account identity** (`user.email`,
  `user.account_uuid`, `organization.id`) recovered host-side from the Cowork
  session path/metadata — so it attributes to the same account as the CLI, not
  keld's login. Model + token counts come from the transcript's assistant
  records. Supersedes v0.9.1's single-event emit. Still **never emits prompt or
  response text** — only lengths, ids, model, tokens.
- The watcher gained a per-line **observe** hook feeding telemetry; enrichment
  (offer) is unchanged.

### Known gaps (not reconstructable host-side, omitted or approximate)
- `duration_ms`, `terminal.type`, `user.id`/`user.account_id`, metric
  `active_time.total` — omitted; `event.sequence` synthesized; `cost_usd`
  **derived** from a per-model price table (may drift with pricing).

## [0.9.1] — 2026-07-21

Telemetry parity for watched sources — Cowork prompts now produce usage telemetry
in Atlas, not just enrichments.

### Added
- **Host-side prompt telemetry emitter** (`internal/agent/promptlog`). For captured
  sources whose own OTEL can't reach Keld — notably **Cowork**, whose agent sandbox
  egress allowlist excludes `atlas.keld.co`, so its natively-configured OTEL export
  is dropped at the firewall — the daemon now emits an equivalent OTLP/HTTP
  user-prompt log to `/v1/logs` **host-side** (unrestricted egress), giving watched
  sources the same telemetry footprint the CLI's native OTEL provides. Carries only
  ids/source/timestamp — **never prompt text**. Claude Code is excluded by default
  (it emits its own OTEL host-side); configurable via `KELD_WATCH_TELEMETRY` (on/off)
  and `KELD_WATCH_TELEMETRY_SOURCES` (comma list). Verified end-to-end: a real Cowork
  prompt → daemon emit → Atlas `/v1/logs` **HTTP 200**.

## [0.9.0] — 2026-07-21

Hook-free prompt capture — Claude Code on every launch surface (incl. the Desktop
app) and Cowork now enrich, not just the terminal CLI.

### Added
- **On-device transcript watcher** (`internal/agent/watch/`). A daemon poll loop
  tails the JSONL transcripts Claude Code (all surfaces) and Cowork already write
  to disk and synthesizes the same enrich pointer the command hook produces —
  never prompt text — into the existing resolve → enrich → publish pipeline. This
  is the hook-free capture path for surfaces (Cowork's Linux sandbox; Claude Code
  in the Desktop app) that don't fire `~/.claude/settings.json` hooks. Sources:
  `~/.claude/projects` → `claude_code`; the Cowork
  `local-agent-mode-sessions/**/.claude/projects` trees → `cowork` (macOS). New
  env: `KELD_WATCH` (default on), `KELD_WATCH_POLL` (5s), `KELD_WATCH_BACKFILL`
  (off = forward-only, so first run doesn't flood on existing history). Cowork
  prompts are classified as general knowledge work, not coding.

### Changed
- **Queue dedup now also covers recently-completed keys**, not just in-flight, so
  a prompt caught by both the hook and the watcher is enriched once (the hook
  typically completes before the watcher's next poll — an in-flight-only dedup
  would miss it). A key is marked completed only on a real publish
  (`queue.Complete`), so re-spooled retries and a hook that couldn't resolve its
  text yet stay re-offerable. Bounded in-memory ring buffer (4096 keys).

## [0.8.0] — 2026-07-19

Agentic-framework classification — measure and improve enrichment on traffic from
agentic workflows (Mastra, LangChain/LangGraph, CrewAI).

### Added
- **Agentic-framework eval corpus** (88 rows: 60 clean sub-tasks + 28 full raw LLM
  calls, multi-judge-consensus-labeled) and `keld-agent eval --agentic` reporting
  task_type/domain accuracy by prompt shape and augmented-vs-bare.
- **Agentic context on `Meta`** — framework, agent role, workflow, step, and recent
  steps, rendered into the classification preamble.

### Changed
- **Facet-selective agentic augmentation.** Measured that naive full-metadata
  augmentation *hurts* task_type (subject-noise) while *helping* domain. task_type
  and the other non-domain classifiers now use a coding-only preamble
  (`Meta.PreambleCoding()`, dropping agentic fields); domain augments with the
  agentic context. On the agentic corpus: task_type 0.64→0.80, domain 0.73→0.78,
  with **zero change to coding/human classification** (coding preamble byte-identical).
- Eval gold `activity_type` + `speech_act` coverage extended to the full 165-row set
  via multi-judge consensus, making those facets measurable on the larger set.

## [0.7.0] — 2026-07-18

Enrichment **classification quality** — routing-aligned task_type, better domain
and sensitivity, and credential false-positive control. Schema **5 → 6**.

### Changed
- **task_type redesigned into a routing-aligned taxonomy (schema v6).** Dropped
  `agentic_tool_use` (a workflow shape, not an inference job — it caused ~half of
  task_type errors); added `text_generation` and `rewriting`; renamed to HF
  conventions (`code_generation`, `information_extraction`, `question_answering`);
  `other` → `general`. task_type is the routing key for the Keld Inference
  Exchange order books. Bakeoff-tuned descriptions; measured 0.696 → 0.744.
- **Domain classification given readable label descriptions** (the A6 treatment).
  Domain classified against bare label strings with a `general` magnet; adding
  bakeoff-tuned descriptions lifted domain **0.462 → 0.68** with no new model
  (CPU-only, single resident model).
- **Sensitivity reframed to concrete leaked DATA, not content domain.** The class
  is a rollup of which concrete sensitive entity is present (SSN → phi, card →
  pci, credential → secrets, other personal identifier → pii); medical/topic
  words no longer flagged. `proprietary` deprecated. sensitive_recall 0.68 → 0.90+.

### Added
- **Deterministic credential detection layer** (vendored gitleaks ruleset + a
  keyword-prefiltered, entropy-gated detector) unioned into the `secrets` class,
  raising credential recall with zero false positives on the eval corpus.
- **Placeholder precision-gate** — placeholder/redacted values (`YOUR_API_KEY`,
  `<API_KEY>`, `sk_live_****`) no longer trigger `secrets` (fpr 0.167 → 0.056),
  with zero recall loss.
- **Confidence-calibration + credential eval metrics** in `keld-agent eval`
  (`--calibration`, `--creds`), and the gold set expanded 82 → 165 rows with
  multi-judge consensus labels.

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
