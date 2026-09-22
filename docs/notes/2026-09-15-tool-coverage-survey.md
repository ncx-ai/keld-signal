# Tool coverage survey — what Signal could capture next

**2026-09-15.** Research only. No production code changed.

Signal wants three things from a tool, in this order: **spend** (tokens, cost,
model, ideally a turn id), **prompt and assistant text readable on device**, and
**a stable turn identity** joining the two. Raw text never leaves the machine; it
is read locally, classified and masked. A tool that stores nothing locally is a
hard limit, not an inconvenience.

The headline finding is that the survey's own premise was too pessimistic.
Signal's two existing lanes — a loopback OTLP proxy and a command hook — are not
Claude-Code-specific mechanisms. **Fourteen tools export OTLP to an address the
user chooses, and fourteen run a Claude-Code-shaped command hook.** Eight do
both. For those eight the work is a config writer, not a transcript reader.

## Evidence key

| Tag | Meaning |
|---|---|
| **M** | Measured on a machine today — a real directory, a real schema, a real byte count. |
| **C** | codeburn's provider doc or parser (`../codeburn`, MIT). codeburn extracts spend only, so it proves where data lives and usually proves text sits in the same file it does not read. |
| **D** | Vendor documentation or source, URL in the Sources list. |
| **U** | Unverified. The row says what would settle it. |

Storage classes reuse `internal/agent/integrations/catalogue.go` — `jsonl-tail`,
`db-poll`, `rpc` — plus four this survey needed:

- **`hook-push`** — the tool runs a command hook that hands over the prompt and
  the response. Nothing is read; the tool pushes. Claude Code's existing lane.
- **`log-tail`** — an append-only prose or ledger file, not a structured record.
- **`cloud-only`** — no readable local history of any kind.
- **`api-only`** — the data exists solely behind a vendor API, usually admin-scoped.

Effort:

- **XS** — the tool already speaks OTLP to a chosen endpoint *and* pushes text
  over a hook. An adapter in `internal/tools/` and a source id. No reader, no
  watcher, no new primitive.
- **S** — a watcher root plus a reader, the shape built three times already
  (`resolve/codex.go` 224 lines, `watch/codex.go` 274, `readers/codex.py` 530).
- **M** — needs a capture primitive Signal does not have: SQLite polling, a live
  RPC, an SSE or Unix-socket subscription, zstd decompression, decryption.
- **L** — a different product surface: a browser extension, a cloud API poll, an
  admin consent flow.
- **X** — not buildable with the current design.

---

## Conclusions

### 1. The loopback proxy scales much further than three tools

Fourteen tools export OTLP to an endpoint the user sets. Signal already points
three of them at `127.0.0.1:14318`. The rest are the same wiring with different
key names **[D]**:

| Tool | How you point it at the proxy |
|---|---|
| Gemini CLI | `telemetry.otlpEndpoint` in settings, default `localhost:4317` |
| Codex | `[otel] exporter = { otlp-http = { endpoint = … } }` |
| Claude Code | `OTEL_EXPORTER_OTLP_ENDPOINT` |
| GitHub Copilot CLI | `COPILOT_OTEL_ENABLED` + `OTEL_EXPORTER_OTLP_ENDPOINT`; http/json or protobuf, **no gRPC** |
| Copilot in VS Code | `github.copilot.chat.otel.otlpEndpoint`, default `:4318`; `COPILOT_OTEL_ENDPOINT` wins over the standard var |
| Goose | standard `OTEL_*`; ⚠️ **HTTP only** — `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` silently disables the signal |
| Grok Build (xAI) | `GROK_EXTERNAL_OTEL=1` **plus** `OTEL_METRICS_EXPORTER=otlp`, then standard vars |
| OpenCode | standard `OTEL_*`; LLM spans need `experimental.openTelemetry: true` |
| Cline | `CLINE_OTEL_EXPORTER_OTLP_ENDPOINT` + protocol/headers; env config sets `bypassUserSettings` |
| Kilo Code | standard `OTEL_*`; `experimental.openTelemetry` **defaults true** |
| Qwen Code | `QWEN_TELEMETRY_OTLP_ENDPOINT`; the bare `OTEL_EXPORTER_OTLP_ENDPOINT` is **not** read |
| Factory Droid | `OTEL_TELEMETRY_ENDPOINT` (falls back to the standard var) |
| Continue CLI | `CONTINUE_METRICS_ENABLED=1` + `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`; **metrics only** |
| Mistral Vibe | `otel_endpoint` in `config.toml`; **traces only** |

Vendor-only, and therefore out of reach of a loopback proxy: Cursor, Kiro,
Windsurf, Zed, Warp, Devin, Antigravity, Aider, Crush, Kimi, Pi, Amp, Roo Code.

### 2. ⚠️ Four of those default to putting prompt text on the wire

This is the privacy item to settle before writing any adapter. **Gemini CLI's
`logPrompts` defaults to `true`**; **Qwen's `QWEN_TELEMETRY_LOG_PROMPTS` defaults
to `true`**; **OpenCode's LLM spans carry prompt and response** because it never
sets `recordInputs/recordOutputs: false` on the Vercel AI SDK; **Kilo Code's
`experimental.openTelemetry` defaults true** **[D]**.

Signal already writes `logPrompts: false` for Gemini and `log_user_prompt = false`
for Codex, and `teleproxy.textKey` strips text attributes at the proxy. Both
halves are load-bearing here, and the second is what makes the first a
defence-in-depth measure rather than the only one. Any new adapter must write the
tool's own off switch **and** be covered by the proxy's gate. A tool whose
attribute names the gate has never seen is the failure mode `striptext_identity_test.go`
exists for.

### 3. Fourteen tools run a Claude-Code-shaped command hook

`UserPromptSubmit` with the prompt text, `Stop` with the assistant message, JSON
on stdin. Signal's hook already speaks this. **[D]**

