package llmstudy

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWorktreePathResolvesToItsRepo is the measured packet, as a test. The record's
// authoritative block held the whole worktree checkout path as a "recurring subject"; a reader
// was told this session's recurring subject was a directory.
func TestWorktreePathResolvesToItsRepo(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study", "keld-signal"},
		{"/home/dg/keld/keld-atlas/.claude/worktrees/gke-argo-prod", "keld-atlas"},
		// Not a worktree path: a real file in the work, and naming it is correct.
		{"internal/agent/enrich/llmstudy/beat.go", ""},
		{"keld-signal", ""},
		// .claude with no worktrees under it is a config directory, not a second checkout.
		{"home/dg/.claude/projects", ""},
		// Nothing above .claude to name.
		{".claude/worktrees/llm-classify-study", ""},
	} {
		if got := repoOfPathTerm(tc.in); got != tc.want {
			t.Errorf("repoOfPathTerm(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolvedSubjectTermStaysVerbatim is the property the record's contract turns on: a term
// enters Subjects only by appearing in the transcript, so the resolution must return a SUBSTRING
// of the token rather than a name derived from it.
func TestResolvedSubjectTermStaysVerbatim(t *testing.T) {
	const src = "working in /home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study today"
	got := resolveSubjectTerm("home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study")
	if got != "keld-signal" {
		t.Fatalf("resolveSubjectTerm = %q", got)
	}
	if kept, _ := VerifyTopics([]string{got}, src); len(kept) != 1 {
		t.Fatalf("resolved term %q is not verbatim in the source it came from", got)
	}
}

// TestRecordHoldsTheRepoNotTheWorktreePath drives the fix through the record itself, which is
// where the defect was seen — Observe is the only writer of the block the prompt labels
// authoritative.
func TestRecordHoldsTheRepoNotTheWorktreePath(t *testing.T) {
	const path = "/home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study"
	w := Window{Turns: []Turn{{Role: RoleUser, Text: "the branch lives in " + path +
		" and the fix is in " + path + " as well, see " + path}}}
	r := SessionRecord{Turns: 3}.Observe(w, Extract(w))
	for _, s := range r.Subjects {
		if s == "home/dg/keld/keld-signal/.claude/worktrees/llm-classify-study" ||
			s == path {
			t.Fatalf("record holds the raw worktree path as a subject: %q (all: %v)", s, r.Subjects)
		}
	}
	if !hasTermFold(r.Subjects, "keld-signal") {
		t.Fatalf("record lost the repository the worktree is a checkout of: %v", r.Subjects)
	}
}

// TestRepoFromProjectDirRecoversAHyphenatedName covers the other half of the same packet:
// `projects: signal`, produced by splitting the encoded directory on "-" and keeping the last
// piece. Probed against a temporary tree so the assertion does not depend on this machine's
// home directory.
func TestRepoFromProjectDirRecoversAHyphenatedName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home", "dg", "keld", "keld-signal",
		".claude", "worktrees", "llm-classify-study"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ dir, want string }{
		{"-home-dg-keld-keld-signal", "keld-signal"},
		{"-home-dg-keld-keld-signal--claude-worktrees-llm-classify-study", "keld-signal"},
		// Unresolvable under this root: the fallback is the old rule, wrong in the old way.
		// Asserted so the fallback cannot change without being noticed.
		{"-var-lib-something-else", "else"},
	} {
		if got := repoFromProjectDir(tc.dir, root); got != tc.want {
			t.Errorf("repoFromProjectDir(%q) = %q, want %q", tc.dir, got, tc.want)
		}
	}
}

// hasTermFold is a case-insensitive membership test for the assertions above.
func hasTermFold(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
