# Full-fidelity Cowork telemetry — Implementation Plan (TDD)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans (inline, strict TDD). Every task: write the failing test → run it, see it fail → minimal implementation → run, see it pass → commit. Steps use `- [ ]`.

**Goal:** Emit field-level-parity OTEL telemetry (user_prompt + api_request + assistant_response logs + token/cost metrics) host-side for watched Cowork prompts, matching the captured Claude Code CLI schema.

**Architecture:** Watcher gains a per-line `observe` hook → daemon feeds it to `promptlog.Telemetry.Observe`, which parses each transcript line and POSTs OTLP logs/metrics to `{endpoint}/v1/logs` and `/v1/metrics`. Never emits text.

**Spec:** docs/superpowers/specs/2026-07-21-fullfidelity-cowork-telemetry-design.md

## Global Constraints

- Module `github.com/ncx-ai/keld-signal`; `gofmt -w` before every commit (CI gate).
- No `enrich.SchemaVersion` change (telemetry-only).
- **Never emit prompt or response TEXT** — only lengths, ids, model, tokens, identity.
- Best-effort: telemetry never blocks/panics capture.
- Default sources `{cowork}`; `KELD_WATCH_TELEMETRY` (off/on), `KELD_WATCH_TELEMETRY_SOURCES` (csv).
- Payloads must match the captured CLI schema: resource `service.name=claude-code`; log `event.name` ∈ {user_prompt, api_request, assistant_response}; identity attrs `user.email`/`user.account_uuid`/`organization.id`.
- Run go with `export PATH="/opt/homebrew/bin:$PATH"`.

---

### Task 1: Cowork identity extraction

**Files:** Create `internal/agent/promptlog/identity.go`, `internal/agent/promptlog/identity_test.go`

**Produces:** `Identity{Email, AccountUUID, OrgID string}`; `coworkIdentity(path string) Identity`; `identityCache` with `newIdentityCache()` + `forCowork(path) Identity`.

- [ ] **Step 1 — failing test** `identity_test.go`:

```go
package promptlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoworkIdentityFromPathAndMeta(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "Library", "Application Support", "Claude", "local-agent-mode-sessions")
	acct, org, sess := "acct-uuid", "org-uuid", "local_sess1"
	proj := filepath.Join(base, acct, org, sess, ".claude", "projects", "enc")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// session metadata one level above the local_ dir
	meta := filepath.Join(base, acct, org, sess+".json")
	if err := os.WriteFile(meta, []byte(`{"emailAddress":"dg@keld.co"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(proj, "sess.jsonl")
	id := coworkIdentity(tp)
	if id.AccountUUID != acct || id.OrgID != org || id.Email != "dg@keld.co" {
		t.Fatalf("got %+v", id)
	}
}

func TestCoworkIdentityNonCoworkPath(t *testing.T) {
	if id := coworkIdentity("/Users/x/.claude/projects/p/s.jsonl"); id != (Identity{}) {
		t.Fatalf("expected zero identity, got %+v", id)
	}
}

func TestIdentityCacheMemoizes(t *testing.T) {
	c := newIdentityCache()
	a := c.forCowork("/no/match.jsonl")
	b := c.forCowork("/no/match.jsonl")
	if a != b {
		t.Fatal("cache should return stable value")
	}
}
```

- [ ] **Step 2** — run `go test ./internal/agent/promptlog/ -run 'Identity|Cache' -v`; expect FAIL (undefined).
- [ ] **Step 3 — implement** `identity.go`: `Identity` struct; `coworkIdentity` splits the path after `local-agent-mode-sessions` into `<acct>/<org>/local_<id>/…`, reads `emailAddress` from `<acct>/<org>/<local_id>.json`; zero Identity when marker absent or <3 segments. `identityCache` = mutex + `map[string]Identity`, compute-on-miss.
- [ ] **Step 4** — run tests; expect PASS.
- [ ] **Step 5** — `gofmt -w`; commit `feat(promptlog): cowork identity extraction from session path/metadata`.

---

### Task 2: Per-model cost derivation

**Files:** Create `internal/agent/promptlog/pricing.go`, `pricing_test.go`

**Produces:** `costUSD(model string, inTok, outTok, cacheCreateTok, cacheReadTok int) (float64, bool)`.

- [ ] **Step 1 — failing test** `pricing_test.go`:

```go
package promptlog

