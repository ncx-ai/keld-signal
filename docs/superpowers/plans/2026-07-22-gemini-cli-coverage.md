# Gemini CLI coverage — Implementation Plan (TDD)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development (or executing-plans). Strict TDD per task: failing test → run/see fail → minimal impl → run/see pass → commit. `- [ ]` steps.

**Goal:** Gemini CLI (`@google/gemini-cli`) reaches Claude-Code parity — enrichment (watcher + reader over `~/.gemini/tmp/*/chats/*.jsonl`) and telemetry (native OTEL: fix config + a keld-managed `~/.gemini/.env` block). Grounded in oracles captured from the installed **v0.51.0** on this machine.

**Spec:** docs/superpowers/specs/2026-07-22-gemini-cli-coverage-design.md (design settled; `.gemini/.env` ownership approved by user).

## Global Constraints
- Module `github.com/ncx-ai/keld-signal`; `gofmt -w` before every commit (CI gate); `export PATH="/opt/homebrew/bin:$PATH"` for `go`.
- No `enrich.SchemaVersion` change.
- **Pointer model** — never write prompt/response text to disk or telemetry.
- **`~/.gemini/.env` edits must be surgical** — marker-delimited block; preserve every other line, ESPECIALLY `GEMINI_API_KEY`; idempotent apply; clean remove. This is the highest-risk unit — test it hardest.
- Telemetry = native OTEL (Gemini is host-side); `promptlog` stays OFF for `gemini`.
- Pinned Gemini v0.51.0 schema (oracles on this machine):
  - Chat line types: meta (`sessionId`/`projectHash`/`kind`, line 0), `{"$set":{…}}` mutations (SKIP), user `{id,timestamp,type:"user",content:[{text}]}` (id = PromptID), gemini `{id,type:"gemini",tokens,model,…}`.
  - OTLP: SDK appends signal path → `<BASE>/v1/logs|/v1/metrics|/v1/traces`; honors `OTEL_EXPORTER_OTLP_HEADERS` + `OTEL_TRACES_EXPORTER=none`; resource `service.name:"gemini-cli"`; identity `user.email`/`installation.id` present even with API-key auth; tokens in metric `gemini_cli.token.usage` + log `gemini_cli.api_response`.
  - BeforeAgent hook stdin: `{session_id, transcript_path, cwd, hook_event_name, timestamp, prompt}` — **no prompt_id**; fires before the user line is written (race) → watcher owns enrichment capture, hook posts context event only. Gemini requires **silent stdout** from hooks.

---

### Task 1: `.env` managed-block helper (surgical, secret-safe)

**Files:** `internal/config/envblock.go` (create), `internal/config/envblock_test.go`. (Mirror the existing `UpsertKeldBlock` marker approach but for a line-oriented `.env` file.)

**Interfaces:** `UpsertEnvBlock(current, body string) string` (insert/replace the keld block, preserving all other lines); `RemoveEnvBlock(current string) string`; `HasEnvBlock(current string) bool`. Markers: `# >>> keld-managed (do not edit) >>>` / `# <<< keld-managed <<<`.

- [ ] **Step 1 — failing tests** (`envblock_test.go`): assert
  - apply to `"GEMINI_API_KEY=sk-abc\n"` → result still contains `GEMINI_API_KEY=sk-abc` verbatim AND the block with the given body between markers.
  - idempotent: applying the same body twice == applying once (no duplicate blocks).
  - replace: applying a NEW body when a block exists replaces only the block, keeps `GEMINI_API_KEY`.
  - remove: strips the block + its markers, leaves `GEMINI_API_KEY` and other lines untouched; remove when absent is a no-op.
  - apply to empty string works.
- [ ] **Step 2** — `go test ./internal/config/ -run EnvBlock`; FAIL.
- [ ] **Step 3 — implement** `envblock.go` (find marker span; replace or append; preserve the rest byte-for-byte; ensure trailing newline hygiene).
- [ ] **Step 4** — PASS.
- [ ] **Step 5 — commit**: `feat(config): surgical .env managed-block upsert/remove (secret-safe)`.

