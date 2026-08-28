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

// The cases production actually hits: 43.1% of recorded lines have a cwd that is NOT the git
// root (measured over 62,920 lines of local transcripts — 17,036 of them in
// keld-atlas/services/web alone), and every git worktree is in that set. Reading only
// <cwd>/.git/HEAD returned no branch and no project for all of them.
func TestGitBranchFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	sub := filepath.Join(root, "services", "web", "components")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(sub); got != "main" {
		t.Fatalf("branch from a subdirectory: got %q, want main", got)
	}
	if got := repoRoot(sub); got != root {
		t.Fatalf("repoRoot from a subdirectory: got %q, want %q", got, root)
	}
}

// A worktree's .git is a FILE holding `gitdir: <path>`, and its HEAD lives at that path — so a
// worktree checked out on a feature branch reported no branch at all, which is precisely where a
// branch carries the most information.
func TestGitBranchInWorktree(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "repo")
	wt := filepath.Join(main, ".claude", "worktrees", "feat-x")
	gitdir := filepath.Join(main, ".git", "worktrees", "feat-x")
	for _, d := range []string{filepath.Join(main, ".git"), gitdir, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(main, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/feat/x\n"), 0o600)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o600)
	if got := gitBranch(wt); got != "feat/x" {
		t.Fatalf("worktree branch: got %q, want feat/x", got)
	}
	sub := filepath.Join(wt, "services", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(sub); got != "feat/x" {
		t.Fatalf("worktree branch from a subdirectory: got %q, want feat/x", got)
	}
}

// The walk must not escape the repository: a cwd outside any checkout stays empty rather than
// inheriting a branch from an unrelated ancestor.
func TestGitBranchStopsOutsideARepo(t *testing.T) {
	dir := t.TempDir()
	if got := gitBranch(dir); got != "" {
		t.Fatalf("no .git anywhere above: got %q, want empty", got)
	}
	if got := repoRoot(dir); got != "" {
		t.Fatalf("repoRoot with no .git: got %q, want empty", got)
	}
}

// A removed worktree must not inherit the parent checkout's branch.
func TestGitBranchDoesNotEscapeADeletedWorktree(t *testing.T) {
	main := t.TempDir()
	if err := os.MkdirAll(filepath.Join(main, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(main, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600)
	gone := filepath.Join(main, ".claude", "worktrees", "removed")
	if got := gitBranch(gone); got != "" {
		t.Fatalf("deleted worktree resolved to %q, want empty", got)
	}
	if got := repoRoot(gone); got != "" {
		t.Fatalf("deleted worktree repoRoot: got %q, want empty", got)
	}
}

func TestProjectNameFromSubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, ".keld.toml"), []byte("name = \"Keld Atlas\"\n"), 0o600)
	sub := filepath.Join(root, "services", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := projectName(sub); got != "Keld Atlas" {
		t.Fatalf("project from a subdirectory: got %q, want Keld Atlas", got)
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
