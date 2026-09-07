package daemon

import (
	"context"
	"testing"
	"time"
)

// THE STORY: a laptop that slept overnight is not judged on the checks it
// missed while it was suspended.
//
// ⚠️ Remove this and three failures at 23:00 plus one at 08:00 are read as four
// consecutive failures — which restarts the daemon on the strength of evidence
// from a machine that no longer exists. Timers do not fire while a machine is
// suspended, and until this nothing in the daemon or the watcher noticed a wake
// at all.
func TestASleepDiscardsTheFailureStreakThatPrecededIt(t *testing.T) {
	rec := &recorder{}
	restarts := 0
	h := newTestHealth(t, func(context.Context) bool { return false },
		func() error { restarts++; return nil }, nil, rec)

	// Two failures before the lid closes: enough to matter, not enough to have
	// acted, which is exactly the state a sleep must not be allowed to finish.
	h.check(context.Background())
	h.check(context.Background())
	if got := h.failCount(); got != 2 {
		t.Fatalf("failures before the sleep = %d, want 2", got)
	}

	h.wokeUp(9 * time.Hour)

	if got := h.failCount(); got != 0 {
		t.Fatalf("failures after waking = %d, want 0 — the pre-sleep streak survived", got)
	}
	if rec.count("service.wake_reset") != 1 {
		t.Fatalf("no service.wake_reset event; a discarded streak that is never "+
			"reported is indistinguishable from a counter that silently misbehaves (events: %v)", rec.events)
	}

	// And the streak really is gone rather than merely renumbered: three more
	// failures must be needed, not one.
	h.check(context.Background())
	h.check(context.Background())
	if restarts != 0 {
		t.Fatalf("restarted after %d post-wake failures; the pre-sleep count was still being carried", 2)
	}
	h.check(context.Background())
	if restarts != 1 {
		t.Fatalf("restarts = %d, want 1 — the ladder should fire on three failures counted AFTER the wake", restarts)
	}
}

// NEGATIVE: waking must not claim the service is healthy.
//
// ⚠️ This is the whole difference between resetting a counter and publishing an
// answer. The owner has not probed since the machine came back, so it does not
// know; reporting `ok` here would be a confident negative from a check that
// never ran — the one thing this codebase refuses everywhere else (thin/absent,
// degraded:, known=false). The probe on the very next line of the loop is what
// is allowed to set the state.
func TestWakingDoesNotClaimTheServiceIsHealthy(t *testing.T) {
	h := newTestHealth(t, func(context.Context) bool { return false }, func() error { return nil }, nil, nil)

	h.check(context.Background())
	before, beforeReason := h.reported()
	if before == serviceOK {
		t.Fatalf("precondition: the owner should be degraded after a failed probe, got %v", before)
	}

	h.wokeUp(9 * time.Hour)

	after, afterReason := h.reported()
	if after == serviceOK {
		t.Fatal("waking published `ok` without probing — a confident answer from a check that did not run")
	}
	if after != before || afterReason != beforeReason {
		t.Fatalf("waking changed the reported state from %v(%q) to %v(%q); it must only discard the count",
			before, beforeReason, after, afterReason)
	}
}

// NEGATIVE: a quiet wake says nothing.
//
// A machine that slept while everything was fine has no streak to discard, and
// an event per lid-open would be noise on every laptop in the fleet every day —
// which is how a signal that matters gets filtered out.
func TestAWakeWithNothingInFlightIsSilent(t *testing.T) {
	rec := &recorder{}
	h := newTestHealth(t, func(context.Context) bool { return true }, func() error { return nil }, nil, rec)

	h.check(context.Background())
	h.wokeUp(9 * time.Hour)

	if n := rec.count("service.wake_reset"); n != 0 {
		t.Fatalf("emitted %d wake events with no failures in flight; that is one per lid-open, forever", n)
	}
}

// NEGATIVE: ordinary scheduling delay is not a sleep.
//
// ⚠️ The dangerous direction. If load could be mistaken for suspension, a busy
// machine would keep wiping its own failure streak and could never reach a rung
// — the detector would silently disable the escalation it was added to protect.
// So the gap threshold must sit far above any delay a loaded machine can
// produce, and comfortably below a real sleep, which is minutes to hours.
func TestALoadedMachineIsNotMistakenForASleepingOne(t *testing.T) {
	h := newTestHealth(t, func(context.Context) bool { return true }, func() error { return nil }, nil, nil)

	gap := h.sleepGap()
	if gap < 2*time.Minute {
		t.Fatalf("sleepGap = %s; below two minutes ordinary scheduling delay starts to look like a sleep", gap)
	}
	if gap <= h.interval {
		t.Fatalf("sleepGap %s is not above the probe interval %s; every ordinary tick would read as a wake",
			gap, h.interval)
	}
}

// failCount and reported are test-only readers for the owner's guarded state.
// They take the same mutex the owner does, so a test can never observe a torn
// read and can never be the reason a race detector fires.
func (h *serviceHealth) failCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fails
}

func (h *serviceHealth) reported() (serviceState, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, h.reason
}
