package llmstudy

import (
	"strings"
	"testing"
)

func beats(n int, changedAt ...int) []Beat {
	ch := map[int]bool{}
	for _, c := range changedAt {
		ch[c] = true
	}
	var out []Beat
	for i := 1; i <= n; i++ {
		out = append(out, Beat{Ordinal: i, Text: "beat " + strings.Repeat("x", i), ChangedSubject: ch[i]})
	}
	return out
}

// Appending marks whether the subject changed — the signal the report samples on, and the one
// the recency work was previously blocked on.
func TestAppendBeatMarksSubjectChange(t *testing.T) {
	var bs []Beat
	bs, ok := AppendBeat(bs, "The work is reconciling the March ledger for Meridian.")
	if !ok || !bs[0].ChangedSubject {
		t.Fatalf("the first beat establishes the subject: ok=%v %v", ok, bs)
	}
	bs, ok = AppendBeat(bs, "Work continues on Meridian's March ledger reconciliation.")
	if ok {
		t.Errorf("a restatement must not be stored: %v", bs)
	}
	bs, ok = AppendBeat(bs, "Applying the Northwind provision under the Larkin blanket policy.")
	if !ok || !bs[len(bs)-1].ChangedSubject {
		t.Errorf("a new subject must be stored and marked: %v", bs)
	}
	if got := bs[len(bs)-1].Ordinal; got != 2 {
		t.Errorf("ordinals must be contiguous over STORED beats, got %d", got)
	}
}

// ChangedSubject carried no information at all: 47 of 47 beats over three real sessions were
// flagged as a change, because the old rule ran the near-duplicate test (beatsRestate at 0.8)
// against beats that were non-duplicates by construction. SelectBeats samples on this flag, so
// selection for the expensive report was choosing on a constant.
//
// The discrimination that matters is between beats that continue a subject and beats that leave
// it — both of which are stored, since neither restates the other. This fixture is the shape the
// real corpus showed: one subject carried across three beats, then a jump.
func TestChangedSubjectDistinguishesContinuationFromAJump(t *testing.T) {
	var bs []Beat
	for _, text := range []string{
		"Working on the CSV export for the overview page, which delivers five files in one ZIP.",
		"The CSV export now ships summary, spend-over-time and breakdowns from the same ZIP endpoint.",
		"The CSV export is finished and pushed, with the confirmation modal explaining the ZIP.",
		"Fixing the ConfirmDialog nesting warning that RemoveMember and remove-key-button both trip.",
	} {
		var ok bool
		bs, ok = AppendBeat(bs, text)
		if !ok {
			t.Fatalf("beat was discarded as a restatement, so the flag cannot be judged: %q", text)
		}
	}
	if !bs[0].ChangedSubject {
		t.Error("the first beat establishes the subject and must be marked")
	}
	for _, i := range []int{1, 2} {
		if bs[i].ChangedSubject {
			t.Errorf("beat %d continues the CSV export subject and must not be marked: %q",
				i+1, bs[i].Text)
		}
	}
	if !bs[3].ChangedSubject {
		t.Errorf("a jump to an unrelated component must be marked: %q", bs[3].Text)
	}
}

// The trap this package documents twice: distinctiveToken admits ANY lowercase word of 7+
// characters, which is why SynopsisLag can match on "remains"/"whether" and why T12's beat
// subject terms came out as gerunds and adverbs. A novelty count built on that vocabulary would
// call a reworded beat a new subject. Two beats naming the same things in different English must
// not be a subject change.
func TestChangedSubjectIgnoresOrdinaryEnglish(t *testing.T) {
	first := "Working on the CSV export for the overview page, which delivers five files in one ZIP."
	second := "The CSV export endpoint is progressing steadily, although the ZIP assembly " +
		"remains unfinished and consequently the overview download is currently unavailable."
	if terms := beatSubjectTerms(second); len(terms) == 0 {
		t.Fatalf("fixture names nothing, so it cannot exercise the rule: %v", terms)
	}
	bs, _ := AppendBeat(nil, first)
	bs, ok := AppendBeat(bs, second)
	if !ok {
		t.Fatalf("the second beat was discarded, so the flag cannot be judged: %v", bs)
	}
	if bs[1].ChangedSubject {
		t.Errorf("long ordinary English words must not read as a new subject: %q", bs[1].Text)
	}
	for _, bad := range []string{"progressing", "steadily", "although", "remains",
		"consequently", "currently", "unavailable", "unfinished"} {
		if beatSubjectTerms(second)[bad] {
			t.Errorf("%q is ordinary English and must not be a subject term", bad)
		}
	}
}

