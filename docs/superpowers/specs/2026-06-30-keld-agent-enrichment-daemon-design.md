# keld-agent: local privacy-preserving enrichment daemon — design

**Date:** 2026-06-30
**Status:** Design approved, pre-implementation
**Supersedes/extends:** [2026-06-27-keld-cli-go-migration-design.md](./2026-06-27-keld-cli-go-migration-design.md)

## 1. Summary

Add a local background daemon, `keld-agent`, that classifies each user prompt
(job type + lightweight entities) using a local ONNX model (GLiNER2) and sends
**only the derived labels** to Keld Atlas, joined to existing telemetry by a
per-source correlation key (`prompt_id` for Claude Code). The raw prompt text
**never leaves the machine**: for sources with a local transcript (Claude Code)
it is read directly off disk; for sources without one (Claude Desktop, agent
frameworks) it is passed to the daemon over loopback only. Enrichment runs
**asynchronously and host-load-aware** so it never adds backpressure to the
workflows that generate the prompts.

`keld-agent` becomes the **primary installable product** ("Keld"), delivered via
native GUI installers. The existing lean `keld` CLI remains as a power-user /
CI escape hatch with no daemon and no enrichment.

### Driving constraint: privacy

Enrichment runs locally because the raw prompt **must not leave the machine**.
Today both telemetry paths deliberately suppress prompt text
(`log_user_prompt = false`, `logPrompts: false`). The daemon is therefore the
*sole producer* of a new, privacy-safe derived layer — it does not augment data
Atlas already holds.

## 2. Locked decisions

| Topic | Decision |
|-------|----------|
| Driver | Privacy: raw prompt never leaves the machine; only derived labels reach Atlas |
| Runtime | Go daemon + ONNX Runtime (`yalue/onnxruntime_go` or `hugot`), GLiNER2 exported to ONNX |
| Distribution | GUI installers (macOS `.pkg`, Windows MSI) + shell+binary for Linux |
| Service | Per-user autostart (LaunchAgent / systemd `--user` / per-user logon task) |
| Scheduler | Async, **host-load-aware**, best-effort & lossy-under-pressure. Floor: bounded queue + low-priority worker + drop-sampling (observable). Governor: scales concurrency + admission/sample rate to spare host CPU headroom, backs off under sustained host load |
| Source identity | Structured, namespaced `source` = `{id, origin, version}` carried end-to-end; differentiates Claude Code, Claude Desktop (chat / cowork), agent frameworks (LangChain / Mastra), etc. Fed to the classifier as a prior |
| Tool/source scope | Claude Code first; provider-agnostic, **multi-source** seam (CLI hook, desktop, SDK/framework, OTEL) |
| Correlation | Per-source join key `{source, scheme, id}`. `prompt_id` for Claude Code (no hashing); trace/span or source-supplied id for frameworks; `session_id` + turn ordinal fallback |
| Prompt source | **Pointer** (daemon reads from `transcript_path` on disk, e.g. Claude Code) **or inline text over loopback** (sources with no local transcript); both stay on-box |
| Primary artifact | `keld-agent` (superset binary) installed via GUI installer; `keld` CLI also installed on `PATH` |

## 3. Why a daemon (vs. CodeBurn's model)

