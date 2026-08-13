package llmstudy

import (
	"fmt"
	"strings"
	"testing"
)

// TestBeatCapHoldsTheAnswerTheSchemaAdmits is the consistency this trio of numbers lacked.
//
// ⚠️ THE PROMPT MUST NOT ASK FOR MORE THAN THE BUDGET CAN STORE. beatEventMaxCount entries at
// beatEventMaxRunes beside a subject at beatSubjectMaxRunes is what the schema permits and the
// prompt asks for; at BeatCap 512 that rendered to 892 runes and the surplus was discarded as
// whole trailing entries — 68 of 274 offered entries across 47 of 69 beats over the 14-session
// sweep, with the full four entries offered on 67 of 69. A cap below this figure makes dropping
// the ORDINARY case rather than the backstop, so the three constants are pinned against each
// other here: move any one of them and this fails until the other two agree.
func TestBeatCapHoldsTheAnswerTheSchemaAdmits(t *testing.T) {
	events := make([]string, beatEventMaxCount)
	for i := range events {
		events[i] = strings.Repeat("e", beatEventMaxRunes)
	}
	worst := renderBeat(strings.Repeat("s", beatSubjectMaxRunes), events, nil, nil)
	if n := runeLen(worst); n > BeatCap {
		t.Errorf("the largest beat the schema admits is %d runes (%d entries of %d beside a "+
			"%d-rune subject) and BeatCap is %d: the prompt asks for an answer that cannot be "+
			"stored whole", n, beatEventMaxCount, beatEventMaxRunes, beatSubjectMaxRunes, BeatCap)
	}
	t.Logf("largest beat the schema admits: %d runes against BeatCap %d", runeLen(worst), BeatCap)
}

// TestBeatCapTradesBeatsInTheReportRatherThanTrippingTheBackstop measures what the larger BeatCap
// actually costs, because what it was BELIEVED to cost is wrong.
//
// ⚠️ RAISING BeatCap DOES NOT TRIP THE PROMPT BACKSTOP, AND NOTHING FAILS WHEN IT IS RAISED.
// The report tier was recorded as having 4 runes of headroom (13,996 of 14,000), from which it
// was inferred that a bigger beat would push the assembled refine prompt over
// DefaultPromptCharBudget and panic assertPromptWithinBudget. It does not: fitDiscretionary
// (digest_refine.go) shrinks the beat SELECTION until the prompt fits, so the prompt stays inside
// its budget at any beat size and the cost lands somewhere quieter — the report reads FEWER
// BEATS. Raising the cap and running the whole package leaves every test green, which is exactly
// why this measurement is worth pinning: the trade is invisible to the suite otherwise.
//
// What this asserts, at the realistic worst-case refine input the budget tests already use: the
// old 512-rune beat left strictly MORE beats in the report than the current cap does — that is
// the cost, stated rather than discovered later — and the current cap still leaves at least
// beatsSurvivingAtCap of MaxBeatSelection. The trade was taken in favour of entries because the
// timeline is the primary product and the report is derived from it.
func TestBeatCapTradesBeatsInTheReportRatherThanTrippingTheBackstop(t *testing.T) {
	atCap := beatsReachingTheReport(t, BeatCap)
	if atCap < beatsSurvivingAtCap {
		t.Errorf("at BeatCap %d only %d of %d beats reach the refine prompt, expected at least %d",
			BeatCap, atCap, MaxBeatSelection, beatsSurvivingAtCap)
	}
	atOld := beatsReachingTheReport(t, 512)
	if atOld <= atCap {
		t.Errorf("a 512-rune beat left %d beats in the report and a %d-rune beat left %d; the "+
			"trade this test exists to measure is not there", atOld, BeatCap, atCap)
	}
	// 774 runes is the rendered length of the longest beat in the sweep with every entry the
	// 512-rune cap dropped put back — the cap that would have lost nothing on THAT material.
	// Covering the schema's whole worst case instead costs no further beats.
	if atLossless := beatsReachingTheReport(t, 774); atLossless != atCap {
		t.Logf("a 774-rune beat leaves %d beats and BeatCap %d leaves %d", atLossless, BeatCap, atCap)
	}
	t.Logf("beats reaching the refine prompt at realistic scale: %d of %d at BeatCap %d, %d at 512",
		atCap, MaxBeatSelection, BeatCap, atOld)
}

// beatsSurvivingAtCap is the measured figure at BeatCap, asserted as a floor rather than an
// equality: this is a property of a worst-case CONSTRUCTION, and pinning it exactly would fail
// on any unrelated change to that construction while telling nobody anything.
const beatsSurvivingAtCap = 2

// beatsReachingTheReport renders MaxBeatSelection distinctly-marked beats of n runes each into
// the realistic refine input and counts how many marks survive into the assembled prompt.
func beatsReachingTheReport(t *testing.T, n int) int {
	t.Helper()
	in := realisticRefineInput()
	for i := range in.Beats {
		mark := fmt.Sprintf("BEATMARK%02d ", i)
		in.Beats[i].Ordinal = i + 1
		in.Beats[i].Text = mark + string([]rune(strings.Repeat(
			"a real beat entry about the work underway ", 40))[:n-runeLen(mark)])
	}
	p := DigestUpdatePromptFrom(realisticPrev(), in)
	if runeLen(p) > DefaultPromptCharBudget {
		t.Fatalf("assembled refine prompt %d runes, over the %d budget at beat size %d",
			runeLen(p), DefaultPromptCharBudget, n)
	}
	kept := 0
	for i := range in.Beats {
		if strings.Contains(p, fmt.Sprintf("BEATMARK%02d ", i)) {
			kept++
		}
	}
	return kept
}
