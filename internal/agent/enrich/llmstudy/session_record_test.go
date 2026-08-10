package llmstudy

import (
	"strings"
	"testing"
)

func TestSessionRecordAccumulatesAcrossWindows(t *testing.T) {
	var r SessionRecord
	w1 := Window{Turns: []Turn{{RoleUser, "reconcile the Meridian ledger"}, {RoleTool, "Read bank-mar.csv"}}}
	w2 := Window{Turns: []Turn{{RoleUser, "now post the Larkin accrual"}, {RoleTool, "Write journals/mar-adj-04.csv"}}}
	r = r.Observe(w1, Extract(w1)).WithProject("meridian")
	r = r.Observe(w2, Extract(w2)).WithProject("meridian")

	if r.Turns != 4 {
		t.Errorf("turns must span the session, got %d", r.Turns)
	}
	if len(r.Projects) != 1 || r.Projects[0] != "meridian" {
		t.Errorf("projects must dedupe, got %v", r.Projects)
	}
	subs := strings.Join(r.Subjects, " ")
	if !strings.Contains(subs, "Meridian") || !strings.Contains(subs, "Larkin") {
		t.Errorf("subjects from BOTH windows must accumulate, got %v", r.Subjects)
	}
}

// A term may only enter by appearing verbatim in the transcript. Plausibility is how a
// fabricated specific would get in.
func TestSessionRecordSubjectsAreVerbatimOnly(t *testing.T) {
	w := Window{Turns: []Turn{{RoleUser, "reconcile the Meridian ledger"}}}
	r := SessionRecord{}.Observe(w, Extract(w))
	for _, s := range r.Subjects {
		if !strings.Contains("reconcile the Meridian ledger", s) {
			t.Errorf("subject %q is not a substring of the source", s)
		}
	}
}

// Bounded, or "minimal" stops being true.
func TestSessionRecordIsBounded(t *testing.T) {
	r := SessionRecord{}
	for i := 0; i < 40; i++ {
		w := Window{Turns: []Turn{{RoleUser, "touching ComponentNumber" + string(rune('A'+i%26)) + " today"}}}
		r = r.Observe(w, Extract(w)).WithProject("proj" + string(rune('a'+i%9)))
	}
	if len(r.Subjects) > MaxRecordSubjects {
		t.Errorf("subjects unbounded: %d", len(r.Subjects))
	}
	if len(r.Projects) > MaxRecordProjects {
		t.Errorf("projects unbounded: %d", len(r.Projects))
	}
}

// Turning points are facts, so a shift is recoverable rather than inferred from prose.
func TestSessionRecordRecordsTurningPoints(t *testing.T) {
	r := SessionRecord{}.
		NoteTurningPoint(2, TriggerFocusShift).
		NoteTurningPoint(3, TriggerVolume).
		NoteTurningPoint(5, TriggerFriction)
	if len(r.TurningPoints) != 2 {
		t.Fatalf("only shift and friction are turning points, got %v", r.TurningPoints)
	}
	if r.TurningPoints[0].Seq != 2 || r.TurningPoints[1].Seq != 5 {
		t.Errorf("wrong points recorded: %v", r.TurningPoints)
	}
}

// An absent field must read as absent. Topics read empty for months because nothing said a
// pass had never run.
func TestSessionRecordReportsWhichFieldsArePopulated(t *testing.T) {
	w := Window{Turns: []Turn{{RoleUser, "reconcile the Meridian ledger"}}}
	r := SessionRecord{}.Observe(w, Extract(w))
	if got := strings.Join(r.Populated(), ","); strings.Contains(got, "focus") {
		t.Errorf("focus must not report as populated before classification: %s", got)
	}
	r = r.WithFocus("finance", "accounting", 0.8)
	if got := strings.Join(r.Populated(), ","); !strings.Contains(got, "focus") {
		t.Errorf("focus must report as populated once set: %s", got)
	}
	if !strings.Contains(r.Block(), "focus:") {
		t.Error("Block must render a populated focus")
	}
	if strings.Contains(SessionRecord{}.Block(), "focus:") {
		t.Error("Block must omit an unpopulated focus rather than showing it empty")
	}
}
