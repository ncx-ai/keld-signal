package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dims builds a full set of dimension verdicts with one shared piece of evidence, so a test can
// vary the one thing it is about.
func dims(verdict, quote string) map[string]DimensionVerdict {
	out := map[string]DimensionVerdict{}
	for _, d := range Dimensions {
		out[d] = DimensionVerdict{Verdict: verdict, Quote: quote}
	}
	return out
}

func writeVerdict(t *testing.T, dir string, v Verdict) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	name := v.PacketID + "." + v.Reviewer + ".json"
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// scoredRound emits the fixture round, lets the caller write verdicts, and scores it.
func scoredRound(t *testing.T, write func(verdictsDir string, key AnswerKey)) Score {
	t.Helper()
	em, _ := fixtureRound(t)
	verdictsDir := filepath.Join(em.Dir, "verdicts")
	write(verdictsDir, em.Key)
	s, err := ScoreRound(em.KeyPath, em.PacketsDir, verdictsDir)
	if err != nil {
		t.Fatalf("ScoreRound: %v", err)
	}
	return s
}

// genuineIDOf finds the genuine packet for a named source item, so a test can pick the packet
// whose window it wants to quote. Position in the id-sorted key is meaningless by design.
func genuineIDOf(t *testing.T, key AnswerKey, session string, ordinal int) string {
	t.Helper()
	for _, e := range key.Entries {
		if e.Kind == KindGenuine && e.SourceSession == session && e.SourceOrdinal == ordinal {
			return e.PacketID
		}
	}
	t.Fatalf("no genuine packet for %s#%d", session, ordinal)
	return ""
}

func idsByKind(key AnswerKey) (genuine []string, planted map[string]string, dup, dupTwin string) {
	planted = map[string]string{}
	for _, e := range key.Entries {
		switch e.Kind {
		case KindGenuine:
			genuine = append(genuine, e.PacketID)
		case KindPlanted:
			planted[e.MutationID] = e.PacketID
		case KindCleanDuplicate:
			dup, dupTwin = e.PacketID, e.DuplicateOf
		}
	}
	return
}

// The whole point of the calibration set: caught per class over planted per class, and a class
// nobody caught named as a blind spot rather than folded into an average.
func TestCalibrationReportsPerClassCountsAndNamesABlindSpot(t *testing.T) {
	s := scoredRound(t, func(dir string, key AnswerKey) {
		_, planted, _, _ := idsByKind(key)
		// f01 is the fabricated identifier: reviewer A locates it by name, B misses it.
		writeVerdict(t, dir, Verdict{
			PacketID: planted["f01"], Reviewer: "A", Dimensions: dims("fail", "user: fix widget.go please"),
			Defect: DefectCall{Claimed: true, Class: string(FabricatedIdentifier),
				QuoteFromStatement: "sprocket.go", Why: "sprocket.go is nowhere in the evidence"},
		})
		writeVerdict(t, dir, Verdict{
			PacketID: planted["f01"], Reviewer: "B", Dimensions: dims("pass", "user: fix widget.go please"),
			Defect: DefectCall{Claimed: false, Class: "none"},
		})
		// f02 is the subject drift: BOTH reviewers object, but to the wrong thing. Flagged
		// twice, located zero times — the case that must not read as a catch.
		for _, r := range ReviewerIDs {
			writeVerdict(t, dir, Verdict{
				PacketID: planted["f02"], Reviewer: r, Dimensions: dims("fail", "user: now gadget.go"),
				Defect: DefectCall{Claimed: true, Class: "other", QuoteFromStatement: "Moving on",
					Why: "the opening is vague"},
			})
		}
	})

	byClass := map[MutationClass]ClassScore{}
	for _, c := range s.Calibration {
		byClass[c.Class] = c
	}
	fab := byClass[FabricatedIdentifier]
	if fab.LocatedReviews != (Count{1, 2}) || fab.PacketsLocatedByEither != (Count{1, 1}) || fab.PacketsLocatedByBoth != (Count{0, 1}) {
		t.Errorf("fabricated_identifier: located %s either %s both %s", fab.LocatedReviews, fab.PacketsLocatedByEither, fab.PacketsLocatedByBoth)
	}
	if fab.ClassNamedReviews != (Count{1, 2}) {
		t.Errorf("class named = %s, want 1 of 2", fab.ClassNamedReviews)
	}
	if fab.BlindSpot {
		t.Error("a located class was called a blind spot")
	}
	drift := byClass[SubjectDrift]
	if drift.FlaggedReviews != (Count{2, 2}) {
		t.Errorf("subject_drift flagged = %s, want 2 of 2", drift.FlaggedReviews)
	}
	if drift.LocatedReviews != (Count{0, 2}) {
		t.Errorf("subject_drift located = %s, want 0 of 2 — objecting to something else is not a catch", drift.LocatedReviews)
	}
	if !drift.BlindSpot {
		t.Error("a class no reviewer located was NOT named a blind spot")
	}
	if len(drift.Missed) != 1 || !strings.Contains(drift.Missed[0], "ledger.csv") {
		t.Errorf("the missed item is not listed with its span: %v", drift.Missed)
	}
	// A class with nothing planted must not read as clean.
	if note := blindSpotNote(byClass[InventedBlocker]); !strings.Contains(note, "NOT PLANTED") {
		t.Errorf("an unplanted class is reported as %q", note)
	}
	if r := s.Render(); !strings.Contains(r, "BLIND SPOT") || !strings.Contains(r, "0 of 2") {
		t.Errorf("the rendered report hides the blind spot or its denominator:\n%s", r)
	}
}

