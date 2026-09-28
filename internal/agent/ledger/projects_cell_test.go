package ledger

import (
	"reflect"
	"testing"
	"time"
)

// cellList reads cell["projects"] as the wire carries it.
func cellList(t *testing.T, cell map[string]any) []map[string]any {
	t.Helper()
	raw, ok := cell["projects"]
	if !ok {
		t.Fatalf("cell has no projects list: %#v", cell)
	}
	list, ok := raw.([]map[string]any)
	if !ok {
		t.Fatalf("projects is %T, want []map[string]any", raw)
	}
	return list
}

// AC-7 (R4). The attributed cell names every project a block landed in, each
// with its method, in the order the pass assigned them — and no group.
func TestProjectsCellAndLegacyRow(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "multi1", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{Projects: []AttributedProject{
		{ProjectID: "products:atlas_platform", Method: MethodRepo},
		{ProjectID: "products:signal_client", Method: MethodRepo},
		{ProjectID: "features:billing", Method: MethodTicket},
	}}, ReasonNone, time.Now())

	cell := attributedCell(t, s, k)
	if cell["status"] != string(StatusOK) {
		t.Fatalf("status = %v", cell["status"])
	}
	want := []map[string]any{
		{"project_id": "products:atlas_platform", "method": "repo"},
		{"project_id": "products:signal_client", "method": "repo"},
		{"project_id": "features:billing", "method": "ticket"},
	}
	if got := cellList(t, cell); !reflect.DeepEqual(got, want) {
		t.Fatalf("projects = %#v\nwant %#v", got, want)
	}
	if _, ok := cell["project_id"]; ok {
		t.Fatal("the retired single project_id must not ride beside the list")
	}

	// A row written before the list existed — only project_id/method set —
	// reads back as a one-entry list.
	legacy := BlockKey{Session: "legacy1", Start: 1788500000}
	s.Cut(legacy, 1788501200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(legacy, Attributed{Projects: []AttributedProject{{ProjectID: "x", Method: MethodRepo}}}, ReasonNone, time.Now())
	if _, err := s.handle().Exec(`UPDATE blocks SET projects='', project_id='p_old', method='repo' WHERE session=?`, legacy.Session); err != nil {
		t.Fatal(err)
	}
	got := cellList(t, attributedCell(t, s, legacy))
	if !reflect.DeepEqual(got, []map[string]any{{"project_id": "p_old", "method": "repo"}}) {
		t.Fatalf("a legacy row must read as a one-entry list: %#v", got)
	}

	// A row Revisions 1–3 wrote carries a group in each stored entry. It still
	// reads — without the group.
	grouped := BlockKey{Session: "grouped1", Start: 1788400000}
	s.Cut(grouped, 1788401200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(grouped, Attributed{Projects: []AttributedProject{{ProjectID: "x", Method: MethodRepo}}}, ReasonNone, time.Now())
	if _, err := s.handle().Exec(`UPDATE blocks SET projects=? WHERE session=?`,
		`[{"project_id":"p_a","group":"products","method":"repo"},{"project_id":"p_b","group":"features","method":"ticket"}]`,
		grouped.Session); err != nil {
		t.Fatal(err)
	}
	got = cellList(t, attributedCell(t, s, grouped))
	if !reflect.DeepEqual(got, []map[string]any{
		{"project_id": "p_a", "method": "repo"},
		{"project_id": "p_b", "method": "ticket"},
	}) {
		t.Fatalf("a row with stored groups must read as {project_id, method}: %#v", got)
	}
}

// An id that fails the shape check is dropped ALONE; the others still land.
func TestOneRefusedIDDoesNotLoseTheOthers(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "multi2", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{Projects: []AttributedProject{
		{ProjectID: "has a space", Method: MethodRepo},
		{ProjectID: "good", Method: MethodRepo},
	}}, ReasonNone, time.Now())
	got := cellList(t, attributedCell(t, s, k))
	if len(got) != 1 || got[0]["project_id"] != "good" {
		t.Fatalf("want only the valid id kept, got %#v", got)
	}
}

// Every id refused is the lost-id defect, refused as a write rather than
// recorded as an ok cell naming nothing.
func TestAnOKAttributionWithNoValidIDIsRefused(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "multi3", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Attribute(k, Attributed{Projects: []AttributedProject{{ProjectID: "has a space"}}}, ReasonNone, time.Now())
	if cell := attributedCell(t, s, k); cell != nil {
		t.Fatalf("an ok attribution naming no valid id must not be recorded: %#v", cell)
	}
}

// The vector cell keeps EVERY id the pass named, with its own confidence — the
// second opinion used to keep only index 0 — and no group.
func TestVectorCellKeepsEveryID(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "vec1", Start: 1788600000}
	s.Cut(k, 1788601200, "idle", "budget", "claude_code", time.Now())
	s.Vector(k, VectorAttributed{Projects: []VectorProject{
		{ProjectID: "products:atlas_platform", Confidence: 0.62},
		{ProjectID: "features:billing", Confidence: 0.51},
	}}, StatusOK, ReasonNone, time.Now())

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	cell := snap.Blocks[0].Cells["vector"]
	want := []map[string]any{
		{"project_id": "products:atlas_platform", "confidence": 0.62},
		{"project_id": "features:billing", "confidence": 0.51},
	}
	if got := cellList(t, cell); !reflect.DeepEqual(got, want) {
		t.Fatalf("vector projects = %#v\nwant %#v", got, want)
	}
}

// one / vone build the single-project answers the older tests in this
// package were written against.
func one(id string, m Method) Attributed {
	return Attributed{Projects: []AttributedProject{{ProjectID: id, Method: m}}}
}

func vone(id string, conf float64) VectorAttributed {
	return VectorAttributed{Projects: []VectorProject{{ProjectID: id, Confidence: conf}}}
}

// firstOf is key of the cell's FIRST project, or nil when the cell names
// none — the single-id reading the older tests make.
func firstOf(cell map[string]any, key string) any {
	list, _ := cell["projects"].([]map[string]any)
	if len(list) == 0 {
		return nil
	}
	return list[0][key]
}

// setLegacyConflict writes the pre-2026-09-23 conflict detail straight into
// the row: nothing produces a conflict any more, but a row that holds one must
// still read.
func setLegacyConflict(t *testing.T, s *Store, k BlockKey, ids string) {
	t.Helper()
	if _, err := s.handle().Exec(`UPDATE blocks SET conflict=? WHERE session=? AND start=?`, ids, k.Session, k.Start); err != nil {
		t.Fatal(err)
	}
}
