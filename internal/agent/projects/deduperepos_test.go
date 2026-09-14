package projects

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// THE STORY: a repository declared twice on an org project is shown once.
//
// ⚠️ Observed on a real machine: `Signal On-Device Client` lists
// `github.com/ncx-ai/keld-signal` twice, and the Projects pane drew it twice.
// It always attributed correctly — the matcher counts a project once — so this
// is about what a person is shown. Cleaning at the import boundary is why one
// call covers both: the matcher's candidates and the pane's `rules` both read
// these values, so storage and display cannot disagree about what was declared.
func TestARepoDeclaredTwiceByTheOrgIsImportedOnce(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID:    "keld_projects:signal_on_device_client",
		Title: "Signal On-Device Client",
		Repos: []string{repoKeldSignal, repoKeldSignal},
	}})

	if len(got) != 1 {
		t.Fatalf("want 1 project, got %d", len(got))
	}
	if n := len(got[0].Repos); n != 1 {
		t.Fatalf("repos = %v, want the duplicate removed", got[0].Repos)
	}
	if got[0].Repos[0] != repoKeldSignal {
		t.Fatalf("repos[0] = %q, want %q", got[0].Repos[0], repoKeldSignal)
	}
}

// ⚠️ **SAMENESS IS THE MATCHER'S, NOT STRING EQUALITY.** normalizeRepo
// lowercases and strips `https://`, `ssh://`, `git@` and `.git`, so all of
// these ARE the same repo as far as attribution is concerned. Deduping on the
// raw string would leave the pane showing four rules for one repository while
// the matcher treats them as one — display and match disagreeing about what a
// duplicate is, which is the bug one level over.
func TestTheSameRepoSpeltFourWaysIsImportedOnce(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID: "keld_projects:x",
		Repos: []string{
			"github.com/ncx-ai/keld-signal",
			"https://GitHub.com/ncx-ai/keld-signal",
			"git@github.com:ncx-ai/keld-signal.git",
			"github.com/ncx-ai/keld-signal.git",
		},
	}})

	if n := len(got[0].Repos); n != 1 {
		t.Fatalf("repos = %v, want one — every spelling normalises to the same repo", got[0].Repos)
	}
	// The FIRST spelling survives: these are shown to a person as the project's
	// rules, and rewriting them into a normalised form would be editing the
	// org's own text rather than removing a repetition.
	if got[0].Repos[0] != "github.com/ncx-ai/keld-signal" {
		t.Fatalf("repos[0] = %q, want the first spelling kept verbatim", got[0].Repos[0])
	}
}

// NEGATIVE, AND THE ONE THAT MATTERS: two DIFFERENT repos are both kept, in
// the order the org declared them.
//
// ⚠️ A dedupe that is too eager silently narrows what a project covers, and the
// blocks it stops matching just quietly become unattributed — the failure would
// look like "attribution got worse", not like a bad dedupe.
func TestDifferentReposAreAllKeptInOrder(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID:    "keld_projects:x",
		Repos: []string{repoKeldSignal, repoKeldAtlas, repoKeldSignal, repoAtlasTSTel},
	}})

	want := []string{repoKeldSignal, repoKeldAtlas, repoAtlasTSTel}
	if len(got[0].Repos) != len(want) {
		t.Fatalf("repos = %v, want %v", got[0].Repos, want)
	}
	for i := range want {
		if got[0].Repos[i] != want[i] {
			t.Fatalf("repos = %v, want %v (order must be the org's)", got[0].Repos, want)
		}
	}
}

