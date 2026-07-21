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
