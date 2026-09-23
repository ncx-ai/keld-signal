package projects

import (
	"reflect"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func ws(id, group string, repos ...string) Project {
	return Project{ID: id, Title: id, Group: group, Repos: repos, Origin: OriginUser}
}

func dimsOf(repo, branch string) map[string]enrich.Labeled {
	d := map[string]enrich.Labeled{}
	if repo != "" {
		d[DimRepo] = enrich.Labeled{Value: repo, Confidence: 1, Status: enrich.DimensionAttributed}
	}
	if branch != "" {
		d[DimBranch] = enrich.Labeled{Value: branch, Confidence: 1, Status: enrich.DimensionAttributed}
	}
	return d
}

func assigned(r Result) []Assigned { return r.Projects }

// AC-1. The same repository claimed in two DIFFERENT groups attributes the
// block to both — the case that used to read "Two projects claim this — pick
// one", for a setup that was valid all along.
func TestAttributePerGroup(t *testing.T) {
	atlas := ws("products:atlas_platform", "products", "ncx-ai/keld-atlas")
	billing := ws("features:billing", "features", "ncx-ai/keld-atlas")
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", ""), []Project{atlas, billing}, nil, nil)

	want := []Assigned{
		{ProjectID: "products:atlas_platform", Group: "products", Method: MethodRepo},
		{ProjectID: "features:billing", Group: "features", Method: MethodRepo},
	}
	if !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
	if res.Reason != ReasonNone {
		t.Fatalf("an attributed block carries no reason, got %q", res.Reason)
	}
}

// AC-2. Two projects in the SAME group matching one block both get it:
// overlap inside a group is allowed, and a conflict is never produced.
func TestSameGroupMatchesAreAllAssigned(t *testing.T) {
	atlas := ws("products:atlas_platform", "products", "ncx-ai/keld-atlas")
	signal := ws("products:signal_client", "products", "ncx-ai/keld-atlas")
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", ""), []Project{atlas, signal}, nil, nil)

	if len(res.Projects) != 2 || res.Projects[0].Group != "products" || res.Projects[1].Group != "products" {
		t.Fatalf("both Products projects must be assigned: %+v", res)
	}
	if res.Reason == ReasonConflict {
		t.Fatal("a conflict must never be produced")
	}
}

// A repo rule on one project and a ticket rule on another both count: a
// block goes to every project that matches it, whichever rule matched.
func TestRepoAndTicketMatchesBothAssign(t *testing.T) {
	platform := ws("features:platform", "features", "ncx-ai/keld-atlas")
	billing := Project{ID: "features:billing", Title: "Billing", Group: "features", TicketKey: "BILL", Origin: OriginUser}
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", "BILL-231-invoices"), []Project{platform, billing}, nil, nil)

	want := []Assigned{
		{ProjectID: "features:platform", Group: "features", Method: MethodRepo},
		{ProjectID: "features:billing", Group: "features", Method: MethodTicket},
	}
	if !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
}

// A project whose repo AND ticket both match is assigned once, by repo.
func TestAProjectMatchingTwoWaysIsAssignedOnce(t *testing.T) {
	w := Project{ID: "w", Title: "W", Group: "g", Repos: []string{"ncx-ai/keld-atlas"}, TicketKey: "KA", Origin: OriginUser}
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", "KA-1"), []Project{w}, nil, nil)
	want := []Assigned{{ProjectID: "w", Group: "g", Method: MethodRepo}}
	if !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
}

func TestNoMatchIsNoRuleMatched(t *testing.T) {
	res := Attribute(dimsOf("github.com/ncx-ai/elsewhere", ""), []Project{ws("a", "g", "ncx-ai/keld-atlas")}, nil, nil)
	if len(res.Projects) != 0 || res.Reason != ReasonNoRuleMatched {
		t.Fatalf("no match must be an honest no_rule_matched: %+v", res)
	}
}

// A group switched off takes its projects out; the other groups still count.
func TestAGroupSwitchedOffDropsOnlyItsOwnProjects(t *testing.T) {
	atlas := ws("products:atlas_platform", "products", "ncx-ai/keld-atlas")
	billing := ws("features:billing", "features", "ncx-ai/keld-atlas")
	off := func(k string) bool { return k == "features" }
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", ""), []Project{atlas, billing}, off, nil)
	if len(res.Projects) != 1 || res.Projects[0].ProjectID != "products:atlas_platform" {
		t.Fatalf("only the Products project may remain: %+v", res)
	}
}

// The injected Vector fills only the groups the rules left empty, and is
// handed only those groups' projects.
type recordingVector struct {
	got    []string
	answer Result
}

func (v *recordingVector) Attribute(_ map[string]enrich.Labeled, cands []Project) Result {
	for _, c := range cands {
		v.got = append(v.got, c.ID)
	}
	return v.answer
}

func TestTheVectorPassFillsOnlyGroupsTheRulesLeftEmpty(t *testing.T) {
	atlas := ws("products:atlas_platform", "products", "ncx-ai/keld-atlas")
	docs := Project{ID: "features:docs", Title: "Docs", Group: "features", Origin: OriginUser}
	v := &recordingVector{answer: Result{Projects: []Assigned{
		{ProjectID: "features:docs", Group: "features", Method: MethodEmbedding},
		// A vector answer for a group the rules already decided is ignored.
		{ProjectID: "products:atlas_platform", Group: "products", Method: MethodEmbedding},
	}}}
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", ""), []Project{atlas, docs}, nil, v)

	if !reflect.DeepEqual(v.got, []string{"features:docs"}) {
		t.Fatalf("the vector must see only the undecided group's projects, saw %v", v.got)
	}
	want := []Assigned{
		{ProjectID: "products:atlas_platform", Group: "products", Method: MethodRepo},
		{ProjectID: "features:docs", Group: "features", Method: MethodEmbedding},
	}
	if !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
}

func TestAVectorThatCouldNotRunIsReportedOnlyWhenNothingElseAssigned(t *testing.T) {
	docs := Project{ID: "features:docs", Title: "Docs", Group: "features", Origin: OriginUser}
	v := &recordingVector{answer: Result{Reason: ReasonWeightsUnavailable}}
	res := Attribute(dimsOf("", ""), []Project{docs}, nil, v)
	if res.Reason != ReasonWeightsUnavailable || len(res.Projects) != 0 {
		t.Fatalf("a vector that could not run must say so: %+v", res)
	}
}

func TestResultIDsAndGroupsAreInAssignmentOrder(t *testing.T) {
	r := Result{Projects: []Assigned{{ProjectID: "a", Group: "g1"}, {ProjectID: "b", Group: "g2"}, {ProjectID: "c", Group: "g1"}}}
	if !reflect.DeepEqual(r.IDs(), []string{"a", "b", "c"}) {
		t.Fatalf("IDs = %v", r.IDs())
	}
	if !r.Attributed() || (Result{}).Attributed() {
		t.Fatal("Attributed must report whether anything was assigned")
	}
}

// only is the single assignment of r, or the zero Assigned when r has none or
// several — the reading the single-match tests in this package make.
func only(r Result) Assigned {
	if len(r.Projects) == 1 {
		return r.Projects[0]
	}
	return Assigned{}
}
