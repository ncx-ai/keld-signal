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
// The second half is the over-correction guard: a genuine path-shaped subject must still be
// admitted, or the cap has bought safety by deleting exactly the specifics the record exists
// to hold. The 54-rune path below is a real one, but ⚠️ it is NOT "the longest source path in
// this package" as this docstring used to claim — the longest tracked .go path is 57 runes
// (internal/agent/enrich/llmstudy/digest_consistency_test.go) and the longest tracked path of
// any kind is 83 (a docs/superpowers/specs design doc). The 83-rune one IS silently dropped
// by maxSubjectTermLen, which is precisely the harm this guard exists to rule out — so the
// guard is real but narrower than it read. See maxSubjectTermLen's doc for why the cap cannot
// simply be raised (measured: at 96 the create-path worst case breaches the window floor).
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

// Observe's frequency map keyed on the UNTRIMMED token, so the same subject appearing once
// before a period and once mid-sentence became two entries. Third site of a bug already fixed
// in distinctiveTerms and RecentSubjects, and the one site the design calls verbatim-verified
// and tells the model to trust — Block() rendered "DigestSchema., DigestSchema" under an
// "authoritative" heading, and the duplicate also consumed one of only MaxRecordSubjects
// slots. Reverting r.freq[trimTermPunct(tok)]++ to r.freq[tok]++ makes this fail.
func TestSessionRecordSubjectsAreNotDuplicatedByPunctuation(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleUser, "The DigestSchema. And again DigestSchema is the DigestSchema."},
	}}
	r := SessionRecord{}.Observe(w, Extract(w))
	seen := map[string]bool{}
	for _, s := range r.Subjects {
		if strings.HasSuffix(s, ".") {
			t.Errorf("subject kept its terminal punctuation: %q in %v", s, r.Subjects)
		}
		if seen[strings.Trim(s, ".-_/")] {
			t.Errorf("the same subject appears twice modulo punctuation: %v", r.Subjects)
		}
		seen[strings.Trim(s, ".-_/")] = true
	}
	if !seen["DigestSchema"] {
		t.Fatalf("the subject itself was lost: %v", r.Subjects)
	}
}

// Populated() and Block() must key off the SAME predicate for counts. They did not: a record
// holding only a project was non-empty by Populated()'s test, and Block() then asserted
// "counts: turns=0 ... corrections=0" under DigestUpdatePromptFrom's
// "SESSION RECORD (measured — authoritative)" heading — the fabricated zero-correction record
// task 5's Critical was raised for, and digestRules tells the model corrections are a measured
// fact its prose must be consistent with, so it inverts anti-rubberstamping. Unreachable via
// today's callers; a trap for the wiring task, and silent when it springs.
func TestBlockOmitsCountsWhenNothingWasCounted(t *testing.T) {
	r := SessionRecord{}.WithProject("keld-signal")
	if got := r.Populated(); len(got) == 0 {
		t.Fatalf("a record with a project must be populated, got %v", got)
	}
	block := r.Block()
	if strings.Contains(block, "counts:") {
		t.Errorf("an uncounted record asserted counts as measured truth:\n%s", block)
	}
	if !strings.Contains(block, "projects: keld-signal") {
		t.Errorf("the field that IS measured was omitted:\n%s", block)
	}
	// And the positive direction: a real record must still state its counts.
	w := Window{Turns: []Turn{{RoleUser, "reconcile the ledger"}}}
	got := SessionRecord{}.Observe(w, Extract(w)).Block()
	if !strings.Contains(got, "counts:") {
		t.Errorf("a counted record must state its counts:\n%s", got)
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

// TestTurningPointsAreCappedAtTheirBound pins MaxRecordTurningPoints, which was unpinned:
// the pre-existing turning-point test only checks WHICH reasons qualify, so deleting the cap
// from NoteTurningPoint broke nothing. Every other list on this record caps itself at
// accumulation time; this one appended forever until fix round 3, and an unbounded
// direction-change history is the realistic shape of exactly the long, friction-heavy
// session the record exists to describe (measured by the review: 60 turning points starved
// the window to 1,313 runes).
//
// Both halves matter: the count is bounded, and what survives is the RECENT history, the
// same recency-over-history preference tailN applies to insights and open items. Confirmed
// by deleting the cap from NoteTurningPoint: "noted 36 turning points, want 12 kept, got 36".
func TestTurningPointsAreCappedAtTheirBound(t *testing.T) {
	r := SessionRecord{}
	const noted = MaxRecordTurningPoints * 3
	for i := 1; i <= noted; i++ {
		r = r.NoteTurningPoint(i, TriggerFocusShift)
	}
	if len(r.TurningPoints) != MaxRecordTurningPoints {
		t.Fatalf("noted %d turning points, want %d kept, got %d",
			noted, MaxRecordTurningPoints, len(r.TurningPoints))
	}
	if got := r.TurningPoints[len(r.TurningPoints)-1].Seq; got != noted {
		t.Errorf("the newest turning point must survive: last seq %d, want %d", got, noted)
	}
	if got := r.TurningPoints[0].Seq; got != noted-MaxRecordTurningPoints+1 {
		t.Errorf("the cap must drop the OLDEST points: first seq %d, want %d",
			got, noted-MaxRecordTurningPoints+1)
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
