package llmstudy

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func ans(labels map[Facet]string) Answer {
	return Answer{Labels: labels, Valid: true}
}

func fixtureRuns() ([]Window, Run, []Run) {
	ws := []Window{
		{PromptID: "p1", Target: "fix the retry loop", Turns: []Turn{{RoleUser, "fix the retry loop"}}},
		{PromptID: "p2", Target: "write a poem", Turns: []Turn{{RoleUser, "write a poem"}}},
	}
	control := Run{Arm: "gliner2", Answers: []Answer{
		ans(map[Facet]string{FacetDomain: "general"}),
		ans(map[Facet]string{FacetDomain: "creative"}),
	}}
	arm := Run{Arm: "qwen3-4b", Answers: []Answer{
		ans(map[Facet]string{FacetDomain: "software"}), // disagrees
		ans(map[Facet]string{FacetDomain: "creative"}), // agrees
	}}
	return ws, control, []Run{arm}
}

func TestAgreementsAreDiscarded(t *testing.T) {
	ws, control, arms := fixtureRuns()
	got := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	if len(got.Items) != 1 {
		t.Fatalf("want 1 disagreement, got %d: %+v", len(got.Items), got.Items)
	}
	if got.Items[0].ID != "p1" {
		t.Errorf("wrong row kept: %+v", got.Items[0])
	}
}

// The adjudicator must not be able to tell which model proposed which label, or
// the blinding is theatre.
func TestAdjudicationItemsHideProvenance(t *testing.T) {
	ws, control, arms := fixtureRuns()
	b, err := json.Marshal(Disagreements(ws, control, arms, []Facet{FacetDomain}, 7).Items)
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(string(b))
	for _, leak := range []string{"gliner2", "qwen", "control", "\"arm\""} {
		if strings.Contains(low, leak) {
			t.Errorf("adjudication items leak provenance %q: %s", leak, b)
		}
	}
}

func TestOptionsCarryReadableDescriptions(t *testing.T) {
	ws, control, arms := fixtureRuns()
	it := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7).Items[0]
	if len(it.Options) != 2 {
		t.Fatalf("want 2 options, got %d", len(it.Options))
	}
	for _, o := range it.Options {
		if o.Description == "" || o.Description == o.Label {
			t.Errorf("option %q needs the readable label wording, got %q", o.Label, o.Description)
		}
	}
}

func TestShuffleIsDeterministicUnderSeed(t *testing.T) {
	ws, control, arms := fixtureRuns()
	a := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	b := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	if a.Items[0].Options[0].Label != b.Items[0].Options[0].Label {
		t.Fatal("same seed produced different option order")
	}
}

func TestProvenanceMapsKeysBackToArms(t *testing.T) {
	ws, control, arms := fixtureRuns()
	got := Disagreements(ws, control, arms, []Facet{FacetDomain}, 7)
	prov := got.Provenance[itemKey("p1", FacetDomain)]
	if len(prov) != 2 {
		t.Fatalf("provenance for p1:domain = %v", prov)
	}
	var sawControl, sawArm bool
	for _, arm := range prov {
		switch arm {
		case "gliner2":
			sawControl = true
		case "qwen3-4b":
			sawArm = true
		}
	}
	if !sawControl || !sawArm {
		t.Errorf("provenance must name both arms, got %v", prov)
	}
}

func TestInvalidAnswersAreSkipped(t *testing.T) {
	ws, control, _ := fixtureRuns()
	broken := Run{Arm: "broken", Answers: []Answer{{Valid: false, Err: "boom"}, {Valid: false}}}
	if got := Disagreements(ws, control, []Run{broken}, []Facet{FacetDomain}, 7); len(got.Items) != 0 {
		t.Fatalf("invalid answers must not become adjudication items, got %+v", got.Items)
	}
}

// A Partial answer is missing exactly one facet. That absence must not be scored
// as a disagreement, or the arm would be punished for a pass that never ran.
func TestPartialAnswerDoesNotManufactureADisagreement(t *testing.T) {
	ws := []Window{{PromptID: "p1", Target: "x", Turns: []Turn{{RoleUser, "x"}}}}
	control := Run{Arm: "gliner2", Answers: []Answer{
		ans(map[Facet]string{FacetDomain: "software", FacetSubcategory: "eng.dev"}),
	}}
	partial := Run{Arm: "qwen3-4b", Answers: []Answer{{
		Labels:  map[Facet]string{FacetDomain: "software"}, // subcategory absent
		Valid:   true,
		Partial: true,
	}}}
	got := Disagreements(ws, control, []Run{partial}, []Facet{FacetDomain, FacetSubcategory}, 7)
	if len(got.Items) != 0 {
		t.Fatalf("a missing facet must not become a disagreement, got %+v", got.Items)
	}
}

