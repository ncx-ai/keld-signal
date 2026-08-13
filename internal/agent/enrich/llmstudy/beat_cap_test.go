package llmstudy

import (
	"fmt"
	"strings"
	"testing"
)

// TestBeatCapTradesBeatsInTheReportRatherThanTrippingTheBackstop measures what a larger BeatCap
// actually costs, because what it was BELIEVED to cost is wrong.
//
// ⚠️ RAISING BeatCap DOES NOT TRIP THE PROMPT BACKSTOP, AND NOTHING FAILS WHEN IT IS RAISED.
// The report tier was recorded as having 4 runes of headroom (13,996 of 14,000), from which it
// was inferred that a bigger beat would push the assembled refine prompt over
// DefaultPromptCharBudget and panic assertPromptWithinBudget. It does not: fitDiscretionary
// (digest_refine.go) shrinks the beat SELECTION until the prompt fits, so the prompt stays inside
// its budget at any beat size and the cost lands somewhere quieter — the report reads FEWER
// BEATS. Setting BeatCap to 640 and running the whole package leaves every test green, which is
// exactly why this measurement is worth pinning: the trade is invisible to the suite otherwise.
//
// What this asserts, at the realistic worst-case refine input the budget tests already use:
// at BeatCap, at least beatsSurvivingAtCap of MaxBeatSelection beats reach the assembled prompt,
// and a beat large enough to hold every entry the 512-rune cap drops (774 runes, the largest
// measured over the 14-session sweep) leaves strictly fewer. Those are the two numbers a decision
// to move the cap has to weigh: entries lost per beat against beats lost per report.
func TestBeatCapTradesBeatsInTheReportRatherThanTrippingTheBackstop(t *testing.T) {
	atCap := beatsReachingTheReport(t, BeatCap)
	if atCap < beatsSurvivingAtCap {
		t.Errorf("at BeatCap %d only %d of %d beats reach the refine prompt, expected at least %d",
			BeatCap, atCap, MaxBeatSelection, beatsSurvivingAtCap)
	}
	// 774 runes is the rendered length of the longest beat in the sweep with every entry the cap
	// dropped put back — i.e. the cap that would lose nothing.
	atLossless := beatsReachingTheReport(t, 774)
	if atLossless >= atCap {
		t.Errorf("a 774-rune beat left %d beats in the report and a %d-rune beat left %d; the "+
			"trade this test exists to measure is not there", atLossless, BeatCap, atCap)
	}
	t.Logf("beats reaching the refine prompt at realistic scale: %d of %d at BeatCap %d, "+
		"%d at 774", atCap, MaxBeatSelection, BeatCap, atLossless)
}

// beatsSurvivingAtCap is the measured figure at BeatCap, asserted as a floor rather than an
// equality: this is a property of a worst-case CONSTRUCTION, and pinning it exactly would fail
// on any unrelated change to that construction while telling nobody anything.
const beatsSurvivingAtCap = 4

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
