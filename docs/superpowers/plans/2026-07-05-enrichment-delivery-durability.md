# Durable Enrichment Delivery (spool fallback) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop silently losing enrichment when the daemon is down — the hook spools each enrich pointer to disk on delivery failure, and the daemon drains the spool on startup + a periodic sweep.

**Architecture:** New pure-data `internal/spool` package (atomic write + oldest-first drain + cap + poison quarantine). The hook keeps its fast HTTP POST and spools the pointer on any miss. Ingress and the daemon drain build the identical `queue.Job` via a shared `ingress.JobFrom(spool.Pointer)` mapper.

**Tech Stack:** Go (module `github.com/ncx-ai/keld-signal`), stdlib only (`os`, `encoding/json`, `path/filepath`).

## Global Constraints

- No prompt **text** is ever spooled — only the pointer (transcript path + prompt id), exactly like the HTTP payload.
- All spool operations are best-effort and panic-free: a broken spool must never block the hook or crash the daemon.
- Spool dir `0700`, files `0600` (mirrors `~/.keld` secret handling).
- Cap `KELD_SPOOL_MAX` (default 500); over cap drop **oldest**, count drops via `debuglog` (never silent).
- Sweep interval `KELD_SPOOL_SWEEP` (default `30s`).
- Tests set `KELD_HOME` to a `t.TempDir()` so they never touch the real `~/.keld`.
- Run tests with `go test ./...` (host Go toolchain).

---

### Task 1: `internal/spool` package + `paths.SpoolDir`

**Files:**
- Create: `internal/spool/spool.go`
- Create: `internal/spool/spool_test.go`
- Modify: `internal/paths/paths.go` (add `SpoolDir`)

**Interfaces:**
- Produces:
  - `spool.Pointer` (+ nested `Source`, `Correlation`, `Ptr`, `Inline`) — JSON shape identical to the `/enrich` body.
  - `spool.Write(p Pointer) error`
  - `spool.Drain(fn func(Pointer) error) (int, error)`
  - `paths.SpoolDir() string` → `<KELD_HOME>/spool`

- [ ] **Step 1: Add `paths.SpoolDir`**

In `internal/paths/paths.go`, after `ModelsDir`:

```go
// SpoolDir is the on-disk queue of undelivered enrich pointers (hook writes,
// daemon drains). Sibling of models/ under KELD_HOME.
func SpoolDir() string { return filepath.Join(KeldHome(), "spool") }
```

- [ ] **Step 2: Write the failing test**

```go
// internal/spool/spool_test.go
package spool

import (
	"os"
	"path/filepath"
	"testing"
)

func ptr(id string) Pointer {
	return Pointer{
		Source:      Source{ID: "claude_code", Origin: "hook"},
		Correlation: Correlation{Scheme: "prompt_id", ID: id, SessionID: "S1"},
		Pointer:     &Ptr{TranscriptPath: "/t/x.jsonl", PromptID: id, Cwd: "/cwd"},
	}
}

func TestWriteThenDrainRoundTrips(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Write(ptr("P1")); err != nil {
		t.Fatal(err)
	}
	var got []string
	n, err := Drain(func(p Pointer) error { got = append(got, p.Correlation.ID); return nil })
	if err != nil || n != 1 || len(got) != 1 || got[0] != "P1" {
		t.Fatalf("drain: n=%d got=%v err=%v", n, got, err)
	}
	// success deletes the file
	files, _ := filepath.Glob(filepath.Join(os.Getenv("KELD_HOME"), "spool", "*.json"))
	if len(files) != 0 {
		t.Fatalf("expected spool empty after drain, found %v", files)
	}
}

func TestFileIsOwnerOnly(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	Write(ptr("P1"))
	files, _ := filepath.Glob(filepath.Join(os.Getenv("KELD_HOME"), "spool", "*.json"))
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %v", files)
	}
	fi, _ := os.Stat(files[0])
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("want 0600, got %o", fi.Mode().Perm())
	}
}

func TestDrainLeavesFileOnHandlerError(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	Write(ptr("P1"))
	n, _ := Drain(func(p Pointer) error { return os.ErrClosed })
	if n != 0 {
		t.Fatalf("want 0 drained, got %d", n)
	}
	files, _ := filepath.Glob(filepath.Join(os.Getenv("KELD_HOME"), "spool", "*.json"))
	if len(files) != 1 {
		t.Fatalf("file should remain after handler error, got %v", files)
	}
}

func TestCapDropsOldest(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_SPOOL_MAX", "2")
	Write(ptr("A"))
	Write(ptr("B"))
	Write(ptr("C")) // over cap -> oldest (A) dropped
	seen := map[string]bool{}
	Drain(func(p Pointer) error { seen[p.Correlation.ID] = true; return nil })
	if seen["A"] || !seen["B"] || !seen["C"] {
		t.Fatalf("cap: expected B,C kept and A dropped; seen=%v", seen)
	}
}

func TestDrainQuarantinesPoison(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "x")
	_ = dir
	sp := filepath.Join(os.Getenv("KELD_HOME"), "spool")
	os.MkdirAll(sp, 0o700)
	os.WriteFile(filepath.Join(sp, "bad.json"), []byte("{not json"), 0o600)
	n, _ := Drain(func(p Pointer) error { return nil })
	if n != 0 {
		t.Fatalf("poison should not count as drained")
	}
	if _, err := os.Stat(filepath.Join(sp, "bad", "bad.json")); err != nil {
		t.Fatalf("poison file should be quarantined to spool/bad/: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd ~/keld/keld-signal && go test ./internal/spool/`
