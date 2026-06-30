# keld-agent: local privacy-preserving enrichment daemon — design

**Date:** 2026-06-30
**Status:** Design approved, pre-implementation
**Supersedes/extends:** [2026-06-27-keld-cli-go-migration-design.md](./2026-06-27-keld-cli-go-migration-design.md)
**Pipeline blueprint:** `~/keld/inference-enrichment` (`docs/superpowers/specs/2026-06-11-inference-enrichment-design.md`)

## 1. Summary

Add a local background daemon, `keld-agent`, that runs a **staged GLiNER2
enrichment pipeline** over each user prompt — producing **(1) job
classification** (task type, domain, entities) and **(2) compliance/security
findings** (PII / secrets / PHI / PCI / proprietary leak detection) — and sends
**only the derived labels** to Keld Atlas, joined to existing telemetry by a
per-source correlation key (`prompt_id` for Claude Code). The compliance/security
findings are surfaced to Atlas **admin** users for monitoring and action on
potential misuse — carrying span labels, offsets, confidence, and a **masked
preview** (e.g. `sk-…AB12`), **never the raw sensitive value**. The raw prompt text
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
| Runtime | Go daemon. Inference behind a **swappable `Model` interface** (`classify`/`entities`/`extract`); **spike Go + ONNX Runtime first** (`yalue/onnxruntime_go` / `hugot`, GLiNER2→ONNX), fall back to a bundled GLiNER2 sidecar child process if structured decode in Go proves too costly |
| Analysis | **Staged multi-extractor pipeline** (blueprint: `~/keld/inference-enrichment`). Wave-1 parallel: `task_type`, `sensitivity` (compliance/security), `domain_entities`. Stage isolation → `partial`. `schema_version` discipline on label vocab |
| Redaction | Sensitive findings cross to Atlas as **label + offsets + confidence + masked preview** only — never the raw value; never logged or persisted |
| Distribution | GUI installers (macOS `.pkg`, Windows MSI) + shell+binary for Linux |
| Service | Per-user autostart (LaunchAgent / systemd `--user` / per-user logon task) |
| Scheduler | Async, **host-load-aware**, best-effort & lossy-under-pressure. Floor: bounded queue + low-priority worker + drop-sampling (observable). Governor: scales concurrency + admission/sample rate to spare host CPU headroom, backs off under sustained host load |
| Source identity | Structured, namespaced `source` = `{id, origin, version}` carried end-to-end; differentiates Claude Code, Claude Desktop (chat / cowork), agent frameworks (LangChain / Mastra), etc. Fed to each extractor as a prior |
| Tool/source scope | Claude Code first; provider-agnostic, **multi-source** seam (CLI hook, desktop, SDK/framework, OTEL) |
| Correlation | Per-source join key `{source, scheme, id}`. `prompt_id` for Claude Code (no hashing); trace/span or source-supplied id for frameworks; `session_id` + turn ordinal fallback |
| Prompt source | **Pointer** (daemon reads from `transcript_path` on disk, e.g. Claude Code) **or inline text over loopback** (sources with no local transcript); both stay on-box |
| Primary artifact | `keld-agent` (superset binary) installed via GUI installer; `keld` CLI also installed on `PATH` |

## 3. Blueprints

### Pipeline blueprint: `~/keld/inference-enrichment`

A working sibling project already implements the exact staged GLiNER2 enrichment
we need (there for inference-exchange routing). We port its **approach** to Go,
keeping its proven shapes:

- **Extractor registry + waves** — `Extractor` interface (`name`, `version`,
  `run(ctx)`); wave-1 (`sensitivity`, `task_type`, `domain_entities`) runs in
  parallel; per-stage isolation yields `partial` instead of failing the job;
  `extractor_versions` + `schema_version` recorded on every profile.
- **`Model` interface** — `classify(text, tasks)`, `entities(text, labels)`,
  `extract(text, labels, tasks)` (GLiNER2 composes entity-extraction +
  classification in one call). This is the swap-point for the Go+ONNX vs.
  sidecar backend decision; the reference's `SidecarClient` is the HTTP form of
  exactly this interface.