import "testing"

func TestCostUSDKnownModel(t *testing.T) {
	c, ok := costUSD("claude-opus-4-8", 1_000_000, 1_000_000, 0, 0)
	if !ok || c <= 0 {
		t.Fatalf("expected positive cost, got %v ok=%v", c, ok)
	}
}

func TestCostUSDUnknownModel(t *testing.T) {
	if _, ok := costUSD("some-unknown-model", 100, 100, 0, 0); ok {
		t.Fatal("unknown model must return ok=false")
	}
}

func TestCostUSDMatchesRateShape(t *testing.T) {
	// output tokens cost more than input for the same count (sanity of the table).
	in, _ := costUSD("claude-opus-4-8", 1_000_000, 0, 0, 0)
	out, _ := costUSD("claude-opus-4-8", 0, 1_000_000, 0, 0)
	if out <= in {
		t.Fatalf("output rate should exceed input rate: in=%v out=%v", in, out)
	}
}
```

- [ ] **Step 2** — run `-run Cost`; expect FAIL.
- [ ] **Step 3 — implement** `pricing.go`: a `map[string]struct{in,out,cacheWrite,cacheRead float64}` USD-per-million-token table (prefix-match model names for opus/sonnet/haiku families), `costUSD` returns sum or `(0,false)` if no family matches. Document that prices may drift (spec-flagged).
- [ ] **Step 4** — run; PASS.
- [ ] **Step 5** — `gofmt -w`; commit `feat(promptlog): per-model cost derivation table`.

---

### Task 3: OTLP payload builders

**Files:** Create `internal/agent/promptlog/otlp.go`, `otlp_test.go`

**Produces:** attribute helper `attr(k, v string) kv` / `attrInt`; `logsPayload(res []kv, records []logRecord) ([]byte,error)`; `metricsPayload(res []kv, metrics []metric) ([]byte,error)`; exported enough for `promptlog` to build records. (Move the OTLP structs from the current `promptlog.go` here.)

- [ ] **Step 1 — failing test** `otlp_test.go`:

```go
package promptlog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogsPayloadShape(t *testing.T) {
	res := []kv{attr("service.name", "claude-code")}
	rec := logRecord{
		TimeUnixNano: "1", SeverityText: "INFO", Body: anyVal{StringValue: "claude_code.user_prompt"},
		Attributes: []kv{attr("event.name", "user_prompt"), attrInt("prompt_length", 19)},
	}
	b, err := logsPayload(res, []logRecord{rec})
	if err != nil {
		t.Fatal(err)
	}
	var p otlpLogs
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if p.ResourceLogs[0].Resource.Attributes[0].Key != "service.name" {
		t.Fatal("resource attr missing")
	}
	lr := p.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if lr.Body.StringValue != "claude_code.user_prompt" {
		t.Fatal("body wrong")
	}
	// int attribute must serialize as intValue, not stringValue
	if !strings.Contains(string(b), "\"intValue\"") {
		t.Fatalf("expected intValue in payload: %s", string(b))
	}
}