// Three arms proposing three distinct labels must yield three options, not two.
func TestThreeWayDisagreementYieldsThreeOptions(t *testing.T) {
	ws := []Window{{PromptID: "p1", Target: "x", Turns: []Turn{{RoleUser, "x"}}}}
	control := Run{Arm: "gliner2", Answers: []Answer{ans(map[Facet]string{FacetDomain: "general"})}}
	a := Run{Arm: "qwen3-4b", Answers: []Answer{ans(map[Facet]string{FacetDomain: "software"})}}
	b := Run{Arm: "qwen3-1.7b", Answers: []Answer{ans(map[Facet]string{FacetDomain: "business"})}}

	got := Disagreements(ws, control, []Run{a, b}, []Facet{FacetDomain}, 7)
	if len(got.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(got.Items))
	}
	if n := len(got.Items[0].Options); n != 3 {
		t.Fatalf("want 3 options for a three-way split, got %d", n)
	}
}

// Two arms agreeing on the same non-control label share one option, and
// provenance records both.
func TestArmsSharingALabelShareAnOption(t *testing.T) {
	ws := []Window{{PromptID: "p1", Target: "x", Turns: []Turn{{RoleUser, "x"}}}}
	control := Run{Arm: "gliner2", Answers: []Answer{ans(map[Facet]string{FacetDomain: "general"})}}
	a := Run{Arm: "qwen3-4b", Answers: []Answer{ans(map[Facet]string{FacetDomain: "software"})}}
	b := Run{Arm: "qwen3-1.7b", Answers: []Answer{ans(map[Facet]string{FacetDomain: "software"})}}

	got := Disagreements(ws, control, []Run{a, b}, []Facet{FacetDomain}, 7)
	if n := len(got.Items[0].Options); n != 2 {
		t.Fatalf("want 2 options when both arms agree, got %d", n)
	}
	var joined bool
	for _, arm := range got.Provenance[itemKey("p1", FacetDomain)] {
		if arm == "qwen3-1.7b+qwen3-4b" {
			joined = true
		}
	}
	if !joined {
		t.Errorf("provenance must record both arms on the shared option: %v",
			got.Provenance[itemKey("p1", FacetDomain)])
	}
}

func manyItems(facet string, n int) AdjudicationSet {
	s := AdjudicationSet{Provenance: map[string]map[string]string{}}
	for i := 0; i < n; i++ {
		id := facet + "-" + strconv.Itoa(i)
		s.Items = append(s.Items, Item{ID: id, Facet: facet,
			Options: []Option{{Key: "a", Label: "x"}, {Key: "b", Label: "y"}}})
		s.Provenance[itemKey(id, Facet(facet))] = map[string]string{"a": "arm", "b": "ctl"}
	}
	return s
}

func TestCapPerFacetBoundsEachFacetAndRecordsDrops(t *testing.T) {
	s := manyItems("domain", 81)
	s.Items = append(s.Items, manyItems("task_type", 115).Items...)
	for k, v := range manyItems("task_type", 115).Provenance {
		s.Provenance[k] = v
	}

	got := CapPerFacet(s, 40, 7)
	counts := map[string]int{}
	for _, it := range got.Items {
		counts[it.Facet]++
	}
	if counts["domain"] != 40 || counts["task_type"] != 40 {
		t.Fatalf("per-facet counts = %v, want 40 each", counts)
	}
	if got.Dropped["domain"] != 41 || got.Dropped["task_type"] != 75 {
		t.Fatalf("Dropped = %v, want domain 41 / task_type 75", got.Dropped)
	}
	// Provenance must be carried for every surviving item, or scoring breaks.
	for _, it := range got.Items {
		if _, ok := got.Provenance[itemKey(it.ID, Facet(it.Facet))]; !ok {
			t.Fatalf("provenance missing for kept item %s:%s", it.ID, it.Facet)
		}
	}
}

func TestCapPerFacetIsDeterministicAndDoesNotPadSmallFacets(t *testing.T) {
	s := manyItems("domain", 5)
	a, b := CapPerFacet(s, 40, 7), CapPerFacet(s, 40, 7)
	if len(a.Items) != 5 {
		t.Fatalf("a facet under the cap must be kept whole, got %d", len(a.Items))
	}
	if len(a.Dropped) != 0 {
		t.Errorf("nothing was dropped, Dropped should be empty: %v", a.Dropped)
	}
	for i := range a.Items {
		if a.Items[i].ID != b.Items[i].ID {
			t.Fatal("same seed produced a different sample")
		}
	}
}

func TestCapPerFacetZeroMeansNoCap(t *testing.T) {
	s := manyItems("domain", 81)
	if got := CapPerFacet(s, 0, 7); len(got.Items) != 81 {
		t.Fatalf("n<=0 must disable capping, got %d", len(got.Items))
	}
}