// A beat that names nothing concrete cannot be judged, and is reported unchanged rather than
// guessed at — the same "continuity is the default" rule SubjectShifted follows. This is a real
// limit, not a nicety: it is why the flag is a lower bound on subject changes (17 of 47 beats in
// the measured corpus abstained this way), and it is pinned so the abstention stays visible
// rather than being mistaken for a verdict.
func TestChangedSubjectAbstainsWhenTheBeatNamesNothing(t *testing.T) {
	bs, _ := AppendBeat(nil, "Reconciling the March ledger for Meridian against the statement.")
	abstaining := "Designing a schema that allows free-form prose while preventing " +
		"rubberstamping, resting on measurable evidence rather than judgement."
	if n := len(beatSubjectTerms(abstaining)); n >= minBeatSubjectTerms {
		t.Fatalf("fixture names %d subjects, so it does not exercise abstention", n)
	}
	bs, ok := AppendBeat(bs, abstaining)
	if !ok {
		t.Fatalf("the beat was discarded, so the flag cannot be judged: %v", bs)
	}
	if bs[1].ChangedSubject {
		t.Errorf("a beat naming nothing must not be reported as a subject change: %q", bs[1].Text)
	}
}

// A return to a subject the session has already covered is history repeating, not a change —
// the semantics the old doc claimed and the old code could not deliver, since it compared only
// against near-duplicates.
func TestChangedSubjectTreatsAReturnAsUnchanged(t *testing.T) {
	var bs []Beat
	for _, text := range []string{
		"Fixing the budget saliency in members-by-team.tsx so the amount reads on mint rows.",
		"Working on the CSV export for the overview page, which delivers five files in one ZIP.",
		"Back on members-by-team.tsx, where the budget saliency still clashes with the pill.",
	} {
		var ok bool
		bs, ok = AppendBeat(bs, text)
		if !ok {
			t.Fatalf("beat discarded, cannot judge the flag: %q", text)
		}
	}
	if !bs[1].ChangedSubject {
		t.Errorf("the CSV export is a new subject: %q", bs[1].Text)
	}
	if bs[2].ChangedSubject {
		t.Errorf("returning to a covered subject is not a change: %q", bs[2].Text)
	}
}

// AppendBeat is the second gate on the shape invariant. GenerateBeat validates and re-requests,
// but a caller assembling beat text some other way must not be able to get a fragment into the
// series either — that is how the defect reached 46 of 47 beats without any test noticing.
func TestAppendBeatNeverStoresAFragment(t *testing.T) {
	var bs []Beat
	bs, ok := AppendBeat(bs, "Closing March for Meridian, focusing on the bank reconciliation. "+
		"The outstanding cheques to Halberd Supply still need")
	if !ok {
		t.Fatal("the complete first sentence must still be stored")
	}
	if got := bs[0].Text; got != "Closing March for Meridian, focusing on the bank reconciliation." {
		t.Errorf("the incomplete tail was stored: %q", got)
	}
	if _, ok := AppendBeat(bs, "no complete sentence here at all"); ok {
		t.Error("a beat with no complete sentence must not be stored")
	}
	long := strings.Repeat("padding words that never terminate ", 40)
	if _, ok := AppendBeat(bs, long); ok {
		t.Error("an over-cap beat with no sentence boundary must not be stored")
	}
}

func TestSelectBeatsKeepsFirstAndLatest(t *testing.T) {
	got := SelectBeats(beats(30), 6)
	if got[0].Ordinal != 1 {
		t.Errorf("the first beat was dropped: %v", got[0])
	}
	if got[len(got)-1].Ordinal != 30 {
		t.Errorf("the latest beat was dropped: %v", got[len(got)-1])
	}
	if len(got) > 6 {
		t.Errorf("cap exceeded: %d", len(got))
	}
}

func TestSelectBeatsPrefersSubjectChanges(t *testing.T) {
	got := SelectBeats(beats(30, 9, 18), 6)
	var have []int
	for _, b := range got {
		have = append(have, b.Ordinal)
	}
	for _, want := range []int{9, 18} {
		var found bool
		for _, h := range have {
			if h == want {
				found = true
			}
		}
		if !found {
			t.Errorf("subject change at %d missing from %v", want, have)
		}
	}
}

func TestSelectBeatsFallsBackToSpacing(t *testing.T) {
	got := SelectBeats(beats(30), 6)
	if len(got) != 6 {
		t.Fatalf("want the cap filled by spacing, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].Ordinal <= got[i-1].Ordinal {
			t.Fatalf("must be chronological and unique: %v", got)
		}
	}
}