// NEGATIVE, AND ASSERTED ON WHAT A PERSON ACTUALLY SEES.
//
// ⚠️ **AN EARLIER VERSION OF THIS TEST CHECKED `Repos` AND PASSED WHILE THE
// PAGE STILL SHOWED THE DUPLICATE.** `Rules` is DeclaredRepos PLUS
// MatchedCandidates, and MatchedCandidates reads `Keywords` — so an org
// declaring the same repository in both `repos` and `keywords` renders it
// twice however clean the repo list is. Caught by running the real daemon
// against the real Atlas after the first fix landed, not by the suite.
// Assert on `Rules`, because that is the list the Projects pane draws.
func TestARepoDeclaredAsBothARepoAndAKeywordAppearsOnceInRules(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID:       "keld_projects:signal_on_device_client",
		Repos:    []string{repoKeldSignal},
		Keywords: []string{repoKeldSignal}, // exactly what Atlas serves today
	}})

	rules := Rules(got[0], []string{repoKeldSignal})
	if len(rules) != 1 {
		t.Fatalf("rules = %v, want the repository named once", rules)
	}
	if rules[0] != repoKeldSignal {
		t.Fatalf("rules = %v, want %q", rules, repoKeldSignal)
	}
}

// NEGATIVE: a keyword naming a DIFFERENT repository is a real candidate and
// survives. Dropping it would silently narrow what the project covers, and the
// blocks it stops matching would just quietly become unattributed.
func TestAKeywordNamingADifferentRepoSurvives(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID:       "keld_projects:x",
		Repos:    []string{repoKeldSignal},
		Keywords: []string{repoKeldAtlas},
	}})

	rules := Rules(got[0], []string{repoKeldSignal, repoKeldAtlas})
	if len(rules) != 2 {
		t.Fatalf("rules = %v, want both repositories", rules)
	}
}

// NEGATIVE: a keyword that is not repo-shaped is free-authored text the org
// meant to keep, and is never touched by this.
func TestANonRepoKeywordIsNeverDropped(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID:       "keld_projects:x",
		Repos:    []string{repoKeldSignal},
		Keywords: []string{"on-device", "privacy", repoKeldSignal},
	}})

	kept := map[string]bool{}
	for _, k := range got[0].Keywords {
		kept[k] = true
	}
	for _, want := range []string{"on-device", "privacy"} {
		if !kept[want] {
			t.Fatalf("keywords = %v, dropped the free-text keyword %q", got[0].Keywords, want)
		}
	}
	if kept[repoKeldSignal] {
		t.Fatalf("keywords = %v, kept the keyword that merely repeats a declared repo", got[0].Keywords)
	}
}

// NEGATIVE: a rule that normalises to nothing is passed through untouched.
//
// ⚠️ It can never match anything, so collapsing several together would be
// tidying at the cost of hiding what the org actually declared — and a project
// whose rules silently shrink is exactly what the test above guards against.
func TestUnmatchableRulesAreNotCollapsed(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID:    "keld_projects:x",
		Repos: []string{"", "   ", ""},
	}})

	if n := len(got[0].Repos); n != 3 {
		t.Fatalf("repos = %v, want all three kept — none of them can match, so none is a duplicate", got[0].Repos)
	}
}

// A project with no repeated rule is byte-identical. This change must not
// alter anything that was already correct.
func TestAProjectWithNoDuplicatesIsUnchanged(t *testing.T) {
	in := []string{repoKeldSignal, repoKeldAtlas}
	got := FromRemoteProjects([]settings.RemoteProject{{ID: "keld_projects:x", Repos: in}})

	if len(got[0].Repos) != len(in) {
		t.Fatalf("repos = %v, want %v unchanged", got[0].Repos, in)
	}
	for i := range in {
		if got[0].Repos[i] != in[i] {
			t.Fatalf("repos = %v, want %v unchanged", got[0].Repos, in)
		}
	}
}

// And the duplicate still attributes, which is what makes this a display fix
// rather than a correctness one. Belt and braces with the matcher-level test.
func TestADedupedProjectStillAttributes(t *testing.T) {
	got := FromRemoteProjects([]settings.RemoteProject{{
		ID:    "keld_projects:signal_on_device_client",
		Repos: []string{repoKeldSignal, repoKeldSignal},
		Team:  "development",
	}})
	dims := dimsWith(map[string]string{DimRepo: repoKeldSignal})

	res := Attribute(dims, got, nil, nil)

	if res.ProjectID != "keld_projects:signal_on_device_client" {
		t.Fatalf("project = %q, want the deduped project to still match", res.ProjectID)
	}
	if res.Reason == ReasonConflict {
		t.Fatalf("deduping produced a conflict: %v", res.Conflict)
	}
}
