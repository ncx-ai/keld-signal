# Enrichment Context Augmentation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give GLiNER richer context — session metadata + recent user prompts — so ambiguous fragment prompts ("Ok, that's fine") classify accurately, gated to interactive coding tools.

**Architecture:** Extend `enrich.Meta` + its `Preamble()` (fed only to classification passes) with git branch, project name, and the last N user prompts. Gather recent prompts via a bounded **tail scan** of the transcript (respecting `ClaudeReader`'s append-only cursor), read branch/project from the cwd on-device, and populate the Meta in `daemon.process()` only for eligible sources.

**Tech Stack:** Go (module `github.com/ncx-ai/keld-cli`), `github.com/pelletier/go-toml/v2` (already a dep), standard `go test`.

## Global Constraints

- **Source-gated:** augmentation applies only to interactive coding tools — default set `{claude_code, codex}` — off for all other sources.
- **Classification passes only:** context goes through `Meta.Preamble()`, which is already fed only to classification passes; entity/sensitivity passes keep raw text (offset-sensitive). Never change that.
- **Best-effort, never fatal:** any read/parse failure (missing transcript, `.git`, `.keld.toml`) yields empty fields; the job still classifies. `process()` is already panic-isolated.
- **Privacy:** context is used only in the on-device classification input — never stored, never sent to Atlas. The persisted enrichment stays labels/confidences only.
- **Respect the cursor:** `ClaudeReader.Read` advances an append-only cursor (reads only new lines). History reading must be a **separate** bounded tail scan that never touches that cursor.
- **Bounds:** N=3 recent prompts; per-prompt cap 400 chars; total recent-prompt budget 1500 chars.
- **Test command:** `go test ./internal/agent/enrich/... ./internal/agent/resolve/... ./internal/agent/daemon/...`

## File structure

- `internal/agent/enrich/meta.go` — Meta gains `GitBranch`, `Project`, `RecentPrompts`; Preamble renders them. (Task 1)
- `internal/agent/enrich/context.go` (new) — `ContextEligible(source)` + default allowlist. (Task 3)
- `internal/agent/resolve/recent.go` (new) — `RecentPrompts` dispatch + `ClaudeReader.RecentUserPrompts` tail scan. (Task 2)
- `internal/agent/daemon/context.go` (new) — `gitBranch`, `projectName`, `budget`, `contextMeta`. (Task 3)
- `internal/agent/daemon/daemon.go` — `process()` uses `contextMeta` for eligible sources. (Task 3)
- `internal/agent/enrich/eval/eval.go` + `gold.jsonl` — GoldRow context fields + fragment rows. (Task 4)

---

### Task 1: Meta fields + Preamble rendering

**Files:**
- Modify: `internal/agent/enrich/meta.go`
- Test: `internal/agent/enrich/meta_test.go`

**Interfaces:**
- Produces: `enrich.Meta` with new fields `GitBranch string`, `Project string`, `RecentPrompts []string` (newest-first); `Meta.Preamble() string` renders them.

- [ ] **Step 1: Update the failing test**

Open `internal/agent/enrich/meta_test.go`, replace the existing `TestPreamble` with:

```go
func TestPreamble(t *testing.T) {
	// baseline: repo + tool, no context extras
	got := Meta{Repo: "acme/api", Tool: "Claude Code"}.Preamble()
	want := "[Context — repository: acme/api; tool: Claude Code]\nTask: "
	if got != want {
		t.Fatalf("baseline preamble\n got: %q\nwant: %q", got, want)
	}

	// empty repo renders "none" for a stable shape
	if p := (Meta{}).Preamble(); p != "[Context — repository: none]\nTask: " {
		t.Fatalf("empty preamble: %q", p)
	}

	// branch + project appear in the context line; recent prompts as a numbered, newest-first block
	full := Meta{
		Repo: "acme/api", Tool: "Claude Code", GitBranch: "feat/pills", Project: "Keld Atlas",
		RecentPrompts: []string{"right-align the pills", "add a compliance flag"},
	}.Preamble()
	want = "[Context — repository: acme/api; branch: feat/pills; project: Keld Atlas; tool: Claude Code]\n" +
		"Recent prompts (newest first):\n 1. right-align the pills\n 2. add a compliance flag\nTask: "
	if full != want {
		t.Fatalf("full preamble\n got: %q\nwant: %q", full, want)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/enrich/ -run TestPreamble`
Expected: FAIL (Meta has no `GitBranch`/`Project`/`RecentPrompts`; Preamble lacks the new rendering) — compile error / mismatch.

- [ ] **Step 3: Implement**

Replace the contents of `internal/agent/enrich/meta.go` with:

```go
package enrich

import (
	"fmt"
	"strings"
)

// Meta is the non-prompt context a classification pass may reason over. Repo (cwd)
// and tool (source) are always known; branch/project/recent-prompts are added for
// interactive coding tools so fragment prompts classify in context. Team/category
// are resolved server-side in Atlas, never here.
type Meta struct {
	Repo          string
	Tool          string
	GitBranch     string
	Project       string
	RecentPrompts []string // prior user prompts, newest-first (bounded upstream)
}

// Preamble renders a compact context block prepended to the text handed to
// CLASSIFICATION passes (never to entity/sensitivity passes, which need raw
// offsets). Only non-empty fields render; empty repo renders "none" for a stable
// shape. The current prompt follows "Task: " last.
func (m Meta) Preamble() string {
	parts := []string{"repository: none"}
	if m.Repo != "" {
		parts[0] = "repository: " + m.Repo
	}
	if m.GitBranch != "" {
		parts = append(parts, "branch: "+m.GitBranch)
	}
	if m.Project != "" {
		parts = append(parts, "project: "+m.Project)
	}
	if m.Tool != "" {
		parts = append(parts, "tool: "+m.Tool)
	}
	var b strings.Builder
	b.WriteString("[Context — " + strings.Join(parts, "; ") + "]\n")
	if len(m.RecentPrompts) > 0 {
		b.WriteString("Recent prompts (newest first):\n")
		for i, p := range m.RecentPrompts {
			fmt.Fprintf(&b, " %d. %s\n", i+1, p)
		}
	}
	b.WriteString("Task: ")
	return b.String()
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/agent/enrich/ -run TestPreamble`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/meta.go internal/agent/enrich/meta_test.go
git commit -m "feat(enrich): Meta carries branch/project/recent-prompts context"
```

---

### Task 2: Recent user prompts from the transcript (tail scan)

**Files:**
- Create: `internal/agent/resolve/recent.go`
- Test: `internal/agent/resolve/recent_test.go`

**Interfaces:**
- Consumes: existing `claudeLine`, `claudeMsg`, `extractText` (same `resolve` package, in `claude.go`); the `readers` registry.
- Produces: `resolve.RecentPrompts(source, transcriptPath, currentPromptID string, n int) []string` (newest-first, excludes current, nil if unsupported/empty); `(*ClaudeReader).RecentUserPrompts(path, currentPromptID string, n int) []string`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/resolve/recent_test.go`:

```go
package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(joinLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func joinLines(ls []string) string {
	out := ""
	for _, l := range ls {
		out += l + "\n"
	}
	return out
}

func TestRecentPromptsNewestFirstExcludingCurrent(t *testing.T) {
	p := writeTranscript(t, []string{
		`{"type":"user","promptId":"p1","message":{"role":"user","content":"first task"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"ok"}}`,
		`{"type":"user","promptId":"p2","message":{"role":"user","content":"second task"}}`,
		`{"type":"user","promptId":"p3","message":{"role":"user","content":"ok that's fine"}}`,
	})
	got := RecentPrompts("claude_code", p, "p3", 3)
	want := []string{"second task", "first task"} // newest-first, current (p3) excluded
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestRecentPromptsRespectsN(t *testing.T) {
	p := writeTranscript(t, []string{
		`{"type":"user","promptId":"a","message":{"role":"user","content":"one"}}`,
		`{"type":"user","promptId":"b","message":{"role":"user","content":"two"}}`,
		`{"type":"user","promptId":"c","message":{"role":"user","content":"three"}}`,
	})
	if got := RecentPrompts("claude_code", p, "c", 1); len(got) != 1 || got[0] != "two" {
		t.Fatalf("N=1 got %v", got)
	}
}

func TestRecentPromptsUnsupportedSourceNil(t *testing.T) {
	p := writeTranscript(t, []string{`{"type":"user","promptId":"x","message":{"role":"user","content":"hi"}}`})
	if got := RecentPrompts("codex", p, "y", 3); got != nil {
		t.Fatalf("unsupported source should be nil, got %v", got)
	}
	if got := RecentPrompts("claude_code", "", "y", 3); got != nil {
		t.Fatalf("empty path should be nil, got %v", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/resolve/ -run TestRecent`
Expected: FAIL — `undefined: RecentPrompts`.

- [ ] **Step 3: Implement**

Create `internal/agent/resolve/recent.go`:

```go
package resolve

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// RecentReader is an optional capability: return the last user prompts preceding
// currentPromptID, newest-first, up to n. Readers that can't provide history omit it.
type RecentReader interface {
	RecentUserPrompts(transcriptPath, currentPromptID string, n int) []string
}

// RecentPrompts returns up to n prior user prompts (newest-first) for the source,
// or nil when the source has no history reader or inputs are empty. Best-effort.
func RecentPrompts(source, transcriptPath, currentPromptID string, n int) []string {
	if n <= 0 || transcriptPath == "" {
		return nil
	}
	r, ok := readers[source]
	if !ok {
		return nil
	}
	rr, ok := r.(RecentReader)
	if !ok {
		return nil
	}
	return rr.RecentUserPrompts(transcriptPath, currentPromptID, n)
}

const recentTailBytes = 128 * 1024

// RecentUserPrompts tail-scans the transcript (bounded window) for user prompts,
// excludes currentPromptID, and returns up to n newest-first. It deliberately does
// NOT use/advance the append-only cursor from Read — history re-reads a bounded
// tail so current-prompt reads stay correct. Any error yields nil.
func (r *ClaudeReader) RecentUserPrompts(path, currentPromptID string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	var off int64
	if st.Size() > recentTailBytes {
		off = st.Size() - recentTailBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	br := bufio.NewReaderSize(f, 64*1024)
	if off > 0 {
		// Drop the first (possibly partial) line so we only parse complete records.
		if _, err := br.ReadString('\n'); err != nil {
			return nil
		}
	}
	type up struct{ id, text string }
	var prompts []up
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: trailing partial line not consumed
		}
		if id, text, ok := userPrompt(line); ok {
			prompts = append(prompts, up{id, text})
		}
	}
	out := make([]string, 0, n)
	for i := len(prompts) - 1; i >= 0 && len(out) < n; i-- {
		if prompts[i].id == currentPromptID {
			continue
		}
		out = append(out, prompts[i].text)
	}
	return out
}

// userPrompt parses one JSONL line and returns (id, text, true) for a user prompt.
// Reuses the tolerant shapes from claude.go (same package).
func userPrompt(line string) (id, text string, ok bool) {
	var ln claudeLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", "", false
	}
	if ln.Type != "user" {
		return "", "", false
	}
	t, ok := extractText(ln.Message)
	if !ok {
		return "", "", false
	}
	id = ln.PromptID
	if id == "" {
		id = ln.UUID
	}
	return id, t, true
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/agent/resolve/ -run TestRecent`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/resolve/recent.go internal/agent/resolve/recent_test.go
git commit -m "feat(resolve): tail-scan recent user prompts from the transcript"
```

---

### Task 3: Eligibility + context gatherer + wire the daemon

**Files:**
- Create: `internal/agent/enrich/context.go`
- Test: `internal/agent/enrich/context_test.go`
- Create: `internal/agent/daemon/context.go`
- Test: `internal/agent/daemon/context_test.go`
- Modify: `internal/agent/daemon/daemon.go` (`process()`, ~line 71)

**Interfaces:**
- Consumes: `enrich.Meta` (Task 1), `resolve.RecentPrompts` (Task 2), `queue.Job` (has `Source`, `Cwd`, `TranscriptPath`, `PromptID`).
- Produces: `enrich.ContextEligible(source string) bool`; daemon `contextMeta(j queue.Job) enrich.Meta`.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/enrich/context_test.go`:

```go
package enrich

import "testing"

func TestContextEligible(t *testing.T) {
	for _, s := range []string{"claude_code", "codex"} {
		if !ContextEligible(s) {
			t.Errorf("%s should be eligible", s)
		}
	}
	for _, s := range []string{"", "cursor", "other", "gemini"} {
		if ContextEligible(s) {
			t.Errorf("%s should not be eligible", s)
		}
	}
}
```

Create `internal/agent/daemon/context_test.go`:

```go
package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

func TestGitBranch(t *testing.T) {
	dir := t.TempDir()
	if got := gitBranch(dir); got != "" {
		t.Fatalf("no .git should yield empty, got %q", got)
	}
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feat/x\n"), 0o600)
	if got := gitBranch(dir); got != "feat/x" {
		t.Fatalf("branch: got %q", got)
	}
	os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("a1b2c3d4\n"), 0o600) // detached
	if got := gitBranch(dir); got != "" {
		t.Fatalf("detached HEAD should be empty, got %q", got)
	}
}

func TestProjectName(t *testing.T) {
	dir := t.TempDir()
	if got := projectName(dir); got != "" {
		t.Fatalf("no .keld.toml should yield empty, got %q", got)
	}
	os.WriteFile(filepath.Join(dir, ".keld.toml"), []byte("name = \"Keld Atlas\"\ndescription = \"x\"\n"), 0o600)
	if got := projectName(dir); got != "Keld Atlas" {
		t.Fatalf("project: got %q", got)
	}
}

func TestBudget(t *testing.T) {
	got := budget([]string{"  line one\nwith break ", strings.Repeat("z", 500)}, 400, 1500)
	if len(got) != 2 || got[0] != "line one with break" {
		t.Fatalf("oneline/trim: got %v", got)
	}
	if r := []rune(got[1]); len(r) != 401 || !strings.HasSuffix(got[1], "…") { // 400 runes + ellipsis
		t.Fatalf("per-item cap: runes=%d", len([]rune(got[1])))
	}
	// total budget: three ~400-char prompts, cap 900 -> keep 2
	big := []string{strings.Repeat("a", 400), strings.Repeat("b", 400), strings.Repeat("c", 400)}
	if kept := budget(big, 400, 900); len(kept) != 2 {
		t.Fatalf("total budget kept %d, want 2", len(kept))
	}
}

func TestContextMetaAssembles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".keld.toml"), []byte("name = \"Proj\"\n"), 0o600)
	tp := filepath.Join(dir, "t.jsonl")
	os.WriteFile(tp, []byte(
		`{"type":"user","promptId":"p1","message":{"role":"user","content":"earlier work"}}`+"\n"+
			`{"type":"user","promptId":"p2","message":{"role":"user","content":"ok"}}`+"\n"), 0o600)
	m := contextMeta(queue.Job{Source: "claude_code", Cwd: dir, TranscriptPath: tp, PromptID: "p2"})
	if m.Repo != dir || m.Tool != "claude_code" || m.GitBranch != "main" || m.Project != "Proj" {
		t.Fatalf("meta base: %+v", m)
	}
	if len(m.RecentPrompts) != 1 || m.RecentPrompts[0] != "earlier work" {
		t.Fatalf("recent: %v", m.RecentPrompts)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/agent/enrich/ -run TestContextEligible ./internal/agent/daemon/ -run 'TestGitBranch|TestProjectName|TestBudget|TestContextMeta'`
Expected: FAIL — `undefined: ContextEligible`, `undefined: gitBranch`, etc.

- [ ] **Step 3: Implement eligibility**

Create `internal/agent/enrich/context.go`:

```go
package enrich

// interactiveCodingTools receive conversation-context augmentation by default:
// their prompts are fragments of an ongoing coding session that need surrounding
// context to classify well. Other sources are one-shot and stay unaugmented.
var interactiveCodingTools = map[string]bool{"claude_code": true, "codex": true}

// ContextEligible reports whether a source should receive context augmentation.
func ContextEligible(source string) bool { return interactiveCodingTools[source] }
```

- [ ] **Step 4: Implement the daemon gatherer**

Create `internal/agent/daemon/context.go`:

```go
package daemon

import (
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
	"github.com/ncx-ai/keld-cli/internal/agent/resolve"
)

const (
	recentPromptCount = 3    // how many prior user prompts to include
	recentPromptCap   = 400  // per-prompt char cap
	recentPromptTotal = 1500 // total char budget across prompts
)

// contextMeta builds an augmented Meta for an eligible job: repo/tool plus git
// branch, project name, and bounded recent user prompts. Every field is
// best-effort — failures degrade to empty, never error.
func contextMeta(j queue.Job) enrich.Meta {
	recent := resolve.RecentPrompts(j.Source, j.TranscriptPath, j.PromptID, recentPromptCount)
	return enrich.Meta{
		Repo:          j.Cwd,
		Tool:          j.Source,
		GitBranch:     gitBranch(j.Cwd),
		Project:       projectName(j.Cwd),
		RecentPrompts: budget(recent, recentPromptCap, recentPromptTotal),
	}
}

// budget one-lines + trims each prompt, caps each to perCap chars, and stops once
// the running total would exceed totalCap (input is newest-first, so newest win).
func budget(prompts []string, perCap, totalCap int) []string {
	out := make([]string, 0, len(prompts))
	total := 0
	for _, p := range prompts {
		p = strings.TrimSpace(oneLine(p))
		if p == "" {
			continue
		}
		if r := []rune(p); len(r) > perCap {
			p = string(r[:perCap]) + "…" // rune-safe: never split a multibyte char
		}
		if total+len(p) > totalCap && len(out) > 0 {
			break
		}
		out = append(out, p)
		total += len(p)
	}
	return out
}

// oneLine collapses whitespace/newlines so a multi-line prompt stays one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// gitBranch returns the current branch from <dir>/.git/HEAD, or "" (detached /
// missing / error).
func gitBranch(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	const prefix = "ref: refs/heads/"
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, prefix) {
		return strings.TrimPrefix(s, prefix)
	}
	return "" // detached HEAD (raw sha) has no branch name
}

// projectName returns the top-level `name` from <dir>/.keld.toml, or "".
func projectName(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".keld.toml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Name string `toml:"name"`
	}
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return ""
	}
	return cfg.Name
}
```

- [ ] **Step 5: Wire `process()`**

In `internal/agent/daemon/daemon.go`, in `process()`, replace the single line:

```go
	profile := enrich.Run(text, j.Source, enrich.Meta{Repo: j.Cwd, Tool: j.Source}, m)
```

with:

```go
	meta := enrich.Meta{Repo: j.Cwd, Tool: j.Source}
	if enrich.ContextEligible(j.Source) {
		meta = contextMeta(j)
	}
	profile := enrich.Run(text, j.Source, meta, m)
```

- [ ] **Step 6: Run the tests + build to verify they pass**

Run: `go test ./internal/agent/enrich/... ./internal/agent/daemon/...`
Expected: PASS (new + existing daemon tests).

- [ ] **Step 7: Commit**

```bash
git add internal/agent/enrich/context.go internal/agent/enrich/context_test.go internal/agent/daemon/context.go internal/agent/daemon/context_test.go internal/agent/daemon/daemon.go
git commit -m "feat(daemon): augment eligible-source classification with session context"
```

---

### Task 4: Eval — context-aware gold rows

**Files:**
- Modify: `internal/agent/enrich/eval/eval.go` (GoldRow fields + `Meta` helper)
- Modify: `internal/agent/enrich/eval/gold.jsonl` (add fragment rows)
- Test: `internal/agent/enrich/eval/eval_test.go` (parse + Meta helper)

**Interfaces:**
- Consumes: `enrich.Meta` (Task 1).
- Produces: `GoldRow` gains `RecentPrompts []string`, `Repo`, `Branch`, `Project`; `func (r GoldRow) Meta(source string) enrich.Meta`.

- [ ] **Step 1: Write the failing test**

Create (or add to) `internal/agent/enrich/eval/eval_test.go`:

```go
package eval

import "testing"

func TestGoldRowMetaFromContext(t *testing.T) {
	r := GoldRow{
		Text:          "ok do it",
		RecentPrompts: []string{"add the compliance flag", "right-align the pills"},
		Repo:          "keld-atlas", Branch: "feat/pills", Project: "Keld Atlas",
	}
	m := r.Meta("claude_code")
	if m.Repo != "keld-atlas" || m.GitBranch != "feat/pills" || m.Project != "Keld Atlas" || m.Tool != "claude_code" {
		t.Fatalf("meta base: %+v", m)
	}
	if len(m.RecentPrompts) != 2 || m.RecentPrompts[0] != "add the compliance flag" {
		t.Fatalf("recent: %v", m.RecentPrompts)
	}
}

func TestLoadGoldParsesContextFields(t *testing.T) {
	rows, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if len(r.RecentPrompts) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected at least one gold row with recent_prompts context")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/enrich/eval/ -run 'TestGoldRowMeta|TestLoadGoldParsesContext'`
Expected: FAIL — `GoldRow` has no `RecentPrompts`/`Meta`; no gold row has context.

- [ ] **Step 3: Add fields + Meta helper**

In `internal/agent/enrich/eval/eval.go`, add the context fields to `GoldRow` (after `Text`):

```go
	RecentPrompts []string `json:"recent_prompts"` // optional preceding user prompts (newest-first)
	Repo          string   `json:"repo"`
	Branch        string   `json:"branch"`
	Project       string   `json:"project"`
```

And add a helper (below the `GoldRow` type):

```go
// Meta builds the enrich.Meta an augmented run would see for this gold row.
func (r GoldRow) Meta(source string) enrich.Meta {
	return enrich.Meta{
		Repo:          r.Repo,
		Tool:          source,
		GitBranch:     r.Branch,
		Project:       r.Project,
		RecentPrompts: r.RecentPrompts,
	}
}
```

- [ ] **Step 4: Add fragment gold rows**

Append these lines to `internal/agent/enrich/eval/gold.jsonl` (each a single line; fragments that are unclassifiable alone but clear in context):

```json
{"text":"ok that's fine, do it","recent_prompts":["add the compliance flag to single-event activity rows","right-align the enrichment pills"],"repo":"keld-atlas","branch":"feat/pills","project":"Keld Atlas","function_guess":"eng","subcategory":"eng.dev","activity_type":"generate"}
{"text":"yes","recent_prompts":["write the failing test for the sidecar confidence fix","run it to confirm it fails"],"repo":"keld-cli","branch":"fix/sidecar","project":"Keld CLI","function_guess":"eng","subcategory":"eng.test","activity_type":"generate"}
{"text":"revert that","recent_prompts":["change the pill fill to a subdued tint","assign a color to the team badge"],"repo":"keld-atlas","branch":"feat/pills","project":"Keld Atlas","function_guess":"eng","subcategory":"eng.dev","activity_type":"transform"}
{"text":"looks good, ship it","recent_prompts":["draft the Q3 revenue summary email","tighten the wording on the forecast paragraph"],"repo":"","branch":"","project":"","function_guess":"fin","subcategory":"fin.fpa","activity_type":"generate"}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/agent/enrich/eval/`
Expected: PASS (new tests + existing eval tests; the blank-gold-field "not scored" behavior is unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/enrich/eval/eval.go internal/agent/enrich/eval/gold.jsonl internal/agent/enrich/eval/eval_test.go
git commit -m "test(eval): context-aware gold rows + GoldRow.Meta for augmentation eval"
```

---

## Notes for the final whole-branch review

- **Manual model-eval (proves the lift; needs the sidecar).** The gold rows + `GoldRow.Meta` above make a before/after measurable, but scoring against the real GLiNER2 requires the model (not in CI). Verification procedure: start the sidecar (`~/.keld/sidecar-venv/bin/python sidecar/serve.py --port=PORT`), and for each context-bearing gold row POST `/classify` twice — once with `Meta{}.Preamble()+text` (baseline) and once with `row.Meta("claude_code").Preamble()+text` (augmented) — and confirm augmented matches the gold `function_guess`/`subcategory` where baseline does not, with no regression on non-fragment rows. (Mirrors the earlier confidence-verify script.)
- **Full build/test before finishing:** `go build ./...` and `go test ./internal/agent/...` (all green; note any pre-existing failures separately).
- **Codex:** `RecentPrompts` returns nil for Codex (no `RecentReader`), so it gets metadata-only augmentation today; a Codex transcript reader implementing `RecentReader` is a clean fast-follow.
- **Configurable source list:** the allowlist is a hardcoded default (`{claude_code, codex}`) in `enrich.ContextEligible`. Making it settings-overridable (via `settings.Live`, threaded like `IncludeEntityText`) is a deliberate follow-up, not v1.
- **Privacy invariant:** confirm context flows only into the classification preamble (never persisted/sent) and entity/sensitivity passes still get raw text.
