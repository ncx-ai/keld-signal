package workstreams

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// The rows of the lifecycle page's "Inclusion and precedence" table, T1-T12, in
// order. The names carry the row so a failure names the claim rather than the
// function.

func org(id, title, team string, repos ...string) settings.RemoteWorkstream {
	return settings.RemoteWorkstream{ID: id, Title: title, Team: team, Repos: repos}
}

func repoDims(v string) map[string]enrich.Labeled {
	return map[string]enrich.Labeled{
		DimRepo: {Value: v, Status: enrich.DimensionAttributed},
	}
}

func doc(ps ...Workstream) Document { return Document{Version: CurrentVersion, Workstreams: ps} }

func ids(ps []Workstream) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.ID)
	}
	return out
}

func TestT1LocalFullyCoveredIsDeletedAndItsBlocksStillAttribute(t *testing.T) {
	d := doc(localWorkstream("p_a", "mine", "github.com/acme/a"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a", "github.com/acme/b"),
	})

	next, removed, trimmed := Reconcile(d, remote, noneOff)
	if len(removed) != 1 || removed[0].ID != "p_a" {
		t.Fatalf("want p_a removed, got removed=%+v trimmed=%+v", removed, trimmed)
	}
	if len(next.Workstreams) != 0 {
		t.Fatalf("want no local projects left, got %v", ids(next.Workstreams))
	}
	// ⚠️ THE REASON DELETION IS SAFE: the rule that placed those blocks is still
	// matched — by Atlas. Asserted through the matcher, not by reading the file.
	got := Attribute(repoDims("github.com/acme/a"), MergeCandidates(next.Workstreams, remote), noneOff, nil)
	if got.WorkstreamID != "org:one" {
		t.Fatalf("after deletion, attributed to %q (reason %q)", got.WorkstreamID, got.Reason)
	}
}

func TestT2CoveredByTwoOrgWorkstreamsIsStillDeleted(t *testing.T) {
	// Covered is covered. WHICH org project claims the block is Atlas's
	// business; the machine's only question is whether anything of its own is
	// left, and nothing is.
	d := doc(localWorkstream("p_a", "mine", "github.com/acme/a"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a"),
		org("org:two", "Two", "Eng", "github.com/acme/a"),
	})
	_, removed, _ := Reconcile(d, remote, noneOff)
	if len(removed) != 1 {
		t.Fatalf("want it removed, got %+v", removed)
	}
}

func TestT3LocalKeepsOnlyWhatTheOrgDoesNotCover(t *testing.T) {
	d := doc(localWorkstream("p_abc", "mine",
		"github.com/acme/a", "github.com/acme/b", "github.com/acme/c"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a", "github.com/acme/b"),
	})

	next, removed, trimmed := Reconcile(d, remote, noneOff)
	if len(removed) != 0 || len(trimmed) != 1 {
		t.Fatalf("want one trim and no removal, got removed=%+v trimmed=%+v", removed, trimmed)
	}
	kept := DeclaredRepos(next.Workstreams[0])
	if len(kept) != 1 || kept[0] != "github.com/acme/c" {
		t.Fatalf("kept %v, want only the uncovered rule", kept)
	}
	if len(trimmed[0].Covered) != 2 {
		t.Fatalf("covered %v, want the two the org holds", trimmed[0].Covered)
	}
}

func TestT4AfterTrimmingAnOverlappingRuleIsNoLongerAConflict(t *testing.T) {
	// ⚠️ THE WHOLE POINT OF PRECEDENCE. Without the trim these two both claim
	// A and the matcher refuses to pick, so the block lands in neither.
	d := doc(localWorkstream("p_abc", "mine",
		"github.com/acme/a", "github.com/acme/b", "github.com/acme/c"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a", "github.com/acme/b"),
	})

	before := Attribute(repoDims("github.com/acme/a"), MergeCandidates(d.Workstreams, remote), noneOff, nil)
	if before.Reason != ReasonConflict {
		t.Fatalf("precondition: want %q, got %q — if two projects sharing a rule is no "+
			"longer a conflict, trimming can be revisited", ReasonConflict, before.Reason)
	}

	next, _, _ := Reconcile(d, remote, noneOff)
	after := Attribute(repoDims("github.com/acme/a"), MergeCandidates(next.Workstreams, remote), noneOff, nil)
	if after.Reason == ReasonConflict {
		t.Fatal("still a conflict after trimming")
	}
	if after.WorkstreamID != "org:one" {
		t.Fatalf("attributed to %q, want the org's project", after.WorkstreamID)
	}
}

