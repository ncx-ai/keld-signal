package llmstudy

import "testing"

func obs(labels map[Facet]string) Answer { return Answer{Labels: labels, Valid: true} }

func TestFocusSettlesOnTheMajorityDespiteChurn(t *testing.T) {
	f := NewFocus(DefaultAlpha)
	// A churning sequence: software dominates but never twice in a row at the start.
	seq := []string{"software", "business", "software", "general", "software", "software", "software"}
	for _, d := range seq {
		f.Observe(obs(map[Facet]string{FacetDomain: d}))
	}
	got, conc := f.Label(FacetDomain)
	if got != "software" {
		t.Fatalf("focus = %q, want software", got)
	}
	if conc <= 0.5 {
		t.Errorf("concentration = %.2f, want > 0.5 once settled", conc)
	}
}

// A genuine topic change must be followed, not averaged away forever.
func TestFocusFollowsARealShift(t *testing.T) {
	f := NewFocus(DefaultAlpha)
	for i := 0; i < 10; i++ {
		f.Observe(obs(map[Facet]string{FacetDomain: "software"}))
	}
	if got, _ := f.Label(FacetDomain); got != "software" {
		t.Fatalf("pre-shift focus = %q", got)
	}
	for i := 0; i < 8; i++ {
		f.Observe(obs(map[Facet]string{FacetDomain: "legal"}))
	}
	if got, _ := f.Label(FacetDomain); got != "legal" {
		t.Fatalf("post-shift focus = %q, want legal — a sustained change must win", got)
	}
}

// One odd turn must not redefine a settled session.
func TestFocusResistsASingleOutlier(t *testing.T) {
	f := NewFocus(DefaultAlpha)
	for i := 0; i < 12; i++ {
		f.Observe(obs(map[Facet]string{FacetDomain: "software"}))
	}
	f.Observe(obs(map[Facet]string{FacetDomain: "creative"}))
	if got, _ := f.Label(FacetDomain); got != "software" {
		t.Fatalf("focus = %q, want software — one outlier must not flip it", got)
	}
}

// Concentration is a confidence signal: a genuinely mixed session must report low
// concentration rather than a confident-looking arbitrary winner.
func TestConcentrationReflectsAmbiguity(t *testing.T) {
	mixed := NewFocus(DefaultAlpha)
	for i := 0; i < 6; i++ {
		mixed.Observe(obs(map[Facet]string{FacetDomain: "software"}))
		mixed.Observe(obs(map[Facet]string{FacetDomain: "business"}))
	}
	settled := NewFocus(DefaultAlpha)
	for i := 0; i < 12; i++ {
		settled.Observe(obs(map[Facet]string{FacetDomain: "software"}))
	}
	_, cm := mixed.Label(FacetDomain)
	_, cs := settled.Label(FacetDomain)
	if cm >= cs {
		t.Fatalf("mixed concentration %.2f must be below settled %.2f", cm, cs)
	}
}

func TestFocusIgnoresInvalidAndDoesNotCountThem(t *testing.T) {
	f := NewFocus(DefaultAlpha)
	f.Observe(Answer{Valid: false, Labels: map[Facet]string{FacetDomain: "legal"}})
	if f.Observations != 0 {
		t.Errorf("Observations = %d, want 0", f.Observations)
	}
	if got, _ := f.Label(FacetDomain); got != "" {
		t.Errorf("invalid answer contributed %q", got)
	}
}

// A missing facet (Partial) must decay without voting, so it neither wins nor
// freezes the estimate.
func TestMissingFacetDecaysWithoutVoting(t *testing.T) {
	f := NewFocus(DefaultAlpha)
	for i := 0; i < 5; i++ {
		f.Observe(obs(map[Facet]string{FacetSubcategory: "eng.dev"}))
	}
	before, _ := f.Label(FacetSubcategory)
	for i := 0; i < 3; i++ {
		f.Observe(obs(map[Facet]string{FacetDomain: "software"})) // subcategory absent
	}
	after, conc := f.Label(FacetSubcategory)
	if before != after {
		t.Errorf("winner changed from %q to %q on absent observations", before, after)
	}
	if conc == 0 {
		t.Error("absent facet should decay, not vanish")
	}
}

func TestThemesRankRecurringTermsAbovePassingOnes(t *testing.T) {
	f := NewFocus(DefaultAlpha)
	for i := 0; i < 6; i++ {
		f.ObserveTopics([]string{"settings poll"})
	}
	f.ObserveTopics([]string{"favicon"})
	got := f.Themes(2)
	if len(got) == 0 || got[0] != "settings poll" {
		t.Fatalf("Themes = %v, want the recurring theme first", got)
	}
}

func TestNewFocusClampsAlpha(t *testing.T) {
	for _, bad := range []float64{0, -1, 1.5} {
		if f := NewFocus(bad); f.Alpha != DefaultAlpha {
			t.Errorf("NewFocus(%v).Alpha = %v, want the default", bad, f.Alpha)
		}
	}
}