func TestRenderBeatsMarksSubjectChanges(t *testing.T) {
	out := RenderBeats([]Beat{
		{Ordinal: 1, Text: "started on the ledger", ChangedSubject: true},
		{Ordinal: 4, Text: "steady progress", ChangedSubject: false},
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("one line per beat, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "subject") {
		t.Errorf("a subject change must be marked: %q", lines[0])
	}
	if strings.Contains(lines[1], "subject") {
		t.Errorf("steady progress must not be marked: %q", lines[1])
	}
}

func TestRenderEmptyBeatsIsEmpty(t *testing.T) {
	if RenderBeats(nil) != "" {
		t.Error("an empty series must render nothing, not a header")
	}
}

// Two consecutive beats over unchanged work reword the same verb — "reconciling" then
// "reconciliation" — because beats regenerate from an overlapping window every few turns.
// insightsMatch, measured on longer "insight" text, scores this pair at 0.667 (4 of 6 shared
// words), below the shared 0.8 threshold: plural-only stemming doesn't fold a gerund onto its
// nominalisation. beatsRestate must, via beatStem's wider stemming, or a beat series fills up
// with reworded duplicates of the same subject.
func TestBeatsRestateFoldsGerundAndNominalisation(t *testing.T) {
	a := "The work is reconciling the March ledger for Meridian."
	b := "Work continues on Meridian's March ledger reconciliation."
	if !beatsRestate(a, b) {
		t.Errorf("a gerund/nominalisation restatement must be caught: %q / %q", a, b)
	}
}

// A stemmer wide enough to catch a gerund/nominalisation pair must not go so far that it
// collapses genuinely different subjects — a dropped beat is lost history, which is worse
// than keeping a near-duplicate.
func TestBeatsRestateKeepsDistinctSubjectsApart(t *testing.T) {
	a := "The work is reconciling the March ledger for Meridian."
	b := "The work has moved to the AR ageing provision policy."
	if beatsRestate(a, b) {
		t.Errorf("distinct subjects must not be treated as a restatement: %q / %q", a, b)
	}
}

// The gerund/nominalisation of an -ate verb ("migrating"/"migration") is the regular, far more
// common counterpart to "reconciling"/"reconciliation"'s irregular case — routine in both
// engineering and business prose describing recurring work. beatStem must fold both onto the
// same root, not just the one instance the brief's fixture happened to use, or a beat series
// about any of these ten verbs still fills with near-duplicates. communicate/negotiate are
// -icate/-iate verbs, where the "ic"/"i" belongs to the stem rather than the suffix, so they
// exercise the longer "icating"/"iating" branches rather than the plain "ating" one the other
// eight take.
func TestBeatsRestateFoldsAteVerbFamily(t *testing.T) {
	pairs := []struct{ gerund, nominal string }{
		{"migrating", "migration"},
		{"creating", "creation"},
		{"generating", "generation"},
		{"operating", "operation"},
		{"negotiating", "negotiation"},
		{"communicating", "communication"},
		{"evaluating", "evaluation"},
		{"terminating", "termination"},
		{"calculating", "calculation"},
		{"allocating", "allocation"},
	}
	for _, p := range pairs {
		if got := beatStem(p.gerund); got != beatStem(p.nominal) {
			t.Errorf("%q -> %q, %q -> %q: must converge to the same stem",
				p.gerund, got, p.nominal, beatStem(p.nominal))
		}
	}
}

// Two beats can share an -ate verb family and still be about different subjects. A stemmer
// aggressive enough to fold gerund onto nominalisation (see TestBeatsRestateFoldsAteVerbFamily)
// must not go on to fold THESE together on the strength of the shared verb alone — that would
// be the over-stemming false positive the coordinator called the expensive direction: a dropped
// beat is lost history nothing can recover.
func TestBeatsRestateKeepsSharedVerbFamilySubjectsApart(t *testing.T) {
	a := "The team is migrating the reporting pipeline to the new data warehouse."
	b := "The team is migrating customer accounts to the new billing system."
	if beatsRestate(a, b) {
		t.Errorf("sharing a verb family must not be enough to collapse distinct subjects: %q / %q", a, b)
	}
	c := "We are evaluating the new vendor contract terms this quarter."
	d := "We are evaluating candidate resumes for the open engineering role."
	if beatsRestate(c, d) {
		t.Errorf("sharing a verb family must not be enough to collapse distinct subjects: %q / %q", c, d)
	}
}

// SelectBeats is exported with a caller-supplied max and documents it as a hard cap, but the
// pick set used to be seeded with the first and last index before any cap check — so max=1
// still returned 2. With only one slot, the invariant of keeping both the first beat and the
// latest can't both hold; the latest is kept, since a single-beat summary is "where things
// stand now", not the origin.
func TestSelectBeatsCapHoldsAtSmallMax(t *testing.T) {
	all := beats(30)
	if got := SelectBeats(all, 1); len(got) != 1 {
		t.Fatalf("max=1 must return exactly 1 beat, got %d: %v", len(got), got)
	} else if got[0].Ordinal != 30 {
		t.Errorf("max=1 must keep the latest beat, got ordinal %d", got[0].Ordinal)
	}
	got := SelectBeats(all, 2)
	if len(got) != 2 {
		t.Fatalf("max=2 must return exactly 2 beats, got %d: %v", len(got), got)
	}
	if got[0].Ordinal != 1 || got[1].Ordinal != 30 {
		t.Errorf("max=2 must be exactly the first and latest, got %v", got)
	}
}
