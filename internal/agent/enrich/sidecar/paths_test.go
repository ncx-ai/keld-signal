package sidecar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// Round-trip: the three OPEN-vocabulary path inventories arrive on the wire
// exactly as the acts inventory does (see acts_test.go), and AnalyzeLabeled
// must carry all three plus their values and counts unchanged.
func TestAnalyzeLabeledCarriesTheFilePathInventories(t *testing.T) {
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"files":       acts("internal/agent/daemon/daemon.go", 5, "sidecar/app/main.py", 3),
		"directories": acts("internal/agent/daemon", 5, "sidecar/app", 3),
		"components":  acts("internal/agent/daemon", 5, "sidecar/app", 3),
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	wantFiles := []enrichAct{{"internal/agent/daemon/daemon.go", 5}, {"sidecar/app/main.py", 3}}
	wantDirsComponents := []enrichAct{{"internal/agent/daemon", 5}, {"sidecar/app", 3}}

	checkPathCounts(t, "Files", got.Files, wantFiles)
	checkPathCounts(t, "Directories", got.Directories, wantDirsComponents)
	checkPathCounts(t, "Components", got.Components, wantDirsComponents)
}

func checkPathCounts(t *testing.T, name string, got []enrich.PathCount, want []enrichAct) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %+v, want %d entries", name, got, len(want))
	}
	for i, w := range want {
		if got[i].Value != w.value || got[i].N != w.n {
			t.Errorf("%s entry %d = %+v, want %+v", name, i, got[i], w)
		}
	}
}

// The structural gate: an entry that does NOT look workspace-relative is
// dropped WITHOUT dropping the rest of the inventory — the same per-entry
// shape convertActs applies for a different reason (vocabulary membership
// rather than shape). Defence in depth: reconcile() is verified never to
// produce these shapes, but the Go decode boundary must not forward one
// unfiltered if it ever did.
func TestANonWorkspaceRelativePathEntryIsDroppedWithoutDroppingTheInventory(t *testing.T) {
	for _, bad := range []string{
		"/etc/passwd", "~/secrets.env", "C:\\Users\\dg\\file.go",
		"../../outside/repo.go", "..",
	} {
		srv := analyzeServer(t, inventoryBody(map[string]any{
			"files": acts("internal/agent/daemon/daemon.go", 9, bad, 4),
		}))
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
		srv.Close()
		if !ok {
			t.Fatalf("%q: AnalyzeLabeled reported failure", bad)
		}
		if len(got.Files) != 1 || got.Files[0].Value != "internal/agent/daemon/daemon.go" {
			t.Fatalf("%q: want only the workspace-relative entry to survive, got %+v", bad, got.Files)
		}
		b, _ := json.Marshal(got)
		if strings.Contains(string(b), bad) {
			t.Errorf("%q: a non-workspace-relative value reached the conversion output: %s", bad, b)
		}
	}
}

// Nil, not an empty slice, for the same reason physical_acts is: a sidecar too
// old to publish the level (or a window that touched nothing at that level)
// must not read as "we looked and the hour touched nothing".
func TestNoFilePathInventoriesIsAbsentNotAnEmptyList(t *testing.T) {
	for name, inv := range map[string]map[string]any{
		"no inventory block at all":       nil,
		"an inventory without these keys": {"physical_acts": acts("read", 2)},
		"the keys present but empty": {
			"files": []map[string]any{}, "directories": []map[string]any{}, "components": []map[string]any{},
		},
		"every entry unrecognised": {"files": acts("/etc/passwd", 4)},
	} {
		body := inventoryBody(inv)
		if inv == nil {
			delete(body, "inventory")
		}
		srv := analyzeServer(t, body)
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
		srv.Close()
		if !ok {
			t.Fatalf("%s: AnalyzeLabeled reported failure", name)
		}
		if got.Files != nil || got.Directories != nil || got.Components != nil {
			t.Errorf("%s: want nil, got files=%+v dirs=%+v components=%+v",
				name, got.Files, got.Directories, got.Components)
		}
	}
}

// inventory_omitted forwards as a plain count map: unchanged, and nil rather
// than empty when nothing was cut (including when the sidecar is too old to
// send the key at all).
func TestAnalyzeLabeledCarriesInventoryOmittedUnchanged(t *testing.T) {
	body := inventoryBody(map[string]any{"files": acts("a.go", 1)})
	body["inventory_omitted"] = map[string]any{"files": 5, "programs": 2}
	srv := analyzeServer(t, body)
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if len(got.InventoryOmitted) != 2 || got.InventoryOmitted["files"] != 5 ||
		got.InventoryOmitted["programs"] != 2 {
		t.Fatalf("InventoryOmitted = %+v, want {files:5 programs:2}", got.InventoryOmitted)
	}
}

func TestNoInventoryOmittedIsAbsentNotAnEmptyMap(t *testing.T) {
	// Key absent entirely (a sidecar that never sends it).
	srv := analyzeServer(t, inventoryBody(map[string]any{"files": acts("a.go", 1)}))
	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	srv.Close()
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.InventoryOmitted != nil {
		t.Errorf("key absent: want nil, got %+v", got.InventoryOmitted)
	}

	// An explicit empty object is the same fact ("nothing was cut") and must
	// also come back nil.
	body := inventoryBody(map[string]any{"files": acts("a.go", 1)})
	body["inventory_omitted"] = map[string]any{}
	srv2 := analyzeServer(t, body)
	defer srv2.Close()
	got2, ok := New(srv2.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got2.InventoryOmitted != nil {
		t.Errorf("explicit empty object: want nil, got %+v", got2.InventoryOmitted)
	}
}
