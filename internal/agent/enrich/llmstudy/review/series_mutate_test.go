package review

import (
	"strings"
	"testing"
)

func applyFixture(t *testing.T, m SeriesMutation) (PlantedSeries, error) {
	t.Helper()
	c := fixtureCorpus()
	all, err := BuildSeries(c)
	if err != nil {
		t.Fatalf("BuildSeries: %v", err)
	}
	return ApplySeries(c, all, m)
}

func mustApply(t *testing.T, m SeriesMutation) PlantedSeries {
	t.Helper()
	p, err := applyFixture(t, m)
	if err != nil {
		t.Fatalf("%s: %v", m.ID, err)
	}
	return p
}

func TestAnOrderShuffleKeepsEveryBeatAndNamesTheJunction(t *testing.T) {
	p := mustApply(t, SeriesMutation{ID: "t-shuffle", Class: OrderShuffle, Session: "Alpha session",
		Order: []int{1, 2, 4, 3, 5}, Note: "fixture"})
	if len(p.Series.Beats) != 5 {
		t.Fatalf("shuffle changed the beat count to %d", len(p.Series.Beats))
	}
	// Every beat text is untouched — the sequence is the only thing that lies.
	src := map[string]bool{}
	for _, b := range p.Source.Beats {
		src[b.Text] = true
	}
	for _, b := range p.Series.Beats {
		if !src[b.Text] {
			t.Errorf("shuffle rewrote a beat: %q", b.Text)
		}
	}
	// The inversion is beats 3 and 4 as PRESENTED (ordinals 4 then 3).
	if got := p.Positions; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("positions = %v, want [3 4]", got)
	}
	if p.LocationBy != LocateByPosition {
		t.Errorf("location_by = %q; a shuffle introduces no text, so position is the only mechanism", p.LocationBy)
	}
	if len(p.Signature) != 0 {
		t.Errorf("shuffle claims a signature %v, but it writes nothing", p.Signature)
	}
}

func TestAShuffleThatIsNotAPermutationOrNotAShuffleIsRefused(t *testing.T) {
	for name, m := range map[string]SeriesMutation{
		"unchanged order": {ID: "t1", Class: OrderShuffle, Session: "Alpha session", Order: []int{1, 2, 3, 4, 5}, Note: "n"},
		"a beat twice":    {ID: "t2", Class: OrderShuffle, Session: "Alpha session", Order: []int{1, 2, 2, 4, 5}, Note: "n"},
		"a beat dropped":  {ID: "t3", Class: OrderShuffle, Session: "Alpha session", Order: []int{1, 2, 3, 4}, Note: "n"},
		"a beat invented": {ID: "t4", Class: OrderShuffle, Session: "Alpha session", Order: []int{1, 2, 3, 4, 9}, Note: "n"},
	} {
		if _, err := applyFixture(t, m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestContaminationMustBeForeignHereAndRealThere(t *testing.T) {
	p := mustApply(t, SeriesMutation{ID: "t-splice", Class: CrossSessionContamination,
		Session: "Alpha session", DonorSession: "Beta session", DonorOrdinal: 2, InsertAt: 3,
		Foreign: []string{"depreciation", "Larkspur"}, Note: "fixture"})
	if len(p.Series.Beats) != 6 {
		t.Fatalf("splice produced %d beats, want 6", len(p.Series.Beats))
	}
	if !strings.HasPrefix(p.Series.Beats[2].Text, "Applying depreciation to the Larkspur") {
		t.Errorf("the spliced beat is not at position 3: %q", p.Series.Beats[2].Text)
	}
	if got := p.Positions; len(got) != 1 || got[0] != 3 {
		t.Errorf("positions = %v, want [3]", got)
	}

	// A token the host session DOES use is not foreign, and calling it foreign would put a claim in
	// the answer key that no reviewer could ever be right about. "cache" is in the spliced beat AND
	// in the host session.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-shared", Class: CrossSessionContamination,
		Session: "Alpha session", DonorSession: "Beta session", DonorOrdinal: 2, InsertAt: 3,
		Foreign: []string{"cache"}, Note: "n"}); err == nil {
		t.Error("accepted a foreign token the host session uses itself")
	}
	// A token the spliced beat does not even contain is not what the beat brought.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-absent", Class: CrossSessionContamination,
		Session: "Alpha session", DonorSession: "Beta session", DonorOrdinal: 2, InsertAt: 3,
		Foreign: []string{"quarterly"}, Note: "n"}); err == nil {
		t.Error("accepted a foreign token that is not in the spliced beat at all")
	}
	// Splicing from the same session is not contamination.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-same", Class: CrossSessionContamination,
		Session: "Alpha session", DonorSession: "Alpha session", DonorOrdinal: 2, InsertAt: 3,
		Foreign: []string{"parser.go"}, Note: "n"}); err == nil {
		t.Error("accepted contamination from the host session itself")
	}
	// The first position is not interior.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-first", Class: CrossSessionContamination,
		Session: "Alpha session", DonorSession: "Beta session", DonorOrdinal: 2, InsertAt: 1,
		Foreign: []string{"depreciation"}, Note: "n"}); err == nil {
		t.Error("accepted a splice at position 1, where it is a preamble rather than a contamination")
	}
}

