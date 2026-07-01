# keld-agent P4 — Org Remote Control-Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** keld-agent daemons poll a per-org settings document from keld-atlas and apply it live (remote overrides local per key), so an admin can govern daemon behavior org-wide — starting with `include_entity_text`.

**Architecture:** Client-first. keld-cli gains a concurrency-safe live settings holder (replacing the static `includeEntityText` bool), an HTTP settings client, and a poller wired into `daemon.Run` (all local/TDD). Then keld-atlas gains a per-org settings model + `GET /v1/agent-settings` (token→org) + an admin set API/toggle.

**Tech Stack:** Go (`github.com/ncx-ai/keld-cli`), stdlib `net/http`/`httptest`, `sync`; keld-atlas (FastAPI + async SQLAlchemy + Alembic + Next/Vitest, in Docker).

## Global Constraints

- **Client-first.** keld-cli tasks (T1–T3) are fully local + TDD. keld-atlas tasks (T4–T6) run that repo's suite in Docker and **coordinate with the shared tree** (another session's unpushed wizard work is on atlas `main`; branch cleanly, touch only new files + a minimal additive settings-page change; hold if that session is editing the same areas).
- **Governance: remote overrides local, per key present.** Effective = local base, with each remote key that is present overlaid on top; a remote doc that omits a key reverts that key to local. (`Remote` fields are pointers so "absent" ≠ "false".)
- **Non-fatal client.** Any fetch error (network, 404 on older Atlas, decode) → keep last-known effective settings; never break the daemon. The endpoint may not exist yet during client rollout.
- **Poll-only, org-wide.** No push/websockets; no per-key enforced flag; no device targeting. Poll on startup + every 5m (`KELD_SETTINGS_POLL` overrides, for tests).
- **Daemon→Atlas auth header is `x-keld-ingest-token: <token>`** (mirror the publisher). The settings GET is read-only, org-scoped by that token.
- **Extensible + forward-compatible.** JSON doc `{"include_entity_text": bool, ...}`; the client applies only keys it knows and ignores unknown keys. Only `include_entity_text` this phase.
- Tenant isolation on the server (org-scoped everywhere).

## File Structure
- `internal/agent/settings/remote.go` (NEW) — `Remote` wire type (pointer fields).
- `internal/agent/settings/live.go` (NEW) — `Live` concurrency-safe effective-settings holder.
- `internal/agent/settings/client.go` (NEW) — HTTP `Client` fetching the org settings.
- `internal/agent/daemon/daemon.go` (MOD) — `settingsEndpoint`, live-value `Worker`, poller.
- keld-atlas: `models.py` (+ migration), `routers/agent_settings.py`, admin API + Settings-page toggle.

---

### Task 1: Live settings holder + Remote type (keld-cli, LOCAL/TDD)

**Files:** Create `internal/agent/settings/remote.go`, `internal/agent/settings/live.go`; Test `internal/agent/settings/live_test.go`.

**Interfaces:**
- Produces: `type Remote struct { IncludeEntityText *bool `json:"include_entity_text"` }`
- `type Live struct{...}`; `func NewLive(base Settings) *Live`; `func (l *Live) IncludeEntityText() bool`; `func (l *Live) Apply(r *Remote)` — recomputes effective from the local `base` with the remote overlaid (present keys only). Concurrency-safe.

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"sync"
	"testing"
)

func ptrBool(b bool) *bool { return &b }

func TestLiveRemoteOverridesLocalPerKey(t *testing.T) {
	l := NewLive(Settings{IncludeEntityText: true}) // local base = true
	if !l.IncludeEntityText() {
		t.Fatal("base should be true before any Apply")
	}
	l.Apply(&Remote{IncludeEntityText: ptrBool(false)}) // remote present → overrides
	if l.IncludeEntityText() {
		t.Fatal("remote false should override local true")
	}
	l.Apply(&Remote{}) // remote omits the key → revert to local base (true)
	if !l.IncludeEntityText() {
		t.Fatal("absent remote key should revert to local base")
	}
	l.Apply(nil) // nil remote → local base
	if !l.IncludeEntityText() {
		t.Fatal("nil remote → local base")
	}
}

func TestLiveConcurrentApplyAndRead(t *testing.T) {
	l := NewLive(Settings{IncludeEntityText: true})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); for i := 0; i < 1000; i++ { l.Apply(&Remote{IncludeEntityText: ptrBool(i%2 == 0)}) } }()
	go func() { defer wg.Done(); for i := 0; i < 1000; i++ { _ = l.IncludeEntityText() } }()
	wg.Wait()
}
```

- [ ] **Step 2: Run to verify fail** — `cd ~/keld/keld-cli && go test ./internal/agent/settings/ -run Live -race -v` → FAIL (undefined `NewLive`/`Remote`).

- [ ] **Step 3: Implement**

`internal/agent/settings/remote.go`:
```go
package settings

