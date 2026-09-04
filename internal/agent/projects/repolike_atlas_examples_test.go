package projects

import "testing"

// ⚠️ **These are the strings ATLAS ITSELF puts in front of an admin.** Its tag
// editor's own test fixture is `repository: acme/website` and the wire_projects
// docstring's example is `repository: acme/web`; the prefix is stripped before
// the daemon sees them, so what arrives is `acme/website` and `acme/web`.
//
// A candidacy filter that rejects those rejects the org that tagged its values
// exactly as the product told it to — and the failure is silent, because the
// value still arrives, still looks correct in Atlas, and simply never
// attributes anything. That is worse than the over-match it was tightened to
// prevent: an over-match is inert unless it collides with a real remote, while
// this is a total loss of deterministic attribution for a correctly configured
// org.
func TestRepoLikeAcceptsAtlasOwnTagExamples(t *testing.T) {
	for _, s := range []string{
		"acme/web",     // wire_projects' docstring example
		"acme/website", // the tag editor's own test fixture
		"keld/atlas",   // two bare words, no punctuation, a real shape
	} {
		if !RepoLike(s) {
			t.Errorf("RepoLike(%q) = false, but this is exactly what Atlas's own tag "+
				"editor teaches an admin to type. A correctly configured org would get "+
				"NO deterministic attribution and no error.", s)
		}
	}
}
