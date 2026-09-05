package ledger

import (
	"sort"
	"testing"
	"time"
)

// TestBlocksSinceOrdersRowsSoAConsumerCanDeriveGaps pins the STORE half of
// "breaks are derived, never stored" (docs/v3/contracts.md): BlocksSince must
// hand back exactly the rows a consumer needs — start and end, per block,
// nothing more — for it to compute the gap between consecutive blocks of one
// session itself. This is deliberately a store-level test, not a page one: it
// asserts against the []BlockRecord rows, never against any rendered break.
func TestBlocksSinceOrdersRowsSoAConsumerCanDeriveGaps(t *testing.T) {
	setHome(t)
	s := New()
	session := "s-gaps"

	// A ends 14:50, B starts 16:38 — a real 1h48m gap, the shape the page
	// would render as a break.
	aStart := mustTime(t, "2026-09-04T14:30:00Z")
	aEnd := mustTime(t, "2026-09-04T14:50:00Z")
	bStart := mustTime(t, "2026-09-04T16:38:00Z")
	bEnd := mustTime(t, "2026-09-04T16:58:00Z")

	// C and D are a SEPARATE pair that abut exactly at the 20-minute cap
	// (blocks.py's MAX_BLOCK_MINUTES): D starts the instant C ends, so their
	// gap is zero — a cap cut, not a pause, and must never read as a break.
	cStart := mustTime(t, "2026-09-04T18:00:00Z")
	cEnd := mustTime(t, "2026-09-04T18:20:00Z")
	dStart := cEnd
	dEnd := mustTime(t, "2026-09-04T18:40:00Z")

	for _, b := range []struct {
		start, end   time.Time
		startR, endR string
	}{
		{aStart, aEnd, "session_start", "idle"},
		{bStart, bEnd, "idle", "budget"},
		{cStart, cEnd, "idle", "budget"},
		{dStart, dEnd, "budget", "session_end"},
	} {
		s.Cut(BlockKey{Session: session, Start: b.start.Unix()}, b.end.Unix(),
			b.startR, b.endR, "claude_code", time.Now())
	}

	recs, err := s.BlocksSince(aStart.Add(-time.Hour), 10)
	if err != nil {
		t.Fatalf("BlocksSince: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("want 4 blocks, got %d: %#v", len(recs), recs)
	}

	// BlocksSince orders DESC by start (see its own doc comment); a consumer
	// sorts ascending before scanning for gaps, the way a person reads a day
	// left to right. Do that here so the assertions below read naturally.
	sort.Slice(recs, func(i, j int) bool { return recs[i].Start < recs[j].Start })

	byStart := map[int64]BlockRecord{}
	for _, r := range recs {
		byStart[r.Start] = r
	}
	a, ok := byStart[aStart.Unix()]
	if !ok || a.End != aEnd.Unix() {
		t.Fatalf("block A missing or wrong end: %#v", a)
	}
	b, ok := byStart[bStart.Unix()]
	if !ok || b.End != bEnd.Unix() {
		t.Fatalf("block B missing or wrong end: %#v", b)
	}
	c, ok := byStart[cStart.Unix()]
	if !ok || c.End != cEnd.Unix() {
		t.Fatalf("block C missing or wrong end: %#v", c)
	}
	d, ok := byStart[dStart.Unix()]
	if !ok || d.End != dEnd.Unix() {
		t.Fatalf("block D missing or wrong end: %#v", d)
	}

	// A consumer derives a gap as the next block's start minus this one's end.
	gapAB := time.Duration(b.Start-a.End) * time.Second
	if gapAB != 108*time.Minute {
		t.Fatalf("A->B gap = %v, want 1h48m", gapAB)
	}
	gapCD := time.Duration(d.Start-c.End) * time.Second
	if gapCD != 0 {
		t.Fatalf("C->D gap = %v, want 0 — they abut at the cap, not a pause", gapCD)
	}
}
