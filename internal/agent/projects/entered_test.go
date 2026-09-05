package projects

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// The rows of the lifecycle page's "What rides the block" table, T13-T16.

func TestT13BothSidesOfTheComparisonAreOnTheRow(t *testing.T) {
	// ⚠️ THE WHOLE FEATURE. Atlas knows its own project's rules and nothing
	// about the local one, so without both on the row "a machine groups C with
	// your A and B" is unanswerable from what it receives.
	local := localProject("p_abc", "mine",
		"github.com/acme/a", "github.com/acme/b", "github.com/acme/c")
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:one", "One", "Eng", "github.com/acme/a", "github.com/acme/b"),
	})
	got := EnteredBy(repoDims("github.com/acme/a"),
		MergeCandidates([]Project{local}, remote), noneOff)

	if len(got) != 2 {
		t.Fatalf("want both projects on the row, got %+v", got)
	}
	var sawLocal, sawOrg bool
	for _, e := range got {
		switch e.Origin {
		case OriginUser:
			sawLocal = true
			if len(e.Repos) != 3 {
				t.Fatalf("the local side must carry ALL its rules, got %v", e.Repos)
			}
		case OriginAtlas:
			sawOrg = true
			if e.ID != "org:one" {
				t.Fatalf("the org side must carry its id, got %q", e.ID)
			}
		}
	}
	if !sawLocal || !sawOrg {
		t.Fatalf("local=%v org=%v — both sides are required", sawLocal, sawOrg)
	}
}

func TestT14EnteredCarriesNoLocalIdentity(t *testing.T) {
	// ⚠️ A local project's ID IS DERIVED FROM ITS TITLE (newProjectID), so
	// sending the id would send the title in a thin disguise. Rules cross
	// because a repository remote already crosses as a block dimension; a
	// person's free text does not. Serialised and searched, because the claim
	// is about the bytes rather than about the struct.
	local := Project{
		ID: "p_super_secret_codename", Title: "Super Secret Codename",
		Repos: []string{"github.com/acme/a"}, Origin: OriginUser,
		Workstream: "development",
	}
	got := EnteredBy(repoDims("github.com/acme/a"), []Project{local}, noneOff)
	if len(got) != 1 {
		t.Fatalf("want the local project, got %+v", got)
	}
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Super Secret Codename", "p_super_secret_codename", "codename"} {
		if strings.Contains(strings.ToLower(string(body)), strings.ToLower(forbidden)) {
			t.Fatalf("the row carries %q:\n%s", forbidden, body)
		}
	}
	if got[0].ID != "" {
		t.Fatalf("a local project must carry no id, got %q", got[0].ID)
	}
}

func TestT15NoMatchIsAnEmptyAnswerNotAWrongOne(t *testing.T) {
	// A block matching nothing enters nothing. The emitter turns nil into an
	// empty list on the wire so "matched nothing" and "this client does not
	// send them" stay different facts — asserted there; here the decision layer
	// must simply not invent a project.
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:one", "One", "Eng", "github.com/acme/z"),
	})
	if got := EnteredBy(repoDims("github.com/acme/a"), remote, noneOff); len(got) != 0 {
		t.Fatalf("want nothing entered, got %+v", got)
	}
	// And a block with no dimensions at all.
	if got := EnteredBy(nil, remote, noneOff); len(got) != 0 {
		t.Fatalf("a block with no dimensions entered %+v", got)
	}
}

func TestT16AWorkstreamThatIsOffIsNeverEntered(t *testing.T) {
	// Its projects are excluded from matching, so reporting a block as having
	// entered one would tell Atlas the opposite of what this machine did.
	remote := FromRemoteProjects([]settings.RemoteProject{
		org("org:mkt", "Campaign", "Marketing", "github.com/acme/a"),
	})
	off := func(key string) bool { return key == "marketing" || key == "Marketing" }
	if got := EnteredBy(repoDims("github.com/acme/a"), remote, off); len(got) != 0 {
		t.Fatalf("entered a switched-off workstream: %+v", got)
	}
}

func TestEnteredRulesAreSortedSoTwoMachinesAgree(t *testing.T) {
	// The row must be a function of WHAT was entered, not of the order the
	// document happened to hold — otherwise two machines with the same projects
	// produce different rows and Atlas cannot group them.
	a := localProject("p_1", "one", "github.com/acme/b", "github.com/acme/a")
	b := localProject("p_2", "two", "github.com/acme/a", "github.com/acme/b")
	ea := EnteredBy(repoDims("github.com/acme/a"), []Project{a}, noneOff)
	eb := EnteredBy(repoDims("github.com/acme/a"), []Project{b}, noneOff)
	ja, _ := json.Marshal(ea)
	jb, _ := json.Marshal(eb)
	if string(ja) != string(jb) {
		t.Fatalf("same rules, different rows:\n%s\n%s", ja, jb)
	}
}

func TestEnteredListsEveryMatchRatherThanPickingOne(t *testing.T) {
	// ⚠️ "Entered" is not "attributed". Attribute picks one and REFUSES when
	// two match; this lists everything, because two projects claiming one
	// repository is precisely the state an admin needs to see. Reconcile should
	// make it impossible for a local/org pair — and if it ever reappears, this
	// carries the evidence instead of swallowing it.
	both := []Project{
		localProject("p_a", "mine", "github.com/acme/a"),
		{ID: "org:one", Title: "One", Origin: OriginAtlas, Repos: []string{"github.com/acme/a"}},
	}
	if res := Attribute(repoDims("github.com/acme/a"), both, noneOff, nil); res.Reason != ReasonConflict {
		t.Fatalf("precondition: want a conflict, got %q", res.Reason)
	}
	got := EnteredBy(repoDims("github.com/acme/a"), both, noneOff)
	if len(got) != 2 {
		t.Fatalf("a conflict must be reported as two entries, got %+v", got)
	}
}

func TestEnteredMatchesOnATicketKeyFromTheBranch(t *testing.T) {
	p := Project{ID: "org:one", Title: "One", Origin: OriginAtlas,
		TicketKey: "KELD", Workstream: "eng"}
	dims := map[string]enrich.Labeled{
		DimBranch: {Value: "keld-637-auth-flow", Status: enrich.WorkstreamAttributed},
	}
	got := EnteredBy(dims, []Project{p}, noneOff)
	if len(got) != 1 || got[0].TicketKey != "KELD" {
		t.Fatalf("a ticket rule did not match: %+v", got)
	}
}
