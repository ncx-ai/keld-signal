package watch

import (
	"path/filepath"
	"runtime"
	"testing"
)

// The analysis allowlist must be the STABLE ancestors, not the globbed leaves
// DiscoverRoots returns: a Gemini or Cowork session directory is created when
// the session starts, so an allowlist of leaves — resolved once, at sidecar
// spawn — would reject every transcript written after the daemon came up.
func TestAnalyzeRootsAreStableAncestors(t *testing.T) {
	home := t.TempDir()
	got := analyzeRoots(home, "darwin")

	want := []string{
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".gemini", "tmp"),
		filepath.Join(home, "Library", "Application Support", "Claude", "local-agent-mode-sessions"),
	}
	for _, w := range want {
		if !contains(got, w) {
			t.Errorf("missing analysis root %q; got %v", w, got)
		}
	}
	// Existence is NOT a precondition: none of these directories exist under
	// this t.TempDir(). Filtering on existence would silently shrink the
	// allowlist to whatever happened to be on disk at spawn — and the sidecar
	// is spawned once, before the user's first session of the day.
	if len(got) != len(want) {
		t.Errorf("got %d roots, want %d: %v", len(got), len(want), got)
	}
}

func TestAnalyzeRootsExcludeDarwinOnlyEntriesElsewhere(t *testing.T) {
	home := t.TempDir()
	got := analyzeRoots(home, "linux")
	for _, g := range got {
		if filepath.Base(g) == "local-agent-mode-sessions" {
			t.Errorf("cowork root leaked onto linux: %v", got)
		}
	}
}

// An operator who moved a transcript tree with KELD_WATCH_ROOTS must not then
// have /analyze refuse to read it: the two settings describe the same files.
func TestAnalyzeRootsIncludeOperatorConfiguredRoots(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "elsewhere", "projects")
	mkdir(t, dir)
	t.Setenv(envExtraRoots, "cowork:"+dir)

	if !contains(analyzeRoots(home, runtime.GOOS), dir) {
		t.Errorf("KELD_WATCH_ROOTS dir not allowlisted for analysis: %v", analyzeRoots(home, runtime.GOOS))
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
