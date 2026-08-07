package llmstudy

import (
	"math"
	"strings"
	"testing"
)

func TestWilsonBracketsThePointEstimate(t *testing.T) {
	ci := Wilson(8, 10)
	if !(ci.Lo < 0.8 && ci.Hi > 0.8) {
		t.Fatalf("Wilson(8,10) = %+v, must bracket 0.8", ci)
	}
	if ci.Lo < 0 || ci.Hi > 1 {
		t.Fatalf("Wilson must stay in [0,1]: %+v", ci)
	}
}

func TestWilsonWidensWithSmallN(t *testing.T) {
	small, large := Wilson(8, 10), Wilson(800, 1000)
	if (small.Hi - small.Lo) <= (large.Hi - large.Lo) {
		t.Fatal("a smaller sample must give a wider interval")
	}
}

func TestWilsonZeroSampleIsFullRange(t *testing.T) {
	if ci := Wilson(0, 0); ci.Lo != 0 || ci.Hi != 1 {
		t.Fatalf("Wilson(0,0) = %+v, want the full range", ci)
	}
}

// A clean sweep must not report a CI lower bound of exactly 1 — that would claim
// certainty from a handful of samples.
func TestWilsonUnanimousSmallSampleIsNotCertain(t *testing.T) {
	ci := Wilson(5, 5)
	if ci.Lo >= 1.0 {
		t.Fatalf("Wilson(5,5).Lo = %v; 5/5 must not read as certainty", ci.Lo)
	}
	if ci.Lo <= 0.5 {
		t.Fatalf("Wilson(5,5).Lo = %v; 5/5 should still clear parity", ci.Lo)
	}
}

// item builds one adjudicated item plus its provenance entry.
func item(id, facet, choice string, prov map[string]string) (Item, string, map[string]string) {
	opts := make([]Option, 0, len(prov))
	for k := range prov {
		opts = append(opts, Option{Key: k})
	}
	return Item{ID: id, Facet: facet, Choice: choice, Options: opts},
		itemKey(id, Facet(facet)), prov
}

func setOf(triples ...func() (Item, string, map[string]string)) AdjudicationSet {
	s := AdjudicationSet{Provenance: map[string]map[string]string{}}
	for _, f := range triples {
		it, key, prov := f()
		s.Items = append(s.Items, it)
		s.Provenance[key] = prov
	}
	return s
}

func TestTalliesCreditWinsAndLossesPairwise(t *testing.T) {
	s := setOf(
		func() (Item, string, map[string]string) {
			return item("p1", "domain", "a", map[string]string{"a": "qwen", "b": "gliner2"})
		},
		func() (Item, string, map[string]string) {
			return item("p2", "domain", "a", map[string]string{"a": "gliner2", "b": "qwen"})
		},
		func() (Item, string, map[string]string) {
			return item("p3", "domain", "tie", map[string]string{"a": "qwen", "b": "gliner2"})
		},
		func() (Item, string, map[string]string) {
			return item("p4", "domain", "both_wrong", map[string]string{"a": "qwen", "b": "gliner2"})
		},
	)
	got := Tallies(s, "gliner2")["domain"]["qwen"]
	want := Tally{Wins: 1, Losses: 1, Ties: 1, BothWrong: 1}
	if got != want {
		t.Fatalf("Tally = %+v, want %+v", got, want)
	}
	if got.Decided() != 2 {
		t.Errorf("Decided = %d, want 2 (ties and both-wrong excluded)", got.Decided())
	}
}

// When a THIRD arm's label wins, this arm neither beat nor lost to the control.
// Counting it as a loss would understate the arm.
func TestThirdArmWinIsOtherNotLoss(t *testing.T) {
	s := setOf(func() (Item, string, map[string]string) {
		return item("p1", "domain", "c", map[string]string{"a": "gliner2", "b": "qwen", "c": "other-arm"})
	})
	got := Tallies(s, "gliner2")["domain"]["qwen"]
	if got.Losses != 0 {
		t.Errorf("Losses = %d, want 0 when a third arm won", got.Losses)
	}
	if got.Other != 1 {
		t.Errorf("Other = %d, want 1", got.Other)
	}
	if got.Decided() != 0 {
		t.Errorf("Decided = %d, want 0", got.Decided())
	}
}

// An arm that happened to agree with the control on an item is not scored on it.
func TestArmAgreeingWithControlIsNotScored(t *testing.T) {
	s := setOf(func() (Item, string, map[string]string) {
		return item("p1", "domain", "a", map[string]string{"a": "gliner2+qwen", "b": "other-arm"})
	})
	if got, ok := Tallies(s, "gliner2")["domain"]["qwen"]; ok && got != (Tally{}) {
		t.Fatalf("arm agreeing with control must not be scored, got %+v", got)
	}
}

func TestTalliesIgnoreUnadjudicatedItems(t *testing.T) {
	s := setOf(func() (Item, string, map[string]string) {
		return item("p1", "domain", "", map[string]string{"a": "qwen", "b": "gliner2"})
	})
	if got := Tallies(s, "gliner2")["domain"]["qwen"]; got != (Tally{}) {
		t.Fatalf("unadjudicated item counted: %+v", got)
	}
}

func TestLatencyPercentiles(t *testing.T) {
	r := Run{Answers: []Answer{
		{LatencyMS: 100, Valid: true}, {LatencyMS: 200, Valid: true},
		{LatencyMS: 300, Valid: true}, {LatencyMS: 4000, Valid: true},
	}}
	p50, p95, max := Latency(r)
	if max != 4000 {
		t.Errorf("max = %d, want 4000", max)
	}
	if p50 < 100 || p50 > 300 {
		t.Errorf("p50 = %d, out of range", p50)
	}
	if p95 < p50 {
		t.Errorf("p95 (%d) < p50 (%d)", p95, p50)
	}
}

func TestLatencyIgnoresInvalidAnswers(t *testing.T) {
	r := Run{Answers: []Answer{{LatencyMS: 100, Valid: true}, {LatencyMS: 99999, Valid: false}}}
	if _, _, max := Latency(r); max != 100 {
		t.Fatalf("max = %d; a failed call's latency must not enter the distribution", max)
	}
}

func TestValidityAndPartialRates(t *testing.T) {
	r := Run{Answers: []Answer{
		{Valid: true}, {Valid: false}, {Valid: true, Partial: true}, {Valid: true},
	}}
	if got := ValidityRate(r); math.Abs(got-0.75) > 1e-9 {
		t.Errorf("ValidityRate = %v, want 0.75", got)
	}
	if got := PartialRate(r); math.Abs(got-1.0/3.0) > 1e-9 {
		t.Errorf("PartialRate = %v, want 1/3 of the valid answers", got)
	}
}

func TestMarkdownReportsRatesAndCaveats(t *testing.T) {
	s := setOf(func() (Item, string, map[string]string) {
		return item("p1", "domain", "a", map[string]string{"a": "qwen", "b": "gliner2"})
	})
	md := Markdown(Tallies(s, "gliner2"))
	for _, want := range []string{"| domain | qwen |", "95% CI", "both-wrong", "label vocabulary"} {
		if !strings.Contains(md, want) {
			t.Errorf("report omits %q:\n%s", want, md)
		}
	}
}
