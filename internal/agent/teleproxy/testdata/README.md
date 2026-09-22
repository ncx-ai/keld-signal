# Captured OTLP payloads

`otlp-<tool>-{logs,metrics}.json` are **real** payloads, captured from the tools
themselves on 2026-09-15, not hand-written. That matters because the only thing
they are used for is deciding which tool a forward came from, and a fixture
written from documentation would agree with a mapping written from the same
documentation while both disagreed with production.

`claude_code_logs.json` predates these; it belongs to the `striptext` identity
test and is kept as a second Claude Code reading from a different machine.

## What each tool sends

| Tool | version | `service.name` | source id |
|---|---|---|---|
| Claude Code | 2.1.271 | `claude-code` | `claude_code` |
| Codex (`codex exec`) | 0.153.4 | `codex_exec` | `codex` |
| Gemini CLI | v25.9.0 (node) | `gemini-cli` | `gemini_cli` |

Two spellings — hyphen for the JS/TS tools, underscore for the Rust one — and
Codex names the resource after its **entrypoint**, so the mapping treats
`codex*` as one source rather than listing every entrypoint.

**Cowork is absent and always will be**: its sandbox blocks egress, so no Cowork
OTLP ever reaches this proxy. `internal/agent/promptlog` mirrors its transcript
host-side and posts to Atlas directly.

## Capture recipe

Run a loopback listener that writes each POST body to a file (handle
`Transfer-Encoding: chunked` — Gemini's exporter uses it and a
`Content-Length`-only reader records zero-byte files), then point one tool at it
and run a single prompt:

```bash
# Claude Code
CLAUDE_CODE_ENABLE_TELEMETRY=1 OTEL_LOGS_EXPORTER=otlp OTEL_METRICS_EXPORTER=otlp \
OTEL_EXPORTER_OTLP_PROTOCOL=http/json OTEL_EXPORTER_OTLP_ENDPOINT=http://127.0.0.1:14999 \
OTEL_METRIC_EXPORT_INTERVAL=3000 OTEL_LOGS_EXPORT_INTERVAL=3000 \
  claude -p "reply with the single word OK"

# Codex — an isolated CODEX_HOME holding auth.json plus the [otel] table
# internal/telemetry.CodexBlockBody writes
CODEX_HOME=/tmp/codexhome codex exec --skip-git-repo-check "reply with the single word OK"

# Gemini CLI — an isolated HOME holding .gemini/settings.json with the
# telemetry block internal/telemetry.GeminiTelemetry writes
HOME=/tmp/gemhome gemini -p "reply with the single word OK"
```

## Redaction

Committed files go through `scripts/redact-otlp.py`, which rewrites emails,
home directories and uuids by shape and blanks the argv/host/owner attributes
whole, then trims to one resource, one scope and three records.

⚠️ **The argv attributes are blanked because Gemini CLI puts the PROMPT in
them.** A `gemini -p "<prompt>"` run sets the OTLP resource attribute
`process.command_args` to the full argv, prompt included, and
`teleproxy.StripText`'s key gate does not match that key — so on a real machine
that prompt is forwarded to Atlas verbatim. That is a defect in the text gate,
not in these fixtures; it is reported rather than fixed here because
`proxy.go` belongs to another workstream.
