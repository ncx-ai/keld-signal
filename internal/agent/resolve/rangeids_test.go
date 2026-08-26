package resolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rangeBase is the instant every fixture in this file is written relative to.
var rangeBase = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)

func at(sec int) float64 { return float64(rangeBase.Add(time.Duration(sec) * time.Second).Unix()) }
func stamp(sec int) string {
	return rangeBase.Add(time.Duration(sec) * time.Second).Format(time.RFC3339)
}
func promptLine(id string, sec int) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u-%s","promptId":%q,"timestamp":%q,`+
		`"message":{"role":"user","content":"distinctive task %s"}}`, id, id, stamp(sec), id)
}
func assistantLine(id string, sec int) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","timestamp":%q,`+
		`"message":{"role":"assistant","content":"working on %s"}}`, id, stamp(sec), id)
}

// padding is a tool_result line — a `"type":"user"` record that is agent output,
// which is where a real transcript's bytes are. `size` bytes of blob each.
func padding(i, sec, size int) string {
	b, _ := json.Marshal(map[string]any{
		"type": "user", "uuid": fmt.Sprintf("t%d", i), "timestamp": stamp(sec),
		"toolUseResult": map[string]any{"ok": true},
		"message": map[string]any{"role": "user",
			"content": []any{map[string]any{"type": "tool_result", "content": strings.Repeat("x", size)}}},
	})
	return string(b)
}

func writeRangeTranscript(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(joinRecentLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// withTailBytes shrinks the TAIL scan's byte window for the duration of one
// test. See the note on idTailBytes: the bug this file exists for is a property
// OF that bound, and no fixture small enough to be a fixture can have a head
// outside a 16 MB window.
func withTailBytes(t *testing.T, n int64) {
	t.Helper()
	prev := idTailBytes
	idTailBytes = n
	t.Cleanup(func() { idTailBytes = prev })
}

func ids(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q (whole answer %v)", i, got[i], want[i], got)
		}
	}
}

// ⚠️ THE REGRESSION TEST — the one that would have caught the measured bug. On a
// real 20 MB transcript the block emitter published 72 blocks with `covers`
// EMPTY on every one, because the prompts sat in the first 4 MB and the ids
// lister read only a 16 MB tail.
//
// WHY IT SHRINKS THE BOUND INSTEAD OF WRITING 16 MB: the failure is a property
// of the tail bound relative to the range asked for, not of any absolute file
// size, so the honest fixture is "a range that lies outside the tail window" —
// which a shrunk bound expresses exactly, in a few hundred bytes, and which a
// 16 MB generated fixture would express only incidentally (and would put ~16 MB
// of writing into every `go test ./...`). The premise is ASSERTED rather than
// assumed: the tail scan is run first and must miss the prompts, so the test
// cannot quietly stop testing anything if the bound moves.
func TestPromptIDsInRangeAnswersBelowTheTailWindow(t *testing.T) {
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines,
			promptLine(fmt.Sprintf("p%d", i), i*60),
			assistantLine(fmt.Sprintf("p%d", i), i*60+5))
	}
	p := writeRangeTranscript(t, lines)

	withTailBytes(t, 400) // a couple of records' worth
	tail := RecentPromptIDs("claude_code", p, "", 64)
	for _, id := range tail {
		if id == "p1" || id == "p2" || id == "p3" {
			t.Fatalf("premise broken: the tail scan already sees the head (%v)", tail)
		}
	}
	if len(tail) == 0 {
		t.Fatal("premise broken: the tail scan must still answer something")
	}

	// The same ground, asked as a RANGE: the head of the file, far below the
	// tail window. A tail-shaped implementation returns nothing here.
	got := PromptIDsInRange("claude_code", p, at(60), at(180), 64)
	ids(t, got, "p1", "p2", "p3")
}

// Both edges, exactly: strictly-inside prompts come back, the ones after toTS do
// not, and the last one at or before fromTS leads the answer.
func TestPromptIDsInRangeBoundsBothEdges(t *testing.T) {
	lines := []string{
		promptLine("p1", 0),
		promptLine("p2", 120), // the last prompt at or before fromTS (180)
		promptLine("p3", 240),
		promptLine("p4", 300),
		promptLine("p5", 600), // after toTS
	}
	p := writeRangeTranscript(t, lines)
	got := PromptIDsInRange("claude_code", p, at(180), at(420), 64)
	ids(t, got, "p2", "p3", "p4")

	// n bounds the answer and the LEADING id is the one kept: it is the only id
	// a later sweep's range can never re-offer.
	if got := PromptIDsInRange("claude_code", p, at(180), at(420), 2); len(got) != 2 ||
		got[0] != "p2" || got[1] != "p3" {
		t.Fatalf("n=2 got %v, want p2,p3", got)
	}
	// A range holding nothing at all, with nothing before it either.
	if got := PromptIDsInRange("claude_code", p, at(-600), at(-300), 64); got != nil {
		t.Fatalf("empty range got %v, want nil", got)
	}
}

