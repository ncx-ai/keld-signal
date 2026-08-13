package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scoredProdRound emits the fixture round, lets the caller write verdicts, and scores it.
func scoredProdRound(t *testing.T, r1 string, write func(verdictsDir string, key AnswerKey)) ProdScore {
	t.Helper()
	em, _ := prodFixtureRound(t)
	write(filepath.Join(em.Dir, "verdicts"), em.Key)
	s, err := ScoreProdRound(em.Dir, r1)
	if err != nil {
		t.Fatalf("ScoreProdRound: %v", err)
	}
	return s
}

// writeR1Score writes a minimal r1 score.json holding the one thing the comparison reads.
func writeR1Score(t *testing.T, fails map[string]Count) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "score.json")
	if err := writeJSON(path, Score{Round: "r1", FalsePositives: FalsePositiveScore{FailsByDimension: fails}}); err != nil {
		t.Fatal(err)
	}
	return path
}

// The comparison is the reason the round exists, so it is reported as counts over their own
// denominators — never as a rate, because the two rounds' denominators are different measurements.
func TestTheDimensionComparisonIsPrintedBesideR1WithBothDenominators(t *testing.T) {
	r1 := writeR1Score(t, map[string]Count{
		"faithful":                         {15, 36},
		"not_rubberstamping":               {22, 36},
		"legible_to_a_manager":             {0, 36},
		"recognisable_to_the_practitioner": {18, 36},
		"domain_neutral_specificity":       {0, 36},
	})
	s := scoredProdRound(t, r1, func(dir string, key AnswerKey) {
		for _, e := range key.Entries {
			if e.Kind == KindPlanted {
				continue
			}
			v := Verdict{PacketID: e.PacketID, Reviewer: "A", Dimensions: dims("pass", "user:"),
				Defect: DefectCall{Claimed: false, Class: "none"}}
			d := v.Dimensions["not_rubberstamping"]
			d.Verdict = "fail"
			v.Dimensions["not_rubberstamping"] = d
			writeVerdict(t, dir, v)
		}
	})

	byDim := map[string]DimensionComparison{}
	for _, c := range s.Comparison {
		byDim[c.Dimension] = c
	}
	if len(byDim) != len(Dimensions) {
		t.Fatalf("comparison covers %d dimensions, want %d", len(byDim), len(Dimensions))
	}
	if got := byDim["not_rubberstamping"]; got.R1 != (Count{22, 36}) || got.This.N != got.This.Of || got.This.Of == 0 {
		t.Errorf("not_rubberstamping = this %s, r1 %s", got.This, got.R1)
	}
	if got := byDim["faithful"]; got.R1 != (Count{15, 36}) || got.This.N != 0 {
		t.Errorf("faithful = this %s, r1 %s", got.This, got.R1)
	}
	r := s.Render()
	for _, want := range []string{"22 of 36", "beside round r1", "not the same measurement"} {
		if !strings.Contains(r, want) {
			t.Errorf("the report does not carry %q:\n%s", want, r)
		}
	}
	// A rate would hide the moving denominator, which is the defect that made three earlier rounds
	// of this study unreadable. There must be no percentage in the comparison table.
	table := r[strings.Index(r, "## Every dimension, beside round r1"):]
	table = table[:strings.Index(table, "## The same dimensions")]
	if strings.Contains(table, "%") {
		t.Errorf("the comparison table prints a rate:\n%s", table)
	}
}

// Without r1's score the table is omitted rather than guessed at, and the omission is loud.
func TestTheComparisonIsOmittedRatherThanGuessedAt(t *testing.T) {
	s := scoredProdRound(t, "", func(dir string, key AnswerKey) {})
	if len(s.Comparison) != 0 {
		t.Fatalf("a comparison was produced with no r1 score: %v", s.Comparison)
	}
	if !strings.Contains(s.Render(), "OMITTED") {
		t.Error("the report does not say the comparison is missing")
	}
}

