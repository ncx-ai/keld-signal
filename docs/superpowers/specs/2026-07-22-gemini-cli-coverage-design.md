# Gemini CLI coverage — design

**Date:** 2026-07-22 · **Status:** spec (awaiting review) · **Prereq tag:** v0.10.0

Extends keld-signal's per-tool coverage to **Gemini CLI** (`@google/gemini-cli`),
bringing it to Claude-Code-parity: both the **enrichment** and **telemetry**
streams, driven from empirically captured oracles (not docs alone). Scope is
**Gemini CLI only** — Code Assist / Vertex / web are out of scope (see §8).

Today (v0.10.0) Gemini is *telemetry-config-only and broken*: `keld setup`
writes a `telemetry` block whose `otlpEndpoint` bakes in `/v1/logs?token=…`,
which the OTLP exporter mangles; there is no enrichment capture at all.

---

## 1. Empirical ground truth (v0.51.0, captured on this machine)

Installed `@google/gemini-cli@0.51.0` (npm global), driven headless with
API-key auth. Oracles captured: real chat transcript, real OTLP wire payloads
(local sink), and real hook stdin. All claims below are observed, not inferred.

### 1.1 Auth / run context
- OAuth "Gemini Code Assist for individuals" is **deprecated** → `IneligibleTierError`
  ("migrate to Antigravity"). The working path is **API key**
  (`security.auth.selectedType:"gemini-api-key"` + `GEMINI_API_KEY`, e.g. via
  `~/.gemini/.env`). Directory trust needed for headless: `GEMINI_CLI_TRUST_WORKSPACE=true`.
- Gemini runs **host-side** (Node) → native OTEL reaches Keld directly; **no
  host-side reconstruction** needed (unlike Cowork).

### 1.2 Enrichment oracles
- **Transcripts:** `~/.gemini/tmp/<project>/chats/session-<ts>-<shortid>.jsonl`.
  (Dir segment is the project basename in this version; a `projectHash` also
  appears inside the file.) One JSONL **event log** per session:
  - line 0 — session meta: `{sessionId, projectHash, startTime, lastUpdated, kind:"main"}`
  - `{"$set": {…}}` — **mutation records** (e.g. `lastUpdated`, initial `messages`
    hydration). **Must be skipped** by the extractor.
  - user prompt — top-level `{id, timestamp, type:"user", content:[{text:"…"}]}`.
    The message **`id` (UUID) is the natural `PromptID`**.
  - model turn — `{id, timestamp, type:"gemini", content, thoughts, tokens, model}`.
- **BeforeAgent hook stdin** (the prompt-submit trigger):
  ```json
  {"session_id":"…","transcript_path":"…/chats/session-….jsonl",
   "cwd":"…","hook_event_name":"BeforeAgent","timestamp":"…Z",
   "prompt":"<inline user text>"}
  ```
  **No `prompt_id`.** Fires *before* the user line is persisted (BeforeAgent at
  T+0.8s; user line written at T+3.8s) — so a hook-issued pointer would race the
  transcript write. All lifecycle hooks share the base fields
  (`session_id`, `transcript_path`, `cwd`, `hook_event_name`, `timestamp`).
- Hooks require **silent stdout** (only final JSON is parsed; any stray text →
  parse-fail). Empty stdout + exit 0 is safe. Gemini even sets `CLAUDE_PROJECT_DIR`
  as a compatibility alias — it mirrors Claude's hook conventions.

### 1.3 Telemetry oracles
- With `telemetry:{enabled:true, target:"local", otlpProtocol:"http",
  otlpEndpoint:"<BASE>", logPrompts:false}`, Gemini POSTs **OTLP/JSON** to
  **`<BASE>/v1/logs`**, **`<BASE>/v1/metrics`**, **`<BASE>/v1/traces`** — the
  exporter appends the signal path itself. (User-Agent
  `OTel-OTLP-Exporter-JavaScript/0.218.0` → standard OTel SDK.)
- **Headers:** no settings field exists; the SDK **honors
  `OTEL_EXPORTER_OTLP_HEADERS`** (verified: `x-keld-ingest-token` + `x-keld-actor`
  arrived on every POST).
- **Trace export cannot be disabled (correction, re-validated 2026-07-22):** an
  earlier capture claimed `OTEL_TRACES_EXPORTER=none` drops trace export. That is
  **false** for gemini-cli. Reading `@google/gemini-cli` 0.51.0's bundled
  `initializeTelemetry`: when telemetry is enabled it *unconditionally* builds
  `new BatchSpanProcessor(new OTLPTraceExporter(...))` plus an `HttpInstrumentation`
  and hands them to the NodeSDK. It constructs its own exporters, so the generic
  OTel SDK env var `OTEL_TRACES_EXPORTER` is ignored. A sink capture confirmed 5
  `/v1/traces` POSTs still arrive with that var set. There is no per-signal
  off-switch; only `telemetryOutfile` (all signals → file, no network) or
  disabling telemetry entirely would stop it — both unacceptable (we need
  logs+metrics on the network). We therefore let content-free traces flow (see §privacy).