func TestAnEntitySwapMustRunThroughTheSeriesAndContradictTheRecord(t *testing.T) {
	p := mustApply(t, SeriesMutation{ID: "t-swap", Class: EntitySwap, Session: "Alpha session",
		Pairs: []SwapPair{{From: "widgetd", To: "intaked"}}, Note: "fixture"})
	for _, b := range p.Series.Beats {
		if strings.Contains(b.Text, "widgetd") {
			t.Errorf("a beat kept the old name: %q", b.Text)
		}
	}
	if !strings.Contains(p.Series.Record.Block(), "widgetd") {
		t.Error("the measured record was rewritten; a mutation may not touch it")
	}
	if len(p.Positions) < 2 {
		t.Errorf("positions = %v; a series-level swap must touch more than one beat", p.Positions)
	}
	if len(p.Signature) == 0 {
		t.Error("swap has no signature, so a reviewer quoting the wrong name would score as a miss")
	}

	// A name that is already in the corpus is not an absent name.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-present", Class: EntitySwap, Session: "Alpha session",
		Pairs: []SwapPair{{From: "widgetd", To: "Larkspur"}}, Note: "n"}); err == nil {
		t.Error("accepted a substituted name that occurs in the corpus")
	}
	// A name the record never counted gives a reviewer nothing to check against.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-norecord", Class: EntitySwap, Session: "Alpha session",
		Pairs: []SwapPair{{From: "intake service", To: "arrival service"}}, Note: "n"}); err == nil {
		t.Error("accepted a swap of a name that is in no measured record")
	}
}

