package projects

import "testing"

// ⚠️ **`RepoLike` is a SHAPE test on free text, so its whole risk is
// over-matching.** A value's tags reach the daemon prefix-stripped
// (`repository: acme/web` is typed in Atlas and `acme/web` arrives), so the
// deterministic pass has to recognise a repository without a label. That makes
// every `a/b` an admin ever types a candidate rule, and a false positive here
// silently attributes someone's work to the wrong project — the failure mode
// this codebase refuses everywhere else.
//
// This table is adversarial on purpose: it is the free tags a real admin writes.
func TestRepoLikeDoesNotOverMatchFreeTags(t *testing.T) {
	for _, s := range []string{
		"internal/agent", // a source path
		"src/main",       // a source path
		"and/or",         // prose
		"24/7",           // prose
		"q3/2026",        // a period
		"design/ux",      // a discipline pair
		"acme/",          // a trailing slash
		"/acme",          // a leading slash
		"a/b/c/d/e/f",    // too deep to be a remote
	} {
		if RepoLike(s) {
			t.Errorf("RepoLike(%q) = true; a free tag must not read as a repository", s)
		}
	}
}

// The other half: the shapes that MUST be recognised, or an org whose admin
// tagged values correctly gets no deterministic attribution at all.
func TestRepoLikeAcceptsRealRemotes(t *testing.T) {
	for _, s := range []string{
		"github.com/ncx-ai/keld-signal",
		"ncx-ai/keld-signal",
		"gitlab.com/group/subgroup/project",
		"NCX-AI/Keld-Signal",
	} {
		if !RepoLike(s) {
			t.Errorf("RepoLike(%q) = false; this is a real remote shape", s)
		}
	}
}
