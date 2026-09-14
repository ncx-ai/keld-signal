package ledger

import (
	"testing"
	"time"
)

func pendingSessions(t *testing.T, s *Store) []string {
	t.Helper()
	snap, err := s.Read(time.Time{}, 50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	out := make([]string, 0, len(snap.Pending))
	for _, p := range snap.Pending {
		out = append(out, p.Session)
	}
	return out
}

// THE STORY, at the store: resolving a session deletes its pending note.
func TestCutResolvedDeletesThePendingNote(t *testing.T) {
	setHome(t)
	s := New()
	s.CutPending("stuck-session", ReasonSidecarBehind, time.Now())
	if got := pendingSessions(t, s); len(got) != 1 {
		t.Fatalf("precondition: want 1 pending, got %v", got)
	}

	s.CutResolved("stuck-session")

	if got := pendingSessions(t, s); len(got) != 0 {
		t.Fatalf("pending after resolve = %v, want none", got)
	}
}

// NEGATIVE: resolving one session leaves every other note exactly as it was.
func TestCutResolvedTouchesOnlyItsOwnSession(t *testing.T) {
	setHome(t)
	s := New()
	s.CutPending("session-a", ReasonSidecarBehind, time.Now())
	s.CutPending("session-b", ReasonSidecarBehind, time.Now())

	s.CutResolved("session-a")

	got := pendingSessions(t, s)
	if len(got) != 1 || got[0] != "session-b" {
		t.Fatalf("pending after resolving a = %v, want [session-b]", got)
	}
}

// Idempotent, and cheap when there is nothing to do. This is called on EVERY
// healthy sweep for every active transcript — the emitter deliberately does not
// remember which sessions it reported pending, because that memory would not
// survive a daemon restart — so the common case is a delete that matches
// nothing, and it must be a no-op rather than an error or a phantom row.
func TestCutResolvedOnNothingIsANoOp(t *testing.T) {
	setHome(t)
	s := New()

	s.CutResolved("never-pending")
	s.CutResolved("never-pending")

	if got := pendingSessions(t, s); len(got) != 0 {
		t.Fatalf("resolving a session with no note produced rows: %v", got)
	}
}

// A resolved note that recurs starts a fresh streak. This is what makes
// `since` honest: the wait the page reports is this wait, not one that ended
// an hour ago plus this one.
func TestANoteWrittenAfterResolutionStartsAFreshStreak(t *testing.T) {
	setHome(t)
	s := New()
	first := time.Date(2026, 9, 8, 9, 0, 0, 0, time.UTC)
	s.CutPending("flapping", ReasonSidecarBehind, first)
	s.CutResolved("flapping")

	again := first.Add(3 * time.Hour)
	s.CutPending("flapping", ReasonSidecarBehind, again)

	snap, err := s.Read(time.Time{}, 50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Pending) != 1 {
		t.Fatalf("want 1 pending, got %v", snap.Pending)
	}
	if got := snap.Pending[0].Since; got != again.Format(time.RFC3339) {
		t.Fatalf("since = %q, want %q — a finished wait's age leaked into the new one", got, again.Format(time.RFC3339))
	}
}

// NEGATIVE: a session that fails the shape check is refused, and refusal writes
// and deletes nothing. Same rule as CutPending: never log the raw value.
func TestCutResolvedRefusesAMalformedSession(t *testing.T) {
	setHome(t)
	s := New()
	s.CutPending("real-session", ReasonSidecarBehind, time.Now())

	s.CutResolved("/Users/someone/secret-plan.md; DROP TABLE pending")

	if got := pendingSessions(t, s); len(got) != 1 || got[0] != "real-session" {
		t.Fatalf("a malformed session changed the table: %v", got)
	}
}
