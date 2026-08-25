package enrich

import "testing"

// The pass carries the four identifier-shaped inventories exactly as it does
// PhysicalActs/Files/Directories/Components: no second /analyze call, no
// inference.
func TestWorkstreamsPassCarriesTheIdentifierInventories(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams:     map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 0.9}},
		HarnessTools:    []NameCount{{"Bash", 30}, {"Read", 12}},
		Programs:        []NameCount{{"git", 9}},
		ExternalSystems: []NameCount{{"github.com", 4}},
		Integrations:    []NameCount{{"notion-fetch", 1}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fa.calls != 1 {
		t.Errorf("the identifier inventories are called %d times; they ride the workstreams call",
			fa.calls)
	}
	tools, _ := got["harness_tools"].([]NameCount)
	if len(tools) != 2 || tools[0].Value != "Bash" || tools[0].N != 30 {
		t.Fatalf("harness_tools inventory not carried: %+v", got)
	}
	programs, _ := got["programs"].([]NameCount)
	if len(programs) != 1 || programs[0].Value != "git" {
		t.Fatalf("programs inventory not carried: %+v", got)
	}
	systems, _ := got["external_systems"].([]NameCount)
	if len(systems) != 1 || systems[0].Value != "github.com" {
		t.Fatalf("external_systems inventory not carried: %+v", got)
	}
	integrations, _ := got["integrations"].([]NameCount)
	if len(integrations) != 1 || integrations[0].Value != "notion-fetch" {
		t.Fatalf("integrations inventory not carried: %+v", got)
	}
}

// An analysis with no evidence at a dimension must publish no key for it, not
// an empty list — same rule the path inventories already follow.
func TestWorkstreamsPassOmitsEmptyIdentifierInventories(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "main", Confidence: 1}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, key := range []string{"harness_tools", "programs", "external_systems", "integrations"} {
		if got[key] != nil {
			t.Errorf("%s: empty inventory published: %+v", key, got[key])
		}
	}
}

// End-to-end with NO Model at all — ml_backend "deterministic" — mirroring
// TestRunPublishesFilePathInventoriesWithoutAModel.
func TestRunPublishesIdentifierInventoriesWithoutAModel(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithWorkstreams(func(path, promptID string, span int) (WindowAnalysis, bool) {
			return WindowAnalysis{
				Workstreams:     map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 1}},
				HarnessTools:    []NameCount{{"Bash", 34}},
				Programs:        []NameCount{{"git", 12}},
				ExternalSystems: []NameCount{{"github.com", 3}},
				Integrations:    []NameCount{{"notion-fetch", 1}},
			}, true
		}))
	if len(p.HarnessTools) != 1 || p.HarnessTools[0].Value != "Bash" || p.HarnessTools[0].N != 34 {
		t.Fatalf("profile missing the harness_tools inventory: %+v", p)
	}
	if len(p.Programs) != 1 || len(p.ExternalSystems) != 1 || len(p.Integrations) != 1 {
		t.Fatalf("profile missing the programs/external_systems/integrations inventories: %+v", p)
	}
}

func TestRunWithoutAnAnalyzerPublishesNoIdentifierInventories(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil, WithPassTimeout(0))
	if p.HarnessTools != nil || p.Programs != nil || p.ExternalSystems != nil || p.Integrations != nil {
		t.Errorf("want no identifier inventories, got %+v", p)
	}
}

// identsFrom's own emptiness guard, exercised directly — same reasoning
// TestPathsFromTreatsAnEmptyListAsNoAnswer pins for pathsFrom.
func TestIdentsFromTreatsAnEmptyListAsNoAnswer(t *testing.T) {
	for name, in := range map[string]map[string]any{
		"nil map":           nil,
		"key absent":        {"workstreams": map[string]Labeled{}},
		"empty slice":       {"harness_tools": []NameCount{}},
		"wrong type":        {"harness_tools": []string{"Bash"}},
		"populated (guard)": {"harness_tools": []NameCount{{"Bash", 3}}},
	} {
		got := identsFrom(in, "harness_tools")
		if name == "populated (guard)" {
			if len(got) != 1 || got[0].Value != "Bash" {
				t.Errorf("%s: a real inventory must survive, got %+v", name, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("%s: want nil so the key is omitted, got %+v (len %d)", name, got, len(got))
		}
	}
}
