package projects

import "testing"

// M4 · AC4, NEGATIVE, AND PINNED WHERE THE BEHAVIOUR ACTUALLY LIVES.
//
// ⚠️ One project naming the same repository twice must attribute to it, not
// read as a conflict with itself. This works today because the repo loop
// `break`s after a project's first matching rule, so a project can be appended
// to `matches` at most once — and that single `break` is the whole of it.
//
// The test exists because a careless "make conflicts more thorough" change
// would remove it, and the failure would be invisible in review: every block on
// a repo with a duplicated rule would silently stop being attributed, reported
// as a conflict between one project and itself. There is a real project on a
// developer's machine right now listing `github.com/ncx-ai/keld-signal` twice,
// so this is not hypothetical.
//
// The store-side twin is TestOneProjectNamingARepoTwiceIsNotAConflict in
// internal/agent/ledger; this one is the matcher's half and is the one that can
// actually regress.
func TestOneProjectNamingTheSameRepoTwiceAttributesRatherThanConflicts(t *testing.T) {
	p := Project{
		ID:    "keld_projects:signal_on_device_client",
		Title: "Signal On-Device Client",
		Repos: []string{repoKeldSignal, repoKeldSignal}, // named twice, as observed
		Group: "development",
	}
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, []Project{p}, nil, nil)

	if len(res.Projects) != 1 {
		t.Fatalf("a project matching its own duplicated rule must be assigned ONCE: %+v", res.Projects)
	}
	if only(res).ProjectID != p.ID {
		t.Fatalf("project = %q, want %q", only(res).ProjectID, p.ID)
	}
	if only(res).Method != MethodRepo {
		t.Fatalf("method = %q, want %q", only(res).Method, MethodRepo)
	}
}

// The other side of the same boundary: two DIFFERENT projects claiming one
// repo are two assignments, not one — the dedupe above is by project id,
// never by rule, so it must not collapse distinct projects.
func TestTwoDifferentProjectsClaimingOneRepoAreBothAssigned(t *testing.T) {
	a := Project{ID: "keld_projects:a", Title: "A", Repos: []string{repoKeldSignal}, Group: "development"}
	b := Project{ID: "keld_projects:b", Title: "B", Repos: []string{repoKeldSignal}, Group: "development"}
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, []Project{a, b}, nil, nil)

	if len(res.Projects) != 2 || res.Reason != ReasonNone {
		t.Fatalf("both claimants must be assigned: %+v", res)
	}
}
