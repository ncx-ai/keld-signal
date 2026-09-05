package projects

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// Stage 5 of the lifecycle. Every test here corresponds to a row in the
// artifact's "the automatic fold" table; the numbers in the names are those
// rows, so a failure names the claim that stopped being true.

func org(id, title, team string, repos ...string) settings.RemoteProject {
	return settings.RemoteProject{ID: id, Title: title, Team: team, Repos: repos}
}

func repoDims(v string) map[string]enrich.Labeled {
	return map[string]enrich.Labeled{
		DimRepo: {Value: v, Status: enrich.WorkstreamAttributed},
	}
}

func TestT5FullContainmentFoldsAndNothingLosesItsAttribution(t *testing.T) {
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("keld_projects:signal", "Signal On-Device Client", "Keld Projects",
			"github.com/ncx-ai/keld-signal", "github.com/ncx-ai/keld-agent"),
	})

	// The "before" is the machine BEFORE the definition arrived — the org
	// project does not exist yet, which is the whole reason the local one was
	// made. (With both present it is already a conflict; T7 asserts that.)
	before := Attribute(repoDims("github.com/ncx-ai/keld-signal"), d.Projects, noneOff, nil)
	if before.ProjectID != "p_signal" {
		t.Fatalf("precondition: attributed to %q", before.ProjectID)
	}

	next, moved := Inherit(d, remote, noneOff)
	if len(moved) != 1 {
		t.Fatalf("want one fold, got %d", len(moved))
	}
	if moved[0].OrgID != "keld_projects:signal" || moved[0].LocalID != "p_signal" {
		t.Fatalf("fold reported wrongly: %+v", moved[0])
	}
	if _, still := findProject(next, "p_signal"); still {
		t.Fatal("the local project survived the fold")
	}
	// The point of the whole rule: the block still lands somewhere, and it
	// lands on the org's project.
	after := Attribute(repoDims("github.com/ncx-ai/keld-signal"),
		MergeCandidates(next.Projects, remote), noneOff, nil)
	if after.ProjectID != "keld_projects:signal" {
		t.Fatalf("after the fold, attributed to %q (reason %q)", after.ProjectID, after.Reason)
	}
}

func TestT6PartialOverlapDoesNotFold(t *testing.T) {
	// ⚠️ THE SAFETY RULE. The org approved one of the two repositories this
	// local project holds. Folding would carry the OTHER one onto the org's
	// project — a local invention wearing the org's name, indistinguishable
	// afterwards from something an admin decided.
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_two", "two repos",
			"github.com/ncx-ai/keld-signal", "github.com/ncx-ai/private-thing"),
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("keld_projects:signal", "Signal", "Keld Projects", "github.com/ncx-ai/keld-signal"),
	})
	next, moved := Inherit(d, remote, noneOff)
	if len(moved) != 0 {
		t.Fatalf("partial overlap folded: %+v", moved)
	}
	if _, still := findProject(next, "p_two"); !still {
		t.Fatal("the local project was removed on a partial overlap")
	}
}

func TestT7TheFoldLeavesNoConflictBehind(t *testing.T) {
	// The state this ordering exists to prevent: local and org both claiming
	// one repository. Asserted through the matcher, which is what would break.
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("keld_projects:signal", "Signal", "Keld Projects", "github.com/ncx-ai/keld-signal"),
	})
	// Without the fold, the two coexist and NEITHER attributes.
	clash := Attribute(repoDims("github.com/ncx-ai/keld-signal"),
		MergeCandidates(d.Projects, remote), noneOff, nil)
	if clash.Reason != ReasonConflict {
		t.Fatalf("precondition: want %q, got %q — if this is no longer a conflict, "+
			"the same-poll ordering can be relaxed", ReasonConflict, clash.Reason)
	}
	next, _ := Inherit(d, remote, noneOff)
	fixed := Attribute(repoDims("github.com/ncx-ai/keld-signal"),
		MergeCandidates(next.Projects, remote), noneOff, nil)
	if fixed.Reason == ReasonConflict {
		t.Fatal("still a conflict after the fold")
	}
}