func TestADroppedMiddleMustBeInteriorContiguousAndWhereTheSubjectTurned(t *testing.T) {
	p := mustApply(t, SeriesMutation{ID: "t-drop", Class: DroppedMiddle, Session: "Alpha session",
		Remove: []int{3}, Note: "fixture"})
	if len(p.Series.Beats) != 4 {
		t.Fatalf("drop produced %d beats, want 4", len(p.Series.Beats))
	}
	if got := p.Positions; len(got) != 2 || got[0] != 2 || got[1] != 3 {
		t.Errorf("positions = %v, want the junction [2 3]", got)
	}
	if got := p.Removed; len(got) != 1 || got[0] != 3 {
		t.Errorf("removed = %v, want [3]", got)
	}

	for name, m := range map[string]SeriesMutation{
		"the first beat":       {ID: "t1", Class: DroppedMiddle, Session: "Alpha session", Remove: []int{1}, Note: "n"},
		"the last beat":        {ID: "t2", Class: DroppedMiddle, Session: "Alpha session", Remove: []int{5}, Note: "n"},
		"a non-contiguous run": {ID: "t3", Class: DroppedMiddle, Session: "Alpha session", Remove: []int{2, 4}, Note: "n"},
		"no marked turn":       {ID: "t4", Class: DroppedMiddle, Session: "Alpha session", Remove: []int{4}, Note: "n"},
		"too much of it":       {ID: "t5", Class: DroppedMiddle, Session: "Beta session", Remove: []int{2}, Note: "n"},
	} {
		if _, err := applyFixture(t, m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAnInventedArcMustAssertAConclusionAndIntroduceSomethingQuotable(t *testing.T) {
	p := mustApply(t, SeriesMutation{ID: "t-arc", Class: InventedArc, Session: "Alpha session",
		Replacement: "The widgetd release is complete and signed off by the platform reviewers.",
		Note:        "fixture"})
	if len(p.Series.Beats) != 5 {
		t.Fatalf("arc produced %d beats, want 5", len(p.Series.Beats))
	}
	if got := p.Positions; len(got) != 1 || got[0] != 5 {
		t.Errorf("positions = %v, want the final position [5]", got)
	}
	if p.Replaced == "" || p.Replaced == p.Series.Beats[4].Text {
		t.Errorf("the replaced beat was not recorded: %q", p.Replaced)
	}
	if len(p.Signature) == 0 {
		t.Error("arc has no signature")
	}

	// A replacement that asserts nothing is not an arc.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-flat", Class: InventedArc, Session: "Alpha session",
		Replacement: "The widgetd release notes are being drafted by the platform reviewers.", Note: "n"}); err == nil {
		t.Error("accepted a replacement that asserts no conclusion")
	}
	// A replacement whose vocabulary is already in the session leaves nothing to quote. Note that
	// absence is a SUBSTRING test, so "finished" counts as present because the last beat says
	// "unfinished" — conservative in the safe direction: it refuses a plant rather than asserting
	// one the evidence half-supports.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-known", Class: InventedArc, Session: "Alpha session",
		Replacement: "The widgetd release notes are finished.", Note: "n"}); err == nil {
		t.Error("accepted a replacement that introduces no absent word")
	}
	// And it must still end at a sentence boundary, per the repository's delimiter rule.
	if _, err := applyFixture(t, SeriesMutation{ID: "t-cut", Class: InventedArc, Session: "Alpha session",
		Replacement: "The widgetd release is complete and signed off by the platform revie", Note: "n"}); err == nil {
		t.Error("accepted a replacement that trails off mid-clause")
	}
}

func TestASeriesMutationMayNotCarryTwoDefects(t *testing.T) {
	if _, err := applyFixture(t, SeriesMutation{ID: "t-two", Class: OrderShuffle, Session: "Alpha session",
		Order: []int{1, 2, 4, 3, 5}, Remove: []int{3}, Note: "n"}); err == nil {
		t.Error("accepted a mutation that both shuffles and drops: a located verdict could not say which was seen")
	}
	if _, err := applyFixture(t, SeriesMutation{ID: "t-none", Class: OrderShuffle, Session: "Alpha session", Note: "n"}); err == nil {
		t.Error("accepted a mutation with none of its class's fields set")
	}
}

func TestEverySeriesMutationClassIsPlantedTwiceInTheSet(t *testing.T) {
	byClass := map[SeriesMutationClass]int{}
	ids := map[string]bool{}
	for _, m := range SeriesMutations {
		if ids[m.ID] {
			t.Errorf("duplicate mutation id %s", m.ID)
		}
		ids[m.ID] = true
		byClass[m.Class]++
	}
	for _, class := range SeriesMutationClasses {
		if byClass[class] < 2 {
			t.Errorf("class %s has %d planted; two is the minimum that can distinguish a blind spot from an off item", class, byClass[class])
		}
	}
}
