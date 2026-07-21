# Codex parity — Implementation Plan (TDD)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans (inline, strict TDD). Every task: failing test → run/see it fail → minimal implementation → run/see it pass → commit. Steps use `- [ ]`.

**Goal:** Codex reaches Claude-Code-level coverage — enrichment (prompts → masked Profile via a watcher + rollout reader) and telemetry (complete Codex's native OTEL config; no reconstruction).

**Spec:** docs/superpowers/specs/2026-07-21-codex-parity-design.md

## Global Constraints

- Module `github.com/ncx-ai/keld-signal`; `gofmt -w` before every commit (CI gate); `export PATH="/opt/homebrew/bin:$PATH"` to run `go`.
- No `enrich.SchemaVersion` change.
- **Never write prompt text to disk** — pointer model only (watcher emits `session_id#ordinal` ids; the reader resolves text locally).
- Telemetry: rely on Codex NATIVE OTEL — do NOT emit host-side telemetry for `codex` (`promptlog` default sources stay `{cowork}`).
- Codex rollout schema (pinned from openai/codex source):
  - Line = `{"timestamp":"…","ordinal":N (optional u64),"type":"<item>","payload":{…}}` (`RolloutItem` is `#[serde(tag="type",content="payload",rename_all="snake_case")]`).
  - User prompt: `type=="event_msg"`, `payload=={"type":"user_message","message":"<TEXT>", …}`.
  - Session: `type=="session_meta"`, `payload` flat → `id` (thread id, string), `cwd` (string), plus `session_id`, `cli_version`, `git`.
  - Sessions dir: `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`; honor `CODEX_HOME`.
- Codex `[otel]` supports `exporter`, `metrics_exporter`, and `OtlpHttp{ endpoint, headers }`.

---

### Task 1: Complete Codex native OTEL config (metrics exporter + header auth)

**Files:** Modify `internal/telemetry/telemetry.go` (`CodexBlockBody`); update `internal/tools/testdata/golden/codex_apply.toml`; Test: `internal/telemetry/telemetry_test.go` (add), `internal/tools/golden_test.go` (regen check).

**Interfaces:** `CodexBlockBody(p SetupParams, source string) string` signature unchanged; output gains a `metrics_exporter` and moves the ingest token from the URL query to an `x-keld-ingest-token` header in both exporters.

- [ ] **Step 1 — failing test** (`telemetry_test.go`):

```go
func TestCodexBlockBodyMetricsAndHeaderAuth(t *testing.T) {
	got := CodexBlockBody(SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}, "codex")
	// logs exporter present, metrics exporter present
	if !strings.Contains(got, "metrics_exporter") {
		t.Error("missing metrics_exporter (token metrics never flow otherwise)")
	}
	if !strings.Contains(got, "/v1/logs") || !strings.Contains(got, "/v1/metrics") {
		t.Errorf("expected both /v1/logs and /v1/metrics endpoints:\n%s", got)
	}
	// header auth, not token-in-URL
	if !strings.Contains(got, `"x-keld-ingest-token" = "tok"`) {
		t.Errorf("expected x-keld-ingest-token header:\n%s", got)
	}
	if strings.Contains(got, "?token=") {
		t.Errorf("token must not ride in the URL:\n%s", got)
	}
	if !strings.Contains(got, `command = 'keld __hook --source codex'`) {
		t.Error("hook command changed unexpectedly")
	}
}
```

- [ ] **Step 2** — `go test ./internal/telemetry/ -run TestCodexBlockBodyMetricsAndHeaderAuth`; expect FAIL.
- [ ] **Step 3 — implement**: rewrite `CodexBlockBody` so the `[otel]` block emits a logs `exporter` and a `metrics_exporter`, each `otlp-http` with `endpoint` (`…/v1/logs`, `…/v1/metrics`, no `?token=`), `protocol = "json"`, and `headers = { "x-keld-ingest-token" = "<tok>", "x-keld-actor" = "<actor>" }`. Keep `environment`, `log_user_prompt = false`, and the two `[[hooks.*]]` blocks unchanged.
- [ ] **Step 4** — `go test ./internal/telemetry/`; PASS. Then regenerate/update the golden: run the tools golden test, inspect the new `codex_apply.toml`, confirm it matches the new block, commit the updated golden.
- [ ] **Step 5** — `go test ./internal/tools/`; PASS.
- [ ] **Step 6 — commit**: `gofmt -w` changed files; `git commit -m "feat(telemetry): codex OTEL — add metrics exporter + header auth"`.

---

### Task 2: Codex rollout TranscriptReader

**Files:** Create `internal/agent/resolve/codex.go`, `internal/agent/resolve/codex_test.go`; Modify `internal/agent/resolve/resolve.go` (register).

**Interfaces:** `NewCodexReader() *CodexReader` implementing `TranscriptReader` (`Source()=="codex"`, `Read(path, promptID)`) and `RecentReader` (`RecentUserPrompts`). `promptID` format `"<sessionID>#<ordinal>"`.

- [ ] **Step 1 — failing test** (`codex_test.go`): write a fixture rollout JSONL to a temp file:

```go
package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

const codexFixture = `{"timestamp":"2026-07-21T19:00:00Z","type":"session_meta","payload":{"id":"thread_1","session_id":"s1","cwd":"/work/proj","cli_version":"1.0.0"}}
{"timestamp":"2026-07-21T19:00:01Z","ordinal":5,"type":"event_msg","payload":{"type":"user_message","message":"refactor the auth module"}}
{"timestamp":"2026-07-21T19:00:02Z","ordinal":6,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}
{"timestamp":"2026-07-21T19:00:03Z","ordinal":7,"type":"event_msg","payload":{"type":"token_count","input_tokens":10}}
{"timestamp":"2026-07-21T19:00:09Z","ordinal":12,"type":"event_msg","payload":{"type":"user_message","message":"now add tests"}}
`

func writeCodexFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-2026-07-21.jsonl")
	if err := os.WriteFile(p, []byte(codexFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCodexReaderReadByOrdinal(t *testing.T) {
	r := NewCodexReader()
	if r.Source() != "codex" {
		t.Fatalf("source=%q", r.Source())
	}
	text, ok := r.Read(writeCodexFixture(t), "thread_1#5")
	if !ok || text != "refactor the auth module" {
		t.Fatalf("read #5: %q ok=%v", text, ok)
	}
	text, ok = r.Read(writeCodexFixture(t), "thread_1#12")
	if !ok || text != "now add tests" {
		t.Fatalf("read #12: %q ok=%v", text, ok)
	}
	// non-user_message ordinal → not found
	if _, ok := r.Read(writeCodexFixture(t), "thread_1#6"); ok {
		t.Fatal("ordinal 6 is a response_item, must not resolve")
	}
}

func TestCodexReaderRecentUserPrompts(t *testing.T) {
	r := NewCodexReader()
	got := r.RecentUserPrompts(writeCodexFixture(t), "thread_1#12", 5) // exclude current (#12)
	if len(got) != 1 || got[0] != "refactor the auth module" {
		t.Fatalf("recent=%v", got)
	}
}

func TestResolveCodexSource(t *testing.T) {
	text, ok := Resolve("codex", writeCodexFixture(t), "thread_1#5", "")
	if !ok || text != "refactor the auth module" {
		t.Fatalf("resolve codex: %q ok=%v", text, ok)
	}
}
```

- [ ] **Step 2** — `go test ./internal/agent/resolve/ -run Codex`; expect FAIL (undefined).
- [ ] **Step 3 — implement** `codex.go`: tolerant parse of each line into `{Ordinal *uint64 `json:"ordinal"`, Type string `json:"type"`, Payload json.RawMessage `json:"payload"`}`; for `Read`, split `promptID` on `#` → `ordinalWant`; scan lines, and when `Type=="event_msg"` decode payload `{Type string; Message string}` — if `payload.Type=="user_message"` and the line's `ordinal`==`ordinalWant`, return `Message`. `RecentUserPrompts`: collect `user_message` messages (excluding the current ordinal), newest-first, capped at n (tail-scan bounded like the Claude reader). Tolerate malformed lines.
- [ ] **Step 4** — register in `resolve.go` `init()`: `register(NewCodexReader())`.
- [ ] **Step 5** — `go test ./internal/agent/resolve/`; PASS (incl. existing).
- [ ] **Step 6 — commit**: gofmt; `git commit -m "feat(resolve): codex rollout transcript reader (user_message by ordinal)"`.

---

### Task 3: Codex watcher root

**Files:** Modify `internal/agent/watch/roots.go`; Test: `internal/agent/watch/roots_test.go`.

**Interfaces:** `discoverRoots(home, goos)` also returns `~/.codex/sessions` → source `codex` (macOS + Linux). Honor `CODEX_HOME` (env override of `~/.codex`).

- [ ] **Step 1 — failing test** (`roots_test.go`): add

```go
func TestDiscoverRootsCodex(t *testing.T) {
	home := t.TempDir()
	cx := filepath.Join(home, ".codex", "sessions")
	mkdir(t, cx)
	for _, goos := range []string{"darwin", "linux"} {
		if !hasRoot(discoverRoots(home, goos), "codex", cx) {
			t.Errorf("%s: missing codex root; got %+v", goos, discoverRoots(home, goos))
		}
	}
}
```

- [ ] **Step 2** — `go test ./internal/agent/watch/ -run TestDiscoverRootsCodex`; expect FAIL.
- [ ] **Step 3 — implement**: in `discoverRoots`, after the claude_code root, add: resolve codex home = `os.Getenv("CODEX_HOME")` if set else `filepath.Join(home, ".codex")`; if `<codexHome>/sessions` `isDir`, append `Root{SourceID: "codex", Dir: <codexHome>/sessions}` (both OSes). (Note: `DiscoverRoots()` passes real home; `CODEX_HOME` read inside `discoverRoots` — to keep it testable, read `CODEX_HOME` in `discoverRoots` and fall back to `home/.codex`.)
- [ ] **Step 4** — `go test ./internal/agent/watch/`; PASS.
- [ ] **Step 5 — commit**: gofmt; `git commit -m "feat(watch): watch ~/.codex/sessions (source codex)"`.

---

### Task 4: Source-aware prompt extraction + stateful Codex extractor

**Files:** Modify `internal/agent/watch/watch.go`; Create `internal/agent/watch/codex.go`; Test: `internal/agent/watch/codex_test.go`.

**Why:** the watcher's current per-line prompt detection (`parsePrompt`) is Claude-format. Codex needs a stateful, path-aware extractor: session id + cwd live only in the `session_meta` line (file head), while prompts arrive later; and the id must be file-global (`ordinal`) so incremental scans produce stable, unique ids.

**Interfaces:**
- `promptExtractor` interface: `extract(path string, line []byte) (promptRec, bool)`.
- `claudeExtractor` (stateless) wraps `parsePrompt`.
- `codexExtractor` (stateful): per-file session-context cache; on `session_meta` caches `{id,cwd}`; on `event_msg`/`user_message` with an `ordinal`, returns `promptRec{PromptID: id+"#"+ordinal, Cwd: cwd, SessionID: id}`. If session context unknown for a file (scan started past the head), it reads the file head once to find `session_meta`.
- `Watcher` gains `extractors map[string]promptExtractor` (built in `New`); `scanFile` selects `extractorFor(source)` and passes `ex.extract` into `scanFrom` (replacing the direct `parsePrompt` call).

- [ ] **Step 1 — failing test** (`codex_test.go`):

```go
package watch

import "testing"

func TestCodexExtractorSessionThenPrompt(t *testing.T) {
	ex := newCodexExtractor()
	path := "/x/rollout.jsonl"
	// session_meta establishes id + cwd
	if _, ok := ex.extract(path, []byte(`{"type":"session_meta","payload":{"id":"thread_1","cwd":"/work"}}`)); ok {
		t.Fatal("session_meta is not a prompt")
	}
	rec, ok := ex.extract(path, []byte(`{"ordinal":5,"type":"event_msg","payload":{"type":"user_message","message":"hi"}}`))
	if !ok || rec.PromptID != "thread_1#5" || rec.Cwd != "/work" || rec.SessionID != "thread_1" {
		t.Fatalf("rec=%+v ok=%v", rec, ok)
	}
	// non-prompt lines rejected
	for _, l := range []string{
		`{"ordinal":6,"type":"response_item","payload":{"type":"message","role":"assistant"}}`,
		`{"ordinal":7,"type":"event_msg","payload":{"type":"token_count","input_tokens":3}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":"no ordinal"}}`, // no ordinal → skip
	} {
		if _, ok := ex.extract(path, []byte(l)); ok {
			t.Fatalf("line should be rejected: %s", l)
		}
	}
}

