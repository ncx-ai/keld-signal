package sidecar

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// inventoryBody wraps an inventory block in an otherwise minimal /analyze body,
// alongside the sibling keys the real sidecar always sends beside it — so a test
// of the acts half cannot pass because nothing else was there to confuse it.
func inventoryBody(inv map[string]any) map[string]any {
	return map[string]any{
		"schema": 7, "session": "453451c2",
		"window_start": "2026-08-23T00:00:00Z", "window_end": "2026-08-23T01:00:00Z",
		"workstreams": map[string]any{
			"project": map[string]any{"value": "keld-signal", "share": 1.0, "evidence": 9,
				"provenance": "known:tool_inputs"},
		},
		"inventory": inv,
	}
}

func acts(vals ...any) []map[string]any {
	out := make([]map[string]any, 0, len(vals)/2)
	for i := 0; i < len(vals); i += 2 {
		out = append(out, map[string]any{"value": vals[i], "n": vals[i+1]})
	}
	return out
}

func TestAnalyzeLabeledCarriesThePhysicalActsInventory(t *testing.T) {
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"physical_acts": acts("read", 41, "edit", 12, "test", 7, "run a service", 2),
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	want := []enrichAct{{"read", 41}, {"edit", 12}, {"test", 7}, {"run a service", 2}}
	if len(got.PhysicalActs) != len(want) {
		t.Fatalf("PhysicalActs = %+v, want %d entries", got.PhysicalActs, len(want))
	}
	for i, w := range want {
		if got.PhysicalActs[i].Value != w.value || got.PhysicalActs[i].N != w.n {
			t.Errorf("entry %d = %+v, want %+v", i, got.PhysicalActs[i], w)
		}
	}
}

type enrichAct struct {
	value string
	n     int
}

// The whole level is published UNTRUNCATED (the sidecar's own decision — the
// vocabulary is closed at 22 values, see workstreams.INVENTORY's cap column), so
// the client must not reintroduce a cut it was deliberately spared. A window
// carrying more than the twelve every other inventory dimension is cut at is the
// case that would expose one.
func TestPhysicalActsAreNotTruncatedClientSide(t *testing.T) {
	var vals []any
	for _, a := range []string{"read", "search", "edit", "create", "test", "build",
		"install", "commit", "fetch", "delegate", "publish", "run code",
		"manage files", "transform", "version control", "sync with remote"} {
		vals = append(vals, a, 1)
	}
	srv := analyzeServer(t, inventoryBody(map[string]any{"physical_acts": acts(vals...)}))
	defer srv.Close()

	got, _ := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if len(got.PhysicalActs) != 16 {
		t.Errorf("got %d acts, want all 16: the level is published whole: %+v",
			len(got.PhysicalActs), got.PhysicalActs)
	}
}

// The vocabulary gate, and the ONE place its rule differs from convertDynamics /
// convertEffort: an inventory is a list of independent items, so an unreadable
// item costs exactly that item. Dropping the list would discard every act the
// binary does understand because of one it does not.
func TestAnUnknownActIsDroppedWithoutDroppingTheInventory(t *testing.T) {
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"physical_acts": acts("read", 9, "refactor a monolith", 4, "edit", 3, "", 2),
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if len(got.PhysicalActs) != 2 {
		t.Fatalf("want the two known acts, got %+v", got.PhysicalActs)
	}
	if got.PhysicalActs[0].Value != "read" || got.PhysicalActs[1].Value != "edit" {
		t.Errorf("known acts must survive in order, got %+v", got.PhysicalActs)
	}
	b, _ := json.Marshal(got)
	for _, forbidden := range []string{"refactor a monolith", `"value":""`} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("an unrecognised act reached the conversion output (%q): %s", forbidden, b)
		}
	}
}

// Nil, not an empty slice: a sidecar too old to publish the level sends nothing,
// and `physical_acts: []` on the wire would read as "we looked and the hour did
// nothing" — the same distinction dynamicsFrom and effortFrom already keep.
func TestNoPhysicalActsIsAbsentNotAnEmptyList(t *testing.T) {
	for name, inv := range map[string]map[string]any{
		"no inventory block at all": nil,
		"an inventory without the key": {
			"named_terms": acts("Federico", 2),
		},
		"the key present but empty": {"physical_acts": []map[string]any{}},
		"every entry unrecognised": {
			"physical_acts": acts("refactor a monolith", 4),
		},
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
		if got.PhysicalActs != nil {
			t.Errorf("%s: want nil, got %+v", name, got.PhysicalActs)
		}
	}
}

// The guarantee AnalyzeResult has always held for the inventory block, now that
// the block is modelled at all: `physical_acts` is the ONLY key with a field, so
// every other one — above all `named_terms`, which is drawn from message text and
// has held real person names — is undecodable rather than decoded-and-dropped.
// Asserted over the STRUCT, so a field added tomorrow fails here.
func TestOnlyPhysicalActsIsDecodableFromTheInventoryBlock(t *testing.T) {
	rt := reflect.TypeOf(InventoryBlock{})
	if rt.NumField() != 1 {
		var names []string
		for i := 0; i < rt.NumField(); i++ {
			names = append(names, rt.Field(i).Name)
		}
		t.Fatalf("InventoryBlock models %v; only physical_acts may be decodable — "+
			"named_terms is drawn from message TEXT and must stay unrepresentable", names)
	}
	if tag := rt.Field(0).Tag.Get("json"); tag != "physical_acts" {
		t.Errorf("the one modelled inventory key is %q, want physical_acts", tag)
	}
	// And the same property end-to-end, because a struct check alone would not
	// catch a leak added elsewhere in the conversion.
	srv := analyzeServer(t, inventoryBody(map[string]any{
		"physical_acts":    acts("read", 5),
		"named_terms":      acts("Federico", 2),
		"programs":         acts("git", 9),
		"harness_tools":    acts("Bash", 30),
		"external_systems": acts("acme-internal.example", 3),
		"integrations":     acts("notion-fetch", 1),
	}))
	defer srv.Close()
	got, _ := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if len(got.PhysicalActs) != 1 || got.PhysicalActs[0].Value != "read" {
		t.Fatalf("the acts half is empty; the leak checks would be vacuous: %+v", got)
	}
	b, _ := json.Marshal(got)
	for _, forbidden := range []string{"Federico", "named_terms", "inventory", "git",
		"programs", "harness_tools", "acme-internal.example", "notion-fetch", "453451c2",
		"window_start", "2026-08-23T00:00:00Z"} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("the inventory conversion leaked %q: %s", forbidden, b)
		}
	}
}
