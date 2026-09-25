package projects

import "testing"

// ⚠️ **This file first asserted that `RepoLike` itself must reject prose, and
// that was the wrong test to write.** It found a real defect — `and/or`,
// `24/7` and `design/ux` were being treated as repository RULES — but it named
// the wrong cause, and fixing `RepoLike` to satisfy it broke the real case: a
// punctuation requirement rejected `acme/web` and `acme/website`, which are
// Atlas's OWN examples in its tag editor. An org that tagged its values exactly
// as the product told it to would have got no deterministic attribution and no
// error. See repolike_atlas_examples_test.go, which pins that.
//
// Shape genuinely cannot separate `acme/web` from `design/ux`; both are two
// bare words joined by a slash, and no regex will ever tell them apart. So the
// assertions here now test the OUTCOME the earlier ones were reaching for: a
// prose tag must never become a rule, appear on a project card, or manufacture
// a conflict. `RepoLike` stays a loose candidacy filter, and matching against a
// repository this machine has actually observed is what decides.
func TestProseTagsNeverBecomeRules(t *testing.T) {
	prose := []string{"internal/agent", "src/main", "and/or", "24/7", "q3/2026", "design/ux"}
	p := Project{ID: "p1", Title: "Marketing", Keywords: prose}

	// Nothing was ever observed, so nothing may be a rule.
	if got := Rules(p, nil); len(got) != 0 {
		t.Errorf("with no observed repositories a project has no rules, got %v", got)
	}

	// Even against a real corpus of observed repositories, prose must not match.
	observed := []string{
		"github.com/ncx-ai/keld-signal",
		"github.com/ncx-ai/keld-atlas",
		"github.com/acme/web",
	}
	if got := Rules(p, observed); len(got) != 0 {
		t.Errorf("prose tags matched real repositories and became rules: %v", got)
	}
	for _, s := range prose {
		if len(MatchedCandidates(Project{Keywords: []string{s}}, observed)) != 0 {
			t.Errorf("%q matched an observed repository; it is not one", s)
		}
	}
}

// The other half of the same rule: a correctly typed tag DOES become a rule,
// but only once it has matched something real.
func TestARealTagBecomesARuleOnlyOnceItHasMatched(t *testing.T) {
	p := Project{ID: "p1", Title: "Web", Keywords: []string{"acme/web"}}

	if got := Rules(p, []string{"github.com/ncx-ai/keld-signal"}); len(got) != 0 {
		t.Errorf("a tag that matches nothing observed is not yet a rule, got %v", got)
	}
	got := Rules(p, []string{"github.com/acme/web"})
	if len(got) != 1 || got[0] != "acme/web" {
		t.Errorf("a tag that matches an observed repository is a rule, got %v", got)
	}
}
