# Full-fidelity host-side telemetry for watched sources (Cowork)

**Status:** Design approved (field ceiling accepted by user), pending TDD implementation
**Date:** 2026-07-21
**Builds on:** v0.9.0 transcript watcher; v0.9.1 minimal `promptlog` emitter

## Goal

Cowork prompts must produce telemetry in Atlas with **field-level parity** to the
Claude Code CLI's native OTEL — not the single thin `user_prompt` event v0.9.1
emits. The daemon emits it **host-side** (unrestricted egress) because Cowork's
sandbox egress allowlist excludes `atlas.keld.co`, so its natively-configured
OTEL never leaves the sandbox.

## Ground truth (captured from a real `claude` OTEL export, 2026-07-21)

Claude Code exports OTLP/HTTP to `{endpoint}/v1/logs` and `/v1/metrics`.

**Resource attributes:** `service.name=claude-code`, `service.version=<ver>`,
`os.type`, `os.version`, `host.arch`.

**Log events** (body=`claude_code.<event.name>`), relevant subset:
- `user_prompt`: `session.id`, `prompt.id`, `prompt_length`, `message.uuid`,
  `event.name`, `event.timestamp`, `event.sequence`, `terminal.type`, `user.email`,
  `user.account_uuid`, `user.id`, `user.account_id`, `organization.id`.
- `api_request`: `model`, `input_tokens`, `output_tokens`, `cache_creation_tokens`,
  `cache_read_tokens`, `cost_usd`, `cost_usd_micros`, `duration_ms`, `request_id`,
  `prompt.id`, `session.id`, `query_source`, + identity attrs.
- `assistant_response`: `model`, `response_length`, `request_id`, `message.uuid`,
  `prompt.id`, `session.id`, + identity attrs.

**Metrics:** `claude_code.token.usage`, `claude_code.cost.usage`,
`claude_code.session.count`, `claude_code.active_time.total`. Datapoint attrs
include `model`, `type`, `session.id`, identity.

## Reconstruction sources (host-side, from the transcript + cowork metadata)

- **Identity** (Anthropic account, NOT keld's login): Cowork transcript path is
  `…/local-agent-mode-sessions/<accountUUID>/<orgUUID>/local_<id>/.claude/projects/…`.
  → `user.account_uuid`=accountUUID, `organization.id`=orgUUID; `user.email` from
  `<…>/<accountUUID>/<orgUUID>/local_<id>.json`'s `emailAddress`.
- **user_prompt**: `session.id`=record `sessionId`; `prompt.id`=`promptId`;
  `message.uuid`=`uuid`; `prompt_length`=rune count of resolved text.
- **api_request / assistant_response**: from the transcript **assistant** records —
  `model`=`message.model`; tokens from `message.usage`
  (`input_tokens`, `output_tokens`, `cache_creation_input_tokens`→`cache_creation_tokens`,
  `cache_read_input_tokens`→`cache_read_tokens`); `request_id`=`message.id`.
- **token.usage metric**: from the same usage. **cost.usage / cost_usd**: derived
  via a small per-model USD price table (opus/sonnet/haiku); omitted for unknown
  models.

## Accepted gaps (flagged, not reconstructable host-side)

- `duration_ms` — runtime-only, absent from the transcript → omitted.
- `terminal.type` — Cowork is a GUI, no tty → omitted.
- `user.id`, `user.account_id` — not in Cowork local metadata → omitted.
- metric `active_time.total` — not reconstructable → omitted.
- `event.sequence` — synthesized as a per-session monotonic counter (best effort).
- `cost_usd` — **derived** from a price table; may drift as prices change.

## Design

### Architecture

The watcher already reads every new complete transcript line. Add a per-line
**observer** hook: the watcher calls `observe(source, transcriptPath, line)` for
every new line (in addition to the existing prompt-offer for enrichment). The
daemon wires `observe` to the telemetry emitter. The emitter parses the line and
emits the matching OTEL:

```
watcher new line ──▶ offer(pointer)         [existing: enrichment]
                └──▶ observe(source,path,line) ──▶ Telemetry.Observe
                        user record   → user_prompt log
                        assistant rec → api_request + assistant_response logs
                                        + token.usage / cost.usage metrics
                        → POST OTLP to {endpoint}/v1/logs and /v1/metrics
```

Telemetry is emitted only for sources in the configured set (default `{cowork}`;
Claude Code excluded — it emits its own OTEL host-side). Never carries prompt or
response **text** — only lengths, ids, model, tokens.

### Files

- `internal/agent/promptlog/otlp.go` (new) — typed OTLP/HTTP JSON builders:
  `logsPayload(resource, records)`, `metricsPayload(resource, metrics)`, attribute
  helpers. Pure, unit-tested against the captured schema.
- `internal/agent/promptlog/identity.go` (new) — `Identity` + `coworkIdentity(path)`
  + cached `identityCache`.
- `internal/agent/promptlog/pricing.go` (new) — `costUSD(model, usage)` small price
  table; returns (cost, ok).
- `internal/agent/promptlog/promptlog.go` (rewrite) — `Telemetry` type with
  `Observe(source, transcriptPath string, line []byte)`; parses the record, builds
  events + metrics, POSTs (best-effort, logs status). Keeps `SourcesFromEnv`.
  Resource attrs from record `version` + `runtime.GOOS`/`GOARCH`.
- `internal/agent/watch/watch.go` (modify) — add optional `observe` callback,
  invoked per complete line in `scanFile`; `New` gains the param.
- `internal/agent/daemon/daemon.go` (modify) — construct `Telemetry`; wire
  `observe`; keep `logsEndpoint`, add `metricsEndpoint`.

### Global constraints

- Module `github.com/ncx-ai/keld-signal`; gofmt gate; no `SchemaVersion` change.
- **Never emit prompt/response text.**
- Telemetry is best-effort: never blocks or panics the watcher (recover in the
  watcher poll already covers the observe call).
- Default sources `{cowork}`; `KELD_WATCH_TELEMETRY` off/on, `KELD_WATCH_TELEMETRY_SOURCES`.
- Payloads validated against the captured CLI schema in tests (event names,
  attribute keys, resource attrs).

## Testing strategy (TDD)

Each unit written test-first. Payload builders asserted against the exact captured
schema (event.name set, attribute keys, resource keys). `Observe` tested with real
transcript-shaped user + assistant lines → asserts the right events/metrics with
correct values and **no text**. Identity tested against the real cowork path shape.
A local `httptest` sink verifies the POST (headers, endpoint split logs vs metrics).

## Out of scope

- The flagged gaps above.
- Claude Code telemetry emission (native OTEL already covers it).
- Windows.