func TestT5TheRemainderStillAttributes(t *testing.T) {
	// A person's extra rule keeps working while an admin decides about it.
	d := doc(localWorkstream("p_abc", "mine",
		"github.com/acme/a", "github.com/acme/b", "github.com/acme/c"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a", "github.com/acme/b"),
	})
	next, _, _ := Reconcile(d, remote, noneOff)
	got := Attribute(repoDims("github.com/acme/c"), MergeCandidates(next.Workstreams, remote), noneOff, nil)
	if got.WorkstreamID != "p_abc" {
		t.Fatalf("the remainder stopped attributing: %q (reason %q)", got.WorkstreamID, got.Reason)
	}
}

func TestT6TheDirectionOfContainmentDoesNotChangeTheRule(t *testing.T) {
	// The mirror of T3: the org holds fewer rules than the local project in
	// both, and the answer is the same shape. Both "cases" are one rule, and
	// this is the test that says so.
	d := doc(localWorkstream("p_ab", "mine", "github.com/acme/a", "github.com/acme/b"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a"),
	})
	next, removed, trimmed := Reconcile(d, remote, noneOff)
	if len(removed) != 0 || len(trimmed) != 1 {
		t.Fatalf("removed=%+v trimmed=%+v", removed, trimmed)
	}
	kept := DeclaredRepos(next.Workstreams[0])
	if len(kept) != 1 || kept[0] != "github.com/acme/b" {
		t.Fatalf("kept %v", kept)
	}
}

func TestT7NoOverlapChangesNothing(t *testing.T) {
	d := doc(localWorkstream("p_a", "mine", "github.com/acme/a"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/z"),
	})
	next, removed, trimmed := Reconcile(d, remote, noneOff)
	if len(removed) != 0 || len(trimmed) != 0 {
		t.Fatalf("an untouched project was touched: removed=%+v trimmed=%+v", removed, trimmed)
	}
	if len(next.Workstreams) != 1 || len(DeclaredRepos(next.Workstreams[0])) != 1 {
		t.Fatalf("document changed: %+v", next.Workstreams)
	}
}

func TestT8ReconcileIsIdempotent(t *testing.T) {
	// The poll runs every five minutes. A rule that is not idempotent rewrites
	// the file for ever.
	d := doc(localWorkstream("p_abc", "mine",
		"github.com/acme/a", "github.com/acme/b", "github.com/acme/c"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a", "github.com/acme/b"),
	})
	once, _, trimmed1 := Reconcile(d, remote, noneOff)
	if len(trimmed1) != 1 {
		t.Fatalf("first pass trimmed %d", len(trimmed1))
	}
	twice, removed2, trimmed2 := Reconcile(once, remote, noneOff)
	if len(removed2) != 0 || len(trimmed2) != 0 {
		t.Fatalf("second pass changed something: removed=%+v trimmed=%+v", removed2, trimmed2)
	}
	if len(twice.Workstreams) != len(once.Workstreams) {
		t.Fatal("second pass changed the document")
	}
}

