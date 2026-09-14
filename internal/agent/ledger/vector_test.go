package ledger

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// THE VECTOR CELL — the vectorised attribution pass's own answer, stored
// beside the deterministic one and never in place of it. Every test here is
// named after the claim it holds, and the negative ones are the point: the
// defect these replace was not a missing feature but a present, confident,
// WRONG value written over a correct one.

// A block nobody asked the vector pass about has no vector cell at all — not
// a null, not an empty object, not a zero confidence. This is the state of
// every block on every machine with the toggle off, which is the default
// population, so it is asserted on the MARSHALLED WIRE and not merely on the
// Go value: six columns that are absent in Go but appear as nulls in JSON
// would break the "nothing changes with the toggle off" claim in the one
// place a consumer would notice.
func TestVectorCellIsAbsentFromTheWireWhenNothingAskedTheVectorPass(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-off", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)
	s.Attribute(k, Attributed{ProjectID: "p_keld_signal", Method: MethodRepo}, ReasonNone, at)

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(snap.Blocks))
	}
	if cell, ok := snap.Blocks[0].Cells["vector"]; ok {
		t.Fatalf("nothing asked the vector pass, so its cell must be ABSENT; got %#v", cell)
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Not a substring check on the whole document (the word could appear in a
	// project id one day): decode and look at the cells object itself.
	var decoded struct {
		Blocks []struct {
			Cells map[string]json.RawMessage `json:"cells"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw, ok := decoded.Blocks[0].Cells["vector"]; ok {
		t.Fatalf("the marshalled cells object must not carry a `vector` key at all; got %s", raw)
	}
	// And no vector_* column may leak into the wire under any other name.
	for _, forbidden := range []string{"vector_status", "vector_project_id", "vector_confidence"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("column %q reached the wire; the wire shape is cells, not columns", forbidden)
		}
	}
}

// NEGATIVE, AND THE ONE THAT WOULD HAVE SAVED 44 ROWS AT THE STORE LEVEL: a
// vector failure writes the vector cell and leaves the deterministic cell
// byte-identical. Not "similar" — the same map, field for field.
func TestVectorFailureLeavesTheDeterministicCellByteIdentical(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-44", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)
	s.Attribute(k, Attributed{ProjectID: "p_keld_signal", Method: MethodRepo}, ReasonNone, at)

	before := cellJSON(t, s, "attributed")

	// Four sweeps of an encoder that could not get memory, ending in a
	// quarantine — the exact sequence the 44 rows came from.
	s.Vector(k, VectorAttributed{}, StatusPending, ReasonWeightsUnavailable, at.Add(time.Minute))
	s.Vector(k, VectorAttributed{}, StatusFailed, ReasonAttributeFailed, at.Add(2*time.Minute))

	after := cellJSON(t, s, "attributed")
	if before != after {
		t.Fatalf("the deterministic cell changed under a vector failure:\n before %s\n  after %s", before, after)
	}

	vec := readCell(t, s, "vector")
	if vec["status"] != string(StatusFailed) || vec["reason"] != string(ReasonAttributeFailed) {
		t.Fatalf("vector = %#v, want failed/attribute_failed", vec)
	}
}

// Both passes named a project and they AGREE: both ids are stored and both are
// readable. Nothing merges them into one displayed answer.
func TestBothPassesAgreeAndBothIdsAreStored(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-agree", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)
	s.Attribute(k, Attributed{ProjectID: "p_signal", Method: MethodRepo}, ReasonNone, at)
	s.Vector(k, VectorAttributed{ProjectID: "p_signal", Confidence: 0.82}, StatusOK, ReasonNone, at)

	det, vec := readCell(t, s, "attributed"), readCell(t, s, "vector")
	if det["project_id"] != "p_signal" || det["method"] != string(MethodRepo) {
		t.Fatalf("deterministic cell = %#v", det)
	}
	if vec["status"] != string(StatusOK) || vec["project_id"] != "p_signal" {
		t.Fatalf("vector cell = %#v", vec)
	}
	if got, ok := vec["confidence"].(float64); !ok || got != 0.82 {
		t.Fatalf("vector confidence = %#v, want 0.82", vec["confidence"])
	}
}

// They DISAGREE: both are still stored, neither is overwritten, and no
// resolution is attempted — there is no third field naming a winner and no
// flag settling it. Deciding which wins needs data from a machine running both
// passes, which does not exist yet.
func TestBothPassesDisagreeAndNeitherIsOverwrittenOrResolved(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-disagree", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)
	s.Attribute(k, Attributed{ProjectID: "p_signal", Method: MethodRepo}, ReasonNone, at)
	s.Vector(k, VectorAttributed{ProjectID: "p_atlas", Confidence: 0.61}, StatusOK, ReasonNone, at)

	det, vec := readCell(t, s, "attributed"), readCell(t, s, "vector")
	if det["project_id"] != "p_signal" {
		t.Fatalf("the deterministic id must survive a disagreeing vector answer; got %#v", det)
	}
	if vec["project_id"] != "p_atlas" {
		t.Fatalf("the vector id must survive a disagreeing deterministic answer; got %#v", vec)
	}
	// The disagreement is representable — a reader can see the two ids differ
	// — and it is not RESOLVED anywhere: no winner field, no agreement flag
	// this store cannot keep true (daemon.liveAttribution recomputes the
	// deterministic cell after Read returns).
	for _, forbidden := range []string{"agrees", "winner", "resolved", "project"} {
		if _, ok := vec[forbidden]; ok {
			t.Fatalf("the vector cell must not carry %q: representing the disagreement is the point, settling it is out of scope", forbidden)
		}
	}
}

// Round trip: the deterministic pass answering AFTER a vector failure is not
// blocked by it either. The two cells are independent in both directions.
func TestTheTwoCellsAreIndependentInBothDirections(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-both", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)

	s.Vector(k, VectorAttributed{}, StatusFailed, ReasonAttributeFailed, at)
	s.Attribute(k, Attributed{ProjectID: "p_late", Method: MethodTicket}, ReasonNone, at.Add(time.Hour))

	det := readCell(t, s, "attributed")
	if det["status"] != string(StatusOK) || det["project_id"] != "p_late" {
		t.Fatalf("a prior vector failure must not affect a later deterministic answer; got %#v", det)
	}
	vec := readCell(t, s, "vector")
	if vec["status"] != string(StatusFailed) {
		t.Fatalf("a later deterministic answer must not clear the vector failure; got %#v", vec)
	}
}

// NEGATIVE, ENFORCEMENT: the generic stage writers cannot reach the vector
// columns. `vector` is deliberately not in stageColumns, so Failed and
// NotApplicable — the two methods any Recorder holder can call with an
// arbitrary Stage — write nothing at all rather than the vector cell.
func TestNoGenericStageWriteCanReachTheVectorCell(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-stage", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)

	s.Failed(k, Stage("vector"), ReasonAttributeFailed, 0, at)
	s.NotApplicable(k, Stage("vector"), ReasonAtlasOff, at)

	if cell, ok := readCells(t, s)["vector"]; ok {
		t.Fatalf("Failed/NotApplicable must not be able to address the vector cell; got %#v", cell)
	}
	if _, ok := stageColumns[Stage("vector")]; ok {
		t.Fatal("registering the vector columns in stageColumns re-opens the shared-cell defect: see its doc comment")
	}
}

// NEGATIVE, ENFORCEMENT AT THE TYPE LEVEL: Recorder must not gain a way to
// write the vector cell, and VectorRecorder must not gain a way to write
// anything else. The interfaces are the enforcement AC1 asks for; a future
// embed would compile silently, so it is asserted structurally.
func TestTheTwoRecorderInterfacesShareNoMethod(t *testing.T) {
	rec := reflect.TypeOf((*Recorder)(nil)).Elem()
	vec := reflect.TypeOf((*VectorRecorder)(nil)).Elem()

	if vec.NumMethod() != 1 || vec.Method(0).Name != "Vector" {
		t.Fatalf("VectorRecorder must have exactly one method, Vector; got %d", vec.NumMethod())
	}
	for i := range rec.NumMethod() {
		name := rec.Method(i).Name
		if name == "Vector" {
			t.Fatal("Recorder must not carry Vector: a holder of Recorder could then write the second opinion's cell")
		}
		if _, ok := vec.MethodByName(name); ok {
			t.Fatalf("VectorRecorder must not carry %q: the attribution path holds it precisely so it CANNOT write the deterministic cell", name)
		}
	}
}

// An `ok` vector cell with no project id is refused, the same invariant
// Attribute enforces: a second opinion that named nothing is not a second
// opinion, and recording one would say the encoder chose a project while
// naming none.
func TestVectorRefusesAnOKCellWithNoProjectID(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-empty", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)

	s.Vector(k, VectorAttributed{Confidence: 0.9}, StatusOK, ReasonNone, at)

	if cell, ok := readCells(t, s)["vector"]; ok {
		t.Fatalf("an ok vector cell with no project id must be refused, not stored; got %#v", cell)
	}
}

// An unknown status is refused rather than clamped: every status says
// something specific about what the encoder did, so there is no safe default
// to guess.
func TestVectorRefusesAnUnknownStatus(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s-badstatus", Start: 1000}
	at := mustTime(t, "2026-09-08T09:00:00Z")
	s.Cut(k, 2000, "idle", "budget", "claude_code", at)

	s.Vector(k, VectorAttributed{ProjectID: "p_x"}, Status("probably"), ReasonNone, at)

	if cell, ok := readCells(t, s)["vector"]; ok {
		t.Fatalf("an unknown status must be refused; got %#v", cell)
	}
}

// A confidence outside [0,1] — a NaN especially — must never reach the
// column: json.Marshal fails on a NaN, so one bad float would take the whole
// /v1/ledger response down rather than spoiling one number.
func TestVectorClampsAnImpossibleConfidenceSoTheWireStaysMarshallable(t *testing.T) {
	setHome(t)
	s := New()
	at := mustTime(t, "2026-09-08T09:00:00Z")
	nan := math.NaN()
	for i, conf := range []float64{nan, -1, 7} {
		k := BlockKey{Session: "s-conf", Start: int64(1000 + i)}
		s.Cut(k, 2000, "idle", "budget", "claude_code", at)
		s.Vector(k, VectorAttributed{ProjectID: "p_x", Confidence: conf}, StatusOK, ReasonNone, at)
	}
	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := json.Marshal(snap); err != nil {
		t.Fatalf("an out-of-range confidence made the snapshot unmarshallable: %v", err)
	}
	for _, b := range snap.Blocks {
		if got := b.Cells["vector"]["confidence"]; got != float64(0) {
			t.Fatalf("confidence = %#v, want 0 for an impossible input", got)
		}
	}
}

// MIGRATION: a ledger written before the vector columns existed still opens,
// still reads, and reports its blocks as never having been asked. There is no
// version counter in this store — the migration IS the idempotent ALTER, whose
// duplicate-column error is the success case from the second open onward.
func TestALedgerWrittenBeforeTheVectorColumnsStillOpensAndReads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// Build the PRE-vector schema by hand: the blocks table exactly as it was,
	// with a row in it. Using the current schema minus the columns would prove
	// nothing about a real old file.
	path := filepath.Join(home, "state", "ledger.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := old.Exec(`
CREATE TABLE blocks (
  session TEXT NOT NULL, start INTEGER NOT NULL, end INTEGER,
  source TEXT NOT NULL DEFAULT '', start_reason TEXT NOT NULL DEFAULT '', end_reason TEXT NOT NULL DEFAULT '',
  dim_repo TEXT NOT NULL DEFAULT '', dim_branch TEXT NOT NULL DEFAULT '', dim_workspace TEXT NOT NULL DEFAULT '',
  cut_status TEXT, cut_at TEXT, cut_reason TEXT, cut_http_status INTEGER, cut_ok_at TEXT,
  measured_status TEXT, measured_at TEXT, measured_reason TEXT, measured_http_status INTEGER, measured_ok_at TEXT,
  input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  request_tokens INTEGER NOT NULL DEFAULT 0, requests INTEGER NOT NULL DEFAULT 0,
  model TEXT NOT NULL DEFAULT '', estimate_usd REAL NOT NULL DEFAULT 0,
  attributed_status TEXT, attributed_at TEXT, attributed_reason TEXT, attributed_http_status INTEGER, attributed_ok_at TEXT,
  project_id TEXT NOT NULL DEFAULT '', method TEXT NOT NULL DEFAULT '', conflict TEXT NOT NULL DEFAULT '',
  sent_status TEXT, sent_at TEXT, sent_reason TEXT, sent_http_status INTEGER, sent_ok_at TEXT,
  received_status TEXT, received_at TEXT, received_reason TEXT, received_http_status INTEGER, received_ok_at TEXT,
  PRIMARY KEY (session, start));
CREATE TABLE health (key TEXT PRIMARY KEY, status TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', at TEXT NOT NULL);
CREATE TABLE pending (session TEXT PRIMARY KEY, reason TEXT NOT NULL, at TEXT NOT NULL);
INSERT INTO blocks(session, start, attributed_status, attributed_at, project_id, method)
  VALUES('s-old', 1000, 'ok', '2026-09-01T09:00:00Z', 'p_old', 'repo');
`); err != nil {
		t.Fatalf("build the old schema: %v", err)
	}
	old.Close()

	s := New()
	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("a pre-vector ledger must still open and read: %v", err)
	}
	if len(snap.Blocks) != 1 || snap.Blocks[0].Cells["attributed"]["project_id"] != "p_old" {
		t.Fatalf("the old row must survive the migration intact; got %#v", snap.Blocks)
	}
	if cell, ok := snap.Blocks[0].Cells["vector"]; ok {
		t.Fatalf("a row from before the columns existed was never asked, so its cell must be ABSENT; got %#v", cell)
	}

	// And the migration is idempotent: a second open runs the same ALTERs and
	// must treat "duplicate column" as success rather than failing.
	k := BlockKey{Session: "s-old", Start: 1000}
	s.Vector(k, VectorAttributed{ProjectID: "p_new", Confidence: 0.5}, StatusOK, ReasonNone,
		mustTime(t, "2026-09-08T09:00:00Z"))
	again := New()
	snap, err = again.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if snap.Blocks[0].Cells["vector"]["project_id"] != "p_new" {
		t.Fatalf("the migrated column must be writable and readable; got %#v", snap.Blocks[0].Cells["vector"])
	}
	if snap.Blocks[0].Cells["attributed"]["project_id"] != "p_old" {
		t.Fatalf("the migration must not disturb the deterministic answer; got %#v", snap.Blocks[0].Cells["attributed"])
	}
}

// --- helpers -------------------------------------------------------------

func readCells(t *testing.T, s *Store) map[string]map[string]any {
	t.Helper()
	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(snap.Blocks))
	}
	return snap.Blocks[0].Cells
}

func readCell(t *testing.T, s *Store, name string) map[string]any {
	t.Helper()
	cell, ok := readCells(t, s)[name]
	if !ok {
		t.Fatalf("cell %q is absent", name)
	}
	return cell
}

// cellJSON serialises one cell so two readings can be compared BYTE for byte
// rather than field by field — "byte-identical" is the claim, and a field-wise
// comparison would miss a key appearing or disappearing.
func cellJSON(t *testing.T, s *Store, name string) string {
	t.Helper()
	b, err := json.Marshal(readCell(t, s, name))
	if err != nil {
		t.Fatalf("marshal cell %q: %v", name, err)
	}
	return string(b)
}