Expected: FAIL — package/undefined `Write`, `Drain`, `Pointer`.

- [ ] **Step 4: Write the implementation**

```go
// internal/spool/spool.go
// Package spool is the on-disk fallback queue for enrich pointers. The hook
// writes a pointer here when the daemon is unreachable; the daemon drains it on
// startup and on a periodic sweep. Only the pointer is stored — never prompt text.
package spool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

type Source struct {
	ID      string `json:"id"`
	Origin  string `json:"origin"`
	Version string `json:"version,omitempty"`
}
type Correlation struct {
	Scheme    string `json:"scheme"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
}
type Ptr struct {
	TranscriptPath string `json:"transcript_path"`
	PromptID       string `json:"prompt_id"`
	Cwd            string `json:"cwd"`
}
type Inline struct {
	Text string `json:"text"`
}

// Pointer is the enrich payload — identical JSON shape to the /enrich body.
type Pointer struct {
	Source      Source      `json:"source"`
	Correlation Correlation `json:"correlation"`
	Pointer     *Ptr        `json:"pointer,omitempty"`
	Inline      *Inline     `json:"inline,omitempty"`
}

func maxFiles() int {
	if v := os.Getenv("KELD_SPOOL_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 500
}

var safe = strings.NewReplacer("/", "_", "\\", "_", "..", "_", string(os.PathSeparator), "_")

func fileName(p Pointer) string {
	id := safe.Replace(p.Correlation.ID)
	if id == "" {
		id = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return id + ".json"
}

// jsonFiles returns spool/*.json sorted oldest-first by mtime.
func jsonFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type fe struct {
		path string
		mod  time.Time
	}
	var fs []fe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		fs = append(fs, fe{filepath.Join(dir, e.Name()), info.ModTime()})
	}
	sort.Slice(fs, func(i, j int) bool { return fs[i].mod.Before(fs[j].mod) })
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.path
	}
	return out
}

