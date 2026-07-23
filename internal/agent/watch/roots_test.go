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

func TestDiscoverRootsGemini(t *testing.T) {
	home := t.TempDir()
	// Create two Gemini project chat directories.
	projA := filepath.Join(home, ".gemini", "tmp", "projA", "chats")
	projB := filepath.Join(home, ".gemini", "tmp", "projB", "chats")
	mkdir(t, projA)
	mkdir(t, projB)

	// Test on both macOS and Linux.
	for _, goos := range []string{"darwin", "linux"} {
		roots := discoverRoots(home, goos)
		if !hasRoot(roots, "gemini_cli", projA) {
			t.Errorf("%s: missing gemini root for projA; got %+v", goos, roots)
		}
		if !hasRoot(roots, "gemini_cli", projB) {
			t.Errorf("%s: missing gemini root for projB; got %+v", goos, roots)
		}
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
