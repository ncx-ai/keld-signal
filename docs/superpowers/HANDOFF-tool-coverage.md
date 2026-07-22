# HANDOFF — AI-tool coverage in keld-signal (next candidate: Antigravity)

**Purpose.** You (a fresh-context instance) are extending keld-signal's per-tool
coverage. Covered to functional parity: Claude Code (all surfaces), Cowork, Codex,
and **Gemini CLI** (shipped v0.11.0). **Next candidate: Antigravity** — Google's
Gemini-based VS Code–fork IDE. It is NOT covered by the Gemini CLI work: it's a
separate app that doesn't use the gemini-cli, `~/.gemini/tmp/chats`, or gemini-cli
settings. Investigate empirically first (does it persist transcripts on disk, and
where; does it emit OTEL) — don't assume. Other Gemini surfaces (Vertex, Code
Assist, gemini.google.com) remain likely-out-of-scope (server-side); see §4.

**How to use this doc.** Read it, then follow the playbook (below). Ground every
claim in real on-disk data or upstream source — never guess a schema. Use the
superpowers flow: brainstorming → spec → writing-plans (TDD) → subagent-driven
execution → final review → finishing-a-development-branch → release.

Date written: 2026-07-22 (updated after Gemini CLI shipped + v0.11.1 auth fix). Repo: `/Users/keldtester1/keld-signal`. Latest tag: **v0.11.1**.
Run go with `export PATH="/opt/homebrew/bin:$PATH"`. `gh` is at `/opt/homebrew/bin/gh` (authed).

---

## 1. The architecture (how capture → enrichment/telemetry works)

Two independent data streams reach Atlas, both derived on-device:

- **Enrichment** — a prompt is classified locally by GLiNER2 into a masked
  `Profile` (task_type, sensitivity, domain, …); only the derived profile is
  published. **Raw prompt text never leaves the machine.** Pipeline:
  `capture → queue → resolve (read text locally) → enrich → mask → publish → Atlas /v1/enrichments`.
- **Telemetry** — OTEL logs/metrics (usage, tokens, model, cost) → Atlas
  `/v1/logs` + `/v1/metrics`.

**Two capture triggers** feed enrichment:
1. **Command hook** — `keld __hook --source <tool>` wired by `keld setup` into the
   tool's config; fires on the tool's prompt-submit event, POSTs a *pointer*
   (transcript path + prompt id) to the daemon.
2. **Transcript watcher** (`internal/agent/watch/`) — the **hook-free** path: a
   daemon poll loop tails the tool's on-disk JSONL transcripts, and for each new
   genuine user-prompt line synthesizes the *same pointer* into the *same* queue.
   This exists because some surfaces don't fire hooks (Cowork sandbox; Claude Code
   in the Desktop app). **Pointer model only — never spool text.**