// Write atomically persists a pointer, enforcing the cap first.
func Write(p Pointer) error {
	dir := paths.SpoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Enforce cap: drop oldest so we keep at most maxFiles()-1 before adding one.
	if files := jsonFiles(dir); len(files) >= maxFiles() {
		drop := len(files) - maxFiles() + 1
		for i := 0; i < drop && i < len(files); i++ {
			os.Remove(files[i])
		}
		debuglog.Append("spool: cap %d reached, dropped %d oldest", maxFiles(), drop)
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	final := filepath.Join(dir, fileName(p))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// Drain applies fn to each spooled pointer oldest-first. On fn success the file
// is deleted; on fn error it is left for the next sweep; on decode error it is
// quarantined to spool/bad/. Returns the number successfully drained.
func Drain(fn func(Pointer) error) (int, error) {
	dir := paths.SpoolDir()
	n := 0
	for _, path := range jsonFiles(dir) {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var p Pointer
		if err := json.Unmarshal(b, &p); err != nil {
			quarantine(dir, path)
			continue
		}
		if err := fn(p); err != nil {
			continue // leave for retry
		}
		os.Remove(path)
		n++
	}
	return n, nil
}

func quarantine(dir, path string) {
	bad := filepath.Join(dir, "bad")
	if os.MkdirAll(bad, 0o700) == nil {
		if err := os.Rename(path, filepath.Join(bad, filepath.Base(path))); err != nil {
			os.Remove(path) // last resort: never let poison block the drain
		}
		debuglog.Append("spool: quarantined poison file %s", filepath.Base(path))
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd ~/keld/keld-signal && go test ./internal/spool/ ./internal/paths/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd ~/keld/keld-signal
git add internal/spool/ internal/paths/paths.go
git commit -m "feat(spool): on-disk enrich-pointer queue (atomic write, oldest-first drain, cap, poison quarantine)"
```

---

### Task 2: Ingress uses `spool.Pointer` + shared `JobFrom` mapper

**Files:**
- Modify: `internal/agent/ingress/ingress.go`
- Test: `internal/agent/ingress/ingress_test.go` (append)

**Interfaces:**
- Consumes: `spool.Pointer`, `queue.Job`.
- Produces: `ingress.JobFrom(p spool.Pointer) queue.Job`.

- [ ] **Step 1: Write the failing test** (append before any `__main__`/end)

```go
// internal/agent/ingress/ingress_test.go  (append)
func TestJobFromMapsAllFields(t *testing.T) {
	p := spool.Pointer{
		Source:      spool.Source{ID: "claude_code", Origin: "hook", Version: "1"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: "P1", SessionID: "S1"},
		Pointer:     &spool.Ptr{TranscriptPath: "/t.jsonl", PromptID: "P1", Cwd: "/c"},
	}
	j := JobFrom(p)
	if j.Source != "claude_code" || j.Origin != "hook" || j.Version != "1" ||
		j.Scheme != "prompt_id" || j.ID != "P1" || j.SessionID != "S1" ||
		j.TranscriptPath != "/t.jsonl" || j.PromptID != "P1" || j.Cwd != "/c" {
		t.Fatalf("JobFrom mismapped: %+v", j)
	}
}
```

Add imports `spool` and `testing` to the test file if missing.

- [ ] **Step 2: Run to verify it fails**

Run: `cd ~/keld/keld-signal && go test ./internal/agent/ingress/ -run JobFrom`
Expected: FAIL — `JobFrom` undefined.

- [ ] **Step 3: Refactor `ingress.go` to use `spool.Pointer` + `JobFrom`**

Replace the local `source`/`correlation`/`pointer`/`inline`/`Request` structs and the inline job-building with `spool.Pointer` and a shared mapper. New `ingress.go` body:

```go
package ingress

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// JobFrom builds the queue.Job for an enrich pointer. Shared by the HTTP handler
// and the daemon's spool drain so both paths enqueue identically.
func JobFrom(p spool.Pointer) queue.Job {
	j := queue.Job{
		Source:    p.Source.ID,
		Origin:    p.Source.Origin,
		Version:   p.Source.Version,
		Scheme:    p.Correlation.Scheme,
		ID:        p.Correlation.ID,
		SessionID: p.Correlation.SessionID,
	}
	if p.Pointer != nil {
		j.TranscriptPath = p.Pointer.TranscriptPath
		j.Cwd = p.Pointer.Cwd
		j.PromptID = p.Pointer.PromptID
	}
	if p.Inline != nil {
		j.Inline = p.Inline.Text
	}
	return j
}

// Handler returns the daemon's HTTP handler.
func Handler(q *queue.Queue, secret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/enrich", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("x-keld-agent-secret")), []byte(secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB cap
		var p spool.Pointer
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if q.Offer(JobFrom(p)) {
			w.WriteHeader(http.StatusAccepted)
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
	return mux
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd ~/keld/keld-signal && go test ./internal/agent/ingress/`
Expected: PASS (existing HTTP tests still green — the wire shape is unchanged; only the struct backing it moved to `spool`).

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-signal
git add internal/agent/ingress/
git commit -m "refactor(ingress): decode into spool.Pointer + shared JobFrom mapper"
```

---

### Task 3: Hook spools the pointer on delivery failure

**Files:**
- Modify: `internal/hook/forward.go`
- Test: `internal/hook/forward_test.go` (append)

**Interfaces:**
- Consumes: `spool.Write`, `spool.Pointer`, `agentcfg.Read`.

- [ ] **Step 1: Write the failing test** (append)

```go
// internal/hook/forward_test.go  (append)
func TestForwardSpoolsWhenDaemonUnreachable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	// agent.json with a dead port -> POST fails -> must spool.
	if err := agentcfg.Write(agentcfg.Info{Port: 1, Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	forwardToAgent("claude_code", "S1", "Pspool", "/t/x.jsonl", "/cwd")

	var ids []string
	spool.Drain(func(p spool.Pointer) error { ids = append(ids, p.Correlation.ID); return nil })
	if len(ids) != 1 || ids[0] != "Pspool" {
		t.Fatalf("expected pointer Pspool spooled, got %v", ids)
	}
}

func TestForwardSpoolsWhenNoDaemonInfo(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir()) // no agent.json at all
	forwardToAgent("claude_code", "S1", "Pnoinfo", "/t/x.jsonl", "/cwd")
	n, _ := spool.Drain(func(p spool.Pointer) error { return nil })
	if n != 1 {
		t.Fatalf("expected 1 spooled when daemon info missing, got %d", n)
	}
}
```

Ensure the test file imports `agentcfg` and `spool`. Confirm the **existing** forward tests set `KELD_HOME` to a temp dir (they call `forwardToAgent` and previously relied on a dead/stub daemon); if any existing test now spools into a real `~/.keld`, add `t.Setenv("KELD_HOME", t.TempDir())` to it.

- [ ] **Step 2: Run to verify it fails**

Run: `cd ~/keld/keld-signal && go test ./internal/hook/ -run Spool`
Expected: FAIL — pointer not spooled (no fallback yet).

- [ ] **Step 3: Implement the spool fallback in `forward.go`**

Rewrite `forwardToAgent` to build a `spool.Pointer` once, try the POST, and spool on any miss:

```go
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// forwardToAgent best-effort delivers an enrich pointer to the local daemon:
// fast path is an HTTP POST; on any miss (no daemon info, transport error, or
// non-2xx) the pointer is spooled to disk so the daemon can drain it later.
// Silent-skip toward the host tool: never returns an error, never blocks it, and
// never records prompt text.
func forwardToAgent(source, sessionID, promptID, transcriptPath, cwd string) {
	if promptID == "" {
		return
	}
	p := spool.Pointer{
		Source:      spool.Source{ID: source, Origin: "hook"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: promptID, SessionID: sessionID},
		Pointer:     &spool.Ptr{TranscriptPath: transcriptPath, PromptID: promptID, Cwd: cwd},
	}
	if !postToAgent(p) {
		if err := spool.Write(p); err != nil {
			debuglog.Append("forward: spool write failed (prompt_id=%s): %v", promptID, err)
		} else {
			debuglog.Append("forward: daemon unreachable, spooled pointer (prompt_id=%s)", promptID)
		}
	}
}

// postToAgent POSTs the pointer to the daemon. Returns true only on a 2xx.
func postToAgent(p spool.Pointer) bool {
	info, err := agentcfg.Read()
	if err != nil || info == nil || info.Port == 0 {
		return false
	}
	body, err := json.Marshal(p)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/enrich", info.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-agent-secret", info.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		debuglog.Append("forward: POST %s failed (prompt_id=%s): %v", url, p.Correlation.ID, err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		debuglog.Append("forward: POST %s returned %d (prompt_id=%s)", url, resp.StatusCode, p.Correlation.ID)
		return false
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `cd ~/keld/keld-signal && go test ./internal/hook/`
Expected: PASS (new spool tests + existing forward tests).

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-signal
git add internal/hook/forward.go internal/hook/forward_test.go
git commit -m "feat(hook): spool enrich pointer on delivery failure (no more silent loss)"
```

---

### Task 4: Daemon drains the spool on startup + periodic sweep

**Files:**
- Modify: `internal/agent/daemon/daemon.go`

**Interfaces:**
- Consumes: `spool.Drain`, `ingress.JobFrom`, `queue.Queue.Offer`.

- [ ] **Step 1: Add the drain wiring in `Run`**

In `internal/agent/daemon/daemon.go`, add imports `"errors"`, `"github.com/ncx-ai/keld-signal/internal/spool"` (ingress + queue already imported). Immediately before `return serve(ctx, ln, ingress.Handler(q, secret), q)`:

```go
	// Drain any enrich pointers the hook spooled while the daemon was down, then
	// keep sweeping for ones spooled during brief unavailability. Idempotent:
	// delete-after-enqueue + Atlas dedups on dedup_key.
	drainSpool := func() {
		spool.Drain(func(p spool.Pointer) error {
			if q.Offer(ingress.JobFrom(p)) {
				return nil
			}
			return errQueueFull // queue full: keep the file, retry next sweep
		})
	}
	drainSpool()
	go func() {
		iv := 30 * time.Second
		if v := os.Getenv("KELD_SPOOL_SWEEP"); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				iv = d
			}
		}
		t := time.NewTicker(iv)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				drainSpool()
			}
		}
	}()
```

Add the sentinel near the top of the file (package level):

```go
var errQueueFull = errors.New("queue full")
```

- [ ] **Step 2: Build + vet**

Run: `cd ~/keld/keld-signal && go build ./... && go vet ./...`
Expected: clean.

- [ ] **Step 3: Full test suite**

Run: `cd ~/keld/keld-signal && go test ./...`
Expected: PASS (pre-existing `internal/agent/daemon` `KELD_SIDECAR_BIN` env tests may fail on a host with a real sidecar installed — unrelated to this change; verify they fail identically on `main`).

- [ ] **Step 4: Commit**

```bash
cd ~/keld/keld-signal
git add internal/agent/daemon/daemon.go
git commit -m "feat(daemon): drain spooled enrich pointers on startup + periodic sweep"
```

---

### Task 5: End-to-end verification + deploy

**Files:** none (ops).

- [ ] **Step 1: gofmt gate (CI parity)**

Run: `cd ~/keld/keld-signal && gofmt -l internal/spool internal/hook internal/agent/ingress internal/agent/daemon internal/paths`
Expected: empty. If not, `gofmt -w` those paths and amend.

- [ ] **Step 2: Simulated-outage integration check**

```bash
cd ~/keld/keld-signal
KELD_HOME=$(mktemp -d) go test ./internal/spool/ ./internal/hook/ ./internal/agent/ingress/ -count=1 -v
```
Expected: spool round-trip, hook-spools-on-failure, and JobFrom tests all PASS.

- [ ] **Step 3: Deploy (rebuild hook + daemon, restart)**

```bash
make build-binaries          # rebuild keld (hook) + keld-agent with the fix
make sidecar                 # ensure sidecar venv present
systemctl --user restart keld-agent.service
```

- [ ] **Step 4: Live verify the fallback**

```bash
# Stop the daemon, fire a hook enrich (it must spool), restart, confirm it drains.
systemctl --user stop keld-agent.service
KELD_HOME=$HOME/.keld ~/.local/bin/keld __hook --source claude_code </dev/null || true   # or send a real prompt
ls ~/.keld/spool/*.json            # expect >=1 spooled pointer while daemon down
systemctl --user start keld-agent.service
sleep 5
ls ~/.keld/spool/*.json 2>/dev/null || echo "spool drained ✓"   # expect empty after drain
```
Expected: a pointer appears in `~/.keld/spool` while the daemon is down, and is gone after restart (drained → enqueued → published). Then a fresh prompt in Claude Code shows enrichment (and a compliance flag for a sensitive one) in the Activity UI.

- [ ] **Step 5: (separate) deploy the memory-eviction build** so the daemon stops getting OOM-restarted in the first place — that build is already on `main`; the rebuild in Step 3 includes it.

---

## Self-Review

**Spec coverage:** §3.1 spool → Task 1; §3.2 hook → Task 3; §3.3 ingress mapper → Task 2; §3.4 daemon drain+sweep → Task 4; §3.5 paths → Task 1; §5 perms/cap/poison → Task 1 (tests) ; §6 tests → Tasks 1–3; §7 rollout → Task 5. ✓

**Placeholder scan:** every code step has complete code; commands have expected output; no TBD. ✓

**Type consistency:** `spool.Pointer`/`Source`/`Correlation`/`Ptr`/`Inline` defined in Task 1, consumed identically in Tasks 2–3; `ingress.JobFrom(spool.Pointer) queue.Job` defined Task 2, used Task 4; `spool.Write`/`Drain` signatures consistent across tasks; `errQueueFull` defined + used in Task 4. ✓