---

### Task 2: GeminiTelemetry fix + env-block builder

**Files:** `internal/telemetry/telemetry.go`; `internal/telemetry/telemetry_test.go`.

**Interfaces:** `GeminiTelemetry(p)` → settings `telemetry` block with `otlpEndpoint = p.Endpoint` (BASE, no `/v1/logs?token=`). New `GeminiEnvBlock(p) string` → the two env lines (`OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=<tok>,x-keld-actor=<actor>` and `OTEL_TRACES_EXPORTER=none`).

- [ ] **Step 1 — failing test**: `GeminiTelemetry` `otlpEndpoint` == `p.Endpoint` exactly (no `/v1/logs`, no `?token=`); `logPrompts:false`, `target:"local"`. `GeminiEnvBlock` contains `x-keld-ingest-token=<tok>` + `x-keld-actor=<actor>` in `OTEL_EXPORTER_OTLP_HEADERS` and `OTEL_TRACES_EXPORTER=none`; no token in any URL.
- [ ] **Step 2** — run; FAIL.
- [ ] **Step 3 — implement** both.
- [ ] **Step 4** — PASS.
- [ ] **Step 5 — commit**: `feat(telemetry): gemini base OTLP endpoint + .env OTEL header/traces block`.

---

### Task 3: Gemini adapter — wire settings (telemetry + BeforeAgent hook) + `.env` block

**Files:** `internal/tools/gemini.go`; `internal/tools/gemini_test.go`; golden `internal/tools/testdata/golden/gemini_apply.*` (update/add).

**Interfaces:** `Apply` sets the settings.json `telemetry` block (fixed) AND a `hooks.BeforeAgent` command hook (`keld __hook --source gemini`) AND writes the `~/.gemini/.env` keld block (via Task 1 helper). `Remove` strips all three. `Status`/`Managed` report both artifacts. `Detect` unchanged. **Pin the exact Gemini `hooks` settings.json shape against v0.51.0** (it mirrors Claude's hook conventions — confirm the JSON structure by reading Gemini's config or a live `settings.json` after a manual hook add; do not guess).

- [ ] **Step 1 — failing tests**: `Apply` on a settings.json with only `security.auth` → result has `telemetry` (fixed endpoint) + `hooks.BeforeAgent` with `keld __hook --source gemini`, and the adapter writes the `.env` block while preserving `GEMINI_API_KEY`. `Remove` strips the settings blocks + the `.env` block, leaving `GEMINI_API_KEY` and `security.auth`. Idempotent apply.
- [ ] **Step 2** — run; FAIL.
- [ ] **Step 3 — implement**: the adapter now manages two files. Note `ConfigPath()` stays settings.json; add `.env` handling in `Apply`/`Remove` (read `~/.gemini/.env`, upsert/remove via Task 1). Keep the JSON edits via the existing ordered-map/UpsertKeldBlock machinery. Ensure `.env` is created if absent (mode 0600 — it holds a secret).
- [ ] **Step 4** — `go test ./internal/tools/`; PASS (regen golden).
- [ ] **Step 5 — commit**: `feat(tools): gemini setup wires telemetry + BeforeAgent hook + .env OTEL block`.

---

### Task 4: Gemini transcript reader

**Files:** `internal/agent/resolve/gemini.go`, `internal/agent/resolve/gemini_test.go`; register in `resolve.go`.

**Interfaces:** `NewGeminiReader()` — `Source()=="gemini"`; `Read(path, promptID)` finds the `type:"user"` record whose `id`==promptID, returns concatenated `content[].text`; skips `$set` and non-user lines. `RecentUserPrompts` tail-scans prior user texts.