**Telemetry** is either the tool's **native OTEL** (if it runs host-side and can
reach Keld) or **host-side reconstruction** by the daemon (`internal/agent/promptlog`)
when the tool's own OTEL can't reach Keld (Cowork's sandbox blocks egress).

### Key files
- `internal/tools/{registry,adapter,claude,codex,gemini}.go` — install adapters; `keld setup` wires each tool's config (hooks + OTEL).
- `internal/telemetry/telemetry.go` — the OTEL/hook snippet builders per tool (`ClaudeEnv`, `CodexBlockBody`, `GeminiTelemetry`, `HookCommand`, `ClaudeHookEvents`).
- `internal/hook/{hook,forward}.go` — the `keld __hook` runner (parses stdin, forwards pointer + posts a context event).
- `internal/agent/watch/{watch,roots,filter,cursor,codex}.go` — the watcher: `discoverRoots` (per-OS roots), a source-aware `promptExtractor` (`claudeExtractor` = stateless `parsePrompt`; `codexExtractor` = stateful rollout parser), per-file byte cursors, per-line `observe` hook (telemetry).
- `internal/agent/resolve/{resolve,claude,codex,recent}.go` — `TranscriptReader`s keyed by source; `Resolve(source, path, promptID, inline)` → text. Register new readers in `resolve.go init()`.
- `internal/agent/promptlog/{promptlog,otlp,identity}.go` — host-side OTLP emitter (Cowork). `SourcesFromEnv()` default `{cowork}`. Fidelity test asserts emitted schema == captured CLI oracle.
- `internal/agent/daemon/daemon.go` — wires it all; `Run` starts Worker + spool drain + watcher; endpoint helpers `enrichEndpoint`/`logsEndpoint`/`metricsEndpoint`; `Worker` calls `queue.Complete` on real publish (dedup).
- `internal/agent/enrich/context.go` (`interactiveCodingTools`) + `a4_compositional.go` (`codingTools`) — classification flags per source.

---

## 2. Coverage matrix (as of v0.11.1)

| Capability | Claude Code | Cowork | Codex | **Gemini CLI** |
|---|---|---|---|---|
| Install adapter + detect | ✅ | (via Claude app) | ✅ | ✅ (`~/.gemini`) |
| `keld setup` writes config | ✅ hooks+OTEL env | n/a | ✅ `[otel]`+hooks | ✅ telemetry block (token in `otlpEndpoint`) + hook |
| Command hook | ✅ UserPromptSubmit | ❌ (sandbox) | ⚠️ SessionStart/PreToolUse (no prompt-submit) | ✅ BeforeAgent (context event only) |
| Transcript watcher root | ✅ `~/.claude/projects` | ✅ cowork dirs | ✅ `~/.codex/sessions` | ✅ `~/.gemini/tmp/*/chats` |
| Transcript reader (enrichment) | ✅ | ✅ (reuses Claude) | ✅ rollout | ✅ Gemini reader (`type:"user"` by id) |
| Enrichment classification flags | ✅ eng | topical (excluded) | ✅ eng | ✅ eng |
| Telemetry | native OTEL (host) | **host-side reconstruction** (promptlog) | native OTEL (host) | native OTEL (host), token in `otlpEndpoint` `?token=` query (v0.11.1) |

**Gemini CLI = full parity (v0.11.0; auth fixed in v0.11.1).** `keld setup` writes
`~/.gemini/settings.json` `telemetry: {enabled:true, target:"local", otlpProtocol:"http",
otlpEndpoint:"<BASE>?token=<tok>", logPrompts:false, traces:false}`
(`telemetry.GeminiTelemetry`) plus a `hooks.BeforeAgent` context hook. Enrichment: watcher
root + `resolve.NewGeminiReader` (pointer model). **Two hard-won gemini gotchas (spec §1.3):**
(1) **Auth can't ride in a header.** gemini reads `~/.gemini/.env`/`OTEL_EXPORTER_OTLP_HEADERS`
only in a *trusted* workspace (untrusted → only `AUTH_ENV_VAR_WHITELIST` vars load), so
v0.11.0's `.env` header 401'd in normal dirs. The token now rides in `otlpEndpoint` as
`?token=` (settings.json is always loaded; the exporter preserves the query when appending
`/v1/logs`). keld no longer writes `.env`; it strips any legacy block on upgrade. Auth is the
token only — **`x-keld-actor` is deprecated, never sent** (dropped across all tools + the
enrichment publisher in v0.11.1). (2) **Traces can't be disabled** — gemini builds its own OTLP
exporters and ignores `OTEL_TRACES_EXPORTER`; content stays out of spans via
`logPrompts:false`+`traces:false`; Atlas ignores `/v1/traces`. Validated on 5/5 real
transcripts and a local OTLP sink (untrusted workspace: `/v1/logs?token=…`, no header).

---

## 3. The playbook (repeat this for Gemini)

This is exactly how Claude Code / Cowork / Codex were done. Follow it.

1. **Empirical investigation FIRST** (the make-or-break step). For each Gemini
   surface, answer with ground truth (real files + upstream source, not docs alone):
   - Does it **write local session transcripts**? Where, what JSONL/other schema,
     which record is a genuine user prompt, session/prompt ids, cwd? (Claude:
     `~/.claude/projects`; Codex: `~/.codex/sessions/**/rollout-*.jsonl`.)
   - Does it support **OTEL/telemetry to a custom endpoint**, and does it run
     **host-side** (native OTEL reaches Keld) or **sandboxed** (egress-blocked →
     needs host-side reconstruction, like Cowork)?
   - What identity does its telemetry carry?
   - Method used this session: `find`/inspect real dirs on this machine; capture a
     real OTEL export to a local sink; read upstream source via `gh api` code
     search + raw file fetch (e.g. openai/codex `protocol.rs`). For Gemini use the
     `google-gemini/gemini-cli` repo similarly.
2. **Brainstorm** the scope with the user (telemetry vs enrichment; native OTEL vs
   reconstruction; which surfaces are in scope). One question at a time.
3. **Spec** → `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`; commit; user review gate.
4. **Plan (TDD)** → `docs/superpowers/plans/YYYY-MM-DD-<topic>.md`; every task RED→GREEN→commit with complete code + a schema oracle where relevant.
5. **Execute** subagent-driven (fresh implementer per task on the cheapest capable model — haiku for transcription, sonnet for integration; sonnet/opus reviewers; opus final whole-branch review). Ledger at `.superpowers/sdd/progress.md`. NOTE: subagents hit `529 Overloaded` intermittently this session — retry, or fall back to inline TDD.
6. **Fidelity test** for any telemetry/enrichment schema: assert emitted keys == captured oracle minus documented omissions. Prevents reactive drift-chasing.
7. **Finish** (finishing-a-development-branch) → merge to `main` → `make release VERSION=X.Y.Z YES=1` (must be on clean `main` synced with origin; push first). CI (`gh run watch`, ~11 min) builds the `.pkg`; install with `sudo installer -pkg <path> -target /` (needs the user — no TTY for sudo here). Restart daemon: `keld-agent restart`.

### Hard-won principles (do NOT relitigate)
- **Functional equivalence = BOTH streams.** "Data arrives like the CLI" means
  telemetry AND enrichment. (User was — rightly — angry when telemetry was skipped.)
- **Mirror the tool's real schema exactly**, driven from a captured oracle; don't
  hand-approximate and patch drift. Enforce with a fidelity test.
- **Native OTEL beats reconstruction** when the tool is host-side (first-hand,
  accurate). Reconstruct only when egress is blocked (Cowork). Don't double-emit.
- **Cost:** don't derive cost on-device from a price table (approximate; ignores
  service tier + 1h/5m cache split). Emit exact tokens; **Atlas computes cost.**
