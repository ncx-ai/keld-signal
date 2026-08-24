package enrich

import "testing"

// The pass makes ONE /analyze call and publishes all FOUR of its halves now: the
// digest, the dynamics, the effort block, and the physical-acts inventory. The
// inventory needs no inference at all — it is a count of tool calls against a
// closed table — so it survives ml_backend "deterministic" for the same reason
// the other three do.
func TestWorkstreamsPassCarriesThePhysicalActsInventory(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams:  map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 0.9}},
		PhysicalActs: []Act{{"read", 41}, {"edit", 12}, {"run a service", 2}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fa.calls != 1 {
		t.Errorf("the analysis was called %d times; the inventory rides the workstreams call",
			fa.calls)
	}
	acts, _ := got["physical_acts"].([]Act)
	if len(acts) != 3 {
		t.Fatalf("inventory not carried: %+v", got)
	}
	if acts[0].Value != "read" || acts[0].N != 41 || acts[2].Value != "run a service" {
		t.Errorf("the inventory was mangled or reordered: %+v", acts)
	}
}

// An analysis with no acts must publish no key, not an empty list: "we looked and
// the hour did nothing" is not a fact a window can state, and it is what an empty
// list reads as. Same rule dynamicsFrom and effortFrom already follow.
func TestWorkstreamsPassOmitsAnEmptyActsInventory(t *testing.T) {
	for name, an := range map[string]WindowAnalysis{
		"nil acts":   {Workstreams: map[string]Labeled{"branch": {Value: "main", Confidence: 1}}},
		"empty acts": {PhysicalActs: []Act{}},
	} {
		fa := &fakeAnalyze{ok: true, out: an}
		got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
		if err != nil {
			t.Fatalf("%s: err = %v", name, err)
		}
		if got["physical_acts"] != nil {
			t.Errorf("%s: empty inventory published: %+v", name, got["physical_acts"])
		}
	}
}

// End-to-end with NO Model at all — ml_backend "deterministic".
func TestRunPublishesPhysicalActsWithoutAModel(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithWorkstreams(func(path, promptID string, span int) (WindowAnalysis, bool) {
			return WindowAnalysis{
				Workstreams:  map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 1}},
				PhysicalActs: []Act{{"read", 34}, {"test", 9}},
			}, true
		}))
	if len(p.PhysicalActs) != 2 {
		t.Fatalf("profile missing the acts inventory: %+v", p)
	}
	if p.PhysicalActs[0].Value != "read" || p.PhysicalActs[0].N != 34 {
		t.Errorf("acts dropped or mangled: %+v", p.PhysicalActs)
	}
	// The whole point of it being an inventory: several acts, no dominance claim.
	// Nothing here is a share, so nothing sums to 1 and nothing is "unattributed".
	if p.PhysicalActs[1].Value != "test" || p.PhysicalActs[1].N != 9 {
		t.Errorf("the second act was dropped — an inventory is plural: %+v", p.PhysicalActs)
	}
}

// actsFrom's own emptiness guard, exercised DIRECTLY. Through the pass it is
// unreachable — the pass already declines to set the key for an empty list — so a
// mutation that turned `len(a) > 0` into a bare type assertion survived the whole
// suite. It is kept (both dynamicsFrom and effortFrom carry the same defensive
// shape, and the pass is not the only thing that can write this map) and it is
// pinned here, which is what turns "unreachable" into "specified".
func TestActsFromTreatsAnEmptyListAsNoAnswer(t *testing.T) {
	for name, in := range map[string]map[string]any{
		"nil map":           nil,
		"key absent":        {"workstreams": map[string]Labeled{}},
		"empty slice":       {"physical_acts": []Act{}},
		"wrong type":        {"physical_acts": []string{"read"}},
		"populated (guard)": {"physical_acts": []Act{{"read", 3}}},
	} {
		got := actsFrom(in)
		if name == "populated (guard)" {
			if len(got) != 1 || got[0].Value != "read" {
				t.Errorf("%s: a real inventory must survive, got %+v", name, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("%s: want nil so the key is omitted, got %+v (len %d)", name, got, len(got))
		}
	}
}

func TestRunWithoutAnAnalyzerPublishesNoPhysicalActs(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil, WithPassTimeout(0))
	if p.PhysicalActs != nil {
		t.Errorf("want no acts, got %+v", p.PhysicalActs)
	}
}