- [ ] **Step 1 — failing test**: use a **sanitized fixture** derived from a real `~/.gemini/tmp/gproj/chats/*.jsonl` (meta line + `$set` + a `type:user` with known id/text + a `type:gemini`). `Read(fixture, "<user-id>")` → the user text; a `$set`/`gemini` id → not found; `RecentUserPrompts` excludes current, newest-first. Malformed/`$set` lines tolerated.
- [ ] **Step 2** — run; FAIL.
- [ ] **Step 3 — implement**: tolerant per-line parse; treat a line with top-level `$set` key as skip; `type:"user"` → concat `content[].text`; empty text → not found (match Claude reader). Register `NewGeminiReader()` in `resolve.go init()`.
- [ ] **Step 4** — `go test ./internal/agent/resolve/`; PASS.
- [ ] **Step 5 — commit**: `feat(resolve): gemini chat transcript reader (type:user by message id)`.

---

### Task 5: Watcher root for Gemini chats

**Files:** `internal/agent/watch/roots.go`; `roots_test.go`.

- [ ] **Step 1 — failing test** `TestDiscoverRootsGemini`: create `~/.gemini/tmp/<proj>/chats/` under a temp home; assert `discoverRoots(home, goos)` returns a `gemini` root for the chats dir(s) on both darwin+linux. (Glob `~/.gemini/tmp/*/chats` — one root per project dir, or a single root the walker recurses.)
- [ ] **Step 2** — run; FAIL.
- [ ] **Step 3 — implement**: in `discoverRoots`, glob `filepath.Join(home,".gemini","tmp","*","chats")`; append `Root{SourceID:"gemini", Dir:<match>}` for each existing dir (both OSes). (transcriptFiles already walks `*.jsonl`.)
- [ ] **Step 4** — `go test ./internal/agent/watch/`; PASS.
- [ ] **Step 5 — commit**: `feat(watch): watch ~/.gemini/tmp/*/chats (source gemini)`.

---

### Task 6: Gemini prompt extractor

**Files:** `internal/agent/watch/gemini.go`, `internal/agent/watch/gemini_test.go`; wire into `watch.go` extractors map.

**Interfaces:** `geminiExtractor` implementing `promptExtractor.extract(path, line) (promptRec, bool)`. Per-line: skip any line with a top-level `$set`; on `type:"user"` with non-empty text → `promptRec{PromptID: record.id, Cwd: <best-effort>, SessionID: <best-effort>}`. `message.id` is a globally-unique UUID → dedup key `gemini|prompt_id|<id>` is collision-free without session state (simpler than codex). Cwd best-effort (optional: resolve `projectHash`→cwd via `~/.gemini/projects.json`; else empty — enrichment degrades gracefully).

- [ ] **Step 1 — failing test** (`gemini_test.go`): feed the meta line (→ no prompt), a `$set` line (→ skip), a `type:user` line (→ promptRec with PromptID==the id, no text in rec), a `type:gemini` line (→ skip). Empty-text user → skip.
- [ ] **Step 2** — run; FAIL.
- [ ] **Step 3 — implement** `geminiExtractor`; add `"gemini": geminiExtractor{}` to the `extractors` map in `watch.New`. (If cwd-from-projects.json is included, make it best-effort + cached; otherwise leave Cwd empty and note it.)
- [ ] **Step 4** — `go test ./internal/agent/watch/`; PASS (all existing green — Claude/Codex/Cowork paths unchanged).
- [ ] **Step 5 — commit**: `feat(watch): gemini prompt extractor (skip $set, type:user by id)`.

---

### Task 7: Enrichment classification flags

**Files:** `internal/agent/enrich/context.go`, `internal/agent/enrich/a4_compositional.go`; a guard test.

- [ ] **Step 1 — failing test**: assert `ContextEligible("gemini")` true and `codingTools["gemini"]` true (gemini is an interactive coding tool → context augmentation + A4=eng).
- [ ] **Step 2** — run; FAIL.
- [ ] **Step 3 — implement**: add `"gemini": true` to `interactiveCodingTools` and `codingTools`.
- [ ] **Step 4** — `go test ./internal/agent/enrich/`; PASS.
- [ ] **Step 5 — commit**: `feat(enrich): classify gemini as an interactive coding tool`.

---

### Task 8: Hook — gemini source, silent stdout, no enrichment pointer

**Files:** `internal/hook/hook_test.go` (add); verify `internal/hook/{hook,forward}.go` behavior for gemini stdin.