- **Privacy:** pointer model; never write prompt/response text to disk or telemetry.
  Emit lengths, ids, model, tokens only.
- **Identity:** telemetry should attribute to the tool's real account. For Cowork
  it's recovered from the session path/metadata; for native OTEL the tool supplies it.
- **`tool=<source>` resource attribute** so Atlas can distinguish surfaces
  (otherwise everything looks like generic `claude-code` traffic).
- **Follow TDD/superpowers discipline** — this was called out mid-session when I
  drifted into ad-hoc coding. Test-first, always.

---

## 4. Next candidate: Antigravity — where to start

**Gemini CLI is DONE (v0.11.0).** Its spec/plan are the freshest worked example of
this playbook: `docs/superpowers/{specs,plans}/2026-07-22-gemini-cli-coverage*`.
Read those first — they show exactly how a new tool gets wired (adapter, watcher
root, reader/extractor, classification, hook, telemetry builders, fidelity/oracle).

**Antigravity** is Google's agentic IDE — a VS Code fork that uses Gemini models.
It is a **separate application** from gemini-cli: it will NOT write to
`~/.gemini/tmp/chats`, will NOT read gemini-cli's `settings.json`/`.env`, and the
v0.11.0 Gemini coverage does nothing for it. No Antigravity footprint existed on
the dev machine (2026-07-22), so everything below is genuinely open — investigate,
don't assume.

**First investigations (empirical — answer these before designing anything):**
1. Is Antigravity even installed / installable here? Find its on-disk footprint —
   likely under `~/Library/Application Support/<name>/`, `~/.config/`, or a
   `~/.antigravity`-style dir (it's an Electron/VSCode fork, so look for a
   `User/workspaceStorage`, `globalStorage`, or SQLite `state.vscdb` like other
   forks). Ask the user to run it and share the layout if it's not present.
2. Does it persist conversation transcripts locally, and in what format (JSON lines?
   SQLite? leveldb)? Which record is a genuine user prompt (vs tool/agent turns)?
   This decides whether the watcher/reader/extractor pattern even applies.