// Remote is the org settings document served by keld-atlas. Fields are pointers
// so an absent key ("not set by the org") is distinct from an explicit false.
type Remote struct {
	IncludeEntityText *bool `json:"include_entity_text"`
}
```

`internal/agent/settings/live.go`:
```go
package settings

import "sync"

// Live holds the effective settings — the local base with the org's remote doc
// overlaid (remote-wins per key present). Safe for concurrent Apply/read.
type Live struct {
	mu   sync.RWMutex
	base Settings // local ~/.keld/agent-config.json, loaded once at startup
	eff  Settings // effective = base + remote overlay
}

func NewLive(base Settings) *Live { return &Live{base: base, eff: base} }

// Apply recomputes the effective settings from the local base with the remote
// keys that are present overlaid. A nil remote (or one omitting a key) leaves
// that key at the local base value.
func (l *Live) Apply(r *Remote) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.base
	if r != nil {
		if r.IncludeEntityText != nil {
			e.IncludeEntityText = *r.IncludeEntityText
		}
	}
	l.eff = e
}

func (l *Live) IncludeEntityText() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.eff.IncludeEntityText
}
```

- [ ] **Step 4: Run to verify pass** — `go test ./internal/agent/settings/ -race -v` → PASS. Then `go build ./... && go vet ./...`.

- [ ] **Step 5: Commit** — `git add internal/agent/settings/ && git commit -m "feat(settings): live holder + Remote (remote-overrides-local per key)"`

---

### Task 2: Settings HTTP client (keld-cli, LOCAL/TDD)

**Files:** Create `internal/agent/settings/client.go`; Test `internal/agent/settings/client_test.go`.

**Interfaces:**
- Consumes: `Remote` (Task 1).
- Produces: `func NewClient(url, token string, timeout time.Duration) *Client`; `func (c *Client) Fetch(ctx context.Context) (*Remote, error)` — GET `url` with header `x-keld-ingest-token: <token>`; non-200 or decode error → error; else the parsed `*Remote`.

- [ ] **Step 1: Write the failing test**

```go
package settings

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientFetchParsesAndSendsToken(t *testing.T) {
	var gotTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTok = r.Header.Get("x-keld-ingest-token")
		w.Write([]byte(`{"include_entity_text": false}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "tok123", 5*time.Second)
	r, err := c.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if gotTok != "tok123" {
		t.Fatalf("token header = %q, want tok123", gotTok)
	}
	if r.IncludeEntityText == nil || *r.IncludeEntityText != false {
		t.Fatalf("include_entity_text = %v, want present false", r.IncludeEntityText)
	}
}

func TestClientFetchErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // older Atlas without the endpoint
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, "t", time.Second).Fetch(t.Context()); err == nil {
		t.Fatal("404 should surface as an error (poller keeps last-known)")
	}
}
```

- [ ] **Step 2: Run to verify fail** — `go test ./internal/agent/settings/ -run Client -v` → FAIL.

- [ ] **Step 3: Implement** `internal/agent/settings/client.go`:
```go
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	url, token string
	hc         *http.Client
}

func NewClient(url, token string, timeout time.Duration) *Client {
	return &Client{url: url, token: token, hc: &http.Client{Timeout: timeout}}
}

// Fetch GETs the org settings document. Errors (including a 404 on an Atlas that
// predates the endpoint) surface so the caller can keep the last-known settings.
func (c *Client) Fetch(ctx context.Context) (*Remote, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-keld-ingest-token", c.token)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent-settings: status %d", resp.StatusCode)
	}
	var r Remote
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}
```

- [ ] **Step 4: Run to verify pass** — `go test ./internal/agent/settings/ -v` → PASS; `go build ./... && go vet ./...`.

- [ ] **Step 5: Commit** — `git commit -m "feat(settings): HTTP client for GET /v1/agent-settings"`

---

### Task 3: Poller + live-apply wiring in daemon.Run (keld-cli, LOCAL/TDD)

**Files:** Modify `internal/agent/daemon/daemon.go`; Test `internal/agent/daemon/settings_poll_test.go`.

**Interfaces:**
- Consumes: `settings.NewLive`, `settings.NewClient`, `settings.Live.Apply`/`IncludeEntityText` (Tasks 1–2).
- Changes: `Worker`'s `includeEntityText bool` param becomes `includeEntityText func() bool`; `process` calls it per job. Adds `settingsEndpoint(ingest string) string` (mirrors `enrichEndpoint` → `<base>/v1/agent-settings`) and `pollSettings(ctx, *settings.Client, *settings.Live, time.Duration)`.

- [ ] **Step 1: Write the failing test** (stub settings server; local base true, remote false → effective false after poll)

```go
package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/settings"
)

func TestPollSettingsAppliesRemoteOverLocal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"include_entity_text": false}`))
	}))
	defer srv.Close()
	live := settings.NewLive(settings.Settings{IncludeEntityText: true}) // local base true
	client := settings.NewClient(srv.URL, "tok", 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// one startup fetch is enough for the assertion; a long interval keeps the ticker quiet
	go pollSettings(ctx, client, live, time.Hour)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && live.IncludeEntityText() {
		time.Sleep(10 * time.Millisecond)
	}
	if live.IncludeEntityText() {
		t.Fatal("poller should have applied remote include_entity_text=false over local true")
	}
}

