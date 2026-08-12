package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seriesDims builds a full set of dimension verdicts sharing one piece of evidence, so a test can
// vary the one thing it is about.
func seriesDims(verdict, quote, source string, beats []int) map[string]SeriesDimensionVerdict {
	out := map[string]SeriesDimensionVerdict{}
	for _, d := range SeriesDimensions {
		out[d] = SeriesDimensionVerdict{Verdict: verdict, Quote: quote, QuoteSource: source, Beats: beats}
	}
	return out
}

func writeSeriesVerdict(t *testing.T, dir string, v SeriesVerdict) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, v.PacketID+"."+v.Reviewer+".json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func scoredSeriesRound(t *testing.T, write func(verdictsDir string, key SeriesAnswerKey)) SeriesScore {
	t.Helper()
	em, _ := fixtureSeriesRound(t)
	verdictsDir := filepath.Join(em.Dir, "verdicts")
	write(verdictsDir, em.Key)
	s, err := ScoreSeriesRound(em.KeyPath, em.PacketsDir, verdictsDir, "")
	if err != nil {
		t.Fatalf("ScoreSeriesRound: %v", err)
	}
	return s
}

func plantedOf(t *testing.T, key SeriesAnswerKey, mutationID string) SeriesKeyEntry {
	t.Helper()
	for _, e := range key.Entries {
		if e.MutationID == mutationID {
			return e
		}
	}
	t.Fatalf("no planted packet for mutation %s", mutationID)
	return SeriesKeyEntry{}
}

func cleanOf(t *testing.T, key SeriesAnswerKey, kind SeriesKind, session string) SeriesKeyEntry {
	t.Helper()
	for _, e := range key.Entries {
		if e.Kind == kind && e.SourceSession == session {
			return e
		}
	}
	t.Fatalf("no %s packet for %s", kind, session)
	return SeriesKeyEntry{}
}

// A shuffle and a dropped middle write no text, so naming the junction beat is the ONLY way to be
// credited. Both halves are asserted, because a scorer that credited a reviewer for merely
// disliking a timeline would report sensitivity as accuracy.
func TestAPositionOnlyPlantIsLocatedByNamingTheJunctionAndNotOtherwise(t *testing.T) {
	s := scoredSeriesRound(t, func(dir string, key SeriesAnswerKey) {
		shuffle := plantedOf(t, key, "f01")
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: shuffle.PacketID, Reviewer: "A",
			Dimensions: seriesDims("fail", "", "", shuffle.Positions),
			Defect:     SeriesDefectCall{Claimed: true, Class: string(OrderShuffle), Beats: shuffle.Positions},
		})
		drop := plantedOf(t, key, "f04")
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: drop.PacketID, Reviewer: "A",
			Dimensions: seriesDims("fail", "", "", []int{1}),
			Defect:     SeriesDefectCall{Claimed: true, Class: "other", Beats: []int{1}, Why: "something feels wrong about this whole timeline"},
		})
	})
	for _, c := range s.Calibration {
		switch c.Class {
		case OrderShuffle:
			if c.LocatedReviews.N != 1 {
				t.Errorf("shuffle located %s, want 1 of 1: naming the junction is a location", c.LocatedReviews)
			}
			if c.ClassNamedReviews.N != 1 {
				t.Errorf("shuffle class named %s, want 1", c.ClassNamedReviews)
			}
			if c.LocationBy != LocateByPosition {
				t.Errorf("shuffle location_by = %q", c.LocationBy)
			}
		case DroppedMiddle:
			if c.LocatedReviews.N != 0 {
				t.Errorf("dropped middle located %s, but the reviewer named an unrelated beat and quoted nothing", c.LocatedReviews)
			}
			if c.FlaggedReviews.N != 1 {
				t.Errorf("dropped middle flagged %s, want 1: the reviewer did claim a defect", c.FlaggedReviews)
			}
			if len(c.Missed) != 1 {
				t.Errorf("a missed plant is not listed with what was missed: %v", c.Missed)
			}
		}
	}
}