- **Distinguisher:** resource attr `service.name:"gemini-cli"` (+ `service.version`).
  No custom `tool=` attribute is available; Atlas keys on `service.name`.
- **Identity present even with API-key auth:** every log record + metric data
  point carries `user.email`, `installation.id`, `auth_type`, `session.id`.
- **Tokens:** metric `gemini_cli.token.usage` (cumulative sum, monotonic,
  per-`type` = input/output/thought/cache/tool) and log `gemini_cli.api_response`
  (`input_token_count`, `output_token_count`, `cached_content_token_count`,
  `thoughts_token_count`, `tool_token_count`, `total_token_count`). Plus GenAI
  semconv `gen_ai.client.token.usage`.
- **Privacy verified (re-validated 2026-07-22):** with `logPrompts:false`, no
  prompt text appears in any log record field or body (`user_prompt` carries only
  `prompt_length`+`prompt_id`). Spans are also content-free: gemini-cli gates
  span payloads behind `shouldIncludePayloads = getTelemetryTracesEnabled() &&
  getTelemetryLogPromptsEnabled()`, so `logPrompts:false` alone keeps prompt and
  response bodies out of spans, and we additionally set `traces:false` in the
  settings block to make that robust to any future default change. Two sink
  captures (old config, and the exact new config) found **no** prompt text and
  **no** `process.command_args` in any `/v1/logs`, `/v1/metrics`, or `/v1/traces`
  body. So although trace export can't be turned off, the traces that flow carry
  no prompt content.

---

## 2. Design overview

Two independent streams, both derived on-device, mirroring the existing
architecture (capture→queue→resolve→enrich→mask→publish; and native OTEL):