func TestSettingsEndpoint(t *testing.T) {
	if got := settingsEndpoint("https://atlas.example/v1/ingest"); got != "https://atlas.example/v1/agent-settings" {
		t.Fatalf("settingsEndpoint = %q", got)
	}
}
```
(Add `"context"` to the test imports.)

- [ ] **Step 2: Run to verify fail** — `go test ./internal/agent/daemon/ -run 'PollSettings|SettingsEndpoint' -race -v` → FAIL (undefined).

- [ ] **Step 3: Implement** in `daemon.go`:
  1. `settingsEndpoint` (next to `enrichEndpoint`):
     ```go
     func settingsEndpoint(ingest string) string {
         if i := strings.Index(ingest, "/v1/"); i >= 0 {
             return ingest[:i] + "/v1/agent-settings"
         }
         return strings.TrimRight(ingest, "/") + "/v1/agent-settings"
     }
     ```
  2. `pollSettings`:
     ```go
     func pollSettings(ctx context.Context, c *settings.Client, live *settings.Live, interval time.Duration) {
         apply := func() {
             if r, err := c.Fetch(ctx); err == nil {
                 live.Apply(r)
             } else {
                 log.Printf("keld-agent: settings poll failed (keeping current): %v", err)
             }
         }
         apply() // startup
         t := time.NewTicker(interval)
         defer t.Stop()
         for {
             select {
             case <-ctx.Done():
                 return
             case <-t.C:
                 apply()
             }
         }
     }
     ```
  3. `Worker` signature: `includeEntityText func() bool`; in `process`, `publish.Build(j, profile, actor, includeEntityText(), time.Now())` (call it per job). Update the `process` signature + its one call site.
  4. In `Run`: build the live holder + start the poller, and pass the live getter to `Worker`:
     ```go
     live := settings.NewLive(set)
     pollInterval := 5 * time.Minute
     if v := os.Getenv("KELD_SETTINGS_POLL"); v != "" {
         if d, err := time.ParseDuration(v); err == nil {
             pollInterval = d
         }
     }
     go pollSettings(ctx, settings.NewClient(settingsEndpoint(cfg.Endpoint), cfg.IngestToken, 10*time.Second), live, pollInterval)
     go Worker(q, model, pub, actor, live.IncludeEntityText, gate, admit)
     ```
     (Replaces the old `set.IncludeEntityText` bool arg.)

- [ ] **Step 4: Run to verify pass** — `go test ./internal/agent/daemon/ -race -v` (poll + endpoint tests + existing daemon tests) → PASS; full `go test ./... -race` green; `go vet ./...`.

- [ ] **Step 5: Commit** — `git commit -m "feat(daemon): poll org settings + live-apply include_entity_text"`

---

### Task 4: keld-atlas — org_agent_settings model + migration (ATLAS; docker)

**Repo:** keld-atlas. **Coordinate** (branch off atlas `main`; new files only). Tests run in Docker (never host Python 3.14).

**Files:** Modify `services/api/app/models.py`; Create `services/api/alembic/versions/0027_org_agent_settings.py`; Test `services/api/tests/test_agent_settings.py`.

- [ ] **Step 1: Add the model** (`models.py`), mirroring existing org-scoped tables:
```python
class OrgAgentSettings(Base):
    __tablename__ = "org_agent_settings"
    org_id: Mapped[uuid.UUID] = mapped_column(ForeignKey("organizations.id", ondelete="CASCADE"), primary_key=True)
    include_entity_text: Mapped[bool] = mapped_column(default=False, nullable=False)
    updated_at: Mapped[datetime] = mapped_column(server_default=func.now(), onupdate=func.now())
