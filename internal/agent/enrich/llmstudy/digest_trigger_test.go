package llmstudy

import (
	"testing"
	"time"
)

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

func TestFloorSuppressesAnEarlyRefresh(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	s := TriggerState{
		HasDigest: true, TurnsSince: 20, Now: now,
		LastDigestAt: now.Add(-10 * time.Minute),
		FocusDomain:  "b", PrevFocusDomain: "a",
	}
	if ok, why := p.ShouldRefresh(s); ok {
		t.Errorf("fired %s inside the %v floor", why, p.MinInterval)
	}
}

// A suppressed reason is DEFERRED, not dropped: a focus shift ten minutes after a digest must
// still be the cause of the next one rather than being lost to a later volume trigger.
func TestSuppressedReasonIsCarried(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	s := TriggerState{
		HasDigest: true, TurnsSince: 20, Now: now,
		LastDigestAt:  now.Add(-2 * time.Hour),
		PendingReason: TriggerFocusShift,
	}
	ok, why := p.ShouldRefresh(s)
	if !ok {
		t.Fatal("the floor has elapsed; it must fire")
	}
	if why != TriggerFocusShift {
		t.Errorf("want the carried reason, got %s", why)
	}
}

// The first digest is not rate-limited: there is nothing to be stale relative to.
func TestFirstDigestIgnoresTheFloor(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	if ok, why := p.ShouldRefresh(TriggerState{Now: now, LastDigestAt: now}); !ok || why != TriggerFirst {
		t.Errorf("first digest was suppressed: ok=%v why=%s", ok, why)
	}
}

// The floor applies to finalisation too — a stopped session is not going anywhere.
func TestFloorAppliesToFinalisation(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	s := TriggerState{HasDigest: true, TurnsSince: p.StaleTurns + 1, Now: now,
		LastDigestAt: now.Add(-time.Minute)}
	if ok, _ := p.ShouldRefresh(s); ok {
		t.Error("finalisation bypassed the floor")
	}
}

func TestStrongerReasonOrdering(t *testing.T) {
	if strongerReason(TriggerVolume, TriggerFocusShift) != TriggerFocusShift {
		t.Error("focus shift must outrank volume")
	}
	if strongerReason(TriggerFriction, TriggerUnsettled) != TriggerFriction {
		t.Error("friction must outrank unsettled")
	}
	if strongerReason(TriggerNone, TriggerVolume) != TriggerVolume {
		t.Error("any reason must outrank none")
	}
}

// A zero MinInterval must not silently disable the floor via a zero-value policy.
//
// DefaultTriggerPolicy reads KELD_DIGEST_MIN_INTERVAL, so asserting an hour as a constant
// made this test env-dependent: verified, it fails under KELD_DIGEST_MIN_INTERVAL=30m even
// though nothing is wrong. The env is therefore cleared for the duration — the property under
// test is "the DEFAULT is an hour, not zero", which is a statement about the fallback and not
// about whatever the operator has exported.
func TestDefaultPolicyHasAnHourFloor(t *testing.T) {
	t.Setenv("KELD_DIGEST_MIN_INTERVAL", "")
	if got := DefaultTriggerPolicy().MinInterval; got != time.Hour {
		t.Errorf("want a 1h floor, got %v", got)
	}
	// And the override still works, so clearing the env above cannot hide a broken reader.
	t.Setenv("KELD_DIGEST_MIN_INTERVAL", "30m")
	if got := DefaultTriggerPolicy().MinInterval; got != 30*time.Minute {
		t.Errorf("KELD_DIGEST_MIN_INTERVAL override ignored: got %v", got)
	}
}

// TestSuppressedReasonSurvivesARoundTrip is the only test that exercises deferral
// end to end: it derives PendingReason from a PRIOR suppressed call's own return
// value, rather than hand-setting it the way TestSuppressedReasonIsCarried does.
// That distinction matters because the brief this package was built from returned
// TriggerNone on suppression while its own comment claimed the caller could persist
// "effective" as PendingReason — a contradiction none of the other trigger tests
// caught, since the two floor tests ignore the returned reason and the carry test
// never obtains it from a real suppressed call. This test fails on that version:
// call 1 would report `why == TriggerNone`, so call 2 (with the live focus signal
// reverted, leaving only a below-MaxTurns turn count) would see no PendingReason to
// rank against and stay quiet — silently losing the focus shift rather than merely
// downgrading it to "volume".
func TestSuppressedReasonSurvivesARoundTrip(t *testing.T) {
	p := DefaultTriggerPolicy()
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	lastDigest := now.Add(-10 * time.Minute)

	// Call 1: focus shifted a -> b, ten minutes after the last digest — inside the
	// 1h floor, so it must be suppressed. The reason must still be reported: that is
	// the half of the contract a caller needs in order to defer it at all.
	ok1, why1 := p.ShouldRefresh(TriggerState{
		HasDigest: true, TurnsSince: 20, Now: now, LastDigestAt: lastDigest,
		FocusDomain: "b", PrevFocusDomain: "a",
	})
	if ok1 {
		t.Fatal("call 1 should be suppressed by the floor")
	}
	if why1 != TriggerFocusShift {
		t.Fatalf("suppression must still report the reason so it can be carried; got %q", why1)
	}

	// Call 2: 70 minutes later, the floor has elapsed, and the live focus signal has
	// reverted to "a" (matching PrevFocusDomain again) — the wobble-back case the
	// requirement itself names. TurnsSince stays below MaxTurns, so the live cascade
	// alone would report nothing at all. Only carrying why1 forward as PendingReason
	// preserves the original cause.
	ok2, why2 := p.ShouldRefresh(TriggerState{
		HasDigest: true, TurnsSince: 5, Now: now.Add(70 * time.Minute), LastDigestAt: lastDigest,
		FocusDomain: "a", PrevFocusDomain: "a",
		PendingReason: why1,
	})
	if !ok2 {
		t.Fatal("call 2 should fire; the floor has elapsed and a reason is pending")
	}
	if why2 != TriggerFocusShift {
		t.Errorf("the deferred focus-shift reason was lost or downgraded: got %q, want %q", why2, TriggerFocusShift)
	}
}