// An entity swap and a splice DO introduce text, so quoting the introduced name locates them even
// with no beat number.
func TestASignaturePlantIsLocatedByQuotingWhatItIntroduced(t *testing.T) {
	s := scoredSeriesRound(t, func(dir string, key SeriesAnswerKey) {
		swap := plantedOf(t, key, "f02")
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: swap.PacketID, Reviewer: "A",
			Dimensions: seriesDims("fail", "", "", nil),
			Defect: SeriesDefectCall{Claimed: true, Class: string(EntitySwap),
				Why: "the beats call the service intaked while the record counts something else"},
		})
	})
	for _, c := range s.Calibration {
		if c.Class != EntitySwap {
			continue
		}
		if c.LocatedReviews.N != 1 {
			t.Errorf("entity swap located %s, want 1: the reviewer named the introduced name", c.LocatedReviews)
		}
		if c.LocationBy != LocateByEither {
			t.Errorf("entity swap location_by = %q", c.LocationBy)
		}
	}
}

func TestABlindSpotIsNamedRatherThanAveragedAway(t *testing.T) {
	s := scoredSeriesRound(t, func(dir string, key SeriesAnswerKey) {
		arc := plantedOf(t, key, "f05")
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: arc.PacketID, Reviewer: "A",
			Dimensions: seriesDims("pass", "", "", nil),
			Defect:     SeriesDefectCall{Claimed: false, Class: "none"},
		})
	})
	report := s.Render()
	for _, c := range s.Calibration {
		switch c.Class {
		case InventedArc:
			if !c.BlindSpot {
				t.Error("an arc nobody located is not marked a blind spot")
			}
		default:
			if c.LocatedReviews.Of != 0 {
				t.Errorf("class %s has reviews it should not: %s", c.Class, c.LocatedReviews)
			}
			if !strings.Contains(report, "the class is unmeasured") && !strings.Contains(report, "NOT PLANTED") {
				t.Errorf("class %s got no verdicts and the report does not say so", c.Class)
			}
		}
	}
	if !strings.Contains(report, "BLIND SPOT") {
		t.Error("the report does not name the blind spot")
	}
}

func TestFalsePositivesOnCleanSeriesAndOnDuplicatesAreReportedApart(t *testing.T) {
	s := scoredSeriesRound(t, func(dir string, key SeriesAnswerKey) {
		clean := cleanOf(t, key, KindSeriesClean, "Beta session")
		dup := cleanOf(t, key, KindSeriesDuplicate, "Beta session")
		// The same reviewer, the same timeline, two ids, two different calls: the one strict
		// self-consistency test in the round.
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: clean.PacketID, Reviewer: "A",
			Dimensions: seriesDims("fail", "", "", nil),
			Defect:     SeriesDefectCall{Claimed: true, Class: string(DroppedMiddle), Beats: []int{1, 2}},
		})
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: dup.PacketID, Reviewer: "A",
			Dimensions: seriesDims("pass", "", "", nil),
			Defect:     SeriesDefectCall{Claimed: false, Class: "none"},
		})
	})
	fp := s.FalsePositives
	if fp.CleanReviews.N != 1 || fp.CleanReviews.Of != 1 {
		t.Errorf("clean-series claims = %s, want 1 of 1", fp.CleanReviews)
	}
	if fp.DuplicateReviews.N != 0 || fp.DuplicateReviews.Of != 1 {
		t.Errorf("duplicate claims = %s, want 0 of 1", fp.DuplicateReviews)
	}
	if fp.DuplicatePairsDiffered.N != 1 {
		t.Errorf("the same reviewer contradicting themselves on identical timelines = %s, want 1", fp.DuplicatePairsDiffered)
	}
	if fp.FailsOnClean["followable"].N != 1 || fp.FailsOnDuplicates["followable"].N != 0 {
		t.Errorf("dimension fails were merged across the two clean populations: clean %s, dup %s",
			fp.FailsOnClean["followable"], fp.FailsOnDuplicates["followable"])
	}
}