func TestT9AGroupThatIsOffCoversNothing(t *testing.T) {
	// ⚠️ Its projects are excluded from matching entirely, so counting their
	// rules as coverage would delete a local project and leave its blocks in NO
	// project — the opposite of what coverage guarantees.
	d := doc(localWorkstream("p_a", "mine", "github.com/acme/a"))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:mkt", "Campaign", "Marketing", "github.com/acme/a"),
	})
	off := func(key string) bool { return key == "marketing" || key == "Marketing" }
	next, removed, trimmed := Reconcile(d, remote, off)
	if len(removed) != 0 || len(trimmed) != 0 {
		t.Fatalf("a switched-off workstream was treated as coverage: removed=%+v trimmed=%+v",
			removed, trimmed)
	}
	if len(next.Workstreams) != 1 {
		t.Fatal("the local project was removed")
	}
}

func TestT10ARulelessLocalWorkstreamIsNeverRemoved(t *testing.T) {
	// The empty set is contained in every set, so the naive reading silently
	// removes anything a person named before giving it a rule.
	d := doc(Workstream{ID: "p_empty", Title: "Thinking", Group: "development", Origin: OriginUser})
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a"),
	})
	next, removed, trimmed := Reconcile(d, remote, noneOff)
	if len(removed) != 0 || len(trimmed) != 0 {
		t.Fatalf("a ruleless project was touched: removed=%+v trimmed=%+v", removed, trimmed)
	}
	if len(next.Workstreams) != 1 {
		t.Fatal("the ruleless project was removed")
	}
}

func TestT11NoOrgWorkstreamsChangesNothing(t *testing.T) {
	// Send to Atlas off, or a first run. An ABSENT list is not an empty one:
	// treating it as coverage would delete every local project on a machine
	// that has simply not spoken to Atlas yet.
	d := doc(localWorkstream("p_a", "mine", "github.com/acme/a"))
	next, removed, trimmed := Reconcile(d, nil, noneOff)
	if len(removed) != 0 || len(trimmed) != 0 || len(next.Workstreams) != 1 {
		t.Fatalf("nothing should change: removed=%+v trimmed=%+v projects=%v",
			removed, trimmed, ids(next.Workstreams))
	}
}

func TestT12RulesAreComparedFoldedLikeEverywhereElse(t *testing.T) {
	// Otherwise coverage silently misses and a project is kept that should
	// have gone — which then conflicts with the org's, so the cost is not
	// cosmetic.
	d := doc(localWorkstream("p_a", "mine", "  GitHub.com/Acme/A  "))
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a"),
	})
	_, removed, _ := Reconcile(d, remote, noneOff)
	if len(removed) != 1 {
		t.Fatalf("case and spacing defeated coverage: %+v", removed)
	}
}

func TestAnOrgOverlayIsNeverReconciledAway(t *testing.T) {
	// An overlay carries an Atlas value's id with local rules of its own. It is
	// not a local project and must be left alone — removing it would drop the
	// rules this machine added while leaving the org's value untouched.
	d := doc(Workstream{ID: "org:one", Title: "One", Origin: OriginAtlas,
		Repos: []string{"github.com/acme/a"}})
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a"),
	})
	next, removed, trimmed := Reconcile(d, remote, noneOff)
	if len(removed) != 0 || len(trimmed) != 0 || len(next.Workstreams) != 1 {
		t.Fatalf("an overlay was reconciled: removed=%+v trimmed=%+v", removed, trimmed)
	}
}

func TestAHiddenLocalWorkstreamIsLeftAlone(t *testing.T) {
	// Hidden means excluded from matching, so it claims nothing and can
	// conflict with nothing. Removing it would delete a thing a person chose to
	// keep out of the way rather than throw away.
	p := localWorkstream("p_a", "mine", "github.com/acme/a")
	p.Hidden = true
	d := doc(p)
	remote := FromRemoteWorkstreams([]settings.RemoteWorkstream{
		org("org:one", "One", "Eng", "github.com/acme/a"),
	})
	_, removed, trimmed := Reconcile(d, remote, noneOff)
	if len(removed) != 0 || len(trimmed) != 0 {
		t.Fatalf("a hidden project was reconciled: removed=%+v trimmed=%+v", removed, trimmed)
	}
}