| Capability | Decision |
|---|---|
| Install adapter + detect | keep (`~/.gemini`) |
| `keld setup` config | **settings.json** (telemetry + hooks) **and** a keld-managed block in **`~/.gemini/.env`** (OTEL auth headers) |
| Command hook | `BeforeAgent` → `keld __hook --source gemini`, **context event only** |
| Transcript watcher root | `~/.gemini/tmp/*/chats/*.jsonl` |
| Transcript reader/extractor | new Gemini reader + extractor (skip `$set`, `type:"user"`) |
| Enrichment classification | add `gemini` to coding-tool flags |
| Telemetry | **native OTEL** (host-side), logs+metrics+traces; traces content-free (can't be disabled) |

## 3. Stream 1 — Telemetry (native OTEL)

**`telemetry.GeminiTelemetry(p)`** → settings.json `telemetry` block:
```json
{ "enabled": true, "target": "local", "otlpProtocol": "http",
  "otlpEndpoint": "<p.Endpoint>", "logPrompts": false, "traces": false }
```
Change from today: `otlpEndpoint` becomes the **base** endpoint (was
`"<endpoint>/v1/logs?token=<tok>"`, which is broken). No token in the URL.
`logPrompts:false` + `traces:false` together gate `shouldIncludePayloads`, so
spans never carry prompt/response bodies (see §1.3).

**Auth via `~/.gemini/.env`** (new managed artifact). keld writes a delimited
block, preserving all other lines (notably the user's `GEMINI_API_KEY`):
```
# >>> keld-managed (do not edit) >>>
OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=<tok>,x-keld-actor=<actor>
# <<< keld-managed <<<
```
No `OTEL_TRACES_EXPORTER` line: gemini-cli ignores it (§1.3), so it would be dead,
misleading config.
`Remove` strips only this block. Header auth mirrors the Codex fix
(`x-keld-ingest-token`/`x-keld-actor` header, not a URL token).

**No reconstruction** — Gemini is host-side; native OTEL reaches Keld. The
`promptlog` host-side emitter stays **off** for gemini. Atlas distinguishes the
surface via `service.name:"gemini-cli"`; identity via `user.email`. Exact tokens
emitted; **Atlas computes cost** (no on-device price table).

## 4. Stream 2 — Enrichment (capture)

**Watcher is the sole enrichment trigger** (chosen for correctness: it has the
message `id` for a collision-free dedup key and is immune to the BeforeAgent
timing race).

- **Watcher root** (`internal/agent/watch/roots.go`): add
  `~/.gemini/tmp/*/chats/*.jsonl`.
- **`geminiExtractor`** (new, alongside `claudeExtractor`/`codexExtractor`):
  per-line; ignore any line where the top-level object has a `$set` key; emit a
  prompt for each top-level record with `type=="user"`, concatenating
  `content[].text`; `PromptID = record.id`; `SessionID = <from filename/meta>`.
- **Gemini transcript reader** registered in `resolve.go init()`: given
  `(path, promptID)`, read the JSONL, find the `type:"user"` record whose `id`
  matches, return its `content[].text`. Pointer model — text read locally at
  enrich time; never spooled.
- **Queue key** `gemini|<scheme>|<message-id>` — no hook/watcher collision
  because only the watcher forwards enrichment pointers.

**BeforeAgent hook — context event only.** `keld setup` wires
`settings.json → hooks.BeforeAgent → [{type:"command", command:"keld __hook --source gemini"}]`.
`keld __hook` already parses `session_id`/`cwd`/`transcript_path` from stdin and
posts the repo/`.keld.toml` context event. For gemini it **does not forward an
enrichment pointer** (no shared `prompt_id`; watcher owns capture). Requirement:
`keld __hook` must write **nothing to stdout** (Gemini's strict-JSON rule) and
exit 0 — to be verified in the plan (add a test asserting empty stdout).

**Classification:** add `gemini` to `interactiveCodingTools`
(`enrich/context.go`) and `codingTools` (`a4_compositional.go`).

## 5. Key files

- `internal/telemetry/telemetry.go` — fix `GeminiTelemetry`; add a `.env` block builder.
- `internal/tools/gemini.go` — `Apply`/`Remove`/`Status` also manage `~/.gemini/.env`
  block + wire `hooks.BeforeAgent`; `Managed` tracks both artifacts.
- `internal/agent/watch/roots.go` — add gemini chats glob.
- `internal/agent/watch/gemini.go` — new `geminiExtractor`.
- `internal/agent/resolve/{resolve.go,gemini.go}` — new reader + registration.
- `internal/agent/enrich/context.go`, `a4_compositional.go` — add `gemini` flag.
- (verify) `internal/hook/*` — gemini source handled; empty stdout guaranteed.

## 6. Fidelity & validation

- **Telemetry fidelity test:** assert keld's emitted/normalized schema matches the
  captured OTLP oracle (`gemini_cli.token.usage` types; `api_response` token keys;
  resource `service.name`/`user.email`) minus documented omissions
  (`logPrompts:false` → no prompt text).
- **Enrichment fixture test:** the captured chat JSONL is the oracle; assert the
  extractor skips `$set`, returns exactly the `type:"user"` texts with correct
  `id`s, and the reader resolves a pointer to the right text.
- **Hook test:** `keld __hook --source gemini` on the captured BeforeAgent stdin
  → exit 0, **empty stdout**, one context event.
- **Live end-to-end** (avoids the Codex no-validation gap): with the machine's
  installed+authed Gemini, run a real session and confirm enrichment + native-OTEL
  token data land in Atlas, tagged `service.name:"gemini-cli"`.

## 7. Privacy & identity (invariants)
- Pointer model; never persist prompt/response text to disk or telemetry.
- `logPrompts:false` + `traces:false` (settings) — gates `shouldIncludePayloads`,
  keeping prompt/response text out of all OTEL payloads (logs and spans). Trace
  *export* itself can't be disabled (§1.3), but the exported spans are content-free.
- Emit lengths, ids, model, tokens only. Atlas computes cost.
- Identity: `user.email`/`installation.id` supplied by Gemini's native OTEL.

## 8. Out of scope
- **Gemini Code Assist** (IDE), **Vertex AI / Gemini API** (server-side),
  **gemini.google.com** (web) — not on-device in a keld-coverable way.
- **Antigravity** — separate product; flagged as the migration target Google now
  pushes individual users toward. Not covered here.

## 9. Risks / open items
- **Env delivery** depends on Gemini loading `~/.gemini/.env`. Verified it loads
  `GEMINI_API_KEY`; confirm it also surfaces `OTEL_*` into process env (plan step).
- **`.env` editing** must be surgical (block markers) to never clobber the user's
  API key. Idempotent apply + clean remove; test both.
- **Project-dir segment** under `~/.gemini/tmp/` is a basename in v0.51.0 (docs say
  hash). The glob `tmp/*/chats/*.jsonl` handles either; don't hard-code.
- **Gemini version drift** — schema pinned to 0.51.0 via fixtures + fidelity test;
  re-capture on major upgrades.
- **Traces to Atlas:** disabled via env; if a user removes the `.env` block,
  Gemini would POST `/v1/traces` — Atlas should tolerate/ignore that path.