// ⚠️ THE LONG-RUNNING-EPISODE CASE, which is what `covers` exists for. The block
// opens in the middle of an episode that began an hour and a megabyte earlier;
// without the leading id the block reads as unattended work.
//
// The padding is what makes this a real test of the mechanism rather than of a
// small slice: the previous prompt sits >1 MB back, so it is out of reach of the
// binary search's landing point AND of the first backward section, and only the
// doubling lookbehind finds it. The same padding pushes the range's start deep
// into the file, so the binary search is genuinely exercised (a fixture under
// one probe grain would skip it entirely).
func TestPromptIDsInRangeReturnsTheLastPromptBeforeItEvenAMegabyteBack(t *testing.T) {
	lines := []string{promptLine("older", 0), assistantLine("older", 5)}
	for i := 0; i < 20; i++ {
		lines = append(lines, padding(i, 30, 64*1024)) // ~1.3 MB of agent output
	}
	lines = append(lines, promptLine("current", 3600), assistantLine("current", 3605))
	p := writeRangeTranscript(t, lines)
	if st, err := os.Stat(p); err != nil || st.Size() < 1<<20 {
		t.Fatalf("premise: fixture should be over a megabyte, got %v/%v", st, err)
	}

	got := PromptIDsInRange("claude_code", p, at(3500), at(3700), 64)
	ids(t, got, "older", "current")

	// And the binary search really did skip most of the file rather than
	// scanning from zero: the start offset it picked is deep in.
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	off := firstOffsetAtOrAfter(f, st.Size(), at(3500)-rangeSlackSeconds)
	if off < st.Size()/2 {
		t.Fatalf("start offset %d of %d: the search did not narrow", off, st.Size())
	}
	// Conservative in the safe direction, and landing on a RECORD BOUNDARY: the
	// first complete record at that offset is not already past the range start,
	// so nothing in range can lie before it.
	br, ok := recordReader(f, off, st.Size())
	if !ok {
		t.Fatal("no reader at the offset the search returned")
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("the offset is not a record boundary: %v", err)
	}
	ts, hasTS := lineTimestamp(line)
	if !hasTS {
		t.Fatalf("the record at the offset has no timestamp: %.80q", line)
	}
	if ts > at(3500) {
		t.Fatalf("landed at ts %v, past the range start %v", ts, at(3500))
	}
}

// A transcript is only APPROXIMATELY chronological — 9 turns in 9,937 measured
// carry a timestamp preceding the line before them. A prompt that belongs in
// range must not be lost to that, wherever the binary search happens to land,
// and the answer must still come out ascending BY INSTANT.
func TestPromptIDsInRangeSurvivesNonMonotonicTimestamps(t *testing.T) {
	lines := []string{promptLine("head", 0)}
	for i := 0; i < 12; i++ {
		lines = append(lines, padding(i, 10, 64*1024)) // pad so the search must narrow
	}
	lines = append(lines,
		promptLine("second", 600), // written first...
		promptLine("first", 595),  // ...but stamped earlier: out of order
		promptLine("third", 700),
	)
	p := writeRangeTranscript(t, lines)

	// "head" leads it as the last prompt at or before fromTS; the two
	// out-of-order records both come back, ordered by INSTANT rather than by
	// file position.
	got := PromptIDsInRange("claude_code", p, at(590), at(800), 64)
	ids(t, got, "head", "first", "second", "third")
}

// ⚠️⚠️ THE PRIVACY ASSERTION. This value rides an HTTP request to the sidecar,
// so it must be ids and nothing else. Asserted against the transcript's OWN
// text — via the text-returning sibling — rather than against a shape, so a
// refactor that reached for the message body would fail here.
func TestPromptIDsInRangeReturnsIDsAndNeverPromptText(t *testing.T) {
	lines := []string{
		promptLine("p1", 0), assistantLine("p1", 5),
		promptLine("p2", 60), promptLine("p3", 120),
	}
	p := writeRangeTranscript(t, lines)
	got := PromptIDsInRange("claude_code", p, at(0), at(120), 64)
	if len(got) != 3 {
		t.Fatalf("got %v, want three ids", got)
	}
	texts := RecentPrompts("claude_code", p, "p3", 10)
	if len(texts) == 0 {
		t.Fatal("premise: the fixture must have prompt text for this to assert anything")
	}
	for _, id := range got {
		for _, txt := range texts {
			if id == txt {
				t.Fatalf("PromptIDsInRange returned prompt TEXT: %q", id)
			}
		}
		if strings.Contains(id, " ") || strings.Contains(id, "distinctive") {
			t.Fatalf("PromptIDsInRange returned something that is not an id: %q", id)
		}
	}
}

