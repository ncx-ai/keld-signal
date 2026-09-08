package daemon

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// THE TWO ATTRIBUTION PASSES, AND THE CELL THEY USED TO SHARE.
//
// Switching vectorised attribution on adds a SECOND OPINION to a block and
// never changes the deterministic one. If the encoder cannot run, the second
// opinion is lost and nothing else is. Every test below is named after that
// claim or after one of the ways it was previously violated.

// newTestV3 is a v3 with a real ledger under an isolated KELD_HOME and nothing
// else — no projects store, no Atlas — because every assertion here is about
// which ledger cell was written, and reading through v3.ledgerReader() would
// serve the RECOMPUTED deterministic cell (v3blocks.go's liveAttribution)
// rather than the stored one. What is stored is the thing at issue.
func newTestV3(t *testing.T) *v3 {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	return &v3{ledger: ledger.New(), atlasOn: true}
}

func storedCells(t *testing.T, v *v3) map[string]map[string]any {
	t.Helper()
	snap, err := v.ledger.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(snap.Blocks))
	}
	return snap.Blocks[0].Cells
}

const (
	testSession = "sess-vec"
	testStart   = int64(1788543000)
)

// seedDeterministicAnswer puts the block and the deterministic pass's answer
// in place — a repository rule matching a declared project, which is the
// state 25 of the 44 damaged rows were in.
func seedDeterministicAnswer(t *testing.T, v *v3) ledger.BlockKey {
	t.Helper()
	k := ledger.BlockKey{Session: testSession, Start: testStart}
	at := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	v.ledger.Cut(k, testStart+1200, "idle", "budget", "claude_code", at)
	v.ledger.Attribute(k, ledger.Attributed{
		ProjectID: "p_keld_signal",
		Method:    ledger.MethodRepo,
	}, ledger.ReasonNone, at)
	return k
}