| Tool | Config path | Prompt event | Response event |
|---|---|---|---|
| Claude Code | `~/.claude/settings.json` | `UserPromptSubmit` | `Stop` |
| Codex | `~/.codex/hooks.json` | `UserPromptSubmit` | `Stop` |
| GitHub Copilot CLI | `~/.copilot/hooks/*.json` **[M]** | `userPromptSubmitted` → `prompt` | `subagentStop` only |
| Cursor | `~/.cursor/hooks.json` | `beforeSubmitPrompt` → `prompt` | `afterAgentResponse` → `text` |
| Windsurf / Cascade | `~/.codeium/windsurf/hooks.json`, JetBrains at `~/.codeium/hooks.json` | `pre_user_prompt` → `user_prompt` | `post_cascade_response` → `response` |
| Goose | Open-Plugins hook spec | `UserPromptSubmit` → `message` | `Stop` → `last_assistant_message` |
| Grok Build | `~/.grok/hooks/*.json` **[M]** | `UserPromptSubmit` | `Stop` |
| Qwen Code | hooks config, **`http` executor type** | `UserPromptSubmit` | `Stop` |
| Factory Droid | `~/.factory/hooks.json` | `UserPromptSubmit` | `Stop`, no message text |
| Kiro | `.kiro/hooks/<id>.json` — **project root only** | `Prompt Submit`, prompt in a `USER_PROMPT` env var | `Agent Stop` |
| Devin CLI | `.devin/hooks.v1.json` | `UserPromptSubmit` → `prompt` | none |
| Kimi Code | `[[hooks]]` in `~/.kimi-code/config.toml` | `UserPromptSubmit` / `TurnStarted` → `prompt` | `Stop` |
| Junie (JetBrains) | `~/.junie/config.json` | `UserPromptSubmit` → `prompt` | `Stop` → `last_assistant_message` |
| Mistral Vibe | `.vibe/hooks.toml` | **none** — only `post_agent` + `transcript_path` | `post_agent` |

Three convergences worth exploiting. **Grok Build reads `~/.claude/settings.json`
and `~/.cursor/hooks.json` as well as its own** **[D]**, so a Claude Code
installation already configures it. **Copilot in VS Code also reads
`.claude/settings.json`** **[D]**. **Devin CLI reads `.claude/settings.json`**
**[D]**. Whatever Signal writes for Claude Code is partly writing for three more
tools already.

And **three hook systems accept an HTTP transport** — Copilot CLI's `type:"http"`,
Qwen's `http` executor, Continue's `data:` destination — which POST the payload
directly. A hook that POSTs to the daemon's loopback `/enrich` needs no `keld`
binary on PATH and no shell.

### 4. So the cheap wins are eight, not three

These have **both** lanes. Each is an adapter and a source id — no reader, no
watcher, no new primitive.

**GitHub Copilot CLI**, **Goose**, **Grok Build**, **Qwen Code**, **Factory
Droid**, **Cline**, **OpenCode**, **Copilot in VS Code**.