func TestEveryEvidenceFaultKindIsDetectedAndNothingIsDroppedSilently(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	verdictsDir := filepath.Join(em.Dir, "verdicts")
	clean := cleanOf(t, em.Key, KindSeriesClean, "Alpha session")
	body, err := os.ReadFile(filepath.Join(em.PacketsDir, clean.PacketID+".md"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParseSeriesPacket(string(body))
	if err != nil {
		t.Fatal(err)
	}

	v := SeriesVerdict{PacketID: clean.PacketID, Reviewer: "A", Dimensions: map[string]SeriesDimensionVerdict{
		// A verdict with neither a quote nor an absence claim.
		"followable": {Verdict: "pass"},
		// A quote that is really in the record, attributed to the beats.
		"continuous": {Verdict: "pass", Quote: "projects: alpha", QuoteSource: "series"},
		// A quote that is nowhere.
		"specifics_present": {Verdict: "pass", Quote: "the Larkspur ledger", QuoteSource: "record"},
		// An absence claim that is refuted by the packet.
		"recognisable_week": {Verdict: "fail", Absent: []string{"widgetd"}},
		// A verdict that is neither pass nor fail, a beat that does not exist, and a quote with no
		// source stated.
		"no_false_thread": {Verdict: "mostly fine", Quote: p.Beats[0], Beats: []int{99}},
	}}
	writeSeriesVerdict(t, verdictsDir, v)
	// And a file that is not a verdict at all: it must be reported, never skipped.
	if err := os.WriteFile(filepath.Join(verdictsDir, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ScoreSeriesRound(em.KeyPath, em.PacketsDir, verdictsDir, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range s.EvidenceFaults {
		got[f.Kind] = true
	}
	for _, want := range []string{
		"unevidenced", "quote_source_wrong", "quote_not_found", "absent_token_present",
		"malformed_verdict", "beat_out_of_range", "quote_source_unstated",
	} {
		if !got[want] {
			t.Errorf("fault kind %q was not detected: %+v", want, s.EvidenceFaults)
		}
	}
	if s.UnevidencedTotal.N != 1 || s.UnevidencedTotal.Of != len(SeriesDimensions) {
		t.Errorf("unevidenced total = %s, want 1 of %d", s.UnevidencedTotal, len(SeriesDimensions))
	}
	if len(s.Problems) == 0 {
		t.Error("an unparseable verdict file was dropped in silence, which is how T1 reported 100%")
	}
}

func TestAMissingDimensionIsAFaultAndNotAPass(t *testing.T) {
	s := scoredSeriesRound(t, func(dir string, key SeriesAnswerKey) {
		clean := cleanOf(t, key, KindSeriesClean, "Alpha session")
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: clean.PacketID, Reviewer: "A",
			Dimensions: map[string]SeriesDimensionVerdict{"followable": {Verdict: "pass", Absent: []string{"nothing like this in it"}}},
		})
	})
	missing := 0
	for _, f := range s.EvidenceFaults {
		if f.Kind == "missing_dimension" {
			missing++
		}
	}
	if missing != len(SeriesDimensions)-1 {
		t.Errorf("missing dimensions detected = %d, want %d", missing, len(SeriesDimensions)-1)
	}
	if s.FalsePositives.FailsOnClean["continuous"].Of != 0 {
		t.Error("a dimension that was never returned was counted in the denominator as if it had been")
	}
}

// The cross-tabulation is the point of the whole exercise: a session whose beats all passed the
// per-beat round and whose series still fails `followable`.
func TestTheCrossTabulationNamesTheSessionWhoseBeatsPassedAndWhoseSeriesFails(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	verdictsDir := filepath.Join(em.Dir, "verdicts")
	clean := cleanOf(t, em.Key, KindSeriesClean, "Alpha session")
	writeSeriesVerdict(t, verdictsDir, SeriesVerdict{
		PacketID: clean.PacketID, Reviewer: "A",
		Dimensions: seriesDims("fail", "", "", nil),
		Defect:     SeriesDefectCall{Claimed: false, Class: "none"},
	})

	// A minimal per-beat round on the same session: two beats, both passed by their reviewer.
	beatDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(beatDir, "withheld"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(beatDir, "verdicts"), 0o755); err != nil {
		t.Fatal(err)
	}
	beatKey := AnswerKey{Round: "r-fixture", Entries: []KeyEntry{
		{PacketID: "PKT-AAAA", Kind: KindGenuine, SourceSession: "Alpha session", SourceOrdinal: 1},
		{PacketID: "PKT-BBBB", Kind: KindGenuine, SourceSession: "Alpha session", SourceOrdinal: 2},
		// A planted beat packet, which must be EXCLUDED from the comparison.
		{PacketID: "PKT-CCCC", Kind: KindPlanted, SourceSession: "Alpha session", SourceOrdinal: 3,
			MutationClass: FabricatedIdentifier},
	}}
	if err := writeJSON(filepath.Join(beatDir, "withheld", "answer-key.json"), beatKey); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"PKT-AAAA", "PKT-BBBB", "PKT-CCCC"} {
		v := Verdict{PacketID: id, Reviewer: "A", Dimensions: dims("pass", "")}
		if id == "PKT-CCCC" {
			v.Defect = DefectCall{Claimed: true, Class: string(FabricatedIdentifier)}
		}
		writeVerdict(t, filepath.Join(beatDir, "verdicts"), v)
	}

	s, err := ScoreSeriesRound(em.KeyPath, em.PacketsDir, verdictsDir, beatDir)
	if err != nil {
		t.Fatal(err)
	}
	if s.VersusBeat == nil {
		t.Fatal("no cross-tabulation was produced")
	}
	var row SessionCrossTab
	for _, r := range s.VersusBeat.Sessions {
		if r.Session == "Alpha session" {
			row = r
		}
	}
	if row.BeatPacketsReviewed.Of != 2 {
		t.Errorf("beat packets counted = %s; the planted beat packet must be excluded", row.BeatPacketsReviewed)
	}
	if row.BeatReviewsClaimingDefect.N != 0 {
		t.Errorf("beat claims = %s, want 0 — the only claim was on the excluded planted packet", row.BeatReviewsClaimingDefect)
	}
	if row.SeriesFailsByDimension["followable"].N != 1 {
		t.Errorf("series followable fails = %s, want 1", row.SeriesFailsByDimension["followable"])
	}
	if len(row.Notable) == 0 || !strings.Contains(strings.Join(row.Notable, " "), "guard passes and the goal is missed") {
		t.Errorf("the interesting cell is not named: %v", row.Notable)
	}
	report := s.Render()
	if !strings.Contains(report, "NOT merged") {
		t.Error("the report does not say the two metrics are kept apart")
	}
	if !strings.Contains(report, "real series exist") {
		t.Error("the report does not state how few real timelines the round was cut from")
	}
}

func TestScoringSurvivesAVerdictForAPacketThatIsNotInTheRound(t *testing.T) {
	s := scoredSeriesRound(t, func(dir string, key SeriesAnswerKey) {
		writeSeriesVerdict(t, dir, SeriesVerdict{
			PacketID: "SER-NOTHERE", Reviewer: "A", Dimensions: seriesDims("pass", "", "", nil),
		})
	})
	if len(s.Problems) == 0 {
		t.Error("a verdict for an unknown packet was accepted in silence")
	}
}

func TestResolveRoundDirAcceptsAbsoluteAndRepoRootedPaths(t *testing.T) {
	dir := t.TempDir()
	got, how, err := ResolveRoundDir(repoRoot, dir)
	if err != nil || got != dir || how != "absolute path" {
		t.Errorf("absolute: got %q %q %v", got, how, err)
	}
	// A path that exists relative to neither is an ERROR, not a silent empty read — the r1 bug was
	// exactly a silent one.
	if _, _, err := ResolveRoundDir(repoRoot, "no/such/round"); err == nil {
		t.Error("a nonexistent relative round directory resolved without error")
	}
	if _, _, err := ResolveRoundDir(repoRoot, ""); err == nil {
		t.Error("an empty round directory resolved without error")
	}
}