- [ ] **Step 1 — failing/guard test**: feed the captured BeforeAgent stdin (`{session_id, transcript_path, cwd, hook_event_name, timestamp, prompt}` — **no prompt_id**) to `hook.Run("gemini", stdin, stderrBuf, now)` with a stdout capture. Assert: exit 0; **stdout is empty** (Gemini's strict-JSON requirement); no enrichment pointer forwarded (no prompt_id → `forwardToAgent` early-returns — watcher owns capture); context event attempted (session_id present).
- [ ] **Step 2** — run; if it FAILS because `hook.Run` writes to stdout, fix `hook.go`/`forward.go` to never write to stdout (route diagnostics to stderr/debuglog only).
- [ ] **Step 3 — implement** any fix needed for silent stdout.
- [ ] **Step 4** — `go test ./internal/hook/`; PASS.
- [ ] **Step 5 — commit**: `test(hook): gemini BeforeAgent → silent stdout, no pointer, context event` (+ any stdout fix).

---

### Task 9: promptlog-off guard + docs + changelog

**Files:** `internal/agent/promptlog/promptlog_test.go` (guard); `README.md`, `AGENTS.md`, `CHANGELOG.md`.

- [ ] **Step 1 — guard test**: `SourcesFromEnv()` default excludes `gemini` (native OTEL owns gemini telemetry; no host-side emit).
- [ ] **Step 2** — run; PASS.
- [ ] **Step 3 — docs**: CHANGELOG `## [0.11.0]` — Gemini CLI parity: native OTEL (fixed base endpoint + `.env` header/traces block), enrichment via `~/.gemini/tmp/*/chats` watcher + reader, `gemini` classified as coding tool, BeforeAgent context hook. Note the keld-managed `~/.gemini/.env` block (preserves `GEMINI_API_KEY`). AGENTS.md capture section: add gemini. README: Gemini line → "enriched + native telemetry".
- [ ] **Step 4** — `go build ./... && go test ./...`; all pass; `gofmt -l internal/` clean.
- [ ] **Step 5 — commit**: `docs+test: gemini CLI coverage (v0.11.0) + no-double-emit guard`.

---

### Task 10: Live end-to-end validation (before release) — Gemini IS installed here

- [ ] Build keld-agent locally; run it (or install the built binary), ensure `keld setup` wired gemini (settings + `.env` block; confirm `GEMINI_API_KEY` intact).
- [ ] Run a real `gemini` session (headless `-p` or interactive) that produces a chat JSONL under `~/.gemini/tmp/*/chats/`.
- [ ] Confirm on-device: watcher captured the prompt (cursor advanced; enrich job ran), `agent.log` shows no errors; native OTEL POSTed to `/v1/logs`+`/v1/metrics` (local-sink capture to verify headers + `service.name:"gemini-cli"` + token metrics).
- [ ] Confirm in Atlas (with user): the enrichment Profile + gemini token telemetry landed, attributed via `service.name:"gemini-cli"` / `user.email`.
- [ ] Only then cut **v0.11.0**.

## Self-Review
- Spec coverage: `.env` helper (T1), telemetry builders (T2), adapter wiring (T3), reader (T4), watcher root (T5), extractor (T6), classification (T7), hook silent-stdout (T8), guard+docs (T9), live validation (T10). All spec §3–§6 mapped.
- Highest risk (`.env` secret safety) isolated in T1 with dedicated tests; adapter (T3) reuses it.
- No prompt text on disk/telemetry (T4/T6 pointer model; T2 `logPrompts:false`+`traces=none`).
- Types: `promptExtractor` (existing) gains `geminiExtractor`; `TranscriptReader`/`RecentReader` (existing) gain `GeminiReader`; adapter interface unchanged.
- Live validation (T10) closes the Codex no-validation gap — Gemini is installed here.

## Execution
Branch is already `feat/gemini-coverage` (spec committed). Execute T1→T9 test-first (subagent-driven or inline), final whole-branch review, T10 live validation, then merge + `make release VERSION=0.11.0`.
