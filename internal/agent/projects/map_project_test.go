package projects

import (
	"errors"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

func localProject(id, title string, repos ...string) Project {
	return Project{ID: id, Title: title, Repos: repos, Group: "development", Origin: OriginUser}
}

func orgValues() []Project {
	return FromRemoteProjects([]settings.RemoteProject{
		{ID: "keld_projects:signal", Title: "Signal On-Device Client", Team: "Keld Projects"},
	})
}

func noneOff(string) bool { return false }

func TestTheStoryTheRulesMoveAndTheLocalEntryGoes(t *testing.T) {
	// ⚠️ THE WHOLE POINT: no block becomes unattributed by the move. A block is
	// attributed by a RULE (a repo remote), never by a project's identity, so
	// moving the rules moves the blocks with them — which is why deleting the
	// local entry is safe and why this asserts the rule landed BEFORE it
	// asserts the entry went.
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}

	next, err := MapProjectTo(d, orgValues(), "p_signal", "keld_projects:signal", noneOff)
	if err != nil {
		t.Fatalf("MapProjectTo: %v", err)
	}

	i, ok := findProject(next, "keld_projects:signal")
	if !ok {
		t.Fatal("no overlay entry was laid down for the org value")
	}
	target := next.Projects[i]
	if len(target.Repos) != 1 || target.Repos[0] != "github.com/ncx-ai/keld-signal" {
		t.Fatalf("the rule did not move: %+v", target.Repos)
	}
	if target.Origin != OriginAtlas {
		t.Fatalf("overlay origin = %q, want %q", target.Origin, OriginAtlas)
	}
	if _, still := findProject(next, "p_signal"); still {
		t.Fatal("the local entry survived — two projects sharing a repo rule is a conflict")
	}
}

func TestNEGATIVEBlocksKeepAttributingAfterTheMove(t *testing.T) {
	// The story's first negative, asserted through the thing that actually
	// decides it: Attribute over the same dims before and after.
	before := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	dims := map[string]enrich.Labeled{
		DimRepo: {Value: "github.com/ncx-ai/keld-signal", Status: enrich.DimensionAttributed},
	}

	got := Attribute(dims, MergeCandidates(before.Projects, orgValues()), noneOff, nil)
	if only(got).ProjectID != "p_signal" {
		t.Fatalf("before the move, attributed to %q", only(got).ProjectID)
	}

	after, err := MapProjectTo(before, orgValues(), "p_signal", "keld_projects:signal", noneOff)
	if err != nil {
		t.Fatalf("MapProjectTo: %v", err)
	}
	got = Attribute(dims, MergeCandidates(after.Projects, orgValues()), noneOff, nil)
	if only(got).ProjectID != "keld_projects:signal" {
		t.Fatalf("after the move, attributed to %q (reason %q) — the block fell out",
			only(got).ProjectID, got.Reason)
	}
}

func TestNEGATIVEKeepingBothWouldDoubleCountWhichIsWhyOneIsRemoved(t *testing.T) {
	// The reason the local entry is deleted rather than kept as a reference,
	// asserted rather than argued: two visible projects sharing a repo rule
	// BOTH get the block (overlap is allowed since 2026-09-23), so a local copy
	// of an org project's rule would count the block twice.
	both := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
		{ID: "keld_projects:signal", Title: "Signal On-Device Client",
			Repos: []string{"github.com/ncx-ai/keld-signal"}, Origin: OriginAtlas},
	}}
	got := Attribute(map[string]enrich.Labeled{
		DimRepo: {Value: "github.com/ncx-ai/keld-signal", Status: enrich.DimensionAttributed},
	}, both.Projects, noneOff, nil)
	if len(got.Projects) != 2 {
		t.Fatalf("assigned = %+v, want both — if a shared rule ever stops assigning "+
			"both, keeping the local entry as a reference becomes viable and "+
			"MapProjectTo's deletion should be revisited", got.Projects)
	}
}

func TestAnOverlayCanBeMapped(t *testing.T) {
	// Revision 2 (2026-09-25): an overlay made with "Same as" is the person's
	// own Signal project — shown and attributed like any other — so it can be
	// folded into another one like any other. It used to be refused as "an org
	// project, not ours to fold away"; with Atlas workstreams off the page, that
	// refusal left the person no way to merge it at all.
	d := Document{Version: CurrentVersion, Projects: []Project{
		{ID: "keld_projects:signal", Title: "Signal", Origin: OriginAtlas,
			Repos: []string{"github.com/ncx-ai/keld-signal"}},
		localProject("p_other", "other", "github.com/acme/other"),
	}}
	next, err := MapProjectTo(d, nil, "keld_projects:signal", "p_other", noneOff)
	if err != nil {
		t.Fatalf("MapProjectTo(overlay onto a local project): %v", err)
	}
	if _, still := findProject(next, "keld_projects:signal"); still {
		t.Fatal("the overlay survived the map — two projects would share its rule")
	}
	i, ok := findProject(next, "p_other")
	if !ok {
		t.Fatal("the target went missing")
	}
	if got := next.Projects[i].Repos; len(got) != 2 || !containsFold(got, "github.com/ncx-ai/keld-signal") {
		t.Fatalf("the overlay's rule did not move onto the target: %v", got)
	}
}

func TestNEGATIVEMappingOntoItselfIsRefused(t *testing.T) {
	// It would delete the entry and then put its rules back on the one just
	// removed. Refused rather than silently doing nothing.
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	if _, err := MapProjectTo(d, nil, "p_signal", "p_signal", noneOff); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
}

func TestMappingOntoAnotherLocalProjectMergesRules(t *testing.T) {
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_a", "A", "github.com/acme/a"),
		localProject("p_b", "B", "github.com/acme/b"),
	}}
	next, err := MapProjectTo(d, nil, "p_a", "p_b", noneOff)
	if err != nil {
		t.Fatalf("MapProjectTo: %v", err)
	}
	i, _ := findProject(next, "p_b")
	if len(next.Projects[i].Repos) != 2 {
		t.Fatalf("rules did not merge: %+v", next.Projects[i].Repos)
	}
	if len(next.Projects) != 1 {
		t.Fatalf("want one project left, got %d", len(next.Projects))
	}
}

func TestNEGATIVEAnUnknownTargetChangesNothing(t *testing.T) {
	d := Document{Version: CurrentVersion, Projects: []Project{
		localProject("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	next, err := MapProjectTo(d, orgValues(), "p_signal", "nope", noneOff)
	if !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("err = %v, want ErrProjectNotFound", err)
	}
	if len(next.Projects) != 1 || next.Projects[0].ID != "p_signal" {
		t.Fatalf("a failed map must leave the document untouched: %+v", next.Projects)
	}
}
