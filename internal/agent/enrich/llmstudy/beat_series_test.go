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
	bs, ok = AppendBeat(bs, "The work has moved to the AR ageing provision policy.")
	if !ok || !bs[len(bs)-1].ChangedSubject {
		t.Errorf("a new subject must be stored and marked: %v", bs)
	}
	if got := bs[len(bs)-1].Ordinal; got != 2 {
		t.Errorf("ordinals must be contiguous over STORED beats, got %d", got)
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