// Sensitivity without specificity is not accuracy. The two clean populations are counted
// separately, and the duplicate control catches a reviewer contradicting themselves on the
// identical statement.
func TestFalsePositivesSeparateGenuineItemsFromCleanDuplicates(t *testing.T) {
	s := scoredRound(t, func(dir string, key AnswerKey) {
		genuine, _, dup, twin := idsByKind(key)
		// The false positive goes on a genuine item that is NOT the duplicate's twin, so the
		// two populations cannot be confused with one another.
		victim := genuineIDOf(t, key, "First session", 1)
		if victim == twin {
			t.Fatal("the fixture's twin is also the false-positive victim; pick another item")
		}
		for _, id := range genuine {
			v := Verdict{PacketID: id, Reviewer: "A", Dimensions: dims("pass", "user:"),
				Defect: DefectCall{Claimed: false, Class: "none"}}
			if id == victim {
				v.Defect = DefectCall{Claimed: true, Class: string(SourcelessSpecificity),
					QuoteFromStatement: "It now compiles", Why: "unsupported"}
			}
			writeVerdict(t, dir, v)
		}
		writeVerdict(t, dir, Verdict{PacketID: dup, Reviewer: "A", Dimensions: dims("fail", "user:"),
			Defect: DefectCall{Claimed: true, Class: "other", QuoteFromStatement: "Closing the ledger", Why: "vague"}})
	})
	fp := s.FalsePositives
	if fp.GenuineReviews.N != 1 || fp.GenuineReviews.Of != 3 {
		t.Errorf("genuine reviews with a claim = %s, want 1 of 3", fp.GenuineReviews)
	}
	if fp.CleanDuplicateReviews != (Count{1, 1}) {
		t.Errorf("clean-duplicate reviews with a claim = %s, want 1 of 1", fp.CleanDuplicateReviews)
	}
	if fp.DuplicatePairsDiffered != (Count{1, 1}) {
		t.Errorf("same reviewer differing on the same statement = %s, want 1 of 1", fp.DuplicatePairsDiffered)
	}
	if got := fp.FailsByDimension["faithful"]; got.Of != 4 {
		t.Errorf("dimension denominator = %s, want the 4 clean reviews", got)
	}
}

