package enrich

import "testing"

// The pass carries the three file-path inventories exactly as it does
// PhysicalActs: no second /analyze call, no inference.
func TestWorkstreamsPassCarriesTheFilePathInventories(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 0.9}},
		Files:       []PathCount{{"internal/agent/daemon/daemon.go", 5}, {"sidecar/app/main.py", 3}},
		Directories: []PathCount{{"internal/agent/daemon", 5}},
		Components:  []PathCount{{"internal/agent/daemon", 5}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fa.calls != 1 {
		t.Errorf("the analysis was called %d times; the path inventories ride the workstreams call",
			fa.calls)
	}
	files, _ := got["files"].([]PathCount)
	if len(files) != 2 || files[0].Value != "internal/agent/daemon/daemon.go" || files[0].N != 5 {
		t.Fatalf("files inventory not carried: %+v", got)
	}
	dirs, _ := got["directories"].([]PathCount)
	if len(dirs) != 1 || dirs[0].Value != "internal/agent/daemon" {
		t.Fatalf("directories inventory not carried: %+v", got)
	}
	components, _ := got["components"].([]PathCount)
	if len(components) != 1 || components[0].Value != "internal/agent/daemon" {
		t.Fatalf("components inventory not carried: %+v", got)
	}
}

// An analysis with no path evidence at a dimension must publish no key for it,
// not an empty list — same rule actsFrom/dynamicsFrom/effortFrom already follow.
func TestWorkstreamsPassOmitsEmptyFilePathInventories(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "main", Confidence: 1}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, key := range []string{"files", "directories", "components"} {
		if got[key] != nil {
			t.Errorf("%s: empty inventory published: %+v", key, got[key])
		}
	}
}

// InventoryOmitted rides the same call and the same "empty means no key" rule.
func TestWorkstreamsPassCarriesInventoryOmitted(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams:      map[string]Labeled{"branch": {Value: "main", Confidence: 1}},
		Files:            []PathCount{{"a.go", 45}},
		InventoryOmitted: map[string]int{"files": 5},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	omitted, _ := got["inventory_omitted"].(map[string]int)
	if len(omitted) != 1 || omitted["files"] != 5 {
		t.Fatalf("inventory_omitted not carried: %+v", got)
	}
}

func TestWorkstreamsPassOmitsEmptyInventoryOmitted(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "main", Confidence: 1}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got["inventory_omitted"] != nil {
		t.Errorf("empty inventory_omitted published: %+v", got["inventory_omitted"])
	}
}

// End-to-end with NO Model at all — ml_backend "deterministic" — mirroring
// TestRunPublishesPhysicalActsWithoutAModel.
func TestRunPublishesFilePathInventoriesWithoutAModel(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithWorkstreams(func(path, promptID string, span int) (WindowAnalysis, bool) {
			return WindowAnalysis{
				Workstreams:      map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 1}},
				Files:            []PathCount{{"internal/agent/daemon/daemon.go", 34}},
				Directories:      []PathCount{{"internal/agent/daemon", 34}},
				Components:       []PathCount{{"internal/agent/daemon", 34}},
				InventoryOmitted: map[string]int{"files": 3},
			}, true
		}))
	if len(p.Files) != 1 || p.Files[0].Value != "internal/agent/daemon/daemon.go" || p.Files[0].N != 34 {
		t.Fatalf("profile missing the files inventory: %+v", p)
	}
	if len(p.Directories) != 1 || len(p.Components) != 1 {
		t.Fatalf("profile missing the directories/components inventories: %+v", p)
	}
	if len(p.InventoryOmitted) != 1 || p.InventoryOmitted["files"] != 3 {
		t.Fatalf("profile missing inventory_omitted: %+v", p)
	}
}

func TestRunWithoutAnAnalyzerPublishesNoFilePathInventories(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil, WithPassTimeout(0))
	if p.Files != nil || p.Directories != nil || p.Components != nil || p.InventoryOmitted != nil {
		t.Errorf("want no path inventories, got %+v", p)
	}
}

// pathsFrom's own emptiness guard, exercised directly — same reasoning
// TestActsFromTreatsAnEmptyListAsNoAnswer pins for actsFrom.
func TestPathsFromTreatsAnEmptyListAsNoAnswer(t *testing.T) {
	for name, in := range map[string]map[string]any{
		"nil map":           nil,
		"key absent":        {"workstreams": map[string]Labeled{}},
		"empty slice":       {"files": []PathCount{}},
		"wrong type":        {"files": []string{"a.go"}},
		"populated (guard)": {"files": []PathCount{{"a.go", 3}}},
	} {
		got := pathsFrom(in, "files")
		if name == "populated (guard)" {
			if len(got) != 1 || got[0].Value != "a.go" {
				t.Errorf("%s: a real inventory must survive, got %+v", name, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("%s: want nil so the key is omitted, got %+v (len %d)", name, got, len(got))
		}
	}
}

func TestInventoryOmittedFromTreatsAnEmptyMapAsNoAnswer(t *testing.T) {
	for name, in := range map[string]map[string]any{
		"nil map":           nil,
		"key absent":        {"workstreams": map[string]Labeled{}},
		"empty map":         {"inventory_omitted": map[string]int{}},
		"wrong type":        {"inventory_omitted": map[string]string{"files": "5"}},
		"populated (guard)": {"inventory_omitted": map[string]int{"files": 5}},
	} {
		got := inventoryOmittedFrom(in)
		if name == "populated (guard)" {
			if len(got) != 1 || got["files"] != 5 {
				t.Errorf("%s: a real map must survive, got %+v", name, got)
			}
			continue
		}
		if got != nil {
			t.Errorf("%s: want nil so the key is omitted, got %+v (len %d)", name, got, len(got))
		}
	}
}
