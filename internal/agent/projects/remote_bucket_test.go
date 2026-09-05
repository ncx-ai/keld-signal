package projects

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// ⚠️ **THE SHAPE HERE IS THE ONE LOCAL ATLAS ACTUALLY SERVES, COPIED FROM THE
// WIRE.** Every earlier test in this area used a fixture with `Workstream`
// already set, which is a shape Atlas never sends — `settings.RemoteProject`
// has no workstream field at all. That is why twenty real org projects could
// arrive, be used for matching, and be listed under no workstream card while
// every test passed.
//
// Measured against localhost:8000 before the fix: 20 projects and 4 workstreams
// fetched, attribution at 91% of 45 blocks, and all four cards reading "No
// projects yet."

func atlasValue(id, title, team string) settings.RemoteProject {
	return settings.RemoteProject{ID: id, Title: title, Team: team}
}

func TestARemoteProjectCarriesItsBucketKey(t *testing.T) {
	// The wire shape: a team, and no workstream field to read.
	ps := FromRemoteProjects([]settings.RemoteProject{
		atlasValue("keld_projects:signal", "Signal On-Device Client", "Keld Projects"),
		atlasValue("keld_campaigns:launch", "Launch Week", "Keld Campaigns"),
	})
	if len(ps) != 2 {
		t.Fatalf("got %d projects", len(ps))
	}
	if ps[0].Workstream != "keld-projects" {
		t.Fatalf("workstream = %q, want the normalised team key — an empty one "+
			"puts the project under no card at all", ps[0].Workstream)
	}
	if ps[1].Workstream != "keld-campaigns" {
		t.Fatalf("workstream = %q", ps[1].Workstream)
	}
	// The human label is NOT the key: it stays on Team, so a heading can read
	// "Keld Projects" rather than "keld-projects".
	if ps[0].Team != "Keld Projects" {
		t.Fatalf("team = %q, want the display name kept", ps[0].Team)
	}
}

func TestAProjectAndItsBucketDeriveTheSameKey(t *testing.T) {
	// ⚠️ THE RULE THAT PREVENTS THE NEXT ONE. The bucket list and the projects
	// inside it must derive the key from the SAME function. Deriving it in two
	// places is precisely what made the cards disagree with their contents, so
	// this asserts the agreement rather than either value on its own.
	for _, team := range []string{
		"Keld Projects", "Keld Campaigns", "Customer Lifecycle",
		"Publish Destinations", "Go To Market",
	} {
		p := FromRemoteProjects([]settings.RemoteProject{atlasValue("x", "X", team)})[0]
		if got := WorkstreamKey(team); got != p.Workstream {
			t.Fatalf("bucket key %q != project workstream %q for team %q",
				got, p.Workstream, team)
		}
	}
}

func TestWorkstreamKeyNormalisation(t *testing.T) {
	for name, want := range map[string]string{
		"Keld Projects":         "keld-projects",
		"  Customer Lifecycle ": "customer-lifecycle",
		"Development":           "development",
		"":                      "",
	} {
		if got := WorkstreamKey(name); got != want {
			t.Fatalf("WorkstreamKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestARemoteProjectWithNoTeamGetsNoBucketRatherThanAWrongOne(t *testing.T) {
	// NEGATIVE: inventing a key for a value the org filed nowhere would put it
	// in a card that does not exist. Empty is the honest answer, and the page's
	// own fallback then groups it — see projectGroups in app.js.
	p := FromRemoteProjects([]settings.RemoteProject{atlasValue("x", "X", "")})[0]
	if p.Workstream != "" {
		t.Fatalf("workstream = %q, want empty for a value with no team", p.Workstream)
	}
}
