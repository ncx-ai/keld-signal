package ledger

import (
	"testing"
	"time"
)

// A block belongs to the day it BEGAN, which is the rule the Today pane's
// date bound rests on.
//
// ⚠️ **THIS IS A DECISION, NOT AN ACCIDENT.** A block that starts at 23:50 and
// ends at 00:10 has to belong somewhere, and `start` is what the row already
// displays and what the list is already ordered by. Splitting it across two
// days, or counting it in both, would make the headline cards stop summing to
// the list underneath them — a number that disagrees with the rows it is drawn
// above is worse than a block filed on the earlier day.
//
// `Read(since, …)` filters on block start, so this pins the property the page
// depends on rather than restating the query.
func TestABlockSpanningMidnightBelongsToTheDayItBegan(t *testing.T) {
	setHome(t)
	s := New()

	midnight := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)

	// Starts twenty minutes before midnight, ends ten minutes after it.
	across := BlockKey{Session: "9eb2b3ff1111bbbb", Start: midnight.Add(-20 * time.Minute).Unix()}
	s.Cut(across, midnight.Add(10*time.Minute).Unix(), "idle", "budget", "claude_code", midnight)

	// Starts a minute after midnight.
	today := BlockKey{Session: "9eb2b3ff2222cccc", Start: midnight.Add(time.Minute).Unix()}
	s.Cut(today, midnight.Add(21*time.Minute).Unix(), "idle", "budget", "claude_code", midnight)

	snap, err := s.Read(midnight, 50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	got := map[string]bool{}
	for _, b := range snap.Blocks {
		got[b.Key.Session] = true
	}
	if got[across.Session] {
		t.Fatal("a block that began yesterday appeared in today — it must be filed on the day it started")
	}
	if !got[today.Session] {
		t.Fatal("a block that began after midnight is missing from today")
	}
}

// NEGATIVE: the bound is inclusive of its own instant. A block starting exactly
// at midnight is the first block of the new day, and an off-by-one here hides
// it every single day.
func TestABlockStartingExactlyAtTheBoundIsIncluded(t *testing.T) {
	setHome(t)
	s := New()

	midnight := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	k := BlockKey{Session: "9eb2b3ff3333dddd", Start: midnight.Unix()}
	s.Cut(k, midnight.Add(20*time.Minute).Unix(), "session_start", "budget", "claude_code", midnight)

	snap, err := s.Read(midnight, 50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("a block starting exactly at the bound was dropped: %d blocks", len(snap.Blocks))
	}
}

// NEGATIVE: bounding the day must not hide anything else the page needs.
//
// ⚠️ Health rows and pending rows are about the MACHINE, not about a day. If
// the date bound reached them, a quiet morning would render as a machine with
// no health at all — a check that did not run showing as a check that passed,
// which is the failure this codebase refuses everywhere.
func TestTheDayBoundDoesNotHideHealthOrPending(t *testing.T) {
	setHome(t)
	s := New()

	yesterday := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	midnight := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)

	s.SetHealth(Health{Key: HealthDaemon, Status: StatusOK, Detail: "2.5.0", At: yesterday})
	s.CutPending("9eb2b3ff4444eeee", ReasonSidecarBehind, yesterday)

	snap, err := s.Read(midnight, 50)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Health) == 0 {
		t.Fatal("the day bound hid the health rows; a quiet morning would look like a machine with no health")
	}
	if len(snap.Pending) == 0 {
		t.Fatal("the day bound hid the pending rows")
	}
}
