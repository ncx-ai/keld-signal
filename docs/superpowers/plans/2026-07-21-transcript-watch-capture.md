# Non-CLI prompt capture via transcript watcher — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Capture prompts from Claude Code (all launch surfaces incl. the Desktop app) and Cowork — which don't fire the `keld __hook` command hook — by tailing their on-disk JSONL transcripts and feeding the existing resolve → enrich → publish pipeline.

**Architecture:** A new `internal/agent/watch` package runs a poll loop in the daemon. It discovers Claude-Code-format transcript roots (`~/.claude/projects` and the nested Cowork `.claude/projects` trees), scans each file forward from a persisted byte cursor, and for every new genuine user-prompt record synthesizes the *same* `spool.Pointer` the hook produces, handing it to `q.Offer(ingress.JobFrom(p))`. Nothing downstream of the queue changes. The queue's dedup is extended with a bounded recently-completed ring buffer so a prompt caught by both the hook and the watcher is enriched once.

**Tech Stack:** Go. Standard library only (no new deps). Module `github.com/ncx-ai/keld-signal`.

## Global Constraints

- **Module path:** `github.com/ncx-ai/keld-signal` — use full import paths.
- **gofmt is a CI gate:** run `gofmt -w <files>` before every commit (`go test` does not check formatting).
- **ML-only, no new backend:** the watcher is a capture *trigger*; it must not add an enrichment backend or bypass the queue/single-flight worker.
- **No schema bump:** this is capture-only. Do **not** change `enrich.SchemaVersion`. No eval gold changes.
- **Privacy invariant:** raw prompt text never leaves the machine. The watcher only ever emits a `spool.Pointer` (path + promptId + cwd) — never text — exactly like the hook.
- **Platforms:** macOS + Linux only. Windows paths are out of scope (deferred).
- **Source naming:** `claude_code` for `~/.claude/projects`; `cowork` for the Cowork nested trees. `Source.Origin = "watch"`.
- **Cowork is knowledge work:** it must stay OUT of `enrich.interactiveCodingTools` and `enrich.codingTools` (topical classification, no compositional-`eng` rule). This is already true in the code — do not add cowork to either map.

---

### Task 1: Queue recently-completed dedup ring buffer

Closes the hook↔watcher overlap: the queue currently drops a key from `inflight` at **dequeue** (`Next`), not completion, so a duplicate offered while the worker is processing (the common hook-vs-watcher timing) is NOT deduped. Add a bounded recently-completed set, populated at dequeue and checked in `Offer`.

**Files:**
- Modify: `internal/agent/queue/queue.go`
- Test: `internal/agent/queue/queue_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Queue.Offer` now also rejects keys present in a bounded recently-completed set; `Queue.New` unchanged signature. New unexported field `recentCap` (default `defaultRecentCap = 4096`) settable in-package for tests.

- [ ] **Step 1: Write the failing tests**

Add to `internal/agent/queue/queue_test.go`:

```go
func TestRecentCompletedDedup(t *testing.T) {
	q := New(4)
	j := Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	if !q.Offer(j) {
		t.Fatal("first offer should enqueue")
	}
	if q.Offer(j) {
		t.Fatal("duplicate while in-flight should be dropped")
	}
	got, ok := q.Next()
	if !ok || got.ID != "X" {
		t.Fatalf("dequeue: got %+v ok=%v", got, ok)
	}
	// inflight is now cleared; the recently-completed buffer must still drop it
	// (this is the hook↔watcher overlap window an inflight-only dedup misses).
	if q.Offer(j) {
		t.Fatal("duplicate after completion should be dropped by recent buffer")
	}
}

func TestRecentEvictionReallowsOffer(t *testing.T) {
	q := New(2)
	q.recentCap = 2
	for _, id := range []string{"A", "B", "C"} {
		if !q.Offer(Job{Source: "s", Scheme: "p", ID: id}) {
			t.Fatalf("offer %s should enqueue", id)
		}
		if _, ok := q.Next(); !ok {
			t.Fatalf("dequeue %s", id)
		}
	}
	// cap=2 now holds {B,C}; A was evicted, so re-offering A is allowed again.
	if !q.Offer(Job{Source: "s", Scheme: "p", ID: "A"}) {
		t.Fatal("A should be re-allowed after eviction from the recent buffer")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/queue/ -run 'TestRecentCompletedDedup|TestRecentEvictionReallowsOffer' -v`
Expected: FAIL — `q.recentCap` undefined and the post-completion dedup does not hold.

- [ ] **Step 3: Implement the ring buffer**

In `internal/agent/queue/queue.go`, add the constant and fields, and update `New`, `Offer`, `Next`:

```go
// defaultRecentCap bounds the recently-completed dedup set. It only needs to
// cover the live window between a hook job completing and the watcher first
// sighting the same prompt (seconds), so a few thousand keys is ample.
const defaultRecentCap = 4096
```

Add to the `Queue` struct (after `inflight map[string]bool`):

```go
	recent    map[string]struct{} // recently-completed keys (bounded)
	recentQ   []string            // FIFO order for eviction
	recentCap int
```

Update `New`:

```go
func New(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{
		ch:        make(chan Job, capacity),
		done:      make(chan struct{}),
		inflight:  map[string]bool{},
		recent:    map[string]struct{}{},
		recentCap: defaultRecentCap,
	}
}
```

