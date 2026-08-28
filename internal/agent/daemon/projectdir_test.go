package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// The decode is FILESYSTEM-GUIDED, and these are the cases that make that
// necessary rather than pedantic. Real shapes, built for real on disk, because
// the whole difficulty is that the string alone is ambiguous.
func TestDecodeProjectDirResolvesTheAmbiguousEncoding(t *testing.T) {
	root := t.TempDir()
	// Build: <root>/keld/keld-atlas/.claude/worktrees/gke-argo-prod
	// The shape verified against a real developer machine: `--claude` is a "/"
	// followed by ".claude", and `gke-argo-prod` is ONE directory whose name
	// contains two hyphens.
	deep := filepath.Join(root, "keld", "keld-atlas", ".claude", "worktrees", "gke-argo-prod")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	// And a DECOY that makes the ambiguity real rather than theoretical: a
	// sibling `gke` directory, so a greedy shortest-match decoder would descend
	// into it and fail, and a greedy longest-match one has to be right for the
	// right reason.
	if err := os.MkdirAll(filepath.Join(root, "keld", "keld-atlas", ".claude",
		"worktrees", "gke"), 0o755); err != nil {
		t.Fatal(err)
	}

	enc := encodeLike(root) + "-keld-keld-atlas--claude-worktrees-gke-argo-prod"
	if got := decodeProjectDir(enc); got != deep {
		t.Errorf("decodeProjectDir(%q) = %q, want %q", enc, got, deep)
	}

	// A shallower one under the same tree, so this is not passing by matching
	// one hardcoded depth.
	mid := filepath.Join(root, "keld", "keld-atlas")
	if got := decodeProjectDir(encodeLike(root) + "-keld-keld-atlas"); got != mid {
		t.Errorf("mid-depth decode = %q, want %q", got, mid)
	}
}

// ⚠️ A NAME WITH NO EXISTING READING RESOLVES TO "", AND THAT IS THE WHOLE POINT.
// A guessed path is handed straight to gitRemote, which would happily read some
// OTHER repository's .git/config and publish its identity for this transcript's
// work. Transcripts routinely outlive their directories — the pre-VM Cowork
// session dirs are never cleaned up, and `/tmp` work is deleted — so this is the
// common case, not the edge one.
func TestDecodeProjectDirRefusesRatherThanGuesses(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		encodeLike(root) + "-gone",          // a sibling that does not exist
		encodeLike(root) + "-real-alsogone", // the prefix exists, the leaf does not
		"home-dg-keld",                      // no leading separator: not this layout
		"-",                                 // just the root
		"",                                  // nothing
		"-nonexistent-top-level-thing-here", // nothing on this machine reads this way
	} {
		if got := decodeProjectDir(name); got != "" {
			t.Errorf("decodeProjectDir(%q) = %q, want \"\" — a guessed path would be handed to "+
				"gitRemote and could name another repository entirely", name, got)
		}
	}
}

// A FILE where a directory segment is expected is not a match: the encoded name
// is a directory path, and matching a file would end the descent somewhere that
// cannot contain a checkout.
func TestDecodeProjectDirIgnoresFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notadir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := decodeProjectDir(encodeLike(root) + "-notadir"); got != "" {
		t.Errorf("matched a file: %q", got)
	}
}

// transcriptCwd is the decoder applied to a transcript PATH, which is the shape
// both callers actually hold: <roots>/<encoded-projdir>/<session>.jsonl.
func TestTranscriptCwdReadsTheProjectsDirectoryOutOfTheTranscriptPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "work", "widget-app")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	projects := filepath.Join(root, "projects", encodeLike(root)+"-work-widget-app")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projects, "0badc0de-0000.jsonl")
	if got := transcriptCwd(path); got != target {
		t.Errorf("transcriptCwd(%q) = %q, want %q", path, got, target)
	}
	if got := transcriptCwd(""); got != "" {
		t.Errorf("empty path = %q, want \"\"", got)
	}
}

// The cache must answer identically to a fresh resolve, and must CACHE THE EMPTY
// ANSWER TOO — a machine with stale Cowork session directories has many
// transcripts whose directories are gone, and re-walking the ReadDir chain for
// each of them on every watcher poll is exactly the cost the memoisation exists
// to avoid.
func TestFactsCacheRemembersTheEmptyAnswer(t *testing.T) {
	c := newFactsCache()
	missing := filepath.Join(t.TempDir(), "-nothing-here", "s.jsonl")
	if f := c.forTranscript(missing); f != (enrichFacts{}) {
		t.Fatalf("want empty facts for an undecodable path, got %+v", f)
	}
	c.mu.Lock()
	_, cached := c.m[missing]
	c.mu.Unlock()
	if !cached {
		t.Error("the empty answer was not cached; every poll would re-walk the filesystem")
	}
	if r := c.forTranscript(missing).resolved(); !r.Zero() {
		t.Errorf("resolved() = %+v, want the zero value so the request omits the object", r)
	}
}

// encodeLike encodes an absolute path the way Claude Code names a projects
// directory: every "/" and every "." becomes "-". It is the INVERSE of what
// decodeProjectDir undoes, written here rather than in the package because
// nothing in production ever needs to produce one — which is itself the reason
// the decode has to be filesystem-guided.
func encodeLike(abs string) string {
	out := make([]byte, 0, len(abs))
	for i := 0; i < len(abs); i++ {
		if abs[i] == '/' || abs[i] == '.' {
			out = append(out, '-')
			continue
		}
		out = append(out, abs[i])
	}
	return string(out)
}
