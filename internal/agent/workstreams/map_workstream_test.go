package workstreams

import (
	"errors"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

func localWorkstream(id, title string, repos ...string) Workstream {
	return Workstream{ID: id, Title: title, Repos: repos, Group: "development", Origin: OriginUser}
}

func orgValues() []Workstream {
	return FromRemoteWorkstreams([]settings.RemoteWorkstream{
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
	d := Document{Version: CurrentVersion, Workstreams: []Workstream{
		localWorkstream("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}

	next, err := MapWorkstreamTo(d, orgValues(), "p_signal", "keld_projects:signal", noneOff)
	if err != nil {
		t.Fatalf("MapWorkstreamTo: %v", err)
	}

	i, ok := findWorkstream(next, "keld_projects:signal")
	if !ok {
		t.Fatal("no overlay entry was laid down for the org value")
	}
	target := next.Workstreams[i]
	if len(target.Repos) != 1 || target.Repos[0] != "github.com/ncx-ai/keld-signal" {
		t.Fatalf("the rule did not move: %+v", target.Repos)
	}
	if target.Origin != OriginAtlas {
		t.Fatalf("overlay origin = %q, want %q", target.Origin, OriginAtlas)
	}
	if _, still := findWorkstream(next, "p_signal"); still {
		t.Fatal("the local entry survived — two projects sharing a repo rule is a conflict")
	}
}

func TestNEGATIVEBlocksKeepAttributingAfterTheMove(t *testing.T) {
	// The story's first negative, asserted through the thing that actually
	// decides it: Attribute over the same dims before and after.
	before := Document{Version: CurrentVersion, Workstreams: []Workstream{
		localWorkstream("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	dims := map[string]enrich.Labeled{
		DimRepo: {Value: "github.com/ncx-ai/keld-signal", Status: enrich.DimensionAttributed},
	}

	got := Attribute(dims, MergeCandidates(before.Workstreams, orgValues()), noneOff, nil)
	if got.WorkstreamID != "p_signal" {
		t.Fatalf("before the move, attributed to %q", got.WorkstreamID)
	}

	after, err := MapWorkstreamTo(before, orgValues(), "p_signal", "keld_projects:signal", noneOff)
	if err != nil {
		t.Fatalf("MapWorkstreamTo: %v", err)
	}
	got = Attribute(dims, MergeCandidates(after.Workstreams, orgValues()), noneOff, nil)
	if got.WorkstreamID != "keld_projects:signal" {
		t.Fatalf("after the move, attributed to %q (reason %q) — the block fell out",
			got.WorkstreamID, got.Reason)
	}
}

func TestNEGATIVEKeepingBothWouldConflictWhichIsWhyOneIsRemoved(t *testing.T) {
	// The reason the local entry is deleted rather than kept as a reference,
	// asserted rather than argued: two visible projects sharing a repo rule
	// attribute to NEITHER.
	both := Document{Version: CurrentVersion, Workstreams: []Workstream{
		localWorkstream("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
		{ID: "keld_projects:signal", Title: "Signal On-Device Client",
			Repos: []string{"github.com/ncx-ai/keld-signal"}, Origin: OriginAtlas},
	}}
	got := Attribute(map[string]enrich.Labeled{
		DimRepo: {Value: "github.com/ncx-ai/keld-signal", Status: enrich.DimensionAttributed},
	}, both.Workstreams, noneOff, nil)
	if got.Reason != ReasonConflict {
		t.Fatalf("reason = %q, want %q — if this ever stops being a conflict, "+
			"keeping the local entry as a reference becomes viable and MapWorkstreamTo's "+
			"deletion should be revisited", got.Reason, ReasonConflict)
	}
}

func TestNEGATIVEAnOrgWorkstreamCannotBeFoldedAway(t *testing.T) {
	// Its identity lives in Atlas. Removing the local overlay would drop the
	// rules this machine added while leaving the org's value untouched — a
	// deletion that looks like a move.
	d := Document{Version: CurrentVersion, Workstreams: []Workstream{
		{ID: "keld_projects:signal", Title: "Signal", Origin: OriginAtlas,
			Repos: []string{"github.com/ncx-ai/keld-signal"}},
		localWorkstream("p_other", "other"),
	}}
	_, err := MapWorkstreamTo(d, orgValues(), "keld_projects:signal", "p_other", noneOff)
	if !errors.Is(err, ErrWorkstreamNotFound) {
		t.Fatalf("err = %v, want ErrWorkstreamNotFound", err)
	}
}

func TestNEGATIVEMappingOntoItselfIsRefused(t *testing.T) {
	// It would delete the entry and then put its rules back on the one just
	// removed. Refused rather than silently doing nothing.
	d := Document{Version: CurrentVersion, Workstreams: []Workstream{
		localWorkstream("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	if _, err := MapWorkstreamTo(d, nil, "p_signal", "p_signal", noneOff); !errors.Is(err, ErrWorkstreamNotFound) {
		t.Fatalf("err = %v, want ErrWorkstreamNotFound", err)
	}
}

func TestMappingOntoAnotherLocalWorkstreamMergesRules(t *testing.T) {
	d := Document{Version: CurrentVersion, Workstreams: []Workstream{
		localWorkstream("p_a", "A", "github.com/acme/a"),
		localWorkstream("p_b", "B", "github.com/acme/b"),
	}}
	next, err := MapWorkstreamTo(d, nil, "p_a", "p_b", noneOff)
	if err != nil {
		t.Fatalf("MapWorkstreamTo: %v", err)
	}
	i, _ := findWorkstream(next, "p_b")
	if len(next.Workstreams[i].Repos) != 2 {
		t.Fatalf("rules did not merge: %+v", next.Workstreams[i].Repos)
	}
	if len(next.Workstreams) != 1 {
		t.Fatalf("want one project left, got %d", len(next.Workstreams))
	}
}

func TestNEGATIVEAnUnknownTargetChangesNothing(t *testing.T) {
	d := Document{Version: CurrentVersion, Workstreams: []Workstream{
		localWorkstream("p_signal", "keld-signal", "github.com/ncx-ai/keld-signal"),
	}}
	next, err := MapWorkstreamTo(d, orgValues(), "p_signal", "nope", noneOff)
	if !errors.Is(err, ErrWorkstreamNotFound) {
		t.Fatalf("err = %v, want ErrWorkstreamNotFound", err)
	}
	if len(next.Workstreams) != 1 || next.Workstreams[0].ID != "p_signal" {
		t.Fatalf("a failed map must leave the document untouched: %+v", next.Workstreams)
	}
}