Update `Offer` (add the recent check alongside inflight):

```go
func (q *Queue) Offer(j Job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	k := j.Key()
	if q.closed || q.inflight[k] {
		return false
	}
	if _, seen := q.recent[k]; seen {
		return false
	}
	select {
	case q.ch <- j:
		q.inflight[k] = true
		return true
	default:
		q.dropped++
		return false
	}
}
```

Update `Next` to record completion, and add `markRecentLocked`:

```go
func (q *Queue) Next() (Job, bool) {
	j, ok := <-q.ch
	if !ok {
		return Job{}, false
	}
	q.mu.Lock()
	k := j.Key()
	delete(q.inflight, k)
	q.markRecentLocked(k)
	q.mu.Unlock()
	return j, true
}

// markRecentLocked records a completed key, evicting the oldest past recentCap.
// Caller holds q.mu. Slicing the front is bounded: append reallocates and copies
// only live elements once the head advances, so memory stays ~O(recentCap).
func (q *Queue) markRecentLocked(k string) {
	if q.recentCap <= 0 {
		return
	}
	if _, ok := q.recent[k]; ok {
		return
	}
	q.recent[k] = struct{}{}
	q.recentQ = append(q.recentQ, k)
	if len(q.recentQ) > q.recentCap {
		old := q.recentQ[0]
		q.recentQ = q.recentQ[1:]
		delete(q.recent, old)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/queue/ -v`