```
(Match the exact `Mapped`/`mapped_column` import + column style already used in models.py.)

- [ ] **Step 2: Alembic migration** `0027_org_agent_settings.py` — `down_revision = "0026_fix_anthropic_cost_cents"` (verify current head with `alembic heads` first); `create_table("org_agent_settings", ...)` with the columns above; `downgrade` drops it.

- [ ] **Step 3: Test** (`test_agent_settings.py`) — apply migration + a round-trip: insert a row for an org, read it back; default `include_entity_text` is false when unset.
  Run: `docker compose exec -T -e KELD_TEST_DATABASE_URL=... -e KELD_TEST_REDIS_URL=... api sh -lc 'cd /app && alembic upgrade head && python -m pytest tests/test_agent_settings.py -q'`

- [ ] **Step 4: Commit** — `git commit -m "feat(agent-settings): org_agent_settings model + migration"`

---

### Task 5: keld-atlas — GET /v1/agent-settings + admin set API (ATLAS; docker)

**Files:** Create `services/api/app/routers/agent_settings.py`; register it in the app; Test `services/api/tests/test_agent_settings_api.py`.

**Interfaces:**
- `GET /v1/agent-settings` — daemon-authed via the existing ingest-token resolver (mirror `routers/enrichments.py` / `otel.py`): resolve token→org, return `{"include_entity_text": <bool>}` (the org's row, or `false` default when no row). Org-scoped.
- Admin: `GET /api/org-settings` + `PATCH /api/org-settings` (`Depends(require_admin)`, `current_org`) to read/set `include_entity_text` (upsert the row).

- [ ] **Step 1: Write the failing tests** — (a) daemon GET with a valid ingest token returns the org's value (and default false when unset); (b) daemon GET with a bad/missing token → 401/403; (c) admin PATCH sets the value and the daemon GET reflects it; (d) cross-org isolation (org A's token never sees org B's value). Use the repo's existing token/admin fixtures.

- [ ] **Step 2: Run to verify fail** — the pytest above → FAIL (router missing).

- [ ] **Step 3: Implement** the router mirroring `enrichments.py` for the token→org dependency and `require_admin`/`current_org` for the admin endpoints; upsert on PATCH (`on_conflict_do_update` by `org_id`, like other upserts in the repo). Register the router in the FastAPI app.

- [ ] **Step 4: Run to verify pass** — `docker compose exec -T ... api sh -lc 'cd /app && python -m pytest tests/test_agent_settings_api.py -q'` → PASS; run the broader router suite to check no regression.

- [ ] **Step 5: Commit** — `git commit -m "feat(agent-settings): GET /v1/agent-settings + admin read/set API"`

---

### Task 6: keld-atlas — minimal admin Settings-page toggle (ATLAS web; coordinate)

**Files:** `services/web/…` admin Settings page (additive) + a hook for the org-settings API; Test with Vitest.

**Coordinate:** the other session touches the web tree — make this **additive** (a single toggle + a small hook), avoid their files, and rebase/branch off atlas `main`.

- [ ] **Step 1: Add a `useOrgSettings` hook** (React Query) — GET/PATCH `/api/org-settings`, mirroring an existing admin hook's shape.
- [ ] **Step 2: Add a single "Include entity text (send domain-entity surface text to Atlas)" toggle** to the existing admin Settings page, wired to the hook. Keep copy minimal + accurate (default off; sensitivity spans always masked regardless).
- [ ] **Step 3: Vitest** — the toggle reads the current value and PATCHes on change (mock the hook/endpoint). Run `cd services/web && pnpm exec vitest run` (full suite green).
- [ ] **Step 4: Commit** — `git commit -m "feat(web): admin toggle for org include_entity_text"`

---

## Notes for the executor
- **T1–T3 are keld-cli, fully local + TDD + `-race`.** Do these first; they ship independently — until the Atlas endpoint exists, the poller's 404 is non-fatal and the daemon keeps local settings.
- **T4–T6 are keld-atlas.** Run that repo's suite in Docker (never host Python 3.14). **Coordinate with the concurrent session** on atlas `main`: branch off `main`, keep changes to new files + one additive settings-page toggle, and hold if that session is mid-edit in models/routers/settings-page. The two-repo end-to-end wiring (daemon actually reading a real Atlas org setting) is verified after T5 (curl the endpoint with a real ingest token) and via the daemon foreground + the settings poll.
- **Keep it non-fatal + org-scoped** throughout; remote-overrides-local per present key is the one governance rule.