func TestCodexExtractorReadsSessionHeadWhenMissing(t *testing.T) {
	// Simulate an incremental scan that never saw session_meta: the extractor
	// must read it from the file head. Write a real file with session_meta first.
	dir := t.TempDir()
	path := dir + "/rollout.jsonl"
	os.WriteFile(path, []byte(
		`{"type":"session_meta","payload":{"id":"thread_9","cwd":"/repo"}}`+"\n"+
			`{"ordinal":3,"type":"event_msg","payload":{"type":"user_message","message":"x"}}`+"\n"), 0o600)
	ex := newCodexExtractor()
	// feed ONLY the later line (as an incremental scan would)
	rec, ok := ex.extract(path, []byte(`{"ordinal":3,"type":"event_msg","payload":{"type":"user_message","message":"x"}}`))
	if !ok || rec.SessionID != "thread_9" || rec.Cwd != "/repo" || rec.PromptID != "thread_9#3" {
		t.Fatalf("rec=%+v ok=%v (should recover session from file head)", rec, ok)
	}
}
```
(Add `import "os"` to the test file.)

- [ ] **Step 2** — `go test ./internal/agent/watch/ -run TestCodexExtractor`; expect FAIL.
- [ ] **Step 3 — implement** `codex.go`: `codexExtractor{ mu sync.Mutex; sess map[string]codexSess }` where `codexSess{id, cwd string}`. `extract(path, line)`: parse `{Ordinal *uint64; Type string; Payload json.RawMessage}`; if `Type=="session_meta"` → decode payload `{Id string; Cwd string}` → cache `sess[path]`; return false. If `Type=="event_msg"` → decode payload `{Type string; Message string}`; if `payload.Type!="user_message"` or `Message==""` or `Ordinal==nil` → false. Look up `sess[path]`; if absent, `readCodexSessionHead(path)` (scan file for the first `session_meta`, cache it); if still absent → false. Return `promptRec{PromptID: fmt.Sprintf("%s#%d", s.id, *Ordinal), Cwd: s.cwd, SessionID: s.id}, true`.
- [ ] **Step 4 — wire watcher**: in `watch.go`, add `extractors map[string]promptExtractor` to `Watcher`, initialized in `New` (`{"claude_code": claudeExtractor{}, "cowork": claudeExtractor{}, "codex": newCodexExtractor()}`); add `func (w *Watcher) extractorFor(source string) promptExtractor` (default `claudeExtractor{}`). Change `scanFile`/`scanFrom` so the prompt-detection call is `ex.extract(path, line)` for the file's source instead of `parsePrompt(line)`. Keep the `observe` (telemetry) call unchanged. Define `claudeExtractor` with `extract(_ path, line) { return parsePrompt(line) }`.
- [ ] **Step 5** — `go test ./internal/agent/watch/`; PASS (all existing watch tests still green — Claude path unchanged via `claudeExtractor`).
- [ ] **Step 6 — commit**: gofmt; `git commit -m "feat(watch): source-aware prompt extraction; stateful codex rollout extractor"`.

---

### Task 5: Guards + docs

**Files:** Test: `internal/agent/promptlog/promptlog_test.go` (guard); Modify `README.md`, `AGENTS.md`, `CHANGELOG.md`.

- [ ] **Step 1 — guard test**: assert `promptlog.SourcesFromEnv()` (default) does NOT include `codex` (native OTEL owns codex telemetry; no host-side double-emit):

```go
func TestCodexNotHostEmitted(t *testing.T) {
	t.Setenv("KELD_WATCH_TELEMETRY", "")
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "")
	if SourcesFromEnv()["codex"] {
		t.Error("codex must not be host-side emitted; its native OTEL is used")
	}
}
```

- [ ] **Step 2** — run it; PASS (documents current behavior).
- [ ] **Step 3 — docs**: CHANGELOG `## [0.10.0]` — Codex parity: native OTEL completed (metrics + header auth); enrichment via `~/.codex/sessions` watcher + rollout reader (user_message by ordinal, pointer model); telemetry stays native (no host-side emit). AGENTS.md capture section: note the codex watcher root + rollout reader + native-OTEL telemetry. README: update the Codex line from "config only" to "enriched + native telemetry".
- [ ] **Step 4** — `go build ./... && go test ./...`; all pass; `gofmt -l internal/` clean.
- [ ] **Step 5 — commit**: `git commit -m "docs+test: codex parity (v0.10.0) + no-double-emit guard"`.

## Self-Review

- Spec coverage: telemetry config (T1), reader (T2), watcher root (T3), source-aware capture + codex extractor (T4), guard + docs (T5). All spec sections mapped.
- No prompt text on disk (pointer model; T2/T4 emit ids, reader resolves locally).
- Type consistency: `promptRec{PromptID,Cwd,SessionID}` (existing) reused; `promptExtractor.extract(path,line)(promptRec,bool)` defined T4, `claudeExtractor` wraps existing `parsePrompt`; `NewCodexReader`/`Read`/`RecentUserPrompts` defined T2, registered T2, exercised by resolve.
- Risk (schema): fully pinned from source; reader/extractor tolerate unknown lines (degrade to "miss", never crash). Live validation on a real Codex host is the documented follow-up.

## Execution + release

Execute T1→T5 test-first on a branch `feat/codex-parity`; final whole-branch review; merge; cut **v0.10.0**. No local end-to-end run is possible (Codex not installed here) — validate live when a Codex host is available; unit tests + pinned schema carry correctness until then.
