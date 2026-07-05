package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/queue"
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
