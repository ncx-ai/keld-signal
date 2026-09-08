package ledger

import (
	"testing"
	"time"
)

// THE STORY: a page must be able to tell a twenty-second wait from a
// twenty-eight-hour one.
//
// ⚠️ **`at` CANNOT ANSWER THAT, AND THAT IS THE WHOLE REASON `since` EXISTS.**
// reportCutPending fires on EVERY sweep a transcript still cannot be cut, and
// the upsert overwrites `at` each time, so `now - at` is bounded by the sweep
// interval (5 minutes) no matter how long the wait has really lasted. Anything
// thresholding on `at` is measuring the sweep timer. Remove this test and the
// distinction silently collapses back to that.
func TestSinceSurvivesTheSweepThatRefreshesAt(t *testing.T) {
	setHome(t)
	s := New()

	began := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	s.CutPending("stuck-session", ReasonSidecarBehind, began)

	// Four more sweeps over the next twenty minutes, exactly as a starved
	// service produces.
	for i := 1; i <= 4; i++ {
		s.CutPending("stuck-session", ReasonSidecarBehind, began.Add(time.Duration(i)*5*time.Minute))
	}

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Pending) != 1 {
		t.Fatalf("want 1 pending entry, got %#v", snap.Pending)
	}
	p := snap.Pending[0]

	if p.Since != began.Format(time.RFC3339) {
		t.Fatalf("Since = %q, want the instant the streak began (%q) — it was overwritten by a later sweep",
			p.Since, began.Format(time.RFC3339))
	}
	if p.At == p.Since {
		t.Fatalf("At and Since are both %q; At must track the LATEST sweep or it is not a heartbeat", p.At)
	}
	if p.At != began.Add(20*time.Minute).Format(time.RFC3339) {
		t.Fatalf("At = %q, want the most recent sweep", p.At)
	}
}

// NEGATIVE: a different reason is a different wait.
//
// ⚠️ Carrying the old instant across a reason change would report the new
// condition as hours old the instant it began — an alarming number attached to
// something that just started, which is exactly the false alarm the threshold
// exists to avoid.
func TestAChangedReasonRestartsTheClock(t *testing.T) {
	setHome(t)
	s := New()

	first := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	s.CutPending("switching-session", ReasonSidecarOutdated, first)

	later := first.Add(2 * time.Hour)
	s.CutPending("switching-session", ReasonSidecarBehind, later)

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Pending) != 1 {
		t.Fatalf("want 1 pending entry, got %#v", snap.Pending)
	}
	if got := snap.Pending[0].Since; got != later.Format(time.RFC3339) {
		t.Fatalf("Since = %q, want the new reason's own start (%q); the previous condition's age was carried across",
			got, later.Format(time.RFC3339))
	}
}

// A recovery ends the streak, so the next one starts fresh rather than
// inheriting an age from a wait that is over.
func TestARecoveryEndsTheStreak(t *testing.T) {
	setHome(t)
	s := New()

	old := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	s.CutPending("recovering-session", ReasonSidecarBehind, old)
	s.Cut(BlockKey{Session: "recovering-session", Start: 9000}, 9060, "idle", "budget", "claude_code", old.Add(time.Minute))

	again := old.Add(6 * time.Hour)
	s.CutPending("recovering-session", ReasonSidecarBehind, again)

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Pending) != 1 {
		t.Fatalf("want 1 pending entry, got %#v", snap.Pending)
	}
	if got := snap.Pending[0].Since; got != again.Format(time.RFC3339) {
		t.Fatalf("Since = %q, want %q — a finished wait's age leaked into a new one", got, again.Format(time.RFC3339))
	}
}

// The migration: opening a store twice must work. There is no version counter
// here, so the ALTER runs on every open and reports "duplicate column" from the
// second onward. Treating that as an error would make the store unopenable.
func TestReopeningTheStoreIsNotAnError(t *testing.T) {
	setHome(t)

	s1 := New()
	s1.CutPending("a-session", ReasonSidecarBehind, time.Now())
	if _, err := s1.Read(time.Time{}, 10); err != nil {
		t.Fatalf("first open: %v", err)
	}

	s2 := New()
	snap, err := s2.Read(time.Time{}, 10)
	if err != nil {
		t.Fatalf("reopening the store failed — the ALTER is not idempotent: %v", err)
	}
	if len(snap.Pending) != 1 {
		t.Fatalf("want the row written by the first handle, got %#v", snap.Pending)
	}
}
