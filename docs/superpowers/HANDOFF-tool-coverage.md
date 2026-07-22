# HANDOFF — AI-tool coverage in keld-signal (next: Gemini)

**Purpose.** You (a fresh-context instance) are extending keld-signal's per-tool
coverage. This session added Claude Code (all surfaces), Cowork, and Codex. **Next
move: add Gemini products** (Gemini CLI, Vertex, Code Assist, …) *to the extent
they empirically expose coverage* — don't assume, investigate first.

**How to use this doc.** Read it, then follow the playbook (below). Ground every
claim in real on-disk data or upstream source — never guess a schema. Use the
superpowers flow: brainstorming → spec → writing-plans (TDD) → subagent-driven
execution → final review → finishing-a-development-branch → release.

Date written: 2026-07-22. Repo: `/Users/keldtester1/keld-signal`. Latest tag: **v0.10.0**.
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

## 2. Coverage matrix (as of v0.10.0)

| Capability | Claude Code | Cowork | Codex | **Gemini (today)** |
|---|---|---|---|---|
| Install adapter + detect | ✅ | (via Claude app) | ✅ | ✅ (`~/.gemini`) |
| `keld setup` writes config | ✅ hooks+OTEL env | n/a | ✅ `[otel]`+hooks | ⚠️ **telemetry block only** |
| Command hook | ✅ UserPromptSubmit | ❌ (sandbox) | ⚠️ SessionStart/PreToolUse (no prompt-submit) | ❌ **no hook** |
| Transcript watcher root | ✅ `~/.claude/projects` | ✅ cowork dirs | ✅ `~/.codex/sessions` | ❌ |
| Transcript reader (enrichment) | ✅ | ✅ (reuses Claude) | ✅ rollout | ❌ |
| Enrichment classification flags | ✅ eng | topical (excluded) | ✅ eng | ❌ (not context-eligible) |
| Telemetry | native OTEL (host) | **host-side reconstruction** (promptlog) | native OTEL (host) | ⚠️ **OTEL configured, unverified** |

**Gemini today = telemetry-config-only, no capture/enrichment.** `keld setup` writes
`~/.gemini/settings.json` `telemetry: {enabled:true, target:"local", otlpProtocol:"http",
otlpEndpoint:"<endpoint>/v1/logs?token=<tok>", logPrompts:false}` (`telemetry.GeminiTelemetry`).
No hook, no watcher root, no reader, zero refs in resolve/watch/promptlog/enrich flags.

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

## 4. Gemini — where to start

**Surfaces to scope with the user** (pick what's empirically coverable):
- **Gemini CLI** (`google-gemini/gemini-cli`, open source, `~/.gemini/`) — most
  likely to have local session logs + OTEL. Primary target.
- **Gemini Code Assist** (VS Code / JetBrains / Cloud) — IDE plugin; unclear local
  footprint.
- **Vertex AI / Gemini API** — server-side; likely NOT on-device (probably out of
  scope, like plain Claude chat was — flag it).
- **Gemini app / gemini.google.com** — web; server-side, likely out of scope.

**First investigations (empirical):**
1. Gemini CLI local sessions: does it persist transcripts? Where under `~/.gemini`
   (e.g. `~/.gemini/tmp/<hash>/logs.json`, chat/checkpoint files)? Schema? Which
   record is a user prompt? (Gemini isn't installed on this machine — inspect the
   `google-gemini/gemini-cli` source via `gh`, and/or ask the user to run it and
   share `~/.gemini` layout + a session file.)
2. Gemini CLI telemetry: it already gets `telemetry.target=local` + `otlpEndpoint`.
   **Verify it actually reaches `atlas.keld.co`** (target `local` vs `gcp`; does the
   CLI export OTLP to the configured endpoint?). Confirm the OTEL schema (event
   names, token/usage fields) so telemetry can be normalized in Atlas. Consider
   header auth vs the current `?token=` in the URL (Codex was moved to a header).
3. Does Gemini CLI have a **hook** mechanism (like Claude's `settings.json` hooks /
   Codex's `[[hooks.*]]`)? If yes → prompt-submit hook capture; if no → watcher
   only (like Cowork/Codex).
4. Host-side vs sandbox: Gemini CLI runs host-side → native OTEL should reach Keld
   (no reconstruction needed) IF telemetry actually exports to the remote endpoint.

**Likely design shape** (confirm empirically): telemetry = complete/verify native
OTEL config (metrics exporter + header auth, mirror the Codex fix); enrichment = a
`~/.gemini` watcher root + a Gemini transcript reader (register in `resolve.go`;
add a source-aware extractor in the watcher like `codexExtractor` if the schema
differs from Claude's); add `gemini` to `interactiveCodingTools`/`codingTools` if
it's a coding tool; keep `promptlog` off for gemini (native OTEL). Same pointer
model, fidelity test, no-double-emit guard.

**Open questions for the user (brainstorm):** which surfaces are in scope; is
Gemini CLI the only realistically-coverable one; do they have a Gemini install to
validate against (Codex shipped WITHOUT live validation — a known gap; ideally
don't repeat that for Gemini if avoidable).

---

## 5. This session's context (state + gotchas)

- **Shipped:** v0.9.0 (hook-free capture: Claude Code all surfaces + Cowork),
  v0.9.1–0.9.5 (full-fidelity Cowork telemetry — host-side OTLP mirroring the CLI
  schema; `tool=cowork`; grouped/monotonic metrics; exact tokens, cost-in-Atlas),
  v0.10.0 (Codex parity). All merged to `main`, released, CI green.
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
  (For Cowork I captured `claude`; for Gemini, capture `gemini` similarly.)

---

## 6. First actions on resume

1. Re-read this doc + the three 2026-07-21 specs.
2. Confirm current state: `git -C ~/keld-signal log --oneline -5`, `git tag | head`.
3. Brainstorm Gemini scope with the user (surfaces; do they have a Gemini install to
   validate against?).
4. Investigate Gemini CLI empirically (source via `gh`, real `~/.gemini` if the user
   provides it, OTEL capture) — mirror the Codex investigation.
5. Spec → TDD plan → execute → review → release, per the playbook.