Two of the eight are better than the rest. **Goose's SQLite carries a
`usage_ledger` with tokens and cost** and its `Stop` hook carries
`last_assistant_message` **[D]** — spend and both text sides without a reader.
**OpenCode's `session` table carries `cost` and `tokens_input/output/reasoning/
cache_read/cache_write` natively** — verified here, alongside `part.data`
`{type:"text", text}` **[M]** — so its spend half needs no OTLP at all.

**Cursor** and **Windsurf** are a half-step behind: full hook coverage of both
text sides, but no user-pointable OTLP, so spend must come from their own stores
(`state.vscdb` `tokenCount` **[M]**) or from a hosted collector.

**One drift to fix while you are there.** `readers/codex.py` exists in this
worktree, but `catalogue.go` still carries `ReaderAvailable: false` for `codex`,
so its reader and watcher lanes are not expected and a broken Codex reader reads
as `idle` rather than `broken` **[M]**. The **Gemini CLI sidecar reader** is the
other half-built item: Go has `resolve/gemini.go`, the sidecar has only
`claude.py` and `codex.py`.

### 5. One new capture primitive buys the rest

Not one primitive — three, in descending order of value.

**SQLite polling** (largest). OpenCode, Goose, Crush, Forge, Hermes, Zed, Warp,
ZCode, Kilo Code, Copilot's VS Code OTel store, Copilot's JetBrains Nitrite
store, Devin, Quick Desktop and Cursor all keep history in a local database.
Signal's watcher resumes from a byte offset; a database resumes from a rowid or a
timestamp, needs a read-only open against a file another process is writing, and
needs WAL-safe polling.

**A local event subscription** (second). Roo Code broadcasts every event including
full message text on a Unix socket named by `ROO_CODE_IPC_SOCKET_PATH`; Crush
serves SSE at `/v1/workspaces/{id}/events` with full parts; OpenCode serves SSE at
`GET /event`; Kilo serves `/global/event`; Cline runs a local WebSocket hub
**[D]**. Same shape five times, and it is the only route into Roo Code now that
its telemetry has been removed.

**ACP proxying** (third, and speculative). Zed and IntelliJ both spawn agents as
subprocesses over JSON-RPC — `agent_servers` in Zed's settings, `~/.jetbrains/acp.json`
in IntelliJ. A proxy agent registered there sees `session/prompt` and
`session/update`, so it sees everything **[D]**. That is a shim process, not a
reader, and it changes what Signal ships.

### 6. Around twenty tools are a reader each, and that is the real question

Pi, OMP, Kimi, Kimi Code, Mistral Vibe, Mux, DSH, OpenClaw, OpenClaude, Open
Design, Codebuff, CodeWhale, Zerostack, Cline's task tree, Cline CLI, Roo Code's
task tree, IBM Bob, Kiro's four on-disk generations, cursor-agent, Amp, Windsurf
— every one is `jsonl-tail` or a per-task JSON tree, and every one carries prompt
text in the file codeburn already parses for spend **[C]**.

Each is small alone and none is small twenty times.
`docs/notes/2026-09-14-normalised-turn-record.md` already names the trigger:
*"Revisit when a paying org asks for a fourth tool. At that point 46 bespoke
readers is not sustainable in Python either, and embedding codeburn's parsers as
a Node step becomes the honest option despite the runtime."* This survey is that
moment arriving.

**OpenClaude is a free row** — a Claude Code fork writing Claude Code's exact
JSONL schema, so the existing reader parses it with a watcher root and nothing
else **[C]**.

### 7. Telemetry-only, and why

Four tools give spend and nothing else, because the file has no text column at
all: **ZCode** (SQLite schema has no content field), **LingTai TUI** (a token
ledger; the doc says the prompt is not included), **Open Design** (an event log
where only usage events are written), **Vercel AI Gateway** (a remote aggregate
API) **[C]**.

**Warp** is telemetry-only by vendor policy: its Enterprise Analytics API is
message-level metadata and **explicitly excludes raw conversation text** **[D]**.
Its SQLite may hold more; unverified.

**Google Gemini in Workspace** is telemetry-only in the weakest sense — the Admin
SDK Reports API emits one categorical event (`feature_utilization`: which app,
which feature) with no text, no model, no tokens and no cost **[D]**.

### 8. Impossible with the current design

- **Antigravity's IDE transcripts are encrypted at rest.**
  `~/.gemini/antigravity/conversations/*.pb` measures **7.999 bits/byte over
  299,680 bytes with zero printable runs of 12 characters or more** **[M]**;
  identified externally as AES-128-CTR with the key in the macOS Keychain
  (`Antigravity Safe Storage`) **[D]**. Its hook payload is coordinates only, and
  the path it hands you points at that encrypted file. Disk-watching the IDE is
  off the table. **The Antigravity CLI is a different answer** —
  `~/.gemini/antigravity-cli/history.jsonl` and
  `brain/<uuid>/.system_generated/logs/transcript_full.jsonl` are readable **[D]**.
- **Claude Desktop chat.** The `claude.ai` IndexedDB cache is **48 KB** on a
  machine with heavy use — a query cache, not a transcript store **[M]**.
- **Cowork on VM-backed builds.** Zero `.jsonl` under `local-agent-mode-sessions`
  here **[M]**. The host keeps only a session index
  (`claude-code-sessions/**/local_*.json`: `cliSessionId`, `sessionId`, `cwd`,
  `model`, `title`, timestamps — no text, no tokens) **[M]**. Cowork's OTLP
  exporter runs *inside the VM*, subject to session egress rules, so the host
  loopback proxy cannot receive it **[D]**. The existing `coworkHidden` advisory
  is the right answer.
- **ChatGPT Desktop, legacy store.** `com.openai.chat/conversations-v3-*/*.data`
  measures **7.964 bits/byte over 5,585 bytes, 256 distinct byte values, zero
  printable runs** — encrypted at rest since the July 2024 fix **[M, D]**.
- **Claude in Slack.** No local storage at all; two server-side copies **[D]**.
- **M365 Copilot, Gemini app, Notion AI, Perplexity Comet, grok.com.** Cloud-only.
  Notion's local `notion.db` does carry `thread` / `thread_message` / `prompt_usage`
  tables, all **zero rows** here **[M]**; whether AI threads ever land there is
  unverified.
- **ChatGPT Atlas.** Discontinued 2026-08-09 **[D]**. Do not build it.

### 9. Six things that change the architecture, not the catalogue

1. **The Anthropic Compliance API returns prompt and assistant text for
   `claude_code`, `cowork` and `office_agents` — recorded server-side as requests
   reach the API, nothing installed on the device, retained six years** **[D]**.
   A cloud pull that substitutes for the device agent across Anthropic's product
   line. Needs a Compliance Access Key the org's *primary owner* creates. No
   tokens and no cost, so it does not replace Atlas's spend half — but it does
   raise what the daemon is for on a Claude-only org.
2. **Microsoft Graph `getAllEnterpriseInteractions` returns full prompt and
   response text**, per user, joined on `requestId`, for Teams/Word/Outlook/BizChat
   — application permission `AiEnterpriseInteraction.Read.All`, 30-day retention,
   no tokens, no cost **[D]**. The only way to see M365 Copilot at all. A
   tenant-admin cloud pull, with text flowing through Keld rather than staying on
   the device. The change-notification variant delivers the same text to a
   **public HTTPS webhook** — loopback is structurally impossible.
3. **Cursor's and Kiro's OTLP exports are server-side pushes to a public
   collector.** Cursor: *"The endpoint must be reachable from the public internet.
   Cursor egresses from a fixed set of source IPs."* Kiro pushes daily at 02:00
   UTC, metrics only **[D]**. Receiving either means Atlas runs an OTLP collector
   — hosted ingest, not the loopback proxy. The same exporter is what Grok Bot
   Enterprise uses.
4. **Kiro's Prompt Logging writes prompts and responses to the customer's own S3
   bucket** **[D]**. That is a third ingest shape: the customer owns the sink and
   Keld reads it. Cheaper than an admin API and it keeps the text out of Keld's
   perimeter, but it is an integration with S3, not with a tool.
5. **Browser assistants need a browser extension.** Third-party sidebars persist
   to unencrypted LevelDB under `Local Extension Settings/<ID>/`, but the values
   are V8-serialized, undocumented and per-vendor **[D]**. Reading the page DOM of
   `gemini.google.com` or `copilot.microsoft.com` is the only general route, and
   an extension cannot see another extension's pages. A second shipped artifact,
   with its own store review and its own consent.
6. **Per-seat products publish no tokens at all.** M365 Copilot meters prompt
   *counts*; Notion meters *AI actions*; Perplexity meters *queries*; Google
   meters nothing **[D]**. If Signal's contract is spend in dollars, these tools
   cannot satisfy it, and the product has to say so rather than show a zero.

---

## The table

### Coding CLIs and TUIs

| Tool | Category | Installed here? | Spend telemetry (how) | Text on device (how) | Turn id | Storage class | Effort | Blocker |
|---|---|---|---|---|---|---|---|---|
| Claude Code | coding CLI | yes **[M]** | OTLP env vars → loopback proxy **[M]** | hook, and `~/.claude/projects/**/*.jsonl` **[M]** | `promptId` | hook-push + jsonl-tail | shipped | — |
| Codex | coding CLI | yes **[M]** | `[otel]` in `config.toml` → loopback **[M]** | hook, and `sessions/<Y>/<M>/<D>/rollout-*.jsonl` **[M]** | `session_id` + turn ordinal | hook-push + jsonl-tail | shipped | hooks stay untrusted — no `hooks.state` here **[M]** |
| Gemini CLI | coding CLI | yes **[M]** | `telemetry.otlpEndpoint` + `?token=` **[M]** | `~/.gemini/tmp/<hash>/chats/*.json(l)` **[M]** | message `id` | jsonl-tail | S | sidecar reader not written; ⚠️ `logPrompts` defaults **true** **[D]** |
| GitHub Copilot CLI | coding CLI | yes **[M]** | `COPILOT_OTEL_ENABLED` + `OTEL_EXPORTER_OTLP_ENDPOINT`, no gRPC **[D]** | hook `userPromptSubmitted` → `prompt`, **`type:"http"` transport**; `session-state/<id>/events.jsonl` **[D, M]** | `requestId` **[C]** | hook-push | **XS** | response text only on `subagentStop` |
| Goose (Block) | coding CLI | no | standard `OTEL_*`, **HTTP only** **[D]**; SQLite `usage_ledger` **[D]** | hook `UserPromptSubmit` → `message`, `Stop` → `last_assistant_message` **[D]** | session id **[C]** | hook-push + db-poll | **XS** | gRPC protocol silently disables the signal |
| Grok Build (xAI) | coding CLI | hooks dir only **[M]** | `GROK_EXTERNAL_OTEL=1` + standard vars **[D]** | Claude-shaped hooks; `sessions/**/chat_history.jsonl` **[D]** | `promptId` / `prompt_id` **[C]** | hook-push + jsonl-tail | **XS** | alpha; ⚠️ **also reads `~/.claude/settings.json`** **[D]** |
| Qwen Code | coding CLI | no | `QWEN_TELEMETRY_OTLP_ENDPOINT` **[D]** | hooks with an **`http` executor**; `projects/<cwd>/chats/<id>.jsonl` **[D]** | `uuid` + `sessionId` **[C]** | hook-push + jsonl-tail | **XS** | ⚠️ `_LOG_PROMPTS` defaults **true**; `tmp/<hash>/logs.json` accumulates text with no opt-out **[D]** |
| Factory Droid | coding CLI | no | `OTEL_TELEMETRY_ENDPOINT` **[D]** | hook `UserPromptSubmit`, **`transcript_path` on every payload** **[D]** | `entry.id` **[C]** | hook-push + jsonl-tail | **XS** | no `last_assistant_message`; metrics fan out to Factory too **[D]** |
| OpenCode | coding CLI | yes **[M]** | **`session` table carries `cost` + five token columns** **[M]** | `part.data` `{type:"text", text}` **[M]**; plugin `chat.message`; SSE `GET /event` **[D]** | `message.id` + `session_id` **[M]** | db-poll | **XS** (plugin) / M (db) | ⚠️ OTLP LLM spans carry prompt **and** response text **[D]** |
| Cline CLI | coding CLI | no | `CLINE_OTEL_EXPORTER_OTLP_ENDPOINT` **[D]** | `~/.cline/data/sessions/<id>/<id>.messages.json` **[D]** | `<sessionId>:<messageId>` **[C]** | hook-push + jsonl-tail | **XS** | which store a 4.1.x task writes to is **[U]** |
| Pi | coding CLI | installed, unused **[M]** | `message.usage` + `usage.cost.total` **[C]** | `~/.pi/agent/sessions/--<path>--/*.jsonl`; extension `before_agent_start` has `event.prompt` **[D]** | `responseId` **[C]** | jsonl-tail | S | no OTel; ⚠️ it is **pi.dev / earendil-works**, not getpi.ai (NXDOMAIN) **[D]**; exports `PI_SESSION_FILE` **[D]** |
| OMP (Oh My Pi) | coding CLI | no | same parser as Pi **[C]** | `~/.omp/agent/sessions/**/*.jsonl` **[C]** | `responseId` | jsonl-tail | S | — |
| Kimi CLI / Kimi Code | coding CLI | no | `token_usage.*` / `usage.record.usage.*` **[C]** | `wire.jsonl`; hook `UserPromptSubmit` / `TurnStarted` → `prompt` **[D]** | `message_id` / `turnId` **[C]** | hook-push + jsonl-tail | S | no OTel at all; command-exec hooks only |
| Mistral Vibe | coding CLI | no | **traces only**, `otel_endpoint` **[D]**; `meta.json` cumulative **[C]** | `logs/session/<id>/messages.jsonl` **[C]** | `message_id` | jsonl-tail | S | **no prompt-submit hook event** — only `post_agent` + `transcript_path` **[D]** |
| Amp (Sourcegraph) | coding CLI | no | **none** — OTLP url hard-coded to Amp's server **[D]** | `~/.local/share/amp/threads/<id>.json`; plugin `agent.start` has the prompt, `agent.end` the messages **[D]** | thread id | jsonl-tail (JSON) | S | `amp.url` redirects *everything*, not just telemetry |
| Aider | coding CLI | no | `--analytics-log FILE.jsonl`; PostHog host is settable, no OTLP **[D]** | `<git_root>/.aider.chat.history.md` — prompts as `>` blockquotes **[D]** | none | log-tail | M | no hooks, no plugins, no event stream; scripting API unsupported |
| Charm Crush | coding TUI | no | `sessions` token/cost columns **[C]** | `messages.parts` JSON; `crush server` SSE with full parts **[C, D]** | none (session-level) | db-poll | M | one SQLite **per project**; only `PreToolUse` today **[D]** |
| Forge | coding CLI | no | `usage.prompt_tokens` etc. **[C]** | `conversations.context` → `message.text.content` **[C]** | `call_id` or synthetic | db-poll | M | — |
| Hermes Agent | coding CLI | no | session aggregate counters **[C]** | `messages.content` full text **[C]** | none (session-level) | db-poll | M | aggregate-per-session only |
| Mux (coder) | coding CLI | no | `metadata.usage.*` per assistant message **[C]** | `parts[].type:"text"` **[C]** | `message.id` | jsonl-tail | S | — |
| DSH (DeepSeek Harness) | coding CLI | no | `usage.*` per step **[C]** | `data.content[]` text blocks **[C]** | `(turn, step)` | jsonl-tail | M | zstd-compressed frames |
| OpenClaw | coding CLI | no | `message.usage.*` + native `usage.cost.total` **[C]** | `message.content[]` text blocks **[C]** | `entry.id` | jsonl-tail | S | four legacy dir names to scan |
| OpenClaude | coding CLI | no | `message.usage.*` **[C]** | Claude Code's exact JSONL schema **[C]** | `message.id` / `uuid` | jsonl-tail | **S (free)** | **the existing reader parses it** — needs a watcher root only |
| Codebuff | coding CLI | no | `metadata.usage` or credits **[C]** | `msg.content`, untruncated **[C]** | `msg.id` | jsonl-tail (JSON) | S | billed in credits |
| CodeWhale | coding CLI | no | `metadata.total_tokens` aggregate **[C]** | first user `message.content` **[C]** | none | jsonl-tail (JSON) | S | session-level totals only |
| Zerostack | coding CLI | no | `total_input_tokens` / `total_output_tokens` **[C]** | `messages[]` `role` + `content` **[C]** | none | jsonl-tail (JSON) | S | no per-turn accounting at all |
| Open Design | coding CLI | no | `data.usage.*` on usage events **[C]** | **no** — only start/status/usage events read **[C]** | none | log-tail | M | text in other event kinds **[U]** |
| ZCode (z.ai) | coding CLI | no | `model_usage` table **[C]** | **no text column in the schema** **[C]** | `turn_id` **[C]** | db-poll | M | telemetry-only by construction |
| LingTai TUI | coding CLI | no | token ledger JSONL **[C]** | **no** — the doc says the prompt is not included **[C]** | none | log-tail | S | telemetry-only by construction |
| Devin CLI | coding agent | no | `metrics.prompt_tokens`, ACU cost **[C]** | hook `UserPromptSubmit` → `prompt`; `~/.local/share/devin/` session SQLite **[D]** | `step_id` **[C]** | hook-push + db-poll | S | no response event; ACU→USD rate must be configured **[C]**; ⚠️ **reads `.claude/settings.json`** **[D]** |
| Kiro (AWS) | coding IDE+CLI | no | OTLP is a **server push to a public collector, 02:00 UTC daily** **[D]**; Prompt Logging → your own S3 **[D]** | hook `Prompt Submit`, prompt in a **`USER_PROMPT` env var** **[D]**; four on-disk generations **[C]** | `executionId` / `turnIndex` **[C]** | hook-push + jsonl-tail + db-poll | S (hook) / M (files) | hooks live in `.kiro/hooks/` at the **project root** — no machine-wide install **[D]** |
| Quick Desktop (Amazon) | coding agent | no | EMF metrics JSONL: `Model`, tokens, `CostUSD` **[C]** | `sessions.db` `session_messages.content` **[C]** | none per message | jsonl-tail + db-poll | M | reverse-engineered schema, no vendor contract |
| Vercel AI Gateway | gateway | no | remote report API, daily aggregates **[C]** | **none, by construction** **[C]** | none | api-only | X (for text) | no local file, no per-turn row |
| OpenHands (ex-OpenDevin) | coding agent | no | **[U]** | **[U]** — self-hosted, a local event stream is plausible | **[U]** | **[U]** | **[U]** | not in codeburn; read `OpenHands/software-agent-sdk` or run it once |

### IDE-integrated agents

| Tool | Category | Installed here? | Spend telemetry (how) | Text on device (how) | Turn id | Storage class | Effort | Blocker |
|---|---|---|---|---|---|---|---|---|
| Cursor (IDE) | IDE agent | yes **[M]** | `state.vscdb` `tokenCount` **[M]**; OTLP is **server-side, public endpoint** **[D]** | hook `beforeSubmitPrompt` → `prompt`, `afterAgentResponse` → `text` **[D]**; also `bubbleId:*` `text` — 12 of 27 non-empty here **[M]** | `conversation_id` + `generation_id` **[D]** | hook-push + db-poll | **XS** (text) / M (spend) | v3 rows report zero tokens; codeburn char-estimates **[C]** |
| cursor-agent CLI | background agent | yes, 1 transcript **[M]** | **none in the file** — codeburn char-estimates everything **[C]** | `agent-transcripts/<id>/<id>.jsonl`, `{role, message:{content}}` **[M]**; `-p --output-format stream-json` **[D]** | **none, and no timestamps either** **[M]** | jsonl-tail | S | hooks are **partial** — no `beforeSubmitPrompt`/`afterAgentResponse` as of 2026-06-07 **[D]**; `~/.cursor/chats/**/store.db` blobs carry a `blobEncryptionKey` **[D]** |
| Windsurf / Cascade | IDE agent | no | **none user-pointable** — Analytics API is aggregates **[D]** | hook `pre_user_prompt` + `post_cascade_response`; `~/.windsurf/transcripts/<trajectory_id>.jsonl` (0600, **100-file cap**) **[D]** | `trajectory_id` + `execution_id` **[D]** | hook-push + jsonl-tail | S | spend has no local source **[U]**; the 100-file cap silently drops history |
| Antigravity (IDE) | IDE agent | yes, 1 `.pb` **[M]** | live language-server RPC; status-line hook fallback **[C]** | **no** — `.pb` is 7.999 bits/byte, AES-128-CTR, Keychain key **[M, D]** | `cascadeId` + `responseId` **[C]** | rpc | X (disk) / M (RPC) | hook payload is coordinates only, pointing at the encrypted file **[D]** |
| Antigravity CLI | coding CLI | no | **[U]** | `~/.gemini/antigravity-cli/history.jsonl` + `brain/<uuid>/.../transcript_full.jsonl` — **readable** **[D]** | `conversationId` **[D]** | jsonl-tail | S | standard `OTEL_*` vars do nothing **[D]** |
| Cline (VS Code) | IDE agent | no | `CLINE_OTEL_EXPORTER_OTLP_ENDPOINT`, three signals **[D]** | hook `UserPromptSubmitData {prompt, attachments}`; local WebSocket hub; task tree JSON **[D, C]** | index into `api_req_started` **[C]** | hook-push + db-poll | **XS** | env config sets `bypassUserSettings: true` **[D]** |
| Roo Code | IDE agent | no | **none** — "Remove all telemetry", 2026-05-11 **[D]** | `ROO_CODE_IPC_SOCKET_PATH` broadcasts every event incl. full `message` **[D]**; task tree JSON **[C]** | `<taskId>:<index>` **[C]** | rpc + db-poll | M | ⚠️ **repo archived 2026-05-15**; storage path is relocatable by a setting **[D]** |
| Kilo Code | IDE agent | no | standard `OTEL_*`, `experimental.openTelemetry` defaults **true** **[D]** | SQLite `~/.local/share/kilo/kilo.db` `part.data.text`; opencode-style plugins; `/global/event` SSE **[D]** | `session.id` + `message.id` **[D]** | db-poll | M | ⚠️ **v7 is a rewrite on opencode**, no longer a Roo fork **[D]** |
| IBM Bob | IDE agent | no | same Cline shape **[C]** | `api_conversation_history.json` **[C]** | `taskId` + index **[C]** | db-poll (JSON tree) | S (free with Cline) | separate `~/.bob` store unparsed **[C]** |
| Copilot in VS Code | IDE agent | yes, 32 empty sessions **[M]** | `github.copilot.chat.otel.otlpEndpoint`, default `:4318` **[D]** | hooks (Preview) at `.github/hooks/*.json`, `~/.copilot/hooks`, **`.claude/settings.json`**; `chatSessions/<id>.json` + `.jsonl` since 1.109 **[D, M]** | `requestId` **[C]** | hook-push + jsonl-tail | **XS** | no documented response-carrying hook event **[D]** |
| Copilot in JetBrains | IDE agent | no | **none** — no token fields at all **[C]** | Nitrite `.db`: full prompt + reply **[C]** | `<conversationId>:<turnIndex>` **[C]** | db-poll | M | Nitrite / H2 MVStore, not SQLite |
| JetBrains AI Assistant | IDE agent | Toolbox installed **[M]** | vendor-only; IDE platform OTLP exists (`idea.diagnostic.opentelemetry.otlp`, default `127.0.0.1:4318`) but AI spans are **[U]** | **plaintext XML** — `workspace/<hash>.xml`, `ChatSessionStateTemp`, `displayContent` **[D]** | **[U]** | db-poll (XML) | M | agent sessions are base64-JSON under `aia-task-history/` **[D]** |
| Junie (JetBrains) | IDE agent | no | **[U]** | hook `UserPromptSubmit` → `prompt`, `Stop` → `last_assistant_message`; `~/.junie/events.jsonl` + `transcript.md` **[D]** | **[U]** | hook-push + jsonl-tail | S | no OTEL var **[D]** |
| Zed | editor agent | no | `request_token_usage` / `cumulative_token_usage` **[C]** | thread JSON inside a **zstd blob** **[C]**; ACP `session/prompt` + `session/update` **[D]** | per-request key in the usage map **[C]** | db-poll | M | zstd, no per-request timestamps; extensions cannot observe the agent **[D]** |
| Warp | terminal agent | no | `inputTokens` estimated, `outputTokens` hardcoded 0 **[C]**; Analytics API **explicitly excludes raw text** **[D]** | `ai_queries.input[0].Query.text` (user only) **[C]** | `exchange_id` **[C]** | db-poll | M | no OTel, no hooks, no plugins; MCP only and Warp is the client **[D]** |
| Continue.dev | IDE extension | no | CLI: `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, **metrics only** **[D]** | `data:` in `config.yaml` POSTs `chatInteraction` with `prompt` + `completion` to **any http(s) or `file://`** **[D]**; `~/.continue/sessions/<id>.json` | session id | hook-push + jsonl-tail | **XS** (via `data:`) | ⚠️ its Claude-shaped hook system is **landed but inert** — zero call sites in 1.5.47 **[D]** |
| Devin (cloud) | cloud agent | no | `/v3/enterprise/sessions/{id}/messages` returns message text **[D]** | none for cloud runs | — | api-only | L | per-org API, not device capture |

### Non-coding and knowledge work

| Tool | Category | Installed here? | Spend telemetry (how) | Text on device (how) | Turn id | Storage class | Effort | Blocker |
|---|---|---|---|---|---|---|---|---|
| Claude Desktop (chat) | assistant | yes **[M]** | Enterprise Analytics API, `products[]=chat` **[D]** | **no** — `claude.ai` IndexedDB is 48 KB here **[M]** | — | cloud-only | X | Compliance API is the only text route **[D]** |
| Claude Code launched from Desktop | coding | yes **[M]** | as Claude Code **[D]** | writes Claude Code's own `~/.claude/projects` JSONL **[D]** | `promptId` | jsonl-tail | shipped | — |
| Cowork | agentic desktop | yes, VM-backed **[M]** | OTLP runs *inside* the VM **[D]** | none — 0 `.jsonl` here; host keeps a metadata index only **[M]** | — | rpc | X | no host-readable path; cloud sessions store nothing locally |
| Claude in Slack (Claude Tag) | assistant | no | Enterprise Analytics API, `products[]=claude-tag`, per-channel spend **[D]** | **none** — two server-side copies **[D]** | — | cloud-only | X | beta gap: transcripts excluded from exports *and* the Compliance API **[D]** |
| ChatGPT Desktop (merged with Codex) | assistant + coding | legacy app **[M]** | Codex half: `[otel]` → loopback **[D]**; seats: Workspace Analytics **[D, U]** | legacy `.data` files **encrypted — 7.964 bits/byte** **[M]**; Codex half plaintext in `~/.codex` **[D]** | Codex `session_id`; chat **[U]** | cloud-only (chat) + jsonl-tail (Codex) | M | whether the merged app honours `[otel]` and writes `~/.codex` **[U]** |
| ChatGPT Atlas | browser | no | — | — | — | — | X | **discontinued 2026-08-09** **[D]** — do not build |
| Microsoft 365 Copilot | knowledge work | no | **no tokens or dollars anywhere** — per-seat. Prompt *counts* via the Graph v2 report **[D]** | **no local store found**; Graph returns full text from the cloud **[D]** | `requestId` joins prompt to response **[D]** | api-only | L | app-only `AiEnterpriseInteraction.Read.All`, tenant consent, 30-day retention |
| Gemini in Workspace | knowledge work | no | **nothing** — no tokens, no cost, no model **[D]** | **none** — web UI inside Google's own origins **[D]** | — | api-only | L | Reports API emits one categorical event; Takeout and Vault are the only text routes |
| Gemini app / desktop | assistant | no | none **[D]** | browser IndexedDB is V8-serialized, not plaintext **[D]** | — | cloud-only | X | desktop app paths **[U]** |
| Notion AI | knowledge work | app installed **[M]** | AI *actions* in a CSV, no API **[D]** | `notion.db` has `thread` / `thread_message`, **0 rows here** **[M]** | **[U]** | cloud-only | L | whether AI threads persist locally **[U]** |
| Perplexity Comet | browser | no | Enterprise Insights: *queries*, no API **[D]** | Sidecar is a web app under `perplexity.ai/sidecar`; threads live in the account **[D]** | — | cloud-only | L | residue in Comet's Chromium profile **[U]** |
| Edge Copilot sidebar | browser assistant | no | none **[D]** | LevelDB under the Edge profile, unencrypted but V8-serialized **[D]** | **[U]** | db-poll | L | needs a browser extension or forensic LevelDB parsing |
| Third-party sidebars (Sider, Monica, Merlin) | browser assistant | Chrome has 14 extensions **[M]** | none **[D]** | `Local Extension Settings/<ID>/` LevelDB, unencrypted **[D]** | none | db-poll | L | per-vendor undocumented schema; one reader each |
| Grok / grok.com / Grok in X | assistant | `~/.grok/hooks` only **[M]** | xAI Management API covers **API** usage, not the consumer app **[D]** | cloud-only; user-driven JSON export **[D]** | — | cloud-only | X | — |
| Grok Bot (xAI + Cursor) | agent | no | Cursor's OTel exporter, **server-side, public endpoint** **[D]** | **[U]** — may inherit Cursor hooks | **[U]** | rpc / cloud-only | M | drop a `hooks.json` and see whether it fires |

---

## What would settle the unverified items

| Item | How to settle it |
|---|---|
| Warp's schema holds full text | `sqlite3 warp.sqlite ".schema agent_conversations"` on a live install. |
| Cline 4.1.x store location | `ls -R ~/.cline/data` after one VS Code task. |
| Droid's session path | `find ~/.factory -name '*.jsonl'` after one session. |
| Windsurf transcripts without hooks | Does `~/.windsurf/transcripts/` appear with no `hooks.json` present? |
| JetBrains AI spans on the platform OTLP | Set `idea.diagnostic.opentelemetry.otlp=true` and watch `127.0.0.1:4318`. |
| cursor-agent OTel | `strings $(which cursor-agent) \| grep -i otel`. |
| Merged ChatGPT desktop honours `[otel]` | Set `endpoint = "http://127.0.0.1:4318"` in `~/.codex/config.toml`, run a turn in the app, listen. |
| Grok Bot inherits Cursor hooks | Drop `hooks.json` at user and project level, run one task, see whether the script spawns. |
| Notion AI threads persist locally | Send a nonce prompt, `sqlite3 .tables` every `*.db` under `~/Library/Application Support/Notion`, grep `Local Storage/leveldb`. |
| Perplexity Comet residue | Send a nonce, `grep -r` it across Comet's `Local Storage/leveldb`, `IndexedDB`, `Service Worker`. |
| OpenHands storage | Read `OpenHands/software-agent-sdk`'s event-stream persistence, or run it and watch the workspace dir. |
| Amp's OTLP override | Confirm `amp.url` is the only lever and that it redirects API traffic too. |
| Open Design / ZCode text | Read an unparsed event kind and `~/.zcode/cli/log/*.jsonl` directly. |

## Sources

- Cursor hooks — https://cursor.com/docs/agent/hooks
- Cursor OpenTelemetry export — https://cursor.com/docs/enterprise/opentelemetry-export
- cursor-agent hook gap — https://forum.cursor.com/t/cursor-cli-doesnt-send-all-events-defined-in-hooks/148316
- Windsurf / Cascade hooks — https://docs.devin.ai/desktop/cascade/hooks
- Kiro hooks — https://kiro.dev/docs/hooks/types/
- Kiro OpenTelemetry — https://kiro.dev/docs/enterprise/monitor-and-track/user-activity/opentelemetry/
- GitHub Copilot CLI OpenTelemetry — https://docs.github.com/en/copilot/concepts/agents/opentelemetry
- GitHub Copilot CLI hooks — https://docs.github.com/en/copilot/reference/hooks-reference
- Copilot CLI session store — https://docs.github.com/en/copilot/concepts/agents/copilot-cli/chronicle
- Copilot in VS Code monitoring — https://code.visualstudio.com/docs/agents/guides/monitoring-agents
- Copilot in VS Code hooks — https://code.visualstudio.com/docs/agent-customization/hooks
- Goose OTLP — https://github.com/block/goose/blob/main/crates/goose/src/otel/otlp.rs
- Goose hooks — https://github.com/block/goose/blob/main/documentation/docs/guides/context-engineering/hooks.md
- OpenCode OTLP — https://github.com/sst/opencode/blob/dev/packages/core/src/observability/otlp.ts
- OpenCode plugins — https://opencode.ai/docs/plugins/
- Cline OpenTelemetry override — https://github.com/cline/cline/blob/main/docs/enterprise-solutions/monitoring/opentelemetry_override.mdx
- Cline hooks proto — https://github.com/cline/cline/blob/main/apps/vscode/proto/cline/hooks.proto
- Roo Code IPC — https://github.com/RooCodeInc/Roo-Code/blob/main/src/extension/api.ts
- Kilo Code settings / session history — https://github.com/Kilo-Org/kilocode/blob/main/packages/kilo-docs/pages/getting-started/settings/index.md
- Gemini CLI telemetry — https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/telemetry.md
- Qwen Code telemetry — https://github.com/QwenLM/qwen-code/blob/main/docs/developers/development/telemetry.md
- Factory Droid telemetry — https://docs.factory.ai/enterprise/telemetry/index.md
- Factory Droid hooks — https://docs.factory.ai/harness/hooks.md
- Continue development data — https://docs.continue.dev/customize/deep-dives/development-data
- Amp plugin API — https://ampcode.com/manual/plugin-api
- Amp settings — https://ampcode.com/docs/cli/settings
- Aider analytics — https://aider.chat/docs/more/analytics.html
- Antigravity hooks — https://antigravity.google/docs/hooks/
- Antigravity OTLP gap — https://github.com/google-antigravity/antigravity-cli/issues/366
- Junie CLI hooks — https://junie.jetbrains.com/docs/junie-cli-hooks.html
- JetBrains platform OpenTelemetry — https://www.jetbrains.com/help/idea/open-telemetry-tracing-and-metrics.html
- Zed telemetry / thread DB — https://github.com/zed-industries/zed/blob/main/crates/agent/src/db.rs
- Warp Analytics API — https://docs.warp.dev/enterprise/enterprise-features/analytics-api/
- Crush OTLP request — https://github.com/charmbracelet/crush/issues/1625
- Pi — https://pi.dev · https://github.com/earendil-works/pi
- Cowork OpenTelemetry — https://support.claude.com/en/articles/14477985-monitor-claude-cowork-activity-with-opentelemetry
- Anthropic Compliance API (local sessions) — https://platform.claude.com/docs/en/manage-claude/compliance-sessions
- Anthropic Usage & Cost API — https://platform.claude.com/docs/en/manage-claude/usage-cost-api
- Claude Enterprise Analytics API — https://platform.claude.com/docs/en/manage-claude/analytics-api
- Claude Tag data lifecycle — https://claude.com/docs/claude-tag/concepts/data-lifecycle
- Codex config (`[otel]`) — https://learn.chatgpt.com/docs/config-file/config-advanced
- ChatGPT macOS plaintext fix (2024) — https://9to5mac.com/2024/07/03/chatgpt-macos-conversations-plain-text/
- Atlas shutdown — https://techcrunch.com/2026/07/09/openai-is-shutting-down-atlas-but-its-ai-browser-ambitions-are-still-growing/
- Graph `getAllEnterpriseInteractions` — https://learn.microsoft.com/en-us/microsoft-365/copilot/extensibility/api/ai-services/interaction-export/aiinteractionhistory-getallenterpriseinteractions
- M365 Copilot usage report — https://learn.microsoft.com/en-us/microsoft-365/copilot/extensibility/api/admin-settings/reports/copilotreportroot-getmicrosoft365copilotusageuserdetail
- Purview `CopilotInteraction` schema — https://learn.microsoft.com/en-us/office/office-365-management-api/copilot-schema
- Gemini in Workspace activity events — https://developers.google.com/workspace/admin/reports/v1/appendix/activity/gemini-in-workspace-apps
- Notion workspace analytics — https://www.notion.com/help/workspace-analytics
- Perplexity enterprise analytics — https://www.perplexity.ai/help-center/en/articles/11844346-enterprise-usage-analytics
- xAI Management API billing — https://docs.x.ai/docs/management-api/billing
- Browser AI forensics (LevelDB) — https://andreafortuna.org/2026/07/28/browser-ai-forensics/

Local-file facts not otherwise cited come from `../codeburn/docs/providers/*.md`
and their parsers.