func TestT8TwoLocalProjectsUnderOneOrgProjectBothFold(t *testing.T) {
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_a", "A", "github.com/acme/a"),
		localProject("p_b", "B", "github.com/acme/b"),
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:platform", "Platform", "Eng", "github.com/acme/a", "github.com/acme/b"),
	})
	next, moved := Inherit(d, remote, noneOff)
	if len(moved) != 2 {
		t.Fatalf("want both folded, got %d: %+v", len(moved), moved)
	}
	if len(next.Projects) != 1 || next.Projects[0].ID != "org:platform" {
		t.Fatalf("want one overlay left, got %+v", next.Projects)
	}
	got := DeclaredRepos(next.Projects[0])
	if len(got) != 2 {
		t.Fatalf("both rules must survive the merge, got %v", got)
	}
}

func TestNEGATIVEARulelessLocalProjectIsNeverFolded(t *testing.T) {
	// ⚠️ The empty set is contained in every set. Treating "no rules" as
	// inheritable would fold every ruleless local project into whichever org
	// value came first — a silent, arbitrary reassignment of something a
	// person named on purpose.
	d := Document{Version: CurrentVersion, Projects: []Project{
		{ID: "p_empty", Title: "Thinking", Workstream: "development", Origin: OriginUser},
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:anything", "Anything", "Eng", "github.com/acme/a"),
	})
	_, moved := Inherit(d, remote, noneOff)
	if len(moved) != 0 {
		t.Fatalf("a ruleless project was folded: %+v", moved)
	}
}

func TestNEGATIVEAnOrgProjectIsNeverFoldedIntoAnother(t *testing.T) {
	d := Document{Version: CurrentVersion, Projects: []Project{
		{ID: "org:one", Title: "One", Origin: OriginAtlas, Repos: []string{"github.com/acme/a"}},
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:two", "Two", "Eng", "github.com/acme/a"),
	})
	_, moved := Inherit(d, remote, noneOff)
	if len(moved) != 0 {
		t.Fatalf("an org overlay was folded: %+v", moved)
	}
}

func TestNEGATIVEAWorkstreamThatIsOffNeverInherits(t *testing.T) {
	// The org's bucket is switched off on this machine, so its projects are
	// excluded from matching. Folding into one would move work into a bucket
	// the person said their work never lands in.
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_a", "A", "github.com/acme/a"),
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:mkt", "Campaign", "Marketing", "github.com/acme/a"),
	})
	off := func(key string) bool { return key == "marketing" || key == "Marketing" }
	_, moved := Inherit(d, remote, off)
	if len(moved) != 0 {
		t.Fatalf("folded into a workstream that is off: %+v", moved)
	}
}

func TestNEGATIVENoRemoteProjectsChangesNothing(t *testing.T) {
	// Send to Atlas off, or a poll that has not happened yet. The local loop
	// is untouched.
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_a", "A", "github.com/acme/a"),
	}}
	next, moved := Inherit(d, nil, noneOff)
	if len(moved) != 0 || len(next.Projects) != 1 {
		t.Fatalf("nothing should change: moved=%+v projects=%+v", moved, next.Projects)
	}
}

func TestInheritIsIdempotent(t *testing.T) {
	// The poll runs every five minutes. A second pass over an already-folded
	// document must do nothing at all, or every poll would rewrite the file.
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_a", "A", "github.com/acme/a"),
	}}
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:platform", "Platform", "Eng", "github.com/acme/a"),
	})
	once, moved := Inherit(d, remote, noneOff)
	if len(moved) != 1 {
		t.Fatalf("first pass folded %d", len(moved))
	}
	twice, again := Inherit(once, remote, noneOff)
	if len(again) != 0 {
		t.Fatalf("second pass folded again: %+v", again)
	}
	if len(twice.Projects) != len(once.Projects) {
		t.Fatal("the second pass changed the document")
	}
}

func TestCoversIsCaseAndSpaceInsensitive(t *testing.T) {
	if !covers([]string{"GitHub.com/Acme/Web "}, []string{"github.com/acme/web"}) {
		t.Fatal("repository comparison must fold case and trim, as it does everywhere else")
	}
	if covers([]string{"github.com/acme/web"}, nil) {
		t.Fatal("an empty want must never count as covered")
	}
}