func TestMetricsPayloadShape(t *testing.T) {
	b, err := metricsPayload([]kv{attr("service.name", "claude-code")},
		[]metric{{Name: "claude_code.token.usage", Value: 42, Attrs: []kv{attr("type", "output")}}})
	if err != nil {
		t.Fatal(err)
	}
	var m otlpMetrics
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].Name != "claude_code.token.usage" {
		t.Fatal("metric name wrong")
	}
}
```

- [ ] **Step 2** — run `-run 'Payload'`; expect FAIL.
- [ ] **Step 3 — implement** `otlp.go`: move `otlpLogs/resourceLogs/scopeLogs/logRecord/kv/anyVal` here; add `intVal`/`anyVal` support (`anyVal{StringValue}` and a separate int path) — simplest: give `kv.Value` an `IntValue *string` via OTLP `{"intValue":"N"}` (OTLP/JSON encodes int as string). Add `attr`, `attrInt`; `otlpMetrics/resourceMetrics/scopeMetrics/metric` types with a `sum` (monotonic=false) datapoint; `logsPayload`/`metricsPayload` marshal. Update `promptlog.go` to use `attr(...)` (keep it building for now).
- [ ] **Step 4** — run `go test ./internal/agent/promptlog/`; PASS (existing minimal tests still green).
- [ ] **Step 5** — `gofmt -w`; commit `feat(promptlog): OTLP logs+metrics payload builders`.

---

### Task 4: Telemetry.Observe — parse transcript lines → events + metrics

**Files:** Rewrite `internal/agent/promptlog/promptlog.go`; update `promptlog_test.go` (replace the v0.9.1 minimal-emit tests with Observe tests).

**Produces:** `Telemetry` with `New(logsURL, metricsURL string, token func() string, sources map[string]bool) *Telemetry` and `Observe(source, transcriptPath string, line []byte)`. `SourcesFromEnv()` unchanged. Removes the old `Emitter`/`Emit`.

- [ ] **Step 1 — failing test** `promptlog_test.go` (replace file): assert
  - a user line (`type:user`, promptId, text content) from a cowork path → one POST to logsURL containing a `user_prompt` record with `session.id`, `prompt.id`, `prompt_length`, and identity `user.email`/`organization.id` from the path/meta; **no prompt text**.
  - an assistant line (`type:assistant`, `message.model`, `message.usage`, `message.id`) → POST(s) with `api_request` + `assistant_response` records (model, input/output/cache tokens, request_id) and a metrics POST with `claude_code.token.usage`; **no response text**.
  - source not in set → no POST.
  - empty token → no POST.

  (Full test body: two `httptest` servers — one for `/v1/logs`, one for `/v1/metrics` — or one server switching on `r.URL.Path`; feed real transcript-shaped JSON lines; assert captured bodies. Content-ban assertion: bodies must not contain the literal prompt/response text.)

- [ ] **Step 2** — run; expect FAIL (Telemetry/Observe undefined; old tests reference removed Emit).
- [ ] **Step 3 — implement** `promptlog.go`:
  - `Telemetry{logsURL, metricsURL string, token func() string, actor string, sources map[string]bool, ids *identityCache, seq map[string]int64, client}` (+ mutex for seq).
  - `Observe`: gate on `sources[source]` and non-empty token; unmarshal line to a tolerant struct; branch:
    - `type=="user"` & genuine prompt (reuse the watch filter's rules: promptId set, has text, not tool_result/sidechain/meta) → build `user_prompt` logRecord (attrs: event.name, event.timestamp from record ts, session.id, prompt.id, message.uuid, prompt_length from content rune count, + identity). POST logs.
    - `type=="assistant"` with `message.usage` → build `api_request` (model, input/output/cache tokens, request_id=message.id, session.id, prompt.id if present, + identity) and `assistant_response` (model, response_length from content rune count, request_id, + identity) logRecords → POST logs; build `claude_code.token.usage` metric datapoints (type=input/output) + `claude_code.cost.usage` if `costUSD` ok → POST metrics.
  - resource attrs: `service.name=claude-code`, `service.version` from record `version`, `os.type`=runtime.GOOS, `host.arch`=runtime.GOARCH.
  - identity via `ids.forCowork(transcriptPath)`.
  - best-effort POST helper (logs status via debuglog, drains body); shared by logs+metrics.
  - **never** put content strings in attributes.
- [ ] **Step 4** — run `go test ./internal/agent/promptlog/`; PASS.
- [ ] **Step 5** — `gofmt -w`; commit `feat(promptlog): Telemetry.Observe emits full-fidelity events + metrics from transcript lines`.

---

### Task 5: Watcher per-line observe hook

**Files:** Modify `internal/agent/watch/watch.go`; add test to `watch_test.go`.

**Produces:** `New(offer func(spool.Pointer), observe func(source, path string, line []byte), version string, poll, backfill)` — `observe` may be nil. `scanFile` calls `observe(source, path, line)` for every complete line, and still offers genuine prompts.

- [ ] **Step 1 — failing test** in `watch_test.go`: a watcher with an `observe` that appends `(source, line)`; write a transcript with a user line + an assistant line; backfill on; `pollOnce()` → observe called for BOTH lines (2), offer called only for the user prompt (1).
- [ ] **Step 2** — run `-run TestWatcherObserve`; FAIL.
- [ ] **Step 3 — implement**: add `observe` field + New param; in `scanFile`, refactor the read loop so each complete line is passed to `w.observe` (if non-nil) before/after prompt parsing; keep cursor + offer behavior identical. Update the existing `New(...)` call sites and `testWatcher` helper to pass `nil` observe (or a recorder).
- [ ] **Step 4** — run `go test ./internal/agent/watch/`; PASS (all existing watch tests still green).
- [ ] **Step 5** — `gofmt -w`; commit `feat(watch): per-line observe hook for telemetry`.

---

### Task 6: Daemon wiring

**Files:** Modify `internal/agent/daemon/daemon.go`.

- [ ] **Step 1 — failing/guard test** in a daemon test: `metricsEndpoint(base)` returns `base + /v1/metrics` (mirror `logsEndpoint`). Add `TestMetricsEndpoint`.
- [ ] **Step 2** — run; FAIL (undefined).
- [ ] **Step 3 — implement**: add `metricsEndpoint`; in the watch block, construct `tel := promptlog.New(logsEndpoint(cfg.Endpoint), metricsEndpoint(cfg.Endpoint), tok.Get, promptlog.SourcesFromEnv())` (drop the old actor-based emitter); `observe := func(source, path string, line []byte){ tel.Observe(source, path, line) }`; pass `offer` + `observe` to `watch.New`. Update the `watch.New` call to the new signature.
- [ ] **Step 4** — run `go build ./... && go test ./internal/agent/daemon/`; PASS.
- [ ] **Step 5** — `gofmt -w`; commit `feat(daemon): wire full-fidelity telemetry observer into the watcher`.

---

### Task 7: Docs + changelog + full verification

**Files:** `CHANGELOG.md`, `AGENTS.md` (capture section telemetry note).

- [ ] **Step 1** — CHANGELOG `## [0.9.2]`: full-fidelity telemetry — user_prompt + api_request + assistant_response logs + token/cost metrics, identity recovered from cowork session path/metadata; list the flagged gaps (duration_ms, terminal.type, user.id/account_id, active_time, derived cost). Note it supersedes v0.9.1's minimal emit.
- [ ] **Step 2** — AGENTS.md: update the capture note to say watched-source telemetry mirrors the CLI's OTEL (logs+metrics) host-side.
- [ ] **Step 3** — `go build ./... && go test ./...`; all pass; `gofmt -l internal/` clean.
- [ ] **Step 4** — commit `docs: v0.9.2 full-fidelity cowork telemetry`.
- [ ] **Step 5 — live verification** (before release): build keld-agent locally, stop the installed daemon, run the local build, make a Cowork prompt, and confirm in `~/.keld/agent.log` a `user_prompt` **and** `api_request` emit with `HTTP 200`; compare the emitted attribute set against the captured CLI schema. Restore the installed daemon. Then cut `v0.9.2` and install.

## Self-Review

- Spec coverage: identity (T1), cost (T2), OTLP (T3), Observe/events/metrics (T4), watcher hook (T5), wiring (T6), docs+verify (T7). All spec sections mapped.
- No text ever emitted (asserted in T3/T4 tests).
- Flagged gaps documented, not silently dropped.
- Types consistent: `kv`/`anyVal`/`logRecord`/`otlpLogs`/`metric`/`otlpMetrics` defined in T3, used in T4; `Telemetry.New`/`Observe` defined T4, wired T6; `watch.New` signature changed T5, called T6.
