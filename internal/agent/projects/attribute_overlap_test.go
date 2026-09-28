package projects

import (
	"reflect"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func ws(id string, repos ...string) Project {
	return Project{ID: id, Title: id, Repos: repos, Origin: OriginUser}
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

// AC-1/AC-2, flat since Revision 4. Two projects claiming one repository both
// get the block — the case that used to read "Two projects claim this — pick
// one", for a setup that was valid all along — and a conflict is never
// produced.
func TestEveryProjectMatchingABlockIsAssigned(t *testing.T) {
	atlas := ws("p_atlas", "ncx-ai/keld-atlas")
	billing := ws("p_billing", "ncx-ai/keld-atlas")
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", ""), []Project{atlas, billing}, nil)

	want := []Assigned{
		{ProjectID: "p_atlas", Method: MethodRepo},
		{ProjectID: "p_billing", Method: MethodRepo},
	}
	if !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
	if res.Reason != ReasonNone {
		t.Fatalf("an attributed block carries no reason, got %q", res.Reason)
	}
}

// A repo rule on one project and a ticket rule on another both count: a
// block goes to every project that matches it, whichever rule matched.
func TestRepoAndTicketMatchesBothAssign(t *testing.T) {
	platform := ws("p_platform", "ncx-ai/keld-atlas")
	billing := Project{ID: "p_billing", Title: "Billing", TicketKey: "BILL", Origin: OriginUser}
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", "BILL-231-invoices"), []Project{platform, billing}, nil)

	want := []Assigned{
		{ProjectID: "p_platform", Method: MethodRepo},
		{ProjectID: "p_billing", Method: MethodTicket},
	}
	if !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
}

// A project whose repo AND ticket both match is assigned once, by repo.
func TestAProjectMatchingTwoWaysIsAssignedOnce(t *testing.T) {
	w := Project{ID: "w", Title: "W", Repos: []string{"ncx-ai/keld-atlas"}, TicketKey: "KA", Origin: OriginUser}
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", "KA-1"), []Project{w}, nil)
	want := []Assigned{{ProjectID: "w", Method: MethodRepo}}
	if !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
}

func TestNoMatchIsNoRuleMatched(t *testing.T) {
	res := Attribute(dimsOf("github.com/ncx-ai/elsewhere", ""), []Project{ws("a", "ncx-ai/keld-atlas")}, nil)
	if len(res.Projects) != 0 || res.Reason != ReasonNoRuleMatched {
		t.Fatalf("no match must be an honest no_rule_matched: %+v", res)
	}
}

// Hidden is the only exclusion since Revision 4: a hidden project takes itself
// out, and every other matching project still counts. (It replaces the
// group-off test: a switched-off group's projects become hidden on upgrade.)
func TestAHiddenProjectDropsOnlyItself(t *testing.T) {
	atlas := ws("p_atlas", "ncx-ai/keld-atlas")
	billing := ws("p_billing", "ncx-ai/keld-atlas")
	billing.Hidden = true
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", ""), []Project{atlas, billing}, nil)
	if len(res.Projects) != 1 || res.Projects[0].ProjectID != "p_atlas" {
		t.Fatalf("only the visible project may remain: %+v", res)
	}
}

// recordingVector is an injected Vector that records what it was handed.
type recordingVector struct {
	called bool
	got    []string
	answer Result
}

func (v *recordingVector) Attribute(_ map[string]enrich.Labeled, cands []Project) Result {
	v.called = true
	for _, c := range cands {
		v.got = append(v.got, c.ID)
	}
	return v.answer
}

// R4-AC-6's Go-side counterpart: ONE pooled competition. When a rule decides,
// the vector pass is not asked at all; when none does, it is handed every
// visible project and its answer is the block's. That is the decision from
// before per-group attribution, exactly.
func TestTheVectorPassIsOnePooledCompetitionAfterTheRules(t *testing.T) {
	atlas := ws("p_atlas", "ncx-ai/keld-atlas")
	docs := Project{ID: "p_docs", Title: "Docs", Origin: OriginUser}
	hidden := Project{ID: "p_old", Title: "Old", Origin: OriginUser, Hidden: true}

	v := &recordingVector{answer: Result{Projects: []Assigned{{ProjectID: "p_docs", Method: MethodEmbedding}}}}
	res := Attribute(dimsOf("github.com/ncx-ai/keld-atlas", ""), []Project{atlas, docs, hidden}, v)
	if v.called {
		t.Fatalf("a rule decided the block; the vector pass must not be asked (it saw %v)", v.got)
	}
	if want := []Assigned{{ProjectID: "p_atlas", Method: MethodRepo}}; !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}

	v = &recordingVector{answer: Result{Projects: []Assigned{{ProjectID: "p_docs", Method: MethodEmbedding}}}}
	res = Attribute(dimsOf("github.com/ncx-ai/elsewhere", ""), []Project{atlas, docs, hidden}, v)
	if !reflect.DeepEqual(v.got, []string{"p_atlas", "p_docs"}) {
		t.Fatalf("with no rule match the vector must see every visible project, saw %v", v.got)
	}
	if want := []Assigned{{ProjectID: "p_docs", Method: MethodEmbedding}}; !reflect.DeepEqual(assigned(res), want) {
		t.Fatalf("assigned = %+v, want %+v", assigned(res), want)
	}
}

func TestAVectorThatCouldNotRunIsReportedOnlyWhenNothingElseAssigned(t *testing.T) {
	docs := Project{ID: "p_docs", Title: "Docs", Origin: OriginUser}
	v := &recordingVector{answer: Result{Reason: ReasonWeightsUnavailable}}
	res := Attribute(dimsOf("", ""), []Project{docs}, v)
	if res.Reason != ReasonWeightsUnavailable || len(res.Projects) != 0 {
		t.Fatalf("a vector that could not run must say so: %+v", res)
	}
}

func TestResultIDsAreInAssignmentOrder(t *testing.T) {
	r := Result{Projects: []Assigned{{ProjectID: "a"}, {ProjectID: "b"}, {ProjectID: "c"}}}
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
