package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// The distinctive string every transcript line in this file says. It exists so
// "the signal carries no content" can be asserted against something that would
// actually be visible if it leaked, rather than against the shape of a
// signature that could later grow a third argument.
const saidMarker = "PROMPT-BODY-MUST-NOT-TRAVEL"

func genuineSaying(id, said string) string {
	return `{"type":"user","promptId":"` + id + `","cwd":"/w","sessionId":"S1","message":{"role":"user","content":"` + said + `"}}` + "\n"
}

// advance is one recorded ingest signal: the coordinates, and nothing else.
type advance struct{ source, path string }

// signalWatcher is testWatcher plus a recording ingest signal.
func signalWatcher(t *testing.T, root Root, backfill bool, rec *[]advance) *Watcher {
	t.Helper()
	w := testWatcher(t, root, func(spool.Pointer) {}, backfill)
	w.advanced = func(source, path string) { *rec = append(*rec, advance{source, path}) }
	return w
}

// The signal is per FILE per POLL, not per line and not per prompt. A transcript
// under active use grows on every poll; one signal per appended line would turn
// a 200-line batch into 200 whole-tail ingests of the same file.
func TestIngestSignalFiresOncePerAdvancedFilePerBatch(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "aaaaaaaa-0000.jsonl")
	b := filepath.Join(dir, "bbbbbbbb-0000.jsonl")
	// Three lines each, two of them genuine prompts: neither count may reach the signal.
	content := genuineSaying("A1", saidMarker) +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + saidMarker + `"}]}}` + "\n" +
		genuineSaying("A2", saidMarker)
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var got []advance
	w := signalWatcher(t, Root{SourceID: "claude_code", Dir: dir}, true, &got)

	w.pollOnce()
	if len(got) != 2 {
		t.Fatalf("want exactly one signal per advanced file (2 files), got %d: %+v", len(got), got)
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g.path]++
		if g.source != "claude_code" {
			t.Errorf("signal source = %q, want claude_code", g.source)
		}
	}
	if seen[a] != 1 || seen[b] != 1 {
		t.Fatalf("each file must be signalled exactly once: %+v", seen)
	}
}

// Coordinates only. A path is what the sidecar needs to resume from its own byte
// offset; the bytes themselves stay on this side of the call.
func TestIngestSignalCarriesNoPromptContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cccccccc-0000.jsonl")
	if err := os.WriteFile(path, []byte(genuineSaying("C1", saidMarker)), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []advance
	w := signalWatcher(t, Root{SourceID: "cowork", Dir: dir}, true, &got)
	w.pollOnce()

	if len(got) != 1 {
		t.Fatalf("want 1 signal, got %d", len(got))
	}
	if got[0].path != path {
		t.Errorf("signal path = %q, want %q", got[0].path, path)
	}
	if joined := got[0].source + "\x00" + got[0].path; strings.Contains(joined, saidMarker) {
		t.Errorf("the signal carried prompt content: %q", joined)
	}
}

// The startup case, and the reason a daemon restart is not a thundering herd of
// whole-file ingests: forward-only (KELD_WATCH_BACKFILL off, the default) sets a
// new file's cursor to EOF and consumes nothing, so nothing advanced. Only a
// file that grows AFTER the daemon came up is signalled.
func TestForwardOnlyFirstSightingDoesNotSignal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dddddddd-0000.jsonl")
	if err := os.WriteFile(path, []byte(genuineSaying("D1", saidMarker)), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []advance
	w := signalWatcher(t, Root{SourceID: "claude_code", Dir: dir}, false, &got)

	w.pollOnce() // first sighting: cursor jumps to EOF, no bytes consumed
	if len(got) != 0 {
		t.Fatalf("a pre-existing file must not be signalled forward-only: %+v", got)
	}
	w.pollOnce() // nothing appended
	if len(got) != 0 {
		t.Fatalf("an unchanged file must not be signalled: %+v", got)
	}

	appendFile(t, path, genuineSaying("D2", saidMarker))
	w.pollOnce()
	if len(got) != 1 || got[0].path != path {
		t.Fatalf("a grown file must be signalled exactly once: %+v", got)
	}
	w.pollOnce() // and not again
	if len(got) != 1 {
		t.Fatalf("no further signal without further growth: %+v", got)
	}
}

// A watcher with no signal wired (every existing construction, and every test
// above this task) must behave exactly as before.
func TestNilIngestSignalIsInert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eeeeeeee-0000.jsonl")
	if err := os.WriteFile(path, []byte(genuineSaying("E1", saidMarker)), 0o600); err != nil {
		t.Fatal(err)
	}
	var offered []spool.Pointer
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(p spool.Pointer) { offered = append(offered, p) }, true)
	w.pollOnce() // must not panic through a nil hook
	if len(offered) != 1 {
		t.Fatalf("capture unaffected by an absent ingest signal, got %d", len(offered))
	}
}