// Both caveats have to be ABOVE the tables. A caveat under a number is a caveat nobody reads.
func TestTheCaveatsArePrintedAboveEveryTable(t *testing.T) {
	s := scoredProdRound(t, "", func(dir string, key AnswerKey) {})
	r := s.Render()
	caveats := strings.Index(r, "Read every table below against these three facts")
	if caveats < 0 {
		t.Fatalf("no caveat block:\n%s", r)
	}
	for _, table := range []string{"## Every dimension, beside round r1", "## Calibration", "## False positives"} {
		if i := strings.Index(r, table); i < caveats {
			t.Errorf("%q is printed above the caveats", table)
		}
	}
	for _, want := range []string{
		"name nothing checkable",                                   // the guard's reach
		"produced NO beat at all",                                  // the absences
		"distinct real conversations",                              // the few-sources ceiling
		"the subject line carries no term occurring in the window", // the lost windows, shown
	} {
		if !strings.Contains(r, want) {
			t.Errorf("the caveats do not carry %q", want)
		}
	}
	// The absences are counted from the run facts, not from the packets, so a round with no
	// verdicts still reports them.
	if s.Facts.Counts.SubjectLadderLosses == 0 || len(s.Facts.Failures) == 0 {
		t.Errorf("run facts lost the absences: %+v", s.Facts)
	}
}

// The judge-versus-heuristic table measured the retired string checks against a reader. That
// comparison has been made and this design deletes the checks; a table of zeros beside a judge's
// verdict would read as agreement, so there must be no table at all.
func TestNoHeuristicTableIsPrinted(t *testing.T) {
	s := scoredProdRound(t, "", func(dir string, key AnswerKey) {
		for _, e := range key.Entries {
			writeVerdict(t, dir, Verdict{PacketID: e.PacketID, Reviewer: "A", Dimensions: dims("pass", "user:"),
				Defect: DefectCall{Claimed: false, Class: "none"}})
		}
	})
	if len(s.Heuristics) != 0 {
		t.Errorf("a heuristic comparison was computed: %+v", s.Heuristics)
	}
	r := s.Render()
	for _, forbidden := range []string{"beat_contradicts_record", "subject_shifted", "Judge versus"} {
		if strings.Contains(r, forbidden) {
			t.Errorf("the report prints the retired heuristic %q", forbidden)
		}
	}
}

// Dimension fails on clean items are split by population, because the corpus reports every figure
// that way and a figure averaged over both describes neither.
func TestCleanDimensionFailsAreSplitRealFromSynthetic(t *testing.T) {
	s := scoredProdRound(t, "", func(dir string, key AnswerKey) {
		for _, e := range key.Entries {
			if e.Kind == KindPlanted {
				continue
			}
			v := Verdict{PacketID: e.PacketID, Reviewer: "A", Dimensions: dims("pass", "user:"),
				Defect: DefectCall{Claimed: false, Class: "none"}}
			if e.SourceDomain == string(PopulationSynthetic) {
				d := v.Dimensions["legible_to_a_manager"]
				d.Verdict, d.Why = "fail", "names no subject a manager could act on"
				v.Dimensions["legible_to_a_manager"] = d
			}
			writeVerdict(t, dir, v)
		}
	})
	realFails := s.FailsByPopulation[string(PopulationReal)]["legible_to_a_manager"]
	synthFails := s.FailsByPopulation[string(PopulationSynthetic)]["legible_to_a_manager"]
	if realFails.N != 0 || realFails.Of == 0 {
		t.Errorf("real population = %s, want 0 fails over a non-zero denominator", realFails)
	}
	if synthFails.N == 0 || synthFails.N != synthFails.Of {
		t.Errorf("synthetic population = %s, want every review failed", synthFails)
	}
	// Every failure is listed in the reviewer's own words, so the count can be read against the
	// items behind it rather than trusted.
	found := false
	for _, d := range s.FailDetail {
		if strings.Contains(d, "names no subject a manager could act on") {
			found = true
		}
	}
	if !found {
		t.Errorf("the failures are not listed with the reviewer's reason: %v", s.FailDetail)
	}
	if !strings.Contains(s.Render(), "in the reviewer's words") {
		t.Error("the report has no section listing the flagged items")
	}
}

// Scoring must not silently invent a round: a directory with no run facts is an error, because the
// caveats come from that file and a report without them is the thing this round exists to avoid.
func TestScoringRefusesARoundWithNoRunFacts(t *testing.T) {
	em, _ := prodFixtureRound(t)
	if err := os.Remove(filepath.Join(em.Dir, "withheld", "run-facts.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ScoreProdRound(em.Dir, ""); err == nil {
		t.Fatal("a round with no run facts scored anyway, so it would have reported no caveats")
	}
}