- **Canonical label vocab** (adopted as-is): `TASK_TYPES`, `DOMAINS`,
  `SENSITIVITY` (`none/pii/secrets/phi/pci/proprietary`),
  `SENSITIVE_ENTITY_LABELS` (`email/phone/ssn/credit_card/api_key/secret/
  person/address`), and the `SENSITIVITY_FROM_ENTITY` mapping where **hard span
  evidence overrides the weak classifier**.
- **Span privacy** — the reference stores sensitive spans as label/offset only
  and excludes raw values from audit output. We extend that to the wire: Atlas
  receives label + offsets + confidence + masked preview, never the value.

We **drop** the exchange-specific stages (`complexity_cost`, `derive`,
`eligible_provider_classes`, cost bands) — those serve provider routing, not
telemetry monitoring. (`complexity` may return later as a pure telemetry signal.)

### Reference contrast: CodeBurn (what we deliberately do differently)

[CodeBurn](https://github.com/getagentseal/codeburn) is the closest public
reference. It
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
- **Deterministic task taxonomy** (13 categories) → corroborates that a
  deterministic backend is viable for P1 / fallback (our canonical vocab is
  inference-enrichment's `TASK_TYPES`, not CodeBurn's list).
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
   per tool; given `transcript_path` + `prompt_id`, returns the turn's text).
   The reader keeps a **per-transcript byte cursor** and scans only newly
   appended lines (transcripts grow unbounded; re-reading from byte 0 each prompt
   would be O(file²)). It advances the cursor only past newline-terminated lines
   (a partial in-flight line is re-read next attempt), resets the cursor on file
   shrink (truncation / rotation / compaction), tolerates malformed lines, does a
   **clean skip** on version drift, and is covered by golden-file tests. For
   *inline* requests it uses the supplied text directly. Either way the text is
   resolved entirely on-box.
4. **EnrichmentPipeline** (Extractor registry + waves; ported from
   `inference-enrichment`) — runs wave-1 extractors in parallel over the prompt:
   - **`task_type`** — job classification (`TASK_TYPES`), top label + alts.
   - **`sensitivity`** — compliance/security: detects PII/secret entities
     (`SENSITIVE_ENTITY_LABELS`) + a sensitivity class
     (`none/pii/secrets/phi/pci/proprietary`); **hard span evidence overrides
     the weak classifier**; emits spans as **label + offsets + confidence +
     masked preview**, never raw values.
   - **`domain_entities`** — domain class + named entities (languages,
     frameworks, libraries, orgs, products).

   Each extractor takes `source` as a prior (source-aware thresholds improve
   reliability across distributions — a Desktop chat turn vs. a Mastra sub-agent
   step). Stage isolation → `partial` on any failure; `extractor_versions` +
   `schema_version` recorded. Extractors call the **`Model`** backend
   (`classify`/`entities`/`extract`); the model loads once and stays warm. A
   **deterministic backend** (regex/keyword for secrets+PII and task keywords) is
   the permanent fallback and the Phase-1 stand-in before the GLiNER2 backend
   lands — useful precisely because secret/PII detection has strong regex priors.
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
                   → EnrichmentPipeline(text, source): task_type ∥ sensitivity ∥ domain_entities
                   → Atlas publisher POST {source, correlation, labels, sensitivity, ...}
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
  the box — never the prompt text itself.
- **Domain-entity surface text is admin-gated, default OFF.** Domain entities
  (`language`/`framework`/`library`/`org`/`product`) are extracted *fragments* of
  the prompt; `org`/`product` in particular can be proprietary. By default the
  publisher sends only their `{label, start, end, confidence}` — the surface
  `text` is cleared. An admin setting (`include_entity_text`, default `false`)
  can enable sending the surface forms when the org wants richer segmentation.
  `sensitivity_spans` are **always masked regardless of this setting** — they are
  never gated and never carry raw values.
- **Sensitive findings are doubly protected:** a detected secret/PII is reported
  as `{label, start, end, confidence, masked}` where `masked` is a redacted hint
  (e.g. `sk-…AB12`, `j***@acme.com`) computed locally. The **raw matched value
  never crosses the wire, is never logged, and is never persisted** beyond the
  in-memory job. Masking is applied at extraction time, before the value can
  reach any sink.
- Daemon binds `127.0.0.1` only; ingress requires a per-user shared secret.
- No prompt text in logs, including the finite-size `~/.keld/agent.log` debug
  log (ids, endpoints, and statuses only).
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

- Hook → localhost POST: silent fire-and-forget toward the host tool, `<500ms`
  timeout; daemon down ⇒ that prompt is simply unenriched. Never blocks or fails
  the host tool. POST transport errors / non-2xx responses are recorded to a
  **finite-size local debug log** (`~/.keld/agent.log`, rotated to bound size) —
  endpoint + status + `prompt_id` only, never prompt text — so failures are
  diagnosable instead of fully invisible.
- Transcript read failure / format drift ⇒ skip enrichment, increment a metric,
  log a warning (no prompt text).
- `Model` backend failure / per-extractor failure ⇒ deterministic backend, else
  stage isolation marks the profile `partial` (other dimensions still publish).
- Atlas publish failure ⇒ bounded disk-backed retry; drop after N attempts.
- Daemon crash ⇒ service manager restarts (`KeepAlive` / `Restart=on-failure`);
  `recover()` in the worker mirrors the existing hook's panic safety.

## 11. Atlas contract (defined here, implemented in keld-atlas)

`POST /v1/enrichments`

```json
{
  "source": { "id": "claude_code", "origin": "hook", "version": "2.1.x" },
  "correlation": { "scheme": "prompt_id", "id": "uuid", "session_id": "sess_…" },
  "actor": "dg@keld.co",
  "task_type": { "value": "codegen", "confidence": 0.91 },
  "task_type_alt": [ { "value": "testing", "confidence": 0.4 } ],
  "domain": { "value": "software", "confidence": 0.8 },
  "entities": [ { "label": "language", "text": "go", "start": 10, "end": 12 } ],
  "sensitivity": { "value": "secrets", "confidence": 0.9 },
  "sensitivity_spans": [
    { "label": "api_key", "start": 120, "end": 160, "confidence": 0.9, "masked": "sk-…AB12" }
  ],
  "pipeline_status": "enriched",
  "extractor_versions": { "task_type": "task_type-v1", "sensitivity": "sensitivity-v1", "domain_entities": "domain_entities-v1" },
  "schema_version": "1",
  "model_version": "gliner2-…",
  "ts": "2026-06-30T…Z"
}
```

- Auth: `x-keld-ingest-token` + `x-keld-actor` (same as existing ingest).
- **Idempotent upsert on `{source.id, correlation.scheme, correlation.id}`.**
- Joins to existing telemetry rows by the same composite key; for Claude Code
  that reduces to `prompt_id`.
- `domain` (named `entities`) is non-sensitive *labels*; the entity `text`
  surface forms are sent only when the admin setting `include_entity_text` is on
  (default off — see Privacy invariants). `sensitivity` carries job-level
  compliance class and `sensitivity_spans` carry **masked** findings only.
- Enrichment coverage is **partial by design** (sampled under host load), so
  Atlas treats enrichments as optional augmentation, not 1:1 with every prompt.

### Admin monitoring surface (keld-atlas)

When `sensitivity.value != "none"` (above a configurable confidence threshold),
Atlas flags the enrichment for **admin** review: who (`actor`), where
(`source` + `correlation` → the telemetry turn), what class + masked spans, and
when. Admins triage and act (contact user, investigate locally) **without ever
receiving the raw sensitive content.** Alerting/severity policy and the admin UI
are a keld-atlas-side spec; only the wire fields above are fixed here.

(Detailed Atlas-side work is a separate spec in the keld-atlas repo; only the
wire contract is fixed here.)

## 12. Phasing (each phase shippable)

- **P1 — de-risk the pipe (headless, internal dogfooding).** `keld-agent`
  skeleton: HTTP ingress (pointer **and** inline), queue/worker with the
  **load-protection floor** (bounded queue + low priority + drop-sampling),
  structured `source` + per-source correlation, Claude `TranscriptReader`,
  **EnrichmentPipeline with the deterministic backend** (task_type +
  regex-driven sensitivity/PII + domain_entities) + masked spans, Atlas
  publisher; subsume login + setup into `keld-agent`; per-user service install on
  all 3 OS via `keld-agent install`; Linux shell distribution. Proves daemon +
  service + privacy + correlation + the full two-dimension contract (job class +
  compliance/security) **without** ML/ORT packaging risk.
- **P2 — GLiNER2 backend + host-load governor.** Spike the **Go + ONNX `Model`
  backend** behind the existing interface (fall back to a bundled GLiNER2 sidecar
  if structured decode in Go proves too costly) + model packaging; port the
  `inference-enrichment` eval set to gate quality; the **adaptive governor** (EWMA
  host load → concurrency + admission/sample rate) graduates from the P1 floor.
  These pair because GLiNER2 inference is the expensive work the governor paces.
- **P3 — GUI installers.** `.pkg` + MSI + signing/notarization. (The
  launch-blocking deliverable, since the installer is the enforced path; P1–P2
  are validated headless first because that is faster.)
- **P4 — more sources + org control plane.** Agent-framework producers
  (LangChain / Mastra) and Claude Desktop (chat / cowork) over the inline path;
  Codex/Gemini `TranscriptReader`s as those tools expose prompt text to a hook;
  tray/menu-bar status UI. **Org-level remote settings:** an admin can push
  settings to all running daemons connected to their org to globally toggle
  behaviors — starting with `include_entity_text` — following one general
  remote-settings pattern (poll/subscribe a per-org settings document; daemons
  apply on next fetch). P1 ships the *local* settings file these later become a
  remote source for; the daemon reads settings at startup so the control-plane is
  a drop-in source later, not a rewrite.

## 13. Open risks

1. **Transcript format is internal and changes between Claude Code versions.**
   Mitigation: tolerant parsing, golden fixtures per known shape, clean skip on
   drift, a CI canary that flags format changes.
2. **Write-timing** — the new user turn may not be flushed to the `.jsonl` at the
   instant `UserPromptSubmit` fires. Mitigation: short bounded poll for the line
   matching `prompt_id`, scanning only from the per-transcript byte cursor; a
   trailing partial line (no newline) is never consumed, so it is re-read once
   flushed; give up after a small budget and skip.
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
8. **GLiNER2 structured decode in Go** — GLiNER2's schema-based extract/classify
   (the reference's `extract(text, labels, tasks)`) is non-trivial to reproduce
   over raw ONNX in Go. Contained by the swappable `Model` interface: if the Go
   spike misses quality/effort bars, fall back to the bundled Python sidecar.
9. **Security-detection precision** — false negatives miss real leaks; false
   positives spam admins. Mitigation: regex priors for high-certainty secrets
   (P1), an eval/gold set with precision/recall gates (ported from
   `inference-enrichment`), confidence thresholds on the admin surface, and
   "hard span evidence overrides weak classifier."
10. **Masking correctness** — a buggy masker could leak the value it should
    redact. Mitigation: mask at extraction time before any sink; a leak test
    asserts no raw span value ever appears in outbound payloads or logs.

## 14. Testing

- **Unit** — `TranscriptReader` against golden `.jsonl` fixtures (including
  malformed / version-drift); each `Extractor` against fixtures (prompt →
  expected labels/spans, with tolerance); `sensitivity` masking (value in →
  masked out, raw never present); `Dispatcher` backpressure + dedup; Atlas
  publisher retry / offline.
- **Eval/gold set** — port `inference-enrichment`'s `gold.jsonl` + eval runner;
  gate `task_type` accuracy and `sensitivity` precision/recall before shipping a
  model backend change (label-vocab changes bump `schema_version` and re-run).
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
