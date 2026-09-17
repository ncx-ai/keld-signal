# Telemetry loopback proxy, capture triggers and watched-source telemetry

> **Provenance — split out of `AGENTS.md` on 2026-09-17** (at `67be5f5`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-08-27**.
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *Architecture* (the telemetry lane) and *Capture triggers* carry the
rules that matter before you edit. This file carries why each one exists and what it cost to learn.

  ⚠️ **This bullet used to read "the hook posts usage telemetry straight to
  Atlas. No daemon involvement", and the change is the whole point rather than a
  refactor.** A tool reads its configuration ONCE, at startup, and keeps the
  credential in memory — so when the org's ingest token rotated, every running
  tool went on posting the old one and its telemetry was rejected until a human
  restarted the editor. Measured on a real machine: `tool_events` froze for 40
  minutes while `keld signal doctor` reported no problems, **correctly** — every
  fact it can reach was right, and the stale copy lived inside a process it
  cannot inspect. The hook cannot detect it either: a Claude Code child process
  sees **no `OTEL_*` variables at all**, because Claude Code applies its `env`
  block to its own OTEL SDK and exports nothing. So detection was impossible and
  remediation could only ever be "ask the human to restart"; the fix is to stop
  handing the tool a credential. `keld signal setup` writes the loopback address
  and a **stable local secret** (`agentcfg.TelemetrySecret`, generated once and
  never rotated — unlike `Info.Secret`, regenerated every daemon start, which
  would rebuild the bug one layer down and fire it daily). The token the daemon
  attaches is read **per request**, so a rotation mid-flight is picked up.
  ⚠️ **The proxy accepts that secret in THREE shapes, because the tools do not
  agree on one**: `x-keld-ingest-token` (Claude Code, Codex), `?token=` in the
  URL (Gemini — its OTLP SDK cannot send a custom header at all), and
  `x-keld-telemetry-secret`. Assuming a single shape 401'd all three tools live
  while the entire Go suite passed, because the tests used the name the proxy
  chose rather than the ones `telemetry.ClaudeEnv`/`CodexBlockBody`/
  `GeminiTelemetry` emit. Widening where the credential may appear does not
  widen what is accepted.
  ⚠️ **THE TEXT GATE OVER-MATCHED `prompt.id` AND SILENTLY BROKE EVERY
  CORRELATION.** `teleproxy.textKey` matched an attribute key by SHAPE —
  `strings.Contains(k, "prompt")` and seven siblings — and its comment said "a
  key it over-matches costs one dropped attribute". That was wrong about the
  most important attribute on the wire: Claude Code sends `prompt.id` on every
  record, and Atlas joins `Enrichment.corr_id` to `ToolEvent.prompt_id`. So the
  proxy blanked the one field relating a block to the telemetry it describes,
  and the failure was invisible in the way a dropped attribute is not — rows
  arrived, attributed, counted, and joined to nothing. Measured on the dev
  Atlas: of one proxied session's 1,107 events, **0** had a non-empty
  `prompt_id`, while unproxied seed rows kept theirs; after the fix, 10 of 10.
  Reported as "blocks show up but the activity panel is empty", which is that
  join returning nothing. The rule is now two-sided — match a text word, then
  subtract the identifier/measurement SUFFIXES (`.id`, `_id`, `_length`,
  `_tokens`, …) — and both halves are load-bearing: drop the first and text
  leaks, drop the second and correlation dies. An unanticipated shape still
  fails CLOSED, toward privacy. Pinned by `striptext_identity_test.go` against a
  **real captured Claude Code payload**, because the original gate shipped
  green: `prompt.id` was in no fixture, so nothing could see the cost.
  ⚠️ **`keld signal setup` now SAYS to restart the tools, and never used to.**
  The reasoning was written in that package twice and printed zero times. A tool
  reads its telemetry config once at startup, so one already running keeps
  posting wherever it was pointed when it launched; nothing on the machine can
  detect or fix that from outside. Measured: a session started before setup ran
  emitted **0** telemetry events over 11 hours while its blocks published
  normally — blocks are read from the transcript by the daemon and never depend
  on the tool's config, so the visible half of the product stayed healthy and
  hid the silent half. The `done` event carries `restart_required` so an
  installer's UI can say it too.
  ⚠️ **And doctor now asks PER SESSION, because the machine-wide check cannot.**
  `localagent.TelemetryState` asks whether telemetry has arrived AT ALL since the
  credential was written, so on a machine running two editors — one started
  before setup, one after — the second vouches for the first and doctor reports
  "No problems found", correctly and uselessly. `SessionTelemetryState` compares
  the sessions whose transcripts are being written NOW against
  `teleproxy.SessionsOnDisk()`, a bounded (64, oldest-evicted) record of which
  tool session ids the proxy has forwarded for. Session ids are IDENTIFIERS —
  the same class already published as `corr_id`; no text, span or offset is read.
  Three refusals keep it from lying: an **empty record is "not tracked yet"**,
  never "nothing is arriving" (on upgrade the state file has a `last_forward` and
  no sessions, and reporting then would call every running tool broken the day it
  shipped); `agent-*.jsonl` **subagent transcripts are excluded** (they share
  their parent's OTEL session id and are **620 of 671** files here, so including
  them means hundreds of false findings); and the session's start instant is read
  by DECODING lines for a top-level `timestamp`, never by pattern-matching the
  first one — Claude Code opens a transcript with untimestamped `custom-title` /
  `mode` / `file-history-snapshot` records, the same trap `capture.scan`
  documents. Scoped to `claude_code`: Cowork's egress is blocked by design and
  Codex/Gemini transcript names are not their OTLP session ids. The record is
  LOADED at proxy construction, not started empty — otherwise a daemon restart
  makes every session look untracked, and the first forward then writes that
  empty map back, erasing the history rather than merely not reading it.
  ⚠️ **And `teleproxy`'s tests now isolate `KELD_HOME` in a `TestMain`, because
  they were writing the developer's real `~/.keld`.** `New()` resolves
  `StatePath()` at construction and every successful forward persists, while most
  tests there pass `t.TempDir()` only for the SPOOL — so the spool was isolated
  and the state file was not. Running `go test ./...` on a live machine
  overwrote its telemetry record, erased the per-session history, and silently
  turned this very check inconclusive. A test that mutates the machine it runs on
  is a worse defect than the one it checks for.
  ⚠️ **Telemetry now depends on the daemon**, where it did not before. Paid for
  with a bounded spool under `spool/telemetry` and not hoped away; a machine
  whose daemon never starts collects nothing, and `keld signal doctor` is the
  detector — which can only exist AFTER this path, since pre-proxy the client
  kept no record of tool telemetry at all. Delivery is confirmed from the
  RESPONSE, not the status code: captive portals answer **200 with an HTML login
  page**, and a status-only check would delete the batch. A drain **stops on a
  REJECTION** (401/403 — every remaining batch would be told the same thing),
  **ends the sweep on UNAVAILABLE** (net/5xx — never a re-onboard, nothing is
  wrong with the credential), and **continues past a REFUSED payload** (4xx —
  or one bad batch blocks every good one behind it). See
  `docs/superpowers/specs/2026-08-27-telemetry-loopback-proxy-design.md`.

**Cowork went VM-backed, and host-side capture cannot follow it.** Newer Claude
desktop builds run Cowork inside a VM (`vm_bundles/claudevm.bundle`) whose
transcripts live in the VM's disk image, not under `local-agent-mode-sessions`.
Nothing on the host can read them: no folder is shared and the VM's address does
not answer the host. Discovery therefore finds no *live* Cowork transcripts on
these machines — and because the pre-VM session directories are never cleaned up,
a root is still discovered, just permanently stale. `coworkHidden`
(`internal/agent/watch/roots.go`) detects that exact shape — VM images touched
within `coworkActiveWindow` while no Cowork `.jsonl` was written in the same
window — and logs one advisory line per daemon run, because the failure is
otherwise completely silent: Cowork just stops appearing in Atlas. It keys on
transcript **freshness**, not root existence, precisely so the stale-directory
machines are caught. Restoring capture needs a path the host can read (or an
in-VM emitter); `KELD_WATCH_ROOTS=cowork:<dir>` points the watcher at one the day
it exists.

**Watched-source telemetry (`internal/agent/promptlog`).** Cowork's own OTEL is
configured to Keld but its sandbox egress blocks `atlas.keld.co`, so the daemon
mirrors the transcript's events into OTLP logs+metrics host-side: the watcher's
per-line `observe` hook → `promptlog.Telemetry.Observe`, which emits `user_prompt`
/ `api_request` / `assistant_response` logs + `token.usage`/`cost.usage` metrics to
`/v1/logs` + `/v1/metrics`, matching the CLI's native OTEL schema. Identity
(`user.email`/`account_uuid`/`organization.id`) is recovered from the Cowork
session path/metadata. **Never emits prompt/response text.** Default source
`{cowork}` (Claude Code emits its own OTEL); `KELD_WATCH_TELEMETRY` (off/on),
`KELD_WATCH_TELEMETRY_SOURCES`. **Codex** and **Gemini** are covered via their own watcher roots
(`~/.codex/sessions` and `~/.gemini/tmp/*/chats`, sources codex and gemini) + specialized readers for enrichment
(TranscriptReader resolves user_message by session_id#ordinal for Codex, by message `id` for Gemini);
telemetry via their native OTEL (config completed in the tool adapters), not host-side promptlog.