// The same human-prompt filter the tail scan applies, for the same load-bearing
// reason: the non-human `"type":"user"` records ARE in the sidecar's turn index,
// so an id for one would resolve to an instant and silently turn the episode
// mapping into a mapping over turns.
func TestPromptIDsInRangeAppliesTheWatchersHumanPromptFilter(t *testing.T) {
	p := writeRangeTranscript(t, []string{
		promptLine("p1", 0),
		fmt.Sprintf(`{"type":"user","uuid":"m","promptId":"meta","timestamp":%q,"isMeta":true,`+
			`"message":{"role":"user","content":"Caveat: injected"}}`, stamp(30)),
		fmt.Sprintf(`{"type":"user","uuid":"s","promptId":"side","timestamp":%q,"isSidechain":true,`+
			`"message":{"role":"user","content":"subagent instructions"}}`, stamp(40)),
		padding(0, 50, 32),
		promptLine("p2", 60),
	})
	ids(t, PromptIDsInRange("claude_code", p, at(0), at(120), 64), "p1", "p2")
}

// One promptId can span several transcript lines (~7% do). A repeat would define
// a zero-length episode at the same instant and steal the real one's span.
func TestPromptIDsInRangeCollapsesOneIDSpanningSeveralLines(t *testing.T) {
	p := writeRangeTranscript(t, []string{
		promptLine("p1", 60), promptLine("p1", 61),
		promptLine("p2", 120),
	})
	ids(t, PromptIDsInRange("claude_code", p, at(0), at(180), 64), "p1", "p2")
	// And when the first occurrence is BEFORE the range, the id is the leading
	// one — not a second entry inside it.
	ids(t, PromptIDsInRange("claude_code", p, at(61), at(180), 64), "p1", "p2")
}

// Malformed and timestamp-less records are skipped, never fatal: the format is
// Claude Code's and may drift, and a best-effort decoration must not lose the
// rest of the file over one line.
func TestPromptIDsInRangeSkipsUnusableLines(t *testing.T) {
	p := writeRangeTranscript(t, []string{
		promptLine("p1", 60),
		`{"type":"user","promptId":"broken","timestamp":`, // truncated JSON
		`{"type":"user","uuid":"n","promptId":"nots","message":{"role":"user","content":"no timestamp"}}`,
		fmt.Sprintf(`{"type":"user","uuid":"b","promptId":"badts","timestamp":"not-a-time",` +
			`"message":{"role":"user","content":"unparseable stamp"}}`),
		promptLine("p2", 120),
	})
	ids(t, PromptIDsInRange("claude_code", p, at(0), at(180), 64), "p1", "p2")
}

func TestPromptIDsInRangeRejectsUnusableInput(t *testing.T) {
	p := writeRangeTranscript(t, []string{promptLine("p1", 60)})
	if got := PromptIDsInRange("claude_code", p, at(0), at(120), 0); got != nil {
		t.Fatalf("n=0 got %v, want nil", got)
	}
	if got := PromptIDsInRange("claude_code", "", at(0), at(120), 64); got != nil {
		t.Fatalf("no path got %v, want nil", got)
	}
	if got := PromptIDsInRange("claude_code", p, at(120), at(0), 64); got != nil {
		t.Fatalf("inverted range got %v, want nil", got)
	}
	// A source with no range reader at all answers nil rather than guessing a
	// format. Codex/Gemini are never eligible for the block path (their
	// transcripts are not what the analysis resolves), so this is the honest
	// answer rather than a gap.
	if got := PromptIDsInRange("gemini", p, at(0), at(120), 64); got != nil {
		t.Fatalf("gemini got %v, want nil", got)
	}
	if got := PromptIDsInRange("nosuchtool", p, at(0), at(120), 64); got != nil {
		t.Fatalf("unknown source got %v, want nil", got)
	}
	// Cowork shares the Claude Code shape and IS eligible.
	if got := PromptIDsInRange("cowork", p, at(0), at(120), 64); len(got) != 1 {
		t.Fatalf("cowork got %v, want one id", got)
	}
}
