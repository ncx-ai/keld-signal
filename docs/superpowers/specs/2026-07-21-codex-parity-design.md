# Codex support parity — enrichment capture + native-OTEL telemetry

**Status:** Design (approved approach), pending spec review → TDD plan
**Date:** 2026-07-21

## Goal

Bring Codex (OpenAI Codex CLI) support up to the Claude Code / Cowork level so a
normal Codex user's data reaches Atlas: **enrichment** (prompts → masked Profile)
and **telemetry** (usage/token data), matching the common structure/dataset as
much as Codex's data model allows.

## Current state (gap analysis)

HAS: install adapter + `~/.codex` detection; OTEL config written by `keld setup`
(but **logs-only**, token-in-URL); hooks `SessionStart`+`PreToolUse` fire
`keld __hook --source codex`; classification flags (`codex` ∈ `interactiveCodingTools`
and `codingTools`/A4=eng). MISSING/broken: **prompt capture** (Codex has no
prompt-submit hook → no `prompt_id` → `forwardToAgent` early-returns), **transcript
reader** (no Codex reader → every enrich job silently skips), **watcher root** for
`~/.codex`, and (deliberately, see below) no host-side telemetry emitter.

## Decisions (settled)

1. **Telemetry: rely on Codex's native OTEL, normalized in Atlas** — NOT
   reconstruction. Codex runs on the host (not a sandbox), so its native OTEL to
   Keld's `/v1/logs` is not egress-blocked and is first-hand/accurate. We only
   **complete its config**. `promptlog` stays off for `codex` (no double-count).
2. **Enrichment: full parity via the watcher** (Codex has no prompt-submit hook,
   so — like Cowork — the transcript **watcher** is the capture path).
3. **Pointer model, no inline text** — never write prompt text to the spool
   (preserve the privacy invariant).
4. **Schema pinned from openai/codex source + a fixture** (Codex isn't installed
   here); validate live when a Codex host is available.

## Codex rollout schema (from openai/codex source, 2026-07-21)

Sessions: `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` (override `CODEX_HOME`).
Each line is a `RolloutLine` wrapping a `RolloutItem`:

```
{"timestamp":"<rfc3339>","type":"<item>","payload":{…}[,"ordinal":N]}
```

- `EventMsg` is `#[serde(tag="type", rename_all="snake_case")]`. A **user prompt**
  is `type":"event_msg"` with `payload":{"type":"user_message","message":"<TEXT>", …}`
  (`UserMessageEvent.message: String` — confirmed in `protocol.rs`).
- `SessionMeta` (`type":"session_meta"`) payload carries `id`, `cwd`, `cli_version`,
  git info. This is the only source of session id + cwd (the user_message line has
  neither).
- Other types (`response_item`, `turn_context`, `token_count`, `compacted`,
  `world_state`, …) are NOT prompts and are ignored for enrichment.
- **No per-prompt id** — causality is temporal; `RolloutLine` has an optional
  `ordinal`. We synthesize a stable prompt id (below).

> The exact `RolloutItem` serde tag/content and `session_meta` field nesting will
> be pinned against source + a captured fixture during implementation; the
> user_message mapping and session_meta id/cwd above are confirmed.

## Design

### A. Telemetry — complete the native OTEL config

`internal/telemetry/telemetry.go` `CodexBlockBody` + `internal/tools/codex.go`:
- **Add a metrics exporter** (→ `/v1/metrics`) so token-usage metrics flow (today
  logs-only → no token metrics reach Atlas).
- **Move the ingest token to a header** (`x-keld-ingest-token`) instead of the URL
  query, matching Claude and header-based auth.
- Keep `log_user_prompt = false`.
- Update the golden file `internal/tools/testdata/golden/codex_apply.toml` + tests.