Expected: PASS (new tests + existing queue tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/queue/queue.go internal/agent/queue/queue_test.go
git add internal/agent/queue/queue.go internal/agent/queue/queue_test.go
git commit -m "feat(queue): dedup against recently-completed keys, not just in-flight"
```

---

### Task 2: Source-parametrized transcript reader + register `cowork`

The Cowork transcript is byte-identical to Claude Code's, so the existing `ClaudeReader` can resolve it — but `Source()` is hard-coded to `"claude_code"`. Make the source a field and register a second reader for `"cowork"`.

**Files:**
- Modify: `internal/agent/resolve/claude.go`
- Modify: `internal/agent/resolve/resolve.go`
- Test: `internal/agent/resolve/cowork_test.go` (create)

**Interfaces:**
- Consumes: nothing new.
- Produces: `resolve.NewClaudeReaderForSource(src string) *ClaudeReader`; `resolve.Resolve("cowork", path, promptID, "")` now resolves via a `ClaudeReader`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/resolve/cowork_test.go`:

```go
package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveCoworkSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	// One genuine Cowork user prompt (same schema as Claude Code).
	line := `{"type":"user","promptId":"P1","cwd":"/work","sessionId":"S1","message":{"role":"user","content":"summarize the Q3 report"}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	text, ok := Resolve("cowork", path, "P1", "")
	if !ok || text != "summarize the Q3 report" {
		t.Fatalf("cowork resolve: text=%q ok=%v", text, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/resolve/ -run TestResolveCoworkSource -v`
Expected: FAIL — `ok=false` (no reader registered for `"cowork"`).

- [ ] **Step 3: Parametrize the reader source**

In `internal/agent/resolve/claude.go`, add a `src` field and split the constructor. Replace the struct header, constructor, and `Source()`:

```go
type ClaudeReader struct {
	src      string
	Attempts int
	Delay    time.Duration

	mu      sync.Mutex
	cursors map[string]int64 // transcript path -> offset of last consumed complete line
}

// NewClaudeReader returns a reader for Claude Code transcripts (source "claude_code").
func NewClaudeReader() *ClaudeReader { return newClaudeReader("claude_code") }

// NewClaudeReaderForSource returns a reader that reports the given source but
// parses the identical Claude-Code JSONL format (used for Cowork, whose
// transcripts share the schema).
func NewClaudeReaderForSource(src string) *ClaudeReader { return newClaudeReader(src) }

func newClaudeReader(src string) *ClaudeReader {
	return &ClaudeReader{src: src, Attempts: 10, Delay: 50 * time.Millisecond, cursors: map[string]int64{}}
}

func (r *ClaudeReader) Source() string { return r.src }
```

- [ ] **Step 4: Register the cowork reader**

In `internal/agent/resolve/resolve.go`, update `init`:

```go
func init() {
	register(NewClaudeReader())
	register(NewClaudeReaderForSource("cowork"))
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/resolve/ -v`
Expected: PASS (new test + existing claude reader tests).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/agent/resolve/claude.go internal/agent/resolve/resolve.go internal/agent/resolve/cowork_test.go
git add internal/agent/resolve/claude.go internal/agent/resolve/resolve.go internal/agent/resolve/cowork_test.go
git commit -m "feat(resolve): parametrize transcript source; register cowork reader"
```

---

### Task 3: `paths.WatchDir` + persistent cursor store

Per-file byte cursors persisted under `~/.keld/watch/` so a daemon restart never reprocesses already-seen transcript lines.

**Files:**
- Modify: `internal/paths/paths.go`
- Create: `internal/agent/watch/cursor.go`
- Test: `internal/agent/watch/cursor_test.go`

**Interfaces:**
- Produces: `paths.WatchDir() string`; `watch.CursorStore` with `NewCursorStore()`, `Get(path)(int64,bool)`, `Set(path, off int64)`, `Save() error`; unexported `newCursorStoreAt(path string)` for tests.

- [ ] **Step 1: Add `paths.WatchDir`**

In `internal/paths/paths.go`, add after `SpoolDir`:

```go
// WatchDir holds the transcript watcher's persisted per-file byte cursors.
// Sibling of spool/ and models/ under KELD_HOME.
func WatchDir() string { return filepath.Join(KeldHome(), "watch") }
```

- [ ] **Step 2: Write the failing test**

Create `internal/agent/watch/cursor_test.go`:

```go
package watch

import (
	"path/filepath"
	"testing"
)

func TestCursorStoreRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cursors.json")
	cs := newCursorStoreAt(p)
	if _, ok := cs.Get("/a.jsonl"); ok {
		t.Fatal("unknown path should report ok=false")
	}
	cs.Set("/a.jsonl", 128)
	if err := cs.Save(); err != nil {
		t.Fatal(err)
	}
	// Reload from disk into a fresh store.
	cs2 := newCursorStoreAt(p)
	off, ok := cs2.Get("/a.jsonl")
	if !ok || off != 128 {
		t.Fatalf("reloaded cursor: off=%d ok=%v", off, ok)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/agent/watch/ -run TestCursorStoreRoundTrip -v`
Expected: FAIL — package/`newCursorStoreAt` does not exist (compile error).

- [ ] **Step 4: Implement the cursor store**

Create `internal/agent/watch/cursor.go`:

```go
// Package watch is the daemon's hook-free capture trigger: it tails Claude-Code
// -format JSONL transcripts (Claude Code and Cowork) and synthesizes enrich
// pointers into the same pipeline the command hook feeds.
package watch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// CursorStore persists per-transcript byte offsets so a daemon restart does not
// reprocess already-seen lines. Concurrency-safe; callers Save() after a poll.
type CursorStore struct {
	path string
	mu   sync.Mutex
	off  map[string]int64
}

// NewCursorStore returns the production store under paths.WatchDir().
func NewCursorStore() *CursorStore {
	return newCursorStoreAt(filepath.Join(paths.WatchDir(), "cursors.json"))
}

func newCursorStoreAt(path string) *CursorStore {
	cs := &CursorStore{path: path, off: map[string]int64{}}
	cs.load()
	return cs
}

func (c *CursorStore) load() {
	b, err := os.ReadFile(c.path)
	if err != nil {
		return
	}
	var m map[string]int64
	if json.Unmarshal(b, &m) == nil && m != nil {
		c.off = m
	}
}

// Get returns the stored offset for path and whether path has been seen.
func (c *CursorStore) Get(path string) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.off[path]
	return v, ok
}

// Set records path's offset.
func (c *CursorStore) Set(path string, off int64) {
	c.mu.Lock()
	c.off[path] = off
	c.mu.Unlock()
}

// Save atomically persists all cursors.
func (c *CursorStore) Save() error {
	c.mu.Lock()
	b, err := json.Marshal(c.off)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/watch/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/paths/paths.go internal/agent/watch/cursor.go internal/agent/watch/cursor_test.go
git add internal/paths/paths.go internal/agent/watch/cursor.go internal/agent/watch/cursor_test.go
git commit -m "feat(watch): persistent per-transcript cursor store + paths.WatchDir"
```

---

### Task 4: Genuine user-prompt filter

Decide which transcript records are genuine human prompts (what the hook captures) vs tool-result / assistant / system records.

**Files:**
- Create: `internal/agent/watch/filter.go`
- Test: `internal/agent/watch/filter_test.go`

**Interfaces:**
- Produces: `promptRec{PromptID, Cwd, SessionID string}` and `parsePrompt(line []byte) (promptRec, bool)` (both unexported, package `watch`).

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/watch/filter_test.go`:

```go
package watch

import "testing"

func TestParsePrompt(t *testing.T) {
	cases := []struct {
		name string
		line string
		ok   bool
		id   string
		cwd  string
	}{
		{"string content", `{"type":"user","promptId":"P1","cwd":"/w","sessionId":"S1","message":{"role":"user","content":"hello"}}`, true, "P1", "/w"},
		{"text block", `{"type":"user","promptId":"P2","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, true, "P2", ""},
		{"tool_result rejected", `{"type":"user","promptId":"P3","message":{"role":"user","content":[{"type":"tool_result","content":"out"}]}}`, false, "", ""},
		{"assistant rejected", `{"type":"assistant","promptId":"P4","message":{"role":"assistant","content":"x"}}`, false, "", ""},
		{"missing promptId rejected", `{"type":"user","message":{"role":"user","content":"hi"}}`, false, "", ""},
		{"empty string content rejected", `{"type":"user","promptId":"P5","message":{"role":"user","content":""}}`, false, "", ""},
		{"malformed rejected", `{not json`, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, ok := parsePrompt([]byte(c.line))
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if ok && (rec.PromptID != c.id || rec.Cwd != c.cwd) {
				t.Fatalf("rec=%+v want id=%q cwd=%q", rec, c.id, c.cwd)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/watch/ -run TestParsePrompt -v`
Expected: FAIL — `parsePrompt` undefined (compile error).

- [ ] **Step 3: Implement the filter**

Create `internal/agent/watch/filter.go`:

```go
package watch

import "encoding/json"

// promptRec is the minimal projection of a transcript user-prompt record needed
// to synthesize an enrich pointer.
type promptRec struct {
	PromptID  string
	Cwd       string
	SessionID string
}

type rawLine struct {
	Type      string          `json:"type"`
	PromptID  string          `json:"promptId"`
	Cwd       string          `json:"cwd"`
	SessionID string          `json:"sessionId"`
	Message   json.RawMessage `json:"message"`
}

// parsePrompt returns the record projection when line is a GENUINE human prompt:
// a type=="user" record with a promptId whose message content is real text
// (a non-empty string, or an array with a text block and no tool_result block).
// Tool-result user records, assistant/system records, and malformed lines are
// rejected. This mirrors what the UserPromptSubmit hook captures.
func parsePrompt(line []byte) (promptRec, bool) {
	var ln rawLine
	if err := json.Unmarshal(line, &ln); err != nil {
		return promptRec{}, false
	}
	if ln.Type != "user" || ln.PromptID == "" {
		return promptRec{}, false
	}
	if !hasHumanText(ln.Message) {
		return promptRec{}, false
	}
	return promptRec{PromptID: ln.PromptID, Cwd: ln.Cwd, SessionID: ln.SessionID}, true
}

func hasHumanText(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil || len(msg.Content) == 0 {
		return false
	}
	// content as a bare string
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return s != ""
	}
	// content as an array of typed blocks
	var blocks []struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return false
	}
	hasText := false
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false // tool output, not a human prompt
		}
		if b.Type == "text" {
			hasText = true
		}
	}
	return hasText
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/watch/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/watch/filter.go internal/agent/watch/filter_test.go
git add internal/agent/watch/filter.go internal/agent/watch/filter_test.go
git commit -m "feat(watch): genuine user-prompt filter (excludes tool results)"
```

---

### Task 5: Transcript root discovery

Discover the transcript directories to watch, per-OS.

**Files:**
- Create: `internal/agent/watch/roots.go`
- Test: `internal/agent/watch/roots_test.go`

**Interfaces:**
- Produces: `Root{SourceID, Dir string}`; `DiscoverRoots() []Root`; testable core `discoverRoots(home, goos string) []Root`.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/watch/roots_test.go`:

```go
package watch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverRootsClaudeAndCowork(t *testing.T) {
	home := t.TempDir()
	// Claude Code root.
	cc := filepath.Join(home, ".claude", "projects")
	mkdir(t, cc)
	// Cowork nested root: .../local-agent-mode-sessions/<a>/<b>/local_<uuid>/.claude/projects
	cw := filepath.Join(home, "Library", "Application Support", "Claude",
		"local-agent-mode-sessions", "aaa", "bbb", "local_ccc", ".claude", "projects")
	mkdir(t, cw)

	// darwin: both roots.
	roots := discoverRoots(home, "darwin")
	if !hasRoot(roots, "claude_code", cc) {
		t.Errorf("missing claude_code root; got %+v", roots)
	}
	if !hasRoot(roots, "cowork", cw) {
		t.Errorf("missing cowork root; got %+v", roots)
	}

	// linux: claude_code only (Cowork is macOS-only).
	roots = discoverRoots(home, "linux")
	if !hasRoot(roots, "claude_code", cc) {
		t.Errorf("linux should still watch claude_code; got %+v", roots)
	}
	if hasRoot(roots, "cowork", cw) {
		t.Errorf("linux must not watch cowork; got %+v", roots)
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

func hasRoot(roots []Root, source, dir string) bool {
	for _, r := range roots {
		if r.SourceID == source && r.Dir == dir {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/watch/ -run TestDiscoverRootsClaudeAndCowork -v`
Expected: FAIL — `discoverRoots`/`Root` undefined (compile error).

- [ ] **Step 3: Implement discovery**

Create `internal/agent/watch/roots.go`:

```go
package watch

import (
	"os"
	"path/filepath"
	"runtime"
)

// Root is a directory tree of Claude-Code-format JSONL transcripts and the
// capture source assigned to prompts found under it.
type Root struct {
	SourceID string
	Dir      string
}

// DiscoverRoots returns the transcript roots to watch on this machine.
func DiscoverRoots() []Root {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return discoverRoots(home, runtime.GOOS)
}

// discoverRoots is the testable core (home + GOOS explicit). Only existing
// directories are returned; the Cowork glob is re-evaluated each call so new
// session dirs are picked up.
func discoverRoots(home, goos string) []Root {
	var roots []Root
	// Claude Code — every launch surface (CLI, Desktop app, IDE) writes here.
	if cc := filepath.Join(home, ".claude", "projects"); isDir(cc) {
		roots = append(roots, Root{SourceID: "claude_code", Dir: cc})
	}
	// Cowork (Claude Code in a sandbox) — macOS only. Each session nests a
	// standard .claude/projects transcript tree two levels down.
	if goos == "darwin" {
		glob := filepath.Join(home, "Library", "Application Support", "Claude",
			"local-agent-mode-sessions", "*", "*", "local_*", ".claude", "projects")
		matches, _ := filepath.Glob(glob)
		for _, m := range matches {
			if isDir(m) {
				roots = append(roots, Root{SourceID: "cowork", Dir: m})
			}
		}
	}
	return roots
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/watch/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/watch/roots.go internal/agent/watch/roots_test.go
git add internal/agent/watch/roots.go internal/agent/watch/roots_test.go
git commit -m "feat(watch): per-OS transcript root discovery (claude_code + cowork)"
```

---

### Task 6: Watcher poll loop + env config

The watcher: scan files forward from cursors, offer genuine prompts as pointers, forward-only for pre-existing files. Plus env config helpers.

**Files:**
- Create: `internal/agent/watch/watch.go`
- Create: `internal/agent/watch/config.go`
- Test: `internal/agent/watch/watch_test.go`
- Test: `internal/agent/watch/config_test.go`

**Interfaces:**
- Consumes: `promptRec`/`parsePrompt` (Task 4), `Root`/`DiscoverRoots` (Task 5), `CursorStore` (Task 3), `spool.Pointer`/`spool.Source`/`spool.Correlation`/`spool.Ptr`.
- Produces: `Watcher` with `New(offer func(spool.Pointer), version string, poll time.Duration, backfill bool) *Watcher` and `Run(ctx context.Context)`; env helpers `EnabledFromEnv() bool`, `PollFromEnv() time.Duration`, `BackfillFromEnv() bool`.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/watch/watch_test.go`:

```go
package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

func genuine(id string) string {
	return `{"type":"user","promptId":"` + id + `","cwd":"/w","sessionId":"S1","message":{"role":"user","content":"hi ` + id + `"}}` + "\n"
}

func toolResult(id string) string {
	return `{"type":"user","promptId":"` + id + `","message":{"role":"user","content":[{"type":"tool_result","content":"out"}]}}` + "\n"
}

// testWatcher wires a Watcher to a single fixed root + a temp cursor store.
func testWatcher(t *testing.T, root Root, offer func(spool.Pointer), backfill bool) *Watcher {
	t.Helper()
	return &Watcher{
		offer:    offer,
		cursors:  newCursorStoreAt(filepath.Join(t.TempDir(), "cursors.json")),
		discover: func() []Root { return []Root{root} },
		version:  "test",
		poll:     time.Second,
		backfill: backfill,
	}
}

func TestWatcherForwardOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(genuine("OLD")), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []spool.Pointer
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(p spool.Pointer) { got = append(got, p) }, false)

	w.pollOnce() // first sighting: forward-only skips existing content
	if len(got) != 0 {
		t.Fatalf("forward-only should skip pre-existing prompts; got %d", len(got))
	}
	// Append a new prompt.
	appendFile(t, path, genuine("NEW"))
	w.pollOnce()
	if len(got) != 1 {
		t.Fatalf("expected 1 new prompt; got %d", len(got))
	}
	p := got[0]
	if p.Source.ID != "claude_code" || p.Source.Origin != "watch" || p.Correlation.ID != "NEW" ||
		p.Pointer == nil || p.Pointer.PromptID != "NEW" || p.Pointer.Cwd != "/w" || p.Pointer.TranscriptPath != path {
		t.Fatalf("unexpected pointer: %+v", p)
	}
}

func TestWatcherBackfillAndToolResultSkip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := genuine("A") + toolResult("T") + genuine("B")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []spool.Pointer
	w := testWatcher(t, Root{SourceID: "cowork", Dir: dir}, func(p spool.Pointer) { got = append(got, p) }, true)
	w.pollOnce()
	if len(got) != 2 {
		t.Fatalf("backfill should offer 2 genuine prompts (tool result skipped); got %d", len(got))
	}
	if got[0].Correlation.ID != "A" || got[1].Correlation.ID != "B" || got[0].Source.ID != "cowork" {
		t.Fatalf("unexpected pointers: %+v", got)
	}
}

func TestWatcherCursorSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(genuine("A")), 0o600); err != nil {
		t.Fatal(err)
	}
	cursorPath := filepath.Join(t.TempDir(), "cursors.json")

	var got []spool.Pointer
	w1 := &Watcher{
		offer: func(p spool.Pointer) { got = append(got, p) }, cursors: newCursorStoreAt(cursorPath),
		discover: func() []Root { return []Root{{SourceID: "cowork", Dir: dir}} }, version: "t", poll: time.Second, backfill: true,
	}
	w1.pollOnce()
	if len(got) != 1 {
		t.Fatalf("first run should offer 1; got %d", len(got))
	}
	// Simulate restart: fresh watcher, same cursor file, no new appends.
	got = nil
	w2 := &Watcher{
		offer: func(p spool.Pointer) { got = append(got, p) }, cursors: newCursorStoreAt(cursorPath),
		discover: func() []Root { return []Root{{SourceID: "cowork", Dir: dir}} }, version: "t", poll: time.Second, backfill: true,
	}
	w2.pollOnce()
	if len(got) != 0 {
		t.Fatalf("restart must not reprocess; got %d", len(got))
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}
```

Create `internal/agent/watch/config_test.go`:

```go
package watch

import (
	"testing"
	"time"
)

func TestEnabledFromEnv(t *testing.T) {
	t.Setenv("KELD_WATCH", "")
	if !EnabledFromEnv() {
		t.Error("default should be enabled")
	}
	t.Setenv("KELD_WATCH", "off")
	if EnabledFromEnv() {
		t.Error("off should disable")
	}
}

func TestPollFromEnv(t *testing.T) {
	t.Setenv("KELD_WATCH_POLL", "")
	if PollFromEnv() != 5*time.Second {
		t.Errorf("default poll = %v", PollFromEnv())
	}
	t.Setenv("KELD_WATCH_POLL", "2s")
	if PollFromEnv() != 2*time.Second {
		t.Errorf("poll = %v", PollFromEnv())
	}
}

func TestBackfillFromEnv(t *testing.T) {
	t.Setenv("KELD_WATCH_BACKFILL", "")
	if BackfillFromEnv() {
		t.Error("default should be forward-only")
	}
	t.Setenv("KELD_WATCH_BACKFILL", "on")
	if !BackfillFromEnv() {
		t.Error("on should enable backfill")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/watch/ -run 'TestWatcher|FromEnv' -v`
Expected: FAIL — `Watcher`, `pollOnce`, and env helpers undefined (compile error).

- [ ] **Step 3: Implement the watcher**

Create `internal/agent/watch/watch.go`:

```go
package watch

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// Watcher tails Claude-Code-format transcript roots and, for each new genuine
// user prompt, synthesizes an enrich pointer and hands it to offer — the same
// pointer shape the hook produces, fed into the same daemon queue. It is the
// hook-free capture trigger for surfaces that don't fire command hooks (Cowork,
// and Claude Code launch surfaces where hooks may not run). It never reads or
// forwards prompt TEXT — only pointers.
type Watcher struct {
	offer    func(spool.Pointer)
	cursors  *CursorStore
	discover func() []Root
	version  string
	poll     time.Duration
	backfill bool
}

// New builds a Watcher. offer receives each synthesized pointer; version stamps
// Source.Version; poll is the scan cadence; backfill=false starts new files at
// EOF (forward-only), true enriches history.
func New(offer func(spool.Pointer), version string, poll time.Duration, backfill bool) *Watcher {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	return &Watcher{
		offer:    offer,
		cursors:  NewCursorStore(),
		discover: DiscoverRoots,
		version:  version,
		poll:     poll,
		backfill: backfill,
	}
}

// Run polls until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.poll)
	defer t.Stop()
	w.pollOnce() // initial pass so forward-only cursors are set promptly
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.pollOnce()
		}
	}
}

func (w *Watcher) pollOnce() {
	changed := false
	for _, root := range w.discover() {
		for _, path := range transcriptFiles(root.Dir) {
			if w.scanFile(root.SourceID, path) {
				changed = true
			}
		}
	}
	if changed {
		if err := w.cursors.Save(); err != nil {
			debuglog.Append("watch: cursor save failed: %v", err)
		}
	}
}

// scanFile reads new complete lines from path's cursor, offers each genuine
// prompt, and advances the cursor. Returns true if the cursor moved.
func (w *Watcher) scanFile(source, path string) bool {
	off, known := w.cursors.Get(path)
	if !known {
		// First sighting. Forward-only: skip existing content by starting the
		// cursor at EOF (unless backfill is on).
		if !w.backfill {
			if st, err := os.Stat(path); err == nil {
				w.cursors.Set(path, st.Size())
				return true
			}
			return false
		}
		off = 0
	}
	// Truncation/rotation guard: restart from 0 if the file shrank.
	if st, err := os.Stat(path); err == nil && st.Size() < off {
		off = 0
	}
	recs, consumed := scanFrom(path, off)
	for _, rec := range recs {
		w.offer(spool.Pointer{
			Source:      spool.Source{ID: source, Origin: "watch", Version: w.version},
			Correlation: spool.Correlation{Scheme: "prompt_id", ID: rec.PromptID, SessionID: rec.SessionID},
			Pointer:     &spool.Ptr{TranscriptPath: path, PromptID: rec.PromptID, Cwd: rec.Cwd},
		})
	}
	if consumed > 0 {
		w.cursors.Set(path, off+consumed)
		return true
	}
	return false
}

// transcriptFiles returns *.jsonl under dir (recursively). Best-effort.
func transcriptFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees
		}
		if !d.IsDir() && filepath.Ext(p) == ".jsonl" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// scanFrom reads complete (newline-terminated) lines from byte offset off and
// returns the genuine prompts found plus the number of bytes of complete lines
// consumed. A trailing partial line (write in progress) is not consumed, so it
// is re-read on the next poll.
func scanFrom(path string, off int64) (recs []promptRec, consumed int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, 0
	}
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: `line` is a partial trailing line; do not consume it
		}
		consumed += int64(len(line))
		if rec, ok := parsePrompt([]byte(line)); ok {
			recs = append(recs, rec)
		}
	}
	return recs, consumed
}
```

Create `internal/agent/watch/config.go`:

```go
package watch

import (
	"os"
	"strings"
	"time"
)

// EnabledFromEnv reports whether the transcript watcher should run. On by
// default; disabled with KELD_WATCH in {off,0,false} (case-insensitive).
func EnabledFromEnv() bool {
	switch strings.ToLower(os.Getenv("KELD_WATCH")) {
	case "off", "0", "false":
		return false
	default:
		return true
	}
}

// PollFromEnv returns the poll cadence (KELD_WATCH_POLL, a Go duration), default 5s.
func PollFromEnv() time.Duration {
	if v := os.Getenv("KELD_WATCH_POLL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 5 * time.Second
}

// BackfillFromEnv reports whether pre-existing transcripts should be enriched
// from the start (KELD_WATCH_BACKFILL in {on,1,true}); default false (forward-only).
func BackfillFromEnv() bool {
	switch strings.ToLower(os.Getenv("KELD_WATCH_BACKFILL")) {
	case "on", "1", "true":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agent/watch/ -v`
Expected: PASS (all watch tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/agent/watch/watch.go internal/agent/watch/config.go internal/agent/watch/watch_test.go internal/agent/watch/config_test.go
git add internal/agent/watch/watch.go internal/agent/watch/config.go internal/agent/watch/watch_test.go internal/agent/watch/config_test.go
git commit -m "feat(watch): poll-loop watcher + env config (forward-only, cursor-tracked)"
```

---

### Task 7: Wire the watcher into the daemon

Start the watcher goroutine when enrichment is enabled and `KELD_WATCH` is on, offering pointers into the existing queue.

**Files:**
- Modify: `internal/agent/daemon/daemon.go`
- Test: `internal/agent/daemon/watch_wire_test.go` (create)

**Interfaces:**
- Consumes: `watch.New/EnabledFromEnv/PollFromEnv/BackfillFromEnv` (Task 6), `ingress.JobFrom` (existing), `version.CLI` (existing import).
- Produces: watcher goroutine started in `Run`; the offer closure `func(spool.Pointer){ q.Offer(ingress.JobFrom(p)) }`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/daemon/watch_wire_test.go` (locks the offer→queue contract the wiring relies on):

```go
package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

func TestWatchOfferEnqueues(t *testing.T) {
	q := queue.New(4)
	offer := func(p spool.Pointer) { q.Offer(ingress.JobFrom(p)) }
	offer(spool.Pointer{
		Source:      spool.Source{ID: "cowork", Origin: "watch"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: "P1"},
		Pointer:     &spool.Ptr{TranscriptPath: "/t.jsonl", PromptID: "P1", Cwd: "/c"},
	})
	j, ok := q.Next()
	if !ok || j.Source != "cowork" || j.Origin != "watch" || j.PromptID != "P1" || j.Cwd != "/c" {
		t.Fatalf("unexpected job: %+v ok=%v", j, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it passes as a contract check**

Run: `go test ./internal/agent/daemon/ -run TestWatchOfferEnqueues -v`
Expected: PASS (this test only depends on existing `ingress`/`queue`/`spool`; it documents the wiring contract before we add the goroutine).

- [ ] **Step 3: Add the watcher import**

In `internal/agent/daemon/daemon.go`, add to the import block (alongside the other `internal/agent/...` imports):

```go
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
```

(`internal/version` is already imported at line 41.)

- [ ] **Step 4: Start the watcher goroutine**

In `internal/agent/daemon/daemon.go`, immediately after the spool-sweep block (the `if enrichmentEnabled { ... }` block that ends around line 623) and before `return serve(...)`, add:

```go
	// Transcript watcher: the hook-free capture trigger. Tails Claude Code and
	// Cowork transcripts and offers pointers into the SAME queue the hook feeds,
	// so surfaces that don't fire the command hook (Cowork; Claude Code in the
	// Desktop app) still enrich. Only when enrichment is enabled (no Worker to
	// consume the queue otherwise) and not explicitly disabled.
	if enrichmentEnabled && watch.EnabledFromEnv() {
		offer := func(p spool.Pointer) { q.Offer(ingress.JobFrom(p)) }
		txw := watch.New(offer, version.CLI, watch.PollFromEnv(), watch.BackfillFromEnv())
		go txw.Run(ctx)
	}
```

- [ ] **Step 5: Verify the build and full daemon tests**

Run: `go build ./... && go test ./internal/agent/daemon/ -v`
Expected: build OK; daemon tests PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/agent/daemon/daemon.go internal/agent/daemon/watch_wire_test.go
git add internal/agent/daemon/daemon.go internal/agent/daemon/watch_wire_test.go
git commit -m "feat(daemon): start transcript watcher (hook-free capture) when enrichment enabled"
```

---

### Task 8: Cowork-classification guard test + docs + changelog

Lock in the "Cowork is knowledge work" invariant with a regression test, and document the new capture path.

**Files:**
- Test: `internal/agent/enrich/cowork_classification_test.go` (create)
- Modify: `README.md`
- Modify: `AGENTS.md`
- Modify: `docs/enrichment-settings.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none (test + docs).

- [ ] **Step 1: Write the guard test**

Create `internal/agent/enrich/cowork_classification_test.go`:

```go
package enrich

import "testing"

// Cowork is knowledge work, not coding: it must be classified topically, so it
// must NOT be in interactiveCodingTools (context augmentation) or codingTools
// (the compositional function_guess=eng rule). This guards against a future
// edit that lumps cowork in with claude_code.
func TestCoworkClassifiedTopically(t *testing.T) {
	if ContextEligible("cowork") {
		t.Error("cowork must not receive interactive-coding context augmentation")
	}
	if codingTools["cowork"] {
		t.Error("cowork must not get the compositional function_guess=eng rule")
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./internal/agent/enrich/ -run TestCoworkClassifiedTopically -v`
Expected: PASS (documents and guards current behavior).

- [ ] **Step 3: Update the README capture section**

In `README.md`, in the "On-device enrichment (the core)" area, after the two-wave sweep description, add a paragraph:

```markdown
**Two capture triggers, one pipeline.** Prompts reach the daemon two ways, both
producing the same masked, derived `Profile`: (1) the **command hook**
(`keld __hook`, installed by `keld setup`) on `UserPromptSubmit`; and (2) an
on-device **transcript watcher** that tails the JSONL transcripts Claude Code
(every launch surface, including the Desktop app) and **Cowork** already write
to disk. The watcher is the hook-free path for surfaces that don't run command
hooks. It reads only a pointer (path + prompt id) into the existing
resolve → enrich → publish flow — **never prompt text** — and dedups against the
hook via the prompt id. Cowork prompts are labeled `source=cowork` and
classified as general knowledge work (not coding). Plain Claude *chat* (server-
synced, no local transcript) is not captured on-device.
```

- [ ] **Step 4: Update AGENTS.md**

In `AGENTS.md`, in the capture/ingest description (the "Enrichment pipeline" / hook area near line 50-66), add after the hook/spool sentence:

```markdown
**Capture triggers.** Two triggers feed the same queue: the **command hook**
(`keld __hook --source <tool>`, wired by `keld setup`), and an on-device
**transcript watcher** (`internal/agent/watch/`) that tails the JSONL transcripts
Claude Code (all surfaces incl. the Desktop app) and **Cowork** write to disk —
the hook-free path. The watcher synthesizes the same `spool.Pointer` the hook
does (never text) keyed on `promptId`; the queue dedups hook↔watcher overlap via
a recently-completed set. Sources: `~/.claude/projects` → `claude_code`, the
Cowork `local-agent-mode-sessions/**/.claude/projects` trees → `cowork` (macOS).
Env: `KELD_WATCH` (default on), `KELD_WATCH_POLL` (default 5s), `KELD_WATCH_BACKFILL`
(default off = forward-only). macOS + Linux; Windows deferred.
```

- [ ] **Step 5: Update docs/enrichment-settings.md**

In `docs/enrichment-settings.md`, under "Why this exists" (after the paragraph describing what the daemon derives), add a note:

```markdown
> **Capture surfaces.** Enrichment covers prompts captured two ways: the command
> hook (Claude Code CLI and other hook-capable tools) and the on-device
> transcript watcher (Claude Code on any surface, incl. the Desktop app, plus
> Cowork). Both go through the same masking and settings governance described
> here — `include_entity_text` and always-on span masking apply identically
> regardless of which trigger captured the prompt.
```

- [ ] **Step 6: Update CHANGELOG.md**

In `CHANGELOG.md`, replace the `## [Unreleased]` line with:

```markdown
## [Unreleased]

## [0.9.0] — 2026-07-21

Hook-free prompt capture — Claude Code on every launch surface (incl. the Desktop
app) and Cowork now enrich, not just the terminal CLI.

### Added
- **On-device transcript watcher** (`internal/agent/watch/`). A daemon poll loop
  tails the JSONL transcripts Claude Code (all surfaces) and Cowork already write
  to disk and synthesizes the same enrich pointer the command hook produces —
  never prompt text — into the existing resolve → enrich → publish pipeline. This
  is the hook-free capture path for surfaces (Cowork's Linux sandbox; Claude Code
  in the Desktop app) that don't fire `~/.claude/settings.json` hooks. Sources:
  `~/.claude/projects` → `claude_code`; the Cowork
  `local-agent-mode-sessions/**/.claude/projects` trees → `cowork` (macOS). New
  env: `KELD_WATCH` (default on), `KELD_WATCH_POLL` (5s), `KELD_WATCH_BACKFILL`
  (off = forward-only, so first run doesn't flood on existing history). Cowork
  prompts are classified as general knowledge work, not coding.

### Changed
- **Queue dedup now also covers recently-completed keys**, not just in-flight, so
  a prompt caught by both the hook and the watcher is enriched once (the hook
  typically completes before the watcher's next poll — an in-flight-only dedup
  would miss it). Bounded in-memory ring buffer (~5000 keys).
```

- [ ] **Step 7: Run the full suite and build**

Run: `go build ./... && go test ./...`
Expected: build OK; all tests PASS.

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/agent/enrich/cowork_classification_test.go
git add internal/agent/enrich/cowork_classification_test.go README.md AGENTS.md docs/enrichment-settings.md CHANGELOG.md
git commit -m "docs+test: transcript-watch capture (README/AGENTS/changelog) + cowork-topical guard"
```

---

## Self-Review

- **Spec coverage:** watcher (Tasks 3-7), cowork reader (Task 2), roots incl. macOS+Linux/Cowork (Task 5), forward-only + cursor persistence (Tasks 3, 6), distinct `cowork` source (Tasks 5, 6), on-by-default + env (Task 6), dedup ring buffer closing the overlap window (Task 1), cowork-topical invariant (Task 8), docs + changelog (Task 8). All spec sections mapped.
- **Type consistency:** `spool.Pointer{Source, Correlation, Pointer *Ptr}`, `spool.Source{ID,Origin,Version}`, `spool.Correlation{Scheme,ID,SessionID}`, `spool.Ptr{TranscriptPath,PromptID,Cwd}`, `ingress.JobFrom`, `queue.Job` fields, `version.CLI`, `enrich.ContextEligible`, `enrich.codingTools` — all verified against the current code.
- **No placeholders:** every code and test block is complete.
- **Scope:** single subsystem (capture trigger); no schema/vocab change; Windows/OTLP/plain-chat explicitly out of scope.

## Execution note

Tasks 1-6 are independent and could be built in any order; Task 7 depends on 1, 2, 6; Task 8 depends on all. Build in listed order for a clean dependency flow.