3. Does it emit OTEL or any telemetry we can point at Keld? Is there a settings
   surface (like gemini-cli's `telemetry` block) or is it fully closed? If closed,
   is there a host-side reconstruction path (like Cowork's promptlog) from the
   transcript store?
4. Is it host-side or sandboxed (egress)? Determines native-OTEL vs reconstruction.
5. Hook mechanism? VSCode forks sometimes expose extension/hook points; if none,
   watcher-only capture (like Cowork/Codex).

**Likely design shape (DO NOT assume — confirm each):** if it stores transcripts
on disk → new watcher root + a source-aware reader/extractor (register in
`resolve.go`, add to `watch.New` extractors), add its source id to
`interactiveCodingTools`/`codingTools`. Telemetry → native OTEL if it has a
configurable exporter; else host-side reconstruction from the transcript store.
Same pointer model (no text on disk), fidelity/oracle test, no-double-emit guard.

**Open questions for the user (brainstorm):** is Antigravity in scope / do they use
it; do they have an install to investigate + validate against (Gemini CLI got real
on-device validation — keep that bar). Other Gemini surfaces (Code Assist, Vertex,
gemini.google.com) remain likely-out-of-scope server-side — flag, don't build blind.

---

## 5. This session's context (state + gotchas)

- **Shipped:** v0.9.0 (hook-free capture: Claude Code all surfaces + Cowork),
  v0.9.1–0.9.5 (full-fidelity Cowork telemetry — host-side OTLP mirroring the CLI
  schema; `tool=cowork`; grouped/monotonic metrics; exact tokens, cost-in-Atlas),
  v0.10.0 (Codex parity), v0.11.0 (Gemini CLI parity — enrichment watcher/reader +
  native OTEL), v0.11.1 (Gemini auth fix: token in `otlpEndpoint` `?token=` query,
  not a trust-gated `.env` header; dropped deprecated `x-keld-actor` everywhere).
  All merged to `main`, released, CI green.
- **Machine:** macOS arm64. Daemon installed via `.pkg`, runs under launchd
  `co.keld.agent` (per-user). Logs: `~/.keld/logs/agent.{out,err}.log`,
  `~/.keld/agent.log` (debuglog; watcher/promptlog write here). Config:
  `~/.keld/{auth,hook,agent,manifest}.json`, `~/.keld/watch/cursors.json`.
- **Known real bugs found (not yet fixed — worth filing):**
  1. **Stale `reauth-required` marker** — daemon writes `~/.keld/reauth-required`
     on a 401 but never deletes it when auth recovers; `keld signal doctor`/`status`
     then falsely report "re-authentication required." Fix: clear the marker on the
     next successful poll/publish; make `doctor` do a live check, not read the file.
  2. **Cowork sandbox egress allowlist omits `atlas.keld.co`** — why Cowork's own
     native OTEL never arrives (the reason the host-side emitter exists). Org/Anthropic
     -side; the host-side emitter is our workaround.
- **Codex caveat:** shipped in v0.10.0 with **NO live end-to-end validation** (Codex
  not installed here). Schema pinned from `openai/codex` source + fixtures. Validate
  on a real Codex host: install v0.10.0, run a session, confirm `~/.codex/sessions/**/
  rollout-*.jsonl` matches the pinned `user_message`/`session_meta`/`ordinal` schema,
  and that enrichment + native-OTEL token data land in Atlas.
- **Deferred Codex Minors** (in `.superpowers/sdd/progress.md`): `readCodexSessionHead`
  O(n²) if a file never has `session_meta`; benign single-goroutine TOCTOU; dup parse
  logic. Non-blocking.
- **Env / release notes:** `sudo` has no TTY in this session → the user runs
  `sudo installer …` themselves. Foreground `sleep` blocked. `timeout` cmd absent.
  Bash cwd resets between calls (use absolute paths / `cd` inside the command).
  The auto-mode classifier blocked a manual `curl` POST of a token to atlas.keld.co
  once — daemon-originated POSTs are fine (not bash).
- **Specs/plans** for this arc: `docs/superpowers/specs/2026-07-21-*` and
  `docs/superpowers/plans/2026-07-21-*` (transcript-watch-capture, fullfidelity-
  cowork-telemetry, codex-parity). Read them for detail + rationale.
- **Captured OTEL oracle** technique (for fidelity): point a tool's OTEL at a local
  python HTTP sink via a settings override, run it, capture the exact OTLP JSON.
  (Done for Cowork `claude` and Gemini `gemini` — the latter also proved
  `OTEL_TRACES_EXPORTER` is ignored and confirmed spans are content-free. Do the
  same for Antigravity if it exposes an OTEL endpoint.)

---

## 6. First actions on resume

1. Re-read this doc + the freshest worked example: the `2026-07-22-gemini-cli-coverage`
   spec + plan (and the earlier 2026-07-21 specs for Cowork/Codex rationale).
2. Confirm current state: `git -C ~/keld-signal log --oneline -5`, `git tag | head`
   (latest should be `v0.11.1`).
3. Brainstorm Antigravity scope with the user: do they use it / is it in scope; do
   they have an install to investigate + validate against?
4. Investigate Antigravity empirically FIRST (§4): find its on-disk footprint, whether
   it persists transcripts + in what format, and whether it exposes any telemetry.
   Nothing was known as of 2026-07-22 — no assuming.
5. Spec → TDD plan → execute → review → release, per the playbook.