(Exact `[otel]` TOML keys for a metrics exporter / header map to be confirmed
against Codex's config schema; the block already uses `exporter = { otlp-http = … }`.)

### B. Enrichment — watcher root + Codex reader

**Watcher root** (`internal/agent/watch/roots.go`): add
`~/.codex/sessions` (honor `CODEX_HOME`) → source `codex`, on macOS + Linux. The
watcher recursively finds `rollout-*.jsonl` (its `transcriptFiles` already walks
`*.jsonl`; restrict to `rollout-*` or accept all under sessions/).

**Source-aware prompt extraction.** The watcher's per-line prompt detection is
currently Claude-format (`parsePrompt`: `type:user`, `promptId`, `message.content`).
Introduce a per-source **prompt extractor** selected by `root.SourceID`:
- `claude_code`/`cowork` → existing `parsePrompt` (stateless).
- `codex` → a **stateful per-file** extractor: track the file's `session_meta`
  (`id`, `cwd`) as lines stream; on an `event_msg`/`user_message` line, emit a
  `promptRec{ PromptID: <session_id>#<ordinal|index>, Cwd: <session cwd>,
  SessionID: <session_id> }`. Synthesized `PromptID` is stable + unique (queue
  dedup key `codex|prompt_id|<id>`).

**Codex `TranscriptReader`** (`internal/agent/resolve/codex.go`, registered in
`resolve.go`): `Source()=="codex"`; `Read(path, promptID)` re-scans the rollout
file, finds the user_message at the ordinal/index encoded in `promptID`, returns
its `message` text. Implements `RecentReader` (tail-scan prior `user_message`
texts) since `codex` is context-eligible. Pointer model — text is read locally,
never spooled.

**Downstream is unchanged:** resolve → enrich (GLiNER2) → mask → publish. Codex
enrichment then produces the same masked Profile as Claude Code. Classification
flags already correct (`codex` context-eligible + A4=eng).

### C. Telemetry emitter stays off for Codex

`promptlog.SourcesFromEnv()` default `{cowork}` already excludes `codex`. The
watcher's `observe` hook will fire for codex lines, but `Telemetry.Observe` ignores
non-listed sources — so no host-side emit for codex (native OTEL owns it). No change
needed; add a guard test.

### Privacy / resource notes

- Never inline prompt text; pointer + local read only.
- Codex session files can be large / long-lived; the watcher's cursor + stat-gate
  (already built) bound re-reads. `rollout-*` files are date-partitioned and many —
  forward-only cursors keep first-run cost bounded.

## Testing (TDD)

- **Codex reader** (`codex_test.go`): fixture rollout JSONL → `Read` extracts the
  right `user_message` text by synthesized id; ignores `response_item`/`token_count`/
  `session_meta`; `RecentUserPrompts` returns prior prompts; malformed lines tolerated.
- **Codex prompt extractor** (watch): stateful — `session_meta` then `user_message`
  → `promptRec` with session cwd + synthesized id; response/tool lines skipped.
- **Watcher roots**: `~/.codex/sessions` discovered (source `codex`) on macOS+Linux;
  `CODEX_HOME` honored.
- **Adapter golden**: new `codex_apply.toml` with metrics exporter + header auth.
- **No-double-emit guard**: `promptlog` default sources exclude `codex`.
- **resolve**: `Resolve("codex", fixture, id, "")` returns text; unknown id → skip.

## Out of scope

- Host-side telemetry reconstruction for Codex (native OTEL used instead).
- Codex-specific identity recovery (enrichment publishes under keld's actor/org,
  like Claude Code enrichment; telemetry identity comes from Codex's own OTEL).
- Live end-to-end validation on a real Codex install (no Codex here) — reader built
  against source + fixture; a follow-up validates on a Codex host.
- Windows (`%USERPROFILE%\.codex`) — after macOS/Linux, consistent with the watcher's
  current platform scope.

## Risks

- **Schema drift / unconfirmed fields** — the `RolloutItem` wrapper serde shape and
  `session_meta` nesting are pinned from source, not a live capture; a fixture from
  a real rollout file (or the user) removes this risk. The reader tolerates unknown
  lines, so drift degrades to "misses some prompts", never crashes.
- **Codex `[otel]` metrics/header schema** — confirm Codex supports a metrics
  exporter + header auth in `config.toml` before finalizing the block; if not, keep
  logs + document the limitation.
