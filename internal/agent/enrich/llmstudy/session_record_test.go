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

// blobToken is a base64url/JWT-shaped run of the kind a real transcript dumps into a turn
// (a pasted token, a data-URI fragment, a long trace id). subjectTokens preserves
// [A-Za-z0-9._/-] deliberately — that is what keeps a path in one piece — so the whole
// thing arrives as ONE candidate subject term.
func blobToken() string {
	return "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + strings.Repeat("aB3dE7fG9hJ2kL4mN6pQ8rS0tU", 38)
}

// TestSessionRecordSubjectsBoundEachTermsLength pins maxSubjectTermLen on the record side.
// MaxRecordSubjects caps how many terms Block() joins; before this cap nothing capped how
// long one of them could be. Measured through this exact turn before the fix: a single
// Subjects entry of 1,025 runes and a Block() of 1,102 — one dimension able to exceed the
// whole prompt budget by itself, which the backstop then has to fail the prompt over.
//
// The second half is the over-correction guard: a genuine path-shaped subject (54 runes,
// the longest source path in this package) must still be admitted, or the cap has bought
// safety by deleting exactly the specifics the record exists to hold.
func TestSessionRecordSubjectsBoundEachTermsLength(t *testing.T) {
	const realPath = "internal/agent/enrich/llmstudy/capability_eval_test.go"
	w := Window{Turns: []Turn{
		{RoleUser, "please decode the token " + blobToken() + " and check " + realPath + " for details"},
	}}
	r := SessionRecord{}.Observe(w, Extract(w))
	for _, s := range r.Subjects {
		if n := len([]rune(s)); n > maxSubjectTermLen {
			t.Errorf("subject term is %d runes, over the %d-rune cap: %.40q...", n, maxSubjectTermLen, s)
		}
	}
	if !strings.Contains(strings.Join(r.Subjects, " "), realPath) {
		t.Errorf("a genuine %d-rune path-shaped subject must still be admitted, got %v",
			len([]rune(realPath)), r.Subjects)
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

// weakProperNoun's job is to catch a short capitalised NAME that distinctiveToken's own
// length floor would drop — not any capitalised word. A line-leading capital carries no
// information (it is just how a new line or bullet opens, same as a new sentence), and a
// review of round 1 found exactly that: sentenceInitial's newline-as-whitespace handling
// (right for LLM-authored digest prose, its only other caller) let a line-leading capital
// in raw turn text read as "mid-sentence" and get admitted. Each case here was verified
// against the pre-fix Observe to actually reach Subjects before the fix landed.
func TestSessionRecordSubjectsIgnoreLineLeadingCapitals(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"newline then verb", "Reading file\nFound 3 matches for the query"},
		{"bullet list", "Fix the auth bug\nUpdate the README\nActivity type is wrong"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := Window{Turns: []Turn{{RoleUser, c.text}}}
			r := SessionRecord{}.Observe(w, Extract(w))
			for _, bad := range []string{"Found", "Update"} {
				for _, s := range r.Subjects {
					if s == bad {
						t.Errorf("%q is a line-leading verb, not a name; got subjects %v", bad, r.Subjects)
					}
				}
			}
		})
	}
}

// A capitalised common word mid-sentence, with no adjacent newline at all, is the case a
// pure line-boundary fix cannot touch — this is the length floor's job, not the position
// check's. "Read" here has nothing else marking it a name: no internal caps, no digit, no
// separator, and it is short (4 chars, under weakProperNounMinLen).
func TestSessionRecordSubjectsRejectMidSentenceCommonWord(t *testing.T) {
	w := Window{Turns: []Turn{{RoleUser, "Please check the Read Me file for details"}}}
	r := SessionRecord{}.Observe(w, Extract(w))
	for _, s := range r.Subjects {
		if s == "Read" {
			t.Errorf("%q is an ordinary word capitalised mid-sentence, not a name; got subjects %v", "Read", r.Subjects)
		}
	}
}

// The fix must not overcorrect: a genuine short proper noun — capitalised, mid-turn, no
// other marker, at or above weakProperNounMinLen — must still reach Subjects on a single
// occurrence. This is the exact shape TestSessionRecordAccumulatesAcrossWindows depends on
// ("Larkin"), isolated here so a future change to the position/length rule fails fast and
// specifically, rather than only via the accumulation test's broader assertion.
func TestSessionRecordSubjectsKeepGenuineShortProperNoun(t *testing.T) {
	w := Window{Turns: []Turn{{RoleUser, "now post the Larkin accrual"}}}
	r := SessionRecord{}.Observe(w, Extract(w))
	if !strings.Contains(strings.Join(r.Subjects, " "), "Larkin") {
		t.Errorf("genuine mid-turn proper noun must still be admitted, got %v", r.Subjects)
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