// ⚠️ **THE NEGATIVE THAT WOULD HAVE SAVED 44 ROWS.** A vectorised job that
// fails every attempt and quarantines must leave the deterministic project id
// exactly where it was — the row still names its project.
//
// On the code this replaces it did the opposite: the quarantine hook called
// ledger.Failed(k, StageAttributed, ReasonAttributeFailed), the same cell the
// deterministic pass had already written, so the row came out reading
// `failed / attribute_failed` with no project at all. Measured on one machine:
// 44 rows in that state, 25 of them on a repository a declared rule matches
// perfectly, all of them caused by an encoder that could not get memory (55
// "encoder silent for 60s, child killed" lines in the same window). A pass
// that ships OFF by default deleted the answer of the pass that ships on.
func TestQuarantinedVectorJobLeavesTheDeterministicProjectIntact(t *testing.T) {
	v := newTestV3(t)
	seedDeterministicAnswer(t, v)

	before, err := json.Marshal(storedCells(t, v)["attributed"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// The real hook, fired the real way: attrib.Attributor's OnQuarantine →
	// noteAttributionQuarantine → the registered handler.
	setAttribQuarantineHandler(v.vectorLedger().recordQuarantine)
	t.Cleanup(func() { setAttribQuarantineHandler(nil) })
	noteAttributionQuarantine(testSession, float64(testStart))

	cells := storedCells(t, v)
	after, err := json.Marshal(cells["attributed"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("the vector pass quarantining changed the DETERMINISTIC cell:\n before %s\n  after %s", before, after)
	}
	if cells["attributed"]["project_id"] != "p_keld_signal" {
		t.Fatalf("the row must still name its project; attributed = %#v", cells["attributed"])
	}
	// And the failure is not lost — it is recorded where it belongs.
	vec := cells["vector"]
	if vec == nil || vec["status"] != string(ledger.StatusFailed) ||
		vec["reason"] != string(ledger.ReasonAttributeFailed) {
		t.Fatalf("the quarantine must be recorded on the VECTOR cell; got %#v", vec)
	}
}

// The vector pass agreed with the rule: both cells are set and both are
// readable, and neither was written by the other's writer.
func TestVectorAgreeingWithTheRuleLeavesBothAnswersReadable(t *testing.T) {
	v := newTestV3(t)
	seedDeterministicAnswer(t, v)

	v.vectorLedger().recordOutcome(attrib.Outcome{
		SessionID: testSession, Start: float64(testStart),
		Status: enrich.ProjectsAttributed, ProjectID: "p_keld_signal", Confidence: 0.88,
	})

	cells := storedCells(t, v)
	if cells["attributed"]["project_id"] != "p_keld_signal" ||
		cells["attributed"]["method"] != string(ledger.MethodRepo) {
		t.Fatalf("deterministic cell = %#v", cells["attributed"])
	}
	if cells["vector"]["status"] != string(ledger.StatusOK) ||
		cells["vector"]["project_id"] != "p_keld_signal" ||
		cells["vector"]["confidence"] != 0.88 {
		t.Fatalf("vector cell = %#v", cells["vector"])
	}
}

// The vector pass DISAGREED: both answers are stored, neither overwrites the
// other, and nothing here resolves the disagreement. Choosing a winner needs
// data from a machine running both passes, which will not exist until this
// ships — so representing the disagreement is the whole job.
func TestVectorDisagreeingWithTheRuleStoresBothAndResolvesNeither(t *testing.T) {
	v := newTestV3(t)
	seedDeterministicAnswer(t, v)

	v.vectorLedger().recordOutcome(attrib.Outcome{
		SessionID: testSession, Start: float64(testStart),
		Status: enrich.ProjectsAttributed, ProjectID: "p_something_else", Confidence: 0.55,
	})

	cells := storedCells(t, v)
	if cells["attributed"]["project_id"] != "p_keld_signal" {
		t.Fatalf("the deterministic answer must survive a disagreement; got %#v", cells["attributed"])
	}
	if cells["vector"]["project_id"] != "p_something_else" {
		t.Fatalf("the vector answer must survive a disagreement; got %#v", cells["vector"])
	}
	if cells["attributed"]["project_id"] == cells["vector"]["project_id"] {
		t.Fatal("this test is meant to exercise a genuine disagreement")
	}
}

// Each answer the sidecar can give states what it means, and the differences
// are the value of the cell: a held wait is `pending`, structurally-nothing-to-
// do is `n/a`, and only a job given up on is `failed`.
func TestEachVectorOutcomeStatesWhatTheEncoderActuallyDid(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		wantStatus ledger.Status
		wantReason ledger.Reason
	}{
		{"attributed", enrich.ProjectsAttributed, ledger.StatusOK, ledger.ReasonNone},
		{"warming", enrich.ProjectsPending, ledger.StatusPending, ledger.ReasonNone},
		{"weights still downloading", enrich.ProjectsDegradedWeights, ledger.StatusPending, ledger.ReasonWeightsUnavailable},
		{"nothing declared to match", enrich.ProjectsSkippedNoProjects, ledger.StatusNA, ledger.ReasonNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestV3(t)
			seedDeterministicAnswer(t, v)
			v.vectorLedger().recordOutcome(attrib.Outcome{
				SessionID: testSession, Start: float64(testStart),
				Status: tc.status, ProjectID: "p_keld_signal", Confidence: 0.7,
			})
			cell := storedCells(t, v)["vector"]
			if cell == nil {
				t.Fatalf("status %q must be stated, not swallowed", tc.status)
			}
			if cell["status"] != string(tc.wantStatus) {
				t.Fatalf("status = %v, want %v", cell["status"], tc.wantStatus)
			}
			gotReason, _ := cell["reason"].(string)
			if gotReason != string(tc.wantReason) {
				t.Fatalf("reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

// NEGATIVE: `skipped:disabled` means the sidecar's attribution is switched
// off, so nothing was asked of the encoder — and never-asked is exactly what
// an ABSENT cell means. Writing a status here would put a state on the wire
// for a machine whose vector pass did nothing.
//
// An unrecognised status is likewise not guessed at: the sidecar is frozen and
// shipped separately, so version skew is real, and inventing a cell state out
// of a string this side does not understand is the confident-answer-over-no-
// evidence failure the whole ledger exists to prevent.
func TestNothingIsRecordedWhenTheVectorPassWasNeverActuallyAsked(t *testing.T) {
	for _, status := range []string{
		enrich.ProjectsSkippedDisabled,
		"attributed:probably", // a status from a sidecar this side does not know
		"",
	} {
		v := newTestV3(t)
		seedDeterministicAnswer(t, v)
		v.vectorLedger().recordOutcome(attrib.Outcome{
			SessionID: testSession, Start: float64(testStart), Status: status,
		})
		if cell, ok := storedCells(t, v)["vector"]; ok {
			t.Fatalf("status %q must leave the vector cell ABSENT; got %#v", status, cell)
		}
	}
}

// ⚠️ **NEGATIVE, ON THE WIRE: with the toggle off the vector field is ABSENT,
// not failed.** This is the default population — attribution ships off — and
// the claim is that nothing changes for them: no new key, no null, no zero
// confidence, no new state. Asserted on the marshalled JSON, because six
// columns that are absent in Go and present as nulls in JSON would break it in
// the one place a consumer would notice.
func TestWithTheToggleOffTheVectorFieldIsAbsentFromTheWire(t *testing.T) {
	v := newTestV3(t)
	seedDeterministicAnswer(t, v)

	// With the toggle off, startAttributor returns nil — nothing is ever wired
	// to either hook, so nothing can write the cell. Pinned rather than
	// assumed, since the whole claim rests on it.
	t.Setenv(attrib.EnvEnabled, "0")
	if hook := startAttributor(t.Context(), nil, nil, "", nil, "", nil, false, nil, nil); hook != nil {
		t.Fatal("with attribution off there must be no attributor and no hook")
	}
	// And even if a handler is somehow still registered from a previous run,
	// nothing calls it: fire nothing, assert nothing appears.
	setAttribQuarantineHandler(nil)
	setAttribOutcomeHandler(nil)

	snap, err := v.ledger.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded struct {
		Blocks []struct {
			Cells map[string]json.RawMessage `json:"cells"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw, ok := decoded.Blocks[0].Cells["vector"]; ok {
		t.Fatalf("the marshalled cells object carries a vector key on a toggle-off machine: %s", raw)
	}
	if strings.Contains(string(b), "vector") {
		t.Fatalf("the word `vector` reached the wire of a toggle-off machine:\n%s", b)
	}
}

// AC5: turning the toggle ON and then OFF again leaves the DETERMINISTIC
// answer exactly as it was.
//
// Note what is deliberately NOT asserted: the vector cell is not erased when
// the toggle goes off. A cell that was written records that the pass was asked
// and what it said, and deleting it would recreate the never-asked /
// asked-and-answered confusion this whole split exists to remove — as well as
// making the toggle destructive. The weaker, right claim is the one here:
// turning the toggle off cannot change any deterministic answer.
func TestTogglingTheVectorPassOnThenOffLeavesTheDeterministicAnswerUnchanged(t *testing.T) {
	v := newTestV3(t)
	seedDeterministicAnswer(t, v)
	before, err := json.Marshal(storedCells(t, v)["attributed"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// ON: a full life of a vectorised job — warming, weights missing, an
	// answer that disagrees, and finally a quarantine.
	vl := v.vectorLedger()
	for _, o := range []attrib.Outcome{
		{Status: enrich.ProjectsPending},
		{Status: enrich.ProjectsDegradedWeights},
		{Status: enrich.ProjectsAttributed, ProjectID: "p_other", Confidence: 0.5},
	} {
		o.SessionID, o.Start = testSession, float64(testStart)
		vl.recordOutcome(o)
	}
	vl.recordQuarantine(testSession, float64(testStart))

	// OFF: nothing more is written.
	setAttribQuarantineHandler(nil)
	setAttribOutcomeHandler(nil)
	noteAttributionQuarantine(testSession, float64(testStart))
	noteAttributionOutcome(attrib.Outcome{
		SessionID: testSession, Start: float64(testStart), Status: enrich.ProjectsAttributed, ProjectID: "p_nope",
	})

	cells := storedCells(t, v)
	after, err := json.Marshal(cells["attributed"])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("the deterministic cell moved across a toggle cycle:\n before %s\n  after %s", before, after)
	}
	// The unwired hooks wrote nothing: the vector cell still reads the last
	// thing the pass actually said while it was on.
	if cells["vector"]["status"] != string(ledger.StatusFailed) {
		t.Fatalf("an unwired hook wrote to the vector cell; got %#v", cells["vector"])
	}
	if cells["vector"]["project_id"] != "p_other" {
		t.Fatalf("the id from the last real answer must survive; got %#v", cells["vector"])
	}
}

// ⚠️ **NEGATIVE, ENFORCEMENT: the coupling must be IMPOSSIBLE, not merely
// absent.** vectorLedger holds a ledger.VectorRecorder — an interface with one
// method that writes one cell — so no code reachable through it can call
// Attribute, Failed or NotApplicable. There is no such method on the value it
// holds, which is why this is a type property rather than a discipline.
//
// Asserted by reflection because the failure mode is a future edit that
// COMPILES: giving vectorLedger a *ledger.Store field, or embedding Recorder
// in VectorRecorder, would restore the reach that cost the 44 rows and nothing
// else would notice.
func TestTheVectorPathHoldsNothingThatCouldWriteTheDeterministicCell(t *testing.T) {
	vl := reflect.TypeOf(vectorLedger{})
	if vl.NumField() != 1 {
		t.Fatalf("vectorLedger must hold exactly one thing, the narrowed recorder; got %d fields", vl.NumField())
	}
	f := vl.Field(0)
	if f.Type != reflect.TypeOf((*ledger.VectorRecorder)(nil)).Elem() {
		t.Fatalf("vectorLedger's field must be ledger.VectorRecorder, not %s — anything wider can reach the deterministic cell", f.Type)
	}
	// The narrowed interface exposes exactly one method, so there is nothing
	// on it to call by mistake.
	if n := f.Type.NumMethod(); n != 1 {
		t.Fatalf("the narrowed recorder must expose one method; got %d", n)
	}
}

// ⚠️ **NEGATIVE, CALL SITES: no file on the attribution path may name the
// deterministic cell, and no file off it may name the vector writer.** The
// type narrowing above closes the door inside vectorLedger; this closes the
// one a future edit could open beside it, by adding a second helper on *v3 (a
// method on *v3 has `v.ledger` in scope, and `v.ledger.Failed(k,
// ledger.StageAttributed, …)` is precisely the line that cost 44 rows).
func TestOnlyOneFileWritesEachAttributionCell(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return strings.HasSuffix(fi.Name(), ".go") && !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse the daemon package: %v", err)
	}

	deterministic := map[string]bool{} // files naming StageAttributed
	vectorWriters := map[string]bool{} // files calling .Vector( or recordOutcome/recordQuarantine
	for _, pkg := range pkgs {
		for name, f := range pkg.Files {
			base := filepath.Base(name)
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				ident, ok := sel.X.(*ast.Ident)
				if ok && ident.Name == "ledger" && sel.Sel.Name == "StageAttributed" {
					deterministic[base] = true
				}
				switch sel.Sel.Name {
				case "Vector", "recordOutcome", "recordQuarantine":
					vectorWriters[base] = true
				}
				return true
			})
		}
	}

	// The deterministic cell is named by the file that owns it, and by the
	// read-side wrapper that recomputes it for display. Nothing else — and
	// nothing on the attribution path.
	allowedDeterministic := map[string]bool{"v3blocks.go": true}
	for file := range deterministic {
		if !allowedDeterministic[file] {
			t.Fatalf("%s names ledger.StageAttributed; the deterministic cell has exactly one writer (v3blocks.go). "+
				"If this file is on the attribution path, it is the 44-row defect returning", file)
		}
	}
	if !deterministic["v3blocks.go"] {
		t.Fatal("v3blocks.go no longer names ledger.StageAttributed — this test has stopped checking anything, fix the test")
	}

	// And the vector cell is written from its own file only.
	for file := range vectorWriters {
		if file != "v3attrib.go" && file != "daemon.go" {
			t.Fatalf("%s reaches a vector-cell writer; that cell is written from v3attrib.go and wired in daemon.go, nowhere else", file)
		}
	}
	if !vectorWriters["v3attrib.go"] {
		t.Fatal("v3attrib.go no longer writes the vector cell — this test has stopped checking anything, fix the test")
	}
}