func TestDisagreementIsCountedPerDimensionOverPacketsReviewedTwice(t *testing.T) {
	s := scoredRound(t, func(dir string, key AnswerKey) {
		first := genuineIDOf(t, key, "First session", 1)
		second := genuineIDOf(t, key, "First session", 2)
		a := Verdict{PacketID: first, Reviewer: "A", Dimensions: dims("pass", "user:"),
			Defect: DefectCall{Claimed: false, Class: "none"}}
		b := Verdict{PacketID: first, Reviewer: "B", Dimensions: dims("pass", "user:"),
			Defect: DefectCall{Claimed: true, Class: "other", QuoteFromStatement: "x", Why: "y"}}
		bd := b.Dimensions["faithful"]
		bd.Verdict = "fail"
		b.Dimensions["faithful"] = bd
		writeVerdict(t, dir, a)
		writeVerdict(t, dir, b)
		// A packet with one verdict must not enter the denominator.
		writeVerdict(t, dir, Verdict{PacketID: second, Reviewer: "A", Dimensions: dims("pass", "user:"),
			Defect: DefectCall{Claimed: false, Class: "none"}})
	})
	byDim := map[string]DimensionDisagreement{}
	for _, d := range s.Disagreement {
		byDim[d.Dimension] = d
	}
	if got := byDim["faithful"].Disagreed; got != (Count{1, 1}) {
		t.Errorf("faithful disagreement = %s, want 1 of 1", got)
	}
	if got := byDim["legible_to_a_manager"].Disagreed; got != (Count{0, 1}) {
		t.Errorf("legible disagreement = %s, want 0 of 1", got)
	}
	if got := byDim["defect_claimed"].Disagreed; got != (Count{1, 1}) {
		t.Errorf("defect-call disagreement = %s, want 1 of 1", got)
	}
	if s.PacketsBothSlots.N != 1 || s.PacketsReviewed.N != 2 {
		t.Errorf("coverage = both %s reviewed %s", s.PacketsBothSlots, s.PacketsReviewed)
	}
}

// Every verdict needs evidence, and the three ways of failing that are distinguished: no
// evidence at all, a quote that is not in the packet, and an absence claim that is refuted by
// the packet.
func TestEvidenceFaultsAreCountedAndListedByKind(t *testing.T) {
	s := scoredRound(t, func(dir string, key AnswerKey) {
		v := Verdict{PacketID: genuineIDOf(t, key, "First session", 1), Reviewer: "A",
			Dimensions: dims("pass", "user: fix widget.go please"),
			Defect:     DefectCall{Claimed: false, Class: "none"}}
		v.Dimensions["faithful"] = DimensionVerdict{Verdict: "pass"}
		v.Dimensions["not_rubberstamping"] = DimensionVerdict{Verdict: "pass", Quote: "a span the packet never contained"}
		v.Dimensions["legible_to_a_manager"] = DimensionVerdict{Verdict: "fail", Absent: []string{"widget.go"}}
		v.Dimensions["domain_neutral_specificity"] = DimensionVerdict{Verdict: "maybe", Quote: "user: fix widget.go please"}
		writeVerdict(t, dir, v)
	})
	kinds := map[string]int{}
	for _, f := range s.EvidenceFaults {
		kinds[f.Kind]++
	}
	for _, want := range []string{"unevidenced", "quote_not_found", "absent_token_present", "malformed_verdict"} {
		if kinds[want] == 0 {
			t.Errorf("no %s fault recorded; got %v", want, kinds)
		}
	}
	if s.UnevidencedTotal != (Count{1, len(Dimensions)}) {
		t.Errorf("unevidenced total = %s, want 1 of %d", s.UnevidencedTotal, len(Dimensions))
	}
	r := s.Render()
	if !strings.Contains(r, "quote_not_found") || !strings.Contains(r, "absent_token_present") {
		t.Errorf("the report does not list the faults:\n%s", r)
	}
	// The quote must be checked against the EVIDENCE, not the statement: quoting the statement
	// back is not evidence.
	s2 := scoredRound(t, func(dir string, key AnswerKey) {
		writeVerdict(t, dir, Verdict{PacketID: genuineIDOf(t, key, "First session", 1), Reviewer: "A",
			Dimensions: dims("pass", "It now compiles."),
			Defect:     DefectCall{Claimed: false, Class: "none"}})
	})
	if len(s2.EvidenceFaults) != len(Dimensions) {
		t.Errorf("quoting the statement back was accepted as evidence: %d faults", len(s2.EvidenceFaults))
	}
}

