package llmstudy

import "testing"

func base() TriggerState {
	return TriggerState{
		HasDigest: true, TurnsSince: 5, Concentration: 0.9,
		PrevFocusDomain: "software", FocusDomain: "software",
		PrevFocusFunc: "eng", FocusFunc: "eng",
	}
}

func TestFirstDigestAlwaysFires(t *testing.T) {
	s := base()
	s.HasDigest = false
	s.TurnsSince = 1
	if ok, why := DefaultTriggerPolicy().ShouldRefresh(s); !ok || why != TriggerFirst {
		t.Fatalf("first digest must fire, got ok=%v why=%s", ok, why)
	}
}

// A run of "sure / yes / go on" must not burn an inference.
func TestShortAcknowledgementRunDoesNotFire(t *testing.T) {
	s := base()
	s.TurnsSince = 2
	if ok, why := DefaultTriggerPolicy().ShouldRefresh(s); ok {
		t.Fatalf("below MinTurns must not fire, got %s", why)
	}
}

// A change of subject is the case a fixed cadence reports late.
func TestFocusShiftFiresImmediatelyAboveMinTurns(t *testing.T) {
	s := base()
	s.FocusFunc = "fin" // work moved from engineering to finance
	ok, why := DefaultTriggerPolicy().ShouldRefresh(s)
	if !ok || why != TriggerFocusShift {
		t.Fatalf("a focus shift must fire, got ok=%v why=%s", ok, why)
	}
}

// A digest written before a correction claims things went well, so friction must
// invalidate it.
func TestFrictionFires(t *testing.T) {
	s := base()
	s.CorrectionsSince = 1
	if ok, why := DefaultTriggerPolicy().ShouldRefresh(s); !ok || why != TriggerFriction {
		t.Fatalf("corrections must fire, got ok=%v why=%s", ok, why)
	}
}

// Diffuse focus means the work is moving even when the argmax has not flipped.
func TestUnsettledFocusFires(t *testing.T) {
	s := base()
	s.Concentration = 0.3
	if ok, why := DefaultTriggerPolicy().ShouldRefresh(s); !ok || why != TriggerUnsettled {
		t.Fatalf("diffuse focus must fire, got ok=%v why=%s", ok, why)
	}
}

// Steady work still gets refreshed eventually.
func TestVolumeIsTheFallback(t *testing.T) {
	s := base()
	s.TurnsSince = 15
	if ok, why := DefaultTriggerPolicy().ShouldRefresh(s); !ok || why != TriggerVolume {
		t.Fatalf("MaxTurns must fire as a fallback, got ok=%v why=%s", ok, why)
	}
}

// Settled, low-volume, friction-free work should stay quiet — that is the saving.
func TestSteadySettledWorkStaysQuiet(t *testing.T) {
	if ok, why := DefaultTriggerPolicy().ShouldRefresh(base()); ok {
		t.Fatalf("settled work below MaxTurns must not fire, got %s", why)
	}
}

// A long quiet stretch is a finalisation point and bypasses MinTurns.
func TestStaleFinalisationBypassesMinTurns(t *testing.T) {
	p := DefaultTriggerPolicy()
	p.MinTurns = 100
	s := base()
	s.TurnsSince = 40
	if ok, why := p.ShouldRefresh(s); !ok || why != TriggerStale {
		t.Fatalf("stale finalisation must fire, got ok=%v why=%s", ok, why)
	}
}