// WithIngestSignal is the wiring seam the daemon uses; it must compose with New
// rather than requiring a sixth positional argument at every construction.
func TestWithIngestSignalWires(t *testing.T) {
	var got []advance
	w := New(func(spool.Pointer) {}, nil, "t", time.Second, false).
		WithIngestSignal(func(source, path string) { got = append(got, advance{source, path}) })
	if w.advanced == nil {
		t.Fatal("WithIngestSignal did not install the hook")
	}
	w.advanced("claude_code", "/x.jsonl")
	if len(got) != 1 || got[0].source != "claude_code" || got[0].path != "/x.jsonl" {
		t.Fatalf("hook not called through: %+v", got)
	}
}

// FIRST SIGHT MUST STILL SIGNAL WHEN HISTORY IS WANTED.
//
// ⚠️ Under forward-only (the KELD_WATCH_BACKFILL default) a first sighting sets
// the cursor to EOF and returns EARLY, so `advanced` was never called. That is
// right for the PROMPT path — re-offering every historical prompt for enrichment
// would be a genuine herd — but it also meant a transcript never entered the
// block emitter's active set until it grew again. A session that ended
// yesterday could therefore never have its blocks backfilled: the emitter's
// backfill only ever reached files that were still being written.
//
// The two paths want different things from the same sighting, so they are
// separated: the cursor still jumps to EOF (no historical prompts are offered),
// and the ingest/blocks signal still fires (the sidecar can ingest the file and
// the emitter can cut its history). The signal carries coordinates only, so
// nothing about it depends on where the cursor sits.
func TestFirstSightSignalsIngestWhenHistoryIsWanted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cccccccc-0000.jsonl")
	if err := os.WriteFile(p, []byte(genuineSaying("P1", "already here before the daemon started")), 0o600); err != nil {
		t.Fatal(err)
	}
	var offered []spool.Pointer
	var rec []advance
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(pt spool.Pointer) {
		offered = append(offered, pt)
	}, false)
	w.advanced = func(source, path string) { rec = append(rec, advance{source, path}) }
	w.signalFirstSight = true

	// Through pollOnce, not scanFile: the signal is BACKLOGGED at the sighting
	// and drained by the poll, so that pacing is on the path under test.
	w.pollOnce()

	if len(rec) != 1 || rec[0].path != p {
		t.Fatalf("first sight signalled %v, want exactly one signal for %s", rec, p)
	}
	// The prompt path stays forward-only: no historical prompt is re-offered.
	if len(offered) != 0 {
		t.Fatalf("first sight offered %d historical prompts, want 0 — that is the herd "+
			"forward-only exists to prevent", len(offered))
	}
}

// With the flag off, first sight is silent exactly as before.
func TestFirstSightIsSilentWhenHistoryIsNotWanted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dddddddd-0000.jsonl")
	if err := os.WriteFile(p, []byte(genuineSaying("P1", "pre-existing")), 0o600); err != nil {
		t.Fatal(err)
	}
	var rec []advance
	w := signalWatcher(t, Root{SourceID: "claude_code", Dir: dir}, false, &rec)
	w.signalFirstSight = false

	w.pollOnce()
	if len(rec) != 0 {
		t.Fatalf("first sight signalled %v with signalFirstSight off, want silence", rec)
	}
}

// FIRST-SIGHT SIGNALS MUST BE PACED, OR THEY ARE DROPPED AND NEVER RETRIED.
//
// ⚠️ The daemon's ingest signal rides a 64-slot, path-coalescing queue whose
// policy is DROP, not retry — safe for a growing transcript, because "the next
// signal catches up". A first sighting has no next signal: the cursor is now at
// EOF and a dormant transcript never grows again. So firing every first sighting
// at once meant a machine with 2,152 known transcripts filled 64 slots and
// dropped ~2,088 permanently — measured here, the ones that most needed
// ingesting were exactly the ones lost.
//
// So first sightings go onto a backlog and drain a few per poll. The pacing is
// the same idiom the block emitter uses for its own backlog: bounded per tick,
// drained across ticks, and the work is one-time.
func TestFirstSightSignalsArePacedAcrossPolls(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < firstSightPerPoll*3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%08d-0000.jsonl", i))
		if err := os.WriteFile(p, []byte(genuineSaying("P", "pre-existing")), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	var rec []advance
	w := signalWatcher(t, Root{SourceID: "claude_code", Dir: dir}, false, &rec)
	w.signalFirstSight = true

	// First poll sights them all but must signal only a paced slice.
	w.pollOnce()
	if len(rec) != firstSightPerPoll {
		t.Fatalf("first poll signalled %d, want %d — an unpaced burst is what the "+
			"64-slot queue drops", len(rec), firstSightPerPoll)
	}
	// Successive polls drain the rest; nothing is lost.
	w.pollOnce()
	w.pollOnce()
	if len(rec) != len(paths) {
		t.Fatalf("after three polls signalled %d of %d — the backlog must drain, not drop",
			len(rec), len(paths))
	}
	seen := map[string]int{}
	for _, a := range rec {
		seen[a.path]++
	}
	for _, p := range paths {
		if seen[p] != 1 {
			t.Errorf("%s signalled %d times, want exactly 1", filepath.Base(p), seen[p])
		}
	}
	// And the backlog is empty: a fourth poll signals nothing.
	before := len(rec)
	w.pollOnce()
	if len(rec) != before {
		t.Fatalf("a drained backlog kept signalling: %d -> %d", before, len(rec))
	}
}