// Nothing may be dropped in silence. A verdict file that will not parse, one naming a packet
// that is not in the round, and a missing second reviewer are all reported.
func TestUnreadableAndStrayVerdictsAreReportedNotSkipped(t *testing.T) {
	s := scoredRound(t, func(dir string, key AnswerKey) {
		writeVerdict(t, dir, Verdict{PacketID: genuineIDOf(t, key, "First session", 1), Reviewer: "A",
			Dimensions: dims("pass", "user:"), Defect: DefectCall{Claimed: false, Class: "none"}})
		if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeVerdict(t, dir, Verdict{PacketID: "PKT-NOTAREALPACKET", Reviewer: "A", Dimensions: dims("pass", "x"),
			Defect: DefectCall{Claimed: false, Class: "none"}})
	})
	var sawBroken, sawStray bool
	for _, p := range s.Problems {
		if strings.Contains(p, "broken.json") {
			sawBroken = true
		}
		if strings.Contains(p, "PKT-NOTAREALPACKET") {
			sawStray = true
		}
	}
	if !sawBroken || !sawStray {
		t.Fatalf("problems = %v", s.Problems)
	}
	if !strings.Contains(s.Render(), "nothing here was skipped silently") {
		t.Error("the report does not surface the problems section")
	}
}

// The retired heuristics run one more round so this comparison exists. Abstention is its own
// column and is never counted as agreement.
func TestJudgeVersusHeuristicIsCountedWithAbstentionSeparate(t *testing.T) {
	s := scoredRound(t, func(dir string, key AnswerKey) {
		for _, e := range key.Entries {
			claim := e.Kind == KindPlanted
			writeVerdict(t, dir, Verdict{PacketID: e.PacketID, Reviewer: "A", Dimensions: dims("pass", "user:"),
				Defect: DefectCall{Claimed: claim, Class: "other", QuoteFromStatement: "x", Why: "y"}})
		}
	})
	byName := map[string]HeuristicComparison{}
	for _, h := range s.Heuristics {
		byName[h.Heuristic] = h
	}
	for _, name := range HeuristicNames {
		h, ok := byName[name]
		if !ok {
			t.Fatalf("no comparison for %s", name)
		}
		if h.Judged+h.Abstained != 6 {
			t.Errorf("%s: judged %d + abstained %d, want 6 — every packet got a verdict", name, h.Judged, h.Abstained)
		}
		if h.BothFlag+h.JudgeOnly+h.HeuristicOnly+h.NeitherFlags != h.Judged {
			t.Errorf("%s: the 2x2 (%d/%d/%d/%d) does not sum to %d judged",
				name, h.BothFlag, h.JudgeOnly, h.HeuristicOnly, h.NeitherFlags, h.Judged)
		}
		if h.FlaggedPlanted.Of != 2 || h.FlaggedClean.Of != 4 {
			t.Errorf("%s: denominators are planted %s clean %s, want 2 and 4", name, h.FlaggedPlanted, h.FlaggedClean)
		}
	}
	// The record in the fixture carries fewer than three subjects for beat 1, so
	// beat_contradicts_record must ABSTAIN somewhere rather than pass.
	if byName[heuristicContradicts].Abstained == 0 {
		t.Errorf("beat_contradicts_record abstained on nothing; a thin record cannot be a pass")
	}
	if !strings.Contains(s.Render(), "abstained on") {
		t.Error("the report does not show abstentions")
	}
}
