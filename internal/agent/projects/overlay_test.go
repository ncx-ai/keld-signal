package projects

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

func atlasValues() []Project {
	return FromRemoteProjects([]settings.RemoteProject{
		{ID: "keld_projects:signal", Title: "Signal On-Device Client", Team: "Keld Projects",
			Description: "the on-device agent", Keywords: []string{"sidecar", "daemon"}},
		{ID: "keld_projects:atlas", Title: "Atlas Platform", Team: "Keld Projects", Keywords: []string{"fastapi"}},
	})
}

func blockOn(repo string) map[string]enrich.Labeled {
	return map[string]enrich.Labeled{
		DimRepo: {Value: repo, Status: enrich.WorkstreamAttributed},
	}
}

// ⚠️ **The decided scope of "same as" (2026-09-05): an UNATTRIBUTED local
// suggestion merges into an already-attributed project, on this machine, and a
// project that exists in Atlas is never changed from Signal.** Before this fix
// PlaceSameAs looked the target up in the local document only, so the org's
// values — the projects a person most wants to merge into — answered
// ErrProjectNotFound. Found by reading the code against the decision, not by a
// failing run, because no test had ever targeted a remote value.
func TestSameAsOntoAnAtlasValueCreatesALocalOverlayAndAttributes(t *testing.T) {
	remote := atlasValues()
	d := Document{Version: 1}
	off := func(string) bool { return false }

	// Before: the block on keld-cli matches nothing — the org's values carry
	// free-text keywords, not repositories.
	before := Attribute(blockOn("github.com/ncx-ai/keld-cli"), MergeCandidates(d.Projects, remote), off, nil)
	if before.ProjectID != "" {
		t.Fatalf("precondition: the block must be unattributed, got %+v", before)
	}
	sugs := Suggest([]UnattributedBlock{{Dims: blockOn("github.com/ncx-ai/keld-cli"), Minutes: 20, Tokens: 1000}})
	if len(sugs) != 1 {
		t.Fatalf("want one suggestion, got %d", len(sugs))
	}

	// "Same as Signal On-Device Client" — an Atlas value with no local entry.
	next, err := PlaceSameAsWithRemote(d, remote, sugs[0].ID, "keld_projects:signal", sugs, off)
	if err != nil {
		t.Fatalf("same-as onto an Atlas value must succeed via an overlay, got %v", err)
	}
	if len(next.Projects) != 1 || next.Projects[0].ID != "keld_projects:signal" || next.Projects[0].Origin != OriginAtlas {
		t.Fatalf("an overlay entry keyed by the Atlas id must be created, got %+v", next.Projects)
	}

	// After: the merged candidate carries the rule, the block attributes to the
	// ATLAS ID (what Atlas matches workstreams against), by repo.
	merged := MergeCandidates(next.Projects, remote)
	after := Attribute(blockOn("github.com/ncx-ai/keld-cli"), merged, off, nil)
	if after.ProjectID != "keld_projects:signal" || after.Method != MethodRepo {
		t.Fatalf("after same-as the block must attribute to the Atlas value by repo, got %+v", after)
	}
	if after.Reason == ReasonConflict {
		t.Fatal("an overlay sharing the value's id must merge, never conflict with itself")
	}
}

// The org's authored fields are read-only from here: the overlay may add rules,
// never change what the org wrote.
func TestOverlayNeverRewritesWhatTheOrgAuthored(t *testing.T) {
	remote := atlasValues()
	local := []Project{{
		ID: "keld_projects:signal", Title: "RENAMED LOCALLY", Description: "changed",
		Team: "Other", Repos: []string{"github.com/ncx-ai/keld-cli"}, Origin: OriginAtlas,
	}}
	merged := MergeCandidates(local, remote)
	if len(merged) != 2 {
		t.Fatalf("one project per id, got %d", len(merged))
	}
	sig := merged[0]
	if sig.Title != "Signal On-Device Client" || sig.Description != "the on-device agent" || sig.Team != "Keld Projects" {
		t.Fatalf("the org's title/description/team must win over the overlay, got %+v", sig)
	}
	if len(sig.Repos) != 1 || sig.Repos[0] != "github.com/ncx-ai/keld-cli" {
		t.Fatalf("the overlay's rules must be unioned onto the value, got %v", sig.Repos)
	}
	if len(sig.Keywords) != 2 {
		t.Fatalf("the org's keywords must be kept as authored, got %v", sig.Keywords)
	}
}

// Local-only projects and remote-only values both pass through; nothing is
// dropped and nothing is duplicated.
func TestMergeCandidatesIsAUnionByID(t *testing.T) {
	remote := atlasValues()
	local := []Project{
		{ID: "p_sdk_work", Title: "SDK work", Repos: []string{"github.com/ncx-ai/sdk-testbench"}, Origin: OriginUser},
		{ID: "keld_projects:atlas", Repos: []string{"github.com/ncx-ai/keld-atlas"}, Origin: OriginAtlas},
	}
	merged := MergeCandidates(local, remote)
	ids := map[string]int{}
	for _, p := range merged {
		ids[p.ID]++
	}
	if len(merged) != 3 || ids["p_sdk_work"] != 1 || ids["keld_projects:atlas"] != 1 || ids["keld_projects:signal"] != 1 {
		t.Fatalf("want exactly {sdk_work, atlas, signal} once each, got %v", ids)
	}
	// Remote first (poll order), then local-only — stable across refreshes.
	if merged[0].ID != "keld_projects:signal" || merged[2].ID != "p_sdk_work" {
		t.Fatalf("order must be remote then local-only, got %v", []string{merged[0].ID, merged[1].ID, merged[2].ID})
	}
}

// A target that is neither local nor one of the org's values does not exist,
// and a value in a switched-off workstream is refused — same rules as before.
func TestSameAsRefusalsStillHold(t *testing.T) {
	remote := atlasValues()
	sugs := Suggest([]UnattributedBlock{{Dims: blockOn("github.com/ncx-ai/keld-cli"), Minutes: 1, Tokens: 1}})
	if _, err := PlaceSameAsWithRemote(Document{}, remote, sugs[0].ID, "nope", sugs, func(string) bool { return false }); err != ErrProjectNotFound {
		t.Fatalf("unknown target must be ErrProjectNotFound, got %v", err)
	}
	off := func(k string) bool { return k == "Keld Projects" }
	if _, err := PlaceSameAsWithRemote(Document{}, remote, sugs[0].ID, "keld_projects:signal", sugs, off); err != ErrWorkstreamOff {
		t.Fatalf("a value in a switched-off workstream must be refused, got %v", err)
	}
}
