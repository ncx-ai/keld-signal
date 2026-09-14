package blocks

import (
	"context"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// hooks captures both halves of the pending conversation so a test can assert
// on the ORDER of events, not only their counts.
type hooks struct {
	pending  []string // sessions reported pending
	resolved []string // sessions reported resolved
}

func wire(e *Emitter) *hooks {
	h := &hooks{}
	e.OnCutPending = func(session, _ string) { h.pending = append(h.pending, session) }
	e.OnCutResolved = func(session string) { h.resolved = append(h.resolved, session) }
	return h
}

// THE STORY: once the analysis service has answered for a session, its
// "waiting" note goes away — whether or not a block closed.
//
// ⚠️ **A NOTE WRITTEN DURING A FIVE-MINUTE HICCUP USED TO LIVE UNTIL A BLOCK
// HAPPENED TO CLOSE.** Only Cut deleted it, and Cut fires only when the sidecar
// closes a block. A sweep that succeeded with nothing new wrote nothing and
// cleared nothing. Measured on a real machine: "still catching up … 19
// minutes" for a session the service had caught up on eighteen minutes
// earlier, the row's `at` equal to its `since` — written once, never refreshed.
func TestAnAnsweredSweepResolvesThePendingNoteEvenWithNothingToCut(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	e := newTestEmitter(t, dig, &fakeSender{})
	now := time.Unix(10000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seeds the cursor past the only block
	h := wire(e)

	// The service falls behind: a note is written.
	dig.mu.Lock()
	dig.fail = true
	dig.mu.Unlock()
	e.Sweep(context.Background(), now.Add(5*time.Minute))
	if len(h.pending) != 1 {
		t.Fatalf("pending reported %d times, want 1 (the hiccup)", len(h.pending))
	}
	if len(h.resolved) != 0 {
		t.Fatalf("resolved fired %d times while the service was behind", len(h.resolved))
	}

	// The service catches up and answers OK — with ZERO new blocks, because the
	// cursor is already past the only one. This is the case that used to clear
	// nothing.
	dig.mu.Lock()
	dig.fail = false
	dig.mu.Unlock()
	e.Sweep(context.Background(), now.Add(10*time.Minute))

	if len(h.resolved) != 1 {
		t.Fatalf("resolved fired %d times after a healthy sweep with nothing to cut, want 1", len(h.resolved))
	}
	if want := sessionIDFor(txPath); h.resolved[0] != want {
		t.Fatalf("resolved session = %q, want %q", h.resolved[0], want)
	}
}

// NEGATIVE, AND THE ONE THAT MUST NOT REGRESS: a session the service genuinely
// cannot answer for keeps its note and keeps refreshing it.
//
// If resolving ever fires on a failed sweep, the page goes quiet about a
// service that is actually behind — the exact silent-clean-page failure this
// codebase refuses everywhere.
func TestAStillBehindServiceKeepsTheNoteAndNeverResolvesIt(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	e := newTestEmitter(t, dig, &fakeSender{})
	now := time.Unix(10000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now)
	h := wire(e)

	dig.mu.Lock()
	dig.fail = true
	dig.mu.Unlock()
	for i := 1; i <= 4; i++ {
		e.Sweep(context.Background(), now.Add(time.Duration(i)*5*time.Minute))
	}

	if len(h.pending) != 4 {
		t.Fatalf("pending reported %d times across 4 failed sweeps, want 4 — the note must keep refreshing", len(h.pending))
	}
	if len(h.resolved) != 0 {
		t.Fatalf("resolved fired %d times while the service never answered", len(h.resolved))
	}
}

// NEGATIVE: a sidecar with no /blocks route at all (version skew) is not an
// answer. It reports `sidecar_outdated` and must never read as resolved.
func TestAMissingRouteIsNotAnAnswer(t *testing.T) {
	dig := &fakeDig{routeGone: true}
	e := newTestEmitter(t, dig, &fakeSender{})
	h := wire(e)
	now := time.Unix(9000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now)
	e.Sweep(context.Background(), now.Add(5*time.Minute))

	if len(h.resolved) != 0 {
		t.Fatalf("resolved fired %d times against a sidecar with no /blocks route", len(h.resolved))
	}
	if len(h.pending) != 2 {
		t.Fatalf("pending reported %d times, want 2", len(h.pending))
	}
}

// splitDig answers OK for every transcript except one, which it fails. It
// exists because fakeDig's `fail` is global, and the isolation claim needs two
// transcripts in two states in one sweep.
type splitDig struct {
	ok      *fakeDig
	badPath string
}

func (d *splitDig) BlocksCharacterised(path, source, sessionID string, since *float64,
	now time.Time, maxBlocks int, resolved enrich.ResolvedFacts) enrich.BlocksAnswer {
	if path == d.badPath {
		return enrich.BlocksAnswer{}
	}
	return d.ok.BlocksCharacterised(path, source, sessionID, since, now, maxBlocks, resolved)
}

// NEGATIVE: an answer for one session never resolves another's note.
//
// Two live transcripts; the service answers for A and is behind on B. B's note
// must be written and A's resolution must name A — if the resolve were keyed
// on anything wider than the session, B would be silenced by A's success.
func TestResolvingOneSessionDoesNotClearAnothers(t *testing.T) {
	const otherPath = "/home/x/.claude/projects/p/other.jsonl"
	inner := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	dig := &splitDig{ok: inner, badPath: otherPath}
	e := newTestEmitter(t, dig, &fakeSender{})
	h := wire(e)

	now := time.Unix(10000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.advanceAt("claude_code", otherPath, now)
	e.Sweep(context.Background(), now)

	wantOK, wantBad := sessionIDFor(txPath), sessionIDFor(otherPath)
	if len(h.resolved) != 1 || h.resolved[0] != wantOK {
		t.Fatalf("resolved = %v, want exactly [%q]", h.resolved, wantOK)
	}
	if len(h.pending) != 1 || h.pending[0] != wantBad {
		t.Fatalf("pending = %v, want exactly [%q]", h.pending, wantBad)
	}
}

// A nil hook is the default on every caller that predates this, and an OK
// sweep must not panic on it.
func TestAnUnwiredResolvedHookIsSafe(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	e := newTestEmitter(t, dig, &fakeSender{})
	e.OnCutResolved = nil
	now := time.Unix(10000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now)
	e.Sweep(context.Background(), now.Add(5*time.Minute)) // did not panic
}