[CodeBurn](https://github.com/getagentseal/codeburn) is the closest reference. It
deliberately makes the **opposite** choice on three axes, and naming the
divergence justifies our added cost:

| Axis | CodeBurn | keld-agent | Why we differ |
|------|----------|------------|---------------|
| Background process | None (on-demand file reads) | Persistent per-user daemon | Real-time per-prompt enrichment + warm model |
| Classification | Deterministic keywords/patterns | Local ML (GLiNER2), deterministic fallback | Richer, model-driven labels |
| Data destination | Local-only | Derived labels → central Atlas | Cross-machine/org telemetry |

Patterns we **borrow** from CodeBurn:

- **Parser-per-provider** (`src/providers/codex.ts`) → our `TranscriptReader`
  interface with one implementation per tool.
- **Reading standardized disk locations** — confirms our prompt source:
  `~/.claude/projects/<sanitized-path>/<session-id>.jsonl`.
- **Deterministic task taxonomy** (13 categories) → seeds our label vocabulary
  and the Phase-1 / fallback classifier.
- **Dedup by message/prompt id** → our `prompt_id` dedup.
- **Menu-bar app + localhost dashboard** → optional future status UX (out of
  scope for v1).

## 4. Artifacts

One repo (`keld-cli`), shared `internal/*` packages, two binaries:

- **`keld-agent`** — the installable product. Superset binary: does everything
  the CLI does (login, configure Claude/Codex/Gemini hooks + OTEL, hook runner)
  **plus** runs the enrichment daemon and embeds ORT. Standard, likely
  org-enforced path.
- **`keld`** — lean, dependency-free CLI via `curl | sh`. Configures telemetry;
  **no daemon, no enrichment** (no ORT). For power users / CI / anyone refusing
  a background service. Also installed on `PATH` by the GUI installer so standard
  users can `keld login` / `keld status` for re-auth and ad-hoc actions.

### Hook entrypoint stays `keld __hook`

Because `keld` is always installed (standard path via the GUI installer,
power-user path via `curl | sh`), every install configures the same hook command:
`keld __hook --source <tool>`. The localhost-forward to the daemon lives in the
shared `internal/hook` package as a **silent-skip branch**: it POSTs the pointer
to `127.0.0.1:<port>` when the daemon answers and does nothing when it does not
(power-user path). No binary-name parameterization is required.

## 5. Components (inside keld-agent)

1. **HTTP ingress** — `POST /enrich` on `127.0.0.1:<port>`. Body carries the
   structured `source`, a `correlation` block (`{scheme, id, session_id?}`), and
   **exactly one of**: a *pointer* (`{transcript_path, prompt_id, cwd}` — daemon
   reads text from disk, e.g. Claude Code) **or** *inline* (`{text}` — for sources
   with no local transcript, e.g. Claude Desktop / agent frameworks). Bound to
   loopback; requires a per-user shared secret (blocks other local processes).
   Responds `202 Accepted` immediately and enqueues. Under governor backpressure
   it may instead return `429` (admission-shed) so producers needn't wait.
   **Port + secret discovery:** the daemon writes `~/.keld/agent.json`
   (`{port, secret}`, mode `0600`) on startup; `keld __hook` reads it to locate
   and authenticate to the daemon. Absent/stale file ⇒ the silent-skip branch
   does nothing (power-user path, or daemon not running).
2. **Dispatcher / queue + host-load governor** — bounded FIFO, dedup by
   correlation id, drop-oldest-and-log backpressure (drops are counted and
   reported — no silent caps). Behind a `Dispatcher` interface. The **governor**
   is a first-class component, not an afterthought: it samples an EWMA of host
   load / available CPU and scales worker concurrency *and* the admission/sample
   rate to spare host headroom, backing off toward zero under sustained host
   load. This is what keeps enrichment from competing with the workflows that
   generate the prompts; under heavy hosts (LangChain / Mastra) enrichment is
   explicitly sampled, never a brake on the producer.
3. **PromptResolver** — turns an ingress request into prompt text. For *pointer*
   requests it dispatches to a provider-agnostic **TranscriptReader** (one impl
   per tool; given `transcript_path` + `prompt_id`, returns the turn's text,
   handling write-timing via a short bounded poll/tail for the matching line,
   tolerant JSONL parsing, **clean skip** on version drift, golden-file tests).
   For *inline* requests it uses the supplied text directly. Either way the text
   is resolved entirely on-box.
4. **Classifier** (interface) — GLiNER2 via ORT, model loaded once at startup
   (warm). Input: prompt text **+ `source` as a prior** (source-aware thresholds
   / taxonomy improve reliability across distinct distributions — a Desktop chat
   turn vs. a Mastra sub-agent step). Output: job-type label(s) + optional
   entities (languages, frameworks). A **deterministic keyword classifier** is
   the permanent fallback (and the Phase-1 stand-in before ORT lands); taxonomy
   seeded from CodeBurn's 13 categories.
5. **Atlas publisher** — `POST /v1/enrichments` with the structured `source`,
   the `correlation` block, `labels`, and `schema_version` / `model_version` /
   `ts`. Reuses `hook.json` (`endpoint` + `ingest_token`) — no new credential
   plumbing. Bounded disk-backed retry queue when offline; drop after N attempts.
   **Never sends raw prompt text.**
6. **OS priority** — sets `nice` / idle priority class at process start
   (the always-on floor). The adaptive part of host-load management lives in the
   governor (component 2), not here.
7. **Lifecycle CLI** — `keld-agent run | install | uninstall | status`.
   `run` is what the service unit invokes (foreground). `install`/`uninstall`
   write the LaunchAgent plist / systemd `--user` unit / Windows logon task.
   `status` reports health, queue depth, model version.

## 6. Data flow

Claude Code (pointer path):

```
user submits prompt in Claude Code
  └─ UserPromptSubmit hook → `keld __hook --source claude_code`
       stdin: {session_id, prompt_id, transcript_path, cwd}
       ├─ (existing) context POST to Atlas
       └─ (new, silent-skip) POST pointer → 127.0.0.1:<port>/enrich
            [fire-and-forget, <500ms timeout, never blocks the tool]
            └─ keld-agent: admit (or 429-shed) → 202
                 worker (governor-paced, low priority):
                   dedup(correlation.id)
                   → PromptResolver → TranscriptReader reads text from disk
                   → Classifier(text, source) → labels
                   → Atlas publisher POST {source, correlation, labels, ...}
```

Source with no local transcript (inline path — Claude Desktop, LangChain, Mastra):

```
producer → POST {source, correlation, inline:{text}} → 127.0.0.1:<port>/enrich
            (text crosses loopback only, secured by per-user secret)
            └─ same worker pipeline; PromptResolver uses inline text directly
```

Atlas joins each enrichment to the turn's telemetry on `{source, scheme, id}`
(idempotent upsert).

## 7. Correlation

Claude Code exposes a stable `prompt_id` in **both** channels:

- Hook stdin: `prompt_id` (UUID; Claude Code ≥ v2.1.196).
- OTEL: `prompt.id` attribute on every event for that turn
  (`user_prompt`, `api_request`, `tool_decision`, `tool_result`,
  `assistant_response`), alongside `session.id`.

The daemon sends `{source, correlation, labels}`; Atlas joins on
`{source, scheme, id}`. **No deterministic-hash scheme is needed** — and a hash
of the prompt could not work anyway, because Atlas never receives the raw prompt
to recompute it.

Correlation is **per-source**, since sources differ in what stable id they can
offer:

| Source | scheme | id |
|--------|--------|----|
| Claude Code | `prompt_id` | hook `prompt_id` = OTEL `prompt.id` |
| Agent frameworks (LangChain / Mastra) | `trace` | OTEL trace/span id, or a producer-supplied id |
| Claude Desktop (chat / cowork) | TBD per surface | a desktop-supplied turn/message id if exposed |
| Fallback (any) | `session_ordinal` | `session_id` + daemon-maintained turn ordinal |

The structured `source` is part of the join key precisely so identical ids from
different sources never collide and so consumers can segment by origin.

## 8. Privacy invariants (explicit, testable)

- Raw prompt text **never leaves the machine.** Two on-box paths:
  - *Pointer sources* (Claude Code): text is read from local disk and is not
    sent even over localhost — only a pointer crosses the hook→daemon boundary.
  - *Inline sources* (Claude Desktop, agent frameworks with no local
    transcript): text travels **over loopback only**, guarded by the per-user
    secret, and is never persisted beyond the in-memory job.
- Only `{source, correlation, labels, schema_version, model_version, ts}` leave
  the box — never prompt text.
- Daemon binds `127.0.0.1` only; ingress requires a per-user shared secret.
- No prompt text in logs (ids and lengths only).
- A dedicated leak test scans all outbound payloads + log output and asserts
  **zero** prompt-text content.

## 9. Distribution — trusted, native mechanisms

The GUI installer is the standard, likely-enforced path. Its payload:
`keld-agent` + `keld` (on `PATH`) + `libonnxruntime.{dylib,so,dll}` + model +
service definition.

- **macOS** — signed + **notarized `.pkg`** (`pkgbuild` / `productbuild`).
  Postinstall registers the LaunchAgent via `launchctl bootstrap gui/$UID`.
  Notarization required to clear Gatekeeper.
- **Windows** — **MSI via WiX Toolset** (most trusted/native; per-user install
  + logon task via custom action). *Inno Setup* is the lighter fallback if MSI
  authoring proves too heavy. Authenticode signing to clear SmartScreen.
- **Linux** — `curl | sh` dropping `keld-agent` + `keld` + `.so` + model to
  `~/.local`, then `systemctl --user enable --now keld-agent`. `.deb` / `.rpm`
  via **nfpm** later.
- **Build** — extend the existing **GoReleaser** pipeline; `.pkg` / MSI as CI
  steps. Note: Go + ORT is not a pure-static single file — each platform ships
  the binary + ORT shared lib + model; the installer places all three.

### First-run flow (inside keld-agent, no second install step)

```
login (device flow opens browser)
  → fetch onboarding (endpoint, ingest_token, actor)
  → configure tools (Claude/Codex/Gemini hooks + OTEL, incl. UserPromptSubmit hook)
  → register per-user service
  → start daemon
```

## 10. Error handling

- Hook → localhost POST: silent fire-and-forget, `<500ms` timeout; daemon down
  ⇒ that prompt is simply unenriched. Never blocks or fails the host tool.
- Transcript read failure / format drift ⇒ skip enrichment, increment a metric,
  log a warning (no prompt text).
- Classifier failure ⇒ deterministic fallback ⇒ else skip.
- Atlas publish failure ⇒ bounded disk-backed retry; drop after N attempts.
- Daemon crash ⇒ service manager restarts (`KeepAlive` / `Restart=on-failure`);
  `recover()` in the worker mirrors the existing hook's panic safety.

## 11. Atlas contract (defined here, implemented in keld-atlas)

`POST /v1/enrichments`

```json
{
  "source": { "id": "claude_code", "origin": "hook", "version": "2.1.x" },
  "correlation": { "scheme": "prompt_id", "id": "uuid", "session_id": "sess_…" },
  "labels": {
    "job_type": "codegen",
    "job_types": ["codegen", "testing"],
    "entities": { "languages": ["go"], "frameworks": [] }
  },
  "schema_version": "1",
  "model_version": "gliner2-…",
  "ts": "2026-06-30T…Z"
}
```

- Auth: `x-keld-ingest-token` + `x-keld-actor` (same as existing ingest).
- **Idempotent upsert on `{source.id, correlation.scheme, correlation.id}`.**
- Joins to existing telemetry rows by the same composite key; for Claude Code
  that reduces to `prompt_id`.
- Enrichment coverage is **partial by design** (sampled under host load), so
  Atlas treats enrichments as optional augmentation, not 1:1 with every prompt.

(Detailed Atlas-side work is a separate spec in the keld-atlas repo; only the
wire contract is fixed here.)

## 12. Phasing (each phase shippable)

- **P1 — de-risk the pipe (headless, internal dogfooding).** `keld-agent`
  skeleton: HTTP ingress (pointer **and** inline), queue/worker with the
  **load-protection floor** (bounded queue + low priority + drop-sampling),
  structured `source` + per-source correlation, Claude `TranscriptReader`,
  **deterministic** classifier, Atlas publisher; subsume login + setup into
  `keld-agent`; per-user service install on all 3 OS via `keld-agent install`;
  Linux shell distribution. Proves daemon + service + privacy + correlation
  **without** ML/ORT packaging risk.
- **P2 — ML + host-load governor.** GLiNER2 ONNX integration behind the
  `Classifier` interface + model packaging; the **adaptive governor** (EWMA host
  load → concurrency + admission/sample rate) graduates from the P1 floor. These
  pair because ML inference is the expensive work the governor must pace.
- **P3 — GUI installers.** `.pkg` + MSI + signing/notarization. (The
  launch-blocking deliverable, since the installer is the enforced path; P1–P2
  are validated headless first because that is faster.)
- **P4 — more sources.** Agent-framework producers (LangChain / Mastra) and
  Claude Desktop (chat / cowork) over the inline path; Codex/Gemini
  `TranscriptReader`s as those tools expose prompt text to a hook; tray/menu-bar
  status UI.

## 13. Open risks

1. **Transcript format is internal and changes between Claude Code versions.**
   Mitigation: tolerant parsing, golden fixtures per known shape, clean skip on
   drift, a CI canary that flags format changes.
2. **Write-timing** — the new user turn may not be flushed to the `.jsonl` at the
   instant `UserPromptSubmit` fires. Mitigation: short bounded poll/tail for the
   line matching `prompt_id`; give up after a small budget and skip.
3. **`UserPromptSubmit` raw-prompt availability** — current docs confirm
   `prompt_id` in stdin but do **not** confirm raw `prompt` text; design assumes
   it is unavailable and sources text from the transcript instead. If a future
   version reliably provides prompt text in stdin, the `TranscriptReader` becomes
   an optional optimization.
4. **ORT shared-lib packaging** across 3 OS/arch — size (hundreds of MB with the
   model) and signing surface are larger than today's single binary. Contained
   to P2/P3.
5. **Codex/Gemini prompt access** — may never expose raw prompt to a local hook;
   enrichment may stay Claude-Code-only or degrade to session-level for those.
6. **High-throughput hosts** (LangChain / Mastra) — prompt volume can far exceed
   enrich capacity. Mitigation: the governor sheds load (sample, `429`, drop)
   to protect the host; coverage is partial and that is acceptable. Risk is
   tuning the EWMA/thresholds so the daemon neither starves the host nor
   collapses to near-zero coverage; needs real-workload profiling.
7. **Multi-source correlation heterogeneity** — non-Claude-Code sources lack a
   `prompt_id`; their join keys (trace ids, desktop message ids, ordinals) are
   less stable and per-surface. Risk of unjoinable enrichments; the `{source,
   scheme, id}` design contains it but each new source needs its scheme defined.

## 14. Testing

- **Unit** — `TranscriptReader` against golden `.jsonl` fixtures (including
  malformed / version-drift); `Classifier` fixtures (prompt → expected labels,
  with tolerance); `Dispatcher` backpressure + dedup; Atlas publisher retry /
  offline.
- **Integration** — spin the daemon on an ephemeral port; exercise both a
  pointer request and an inline request; assert the Atlas mock receives
  `{source, correlation, labels}` and **never** raw prompt; assert governor
  shedding (`429`/drop) under a synthetic high-rate burst, with drop counts
  reported.
- **Privacy leak test** — assert no prompt-text content appears in any
  **outbound** (Atlas) payload or log line. (Inline text legitimately crosses
  loopback ingress; the invariant is that it never leaves the box and is never
  logged or persisted beyond the in-memory job.)
- **Installer smoke** — CI builds `.pkg` / MSI; verify service registration on
  macOS / Windows / Linux runners.
