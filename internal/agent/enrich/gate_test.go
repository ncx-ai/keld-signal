package enrich

import (
	"testing"
)

func TestGateEnabledDefaultsOff(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "")
	if gateEnabled() {
		t.Fatal("gate must default OFF when unset")
	}
	for _, on := range []string{"1", "true", "TRUE", "on"} {
		t.Setenv("KELD_ENRICH_GATE_ENABLED", on)
		if !gateEnabled() {
			t.Fatalf("gate should be ON for %q", on)
		}
	}
	for _, off := range []string{"0", "false", "off", "no"} {
		t.Setenv("KELD_ENRICH_GATE_ENABLED", off)
		if gateEnabled() {
			t.Fatalf("gate should be OFF for %q", off)
		}
	}
}

func TestPrefilterContentFree(t *testing.T) {
	contentFree := []string{
		"ok", "yes", "do that", "ok, do that", "go ahead", "yes please",
		"continue", "lgtm", "sounds good, proceed", "perfect, ship it",
		"sure", "yep that works", "thanks!", "hmm", "wait", "one sec",
	}
	substantive := []string{
		"Add a rate limiter to the /login endpoint",
		"No, use a dictionary instead of a list",
		"Why is this test failing?",
		"Use tabs, not spaces",
		"Change the variable name to userCount",
		"deploy the staging build now please", // 6 tokens → over cap even though wordy-approval
	}
	for _, p := range contentFree {
		if !prefilterContentFree(p) {
			t.Errorf("expected content-free: %q", p)
		}
	}
	for _, p := range substantive {
		if prefilterContentFree(p) {
			t.Errorf("expected substantive (not gated): %q", p)
		}
	}
	if prefilterContentFree("") {
		t.Error("empty string must not be content-free-gated")
	}
}

func TestAlwaysRunMarkers(t *testing.T) {
	always := func(ex Extractor) bool {
		ar, ok := ex.(alwaysRunner)
		return ok && ar.AlwaysRun()
	}
	if !always(SensitivityExtractor{}) {
		t.Error("sensitivity must be always-run")
	}
	if !always(SpeechActExtractor{}) {
		t.Error("speech_act must be always-run")
	}
	for _, ex := range []Extractor{TaskTypeExtractor{}, DomainEntitiesExtractor{},
		passExtractor{Pass{Name: "activity_type", Labels: Activities}}, funcGuessExtractor{}} {
		if always(ex) {
			t.Errorf("%s must NOT be always-run (it's gated)", ex.Name())
		}
	}
}
