package resolve

import (
	"bufio"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/watch"
)

// RangeIDReader is an optional capability: return the IDS of the human prompts
// a TIME RANGE of a transcript holds. Readers that cannot answer about time
// omit it.
//
// ⚠️ WHY THIS IS A SIBLING OF RecentIDReader AND NOT A WIDENING OF IT.
// RecentUserPromptIDs answers "the prompts nearest the END of the file", which
// it does by reading a bounded TAIL (idTailBytes). That is exactly right for
// its consumer — a model's context window, which is about the present — and
// exactly wrong for the block emitter, which drains blocks CHRONOLOGICALLY from
// a persisted cursor. Measured on a real 20 MB transcript: 72 blocks emitted,
// 0 of them with any `covers`, because every one of their prompts sat in the
// first 4 MB, outside a 16 MB tail. A range question needs a range answer, so
// this method exists rather than a flag on that one.
//
// ⚠️⚠️ IDS ONLY, AND THAT IS THE POINT — the same rule RecentIDReader states.
// resolve.RecentPrompts returns prompt TEXT, because it fills a model's context
// window on this device. This value rides an HTTP request to the sidecar
// (POST /blocks, the `covers` mapping), so it must never read, return or log
// message text. The two capabilities stay separate methods rather than one
// method with a flag: a flag is a text leak one wrong argument away.
type RangeIDReader interface {
	PromptIDsInRange(transcriptPath string, fromTS, toTS float64, n int) []string
}

// PromptIDsInRange returns the HUMAN prompt ids of the transcript whose
// instants fall in [fromTS, toTS] — ASCENDING, bounded by n — plus the last
// human prompt at or before fromTS. Nil when the source has no range reader or
// the inputs are unusable. Best-effort: this is decoration on a facet, never a
// reason to fail a job.
//
// The leading at-or-before prompt is not a courtesy. `covers` exists to say
// which EPISODE was responsible for a block, and an episode that began before
// the block still covers the block's opening — that long-running-episode case
// is the whole reason the feature exists. Without it a block cut in the middle
// of an hour of autonomous work reads as unattended.
//
// NEVER TEXT. See RangeIDReader.
func PromptIDsInRange(source, transcriptPath string, fromTS, toTS float64, n int) []string {
	if n <= 0 || transcriptPath == "" || toTS < fromTS {
		return nil
	}
	r, ok := readers[source]
	if !ok {
		return nil
	}
	rr, ok := r.(RangeIDReader)
	if !ok {
		return nil
	}
	return rr.PromptIDsInRange(transcriptPath, fromTS, toTS, n)
}

// rangeSlackSeconds is how generously the range is bracketed at both edges
// before the exact timestamp test is applied.
//
// It exists because a transcript is only APPROXIMATELY chronological: measured
// in this repo, 9 turns in 9,937 carry a timestamp preceding the line before
// them (see AGENTS.md on the ingest watermark). A binary search that trusted an
// exact boundary would land one line late on exactly those files and silently
// drop a prompt. So the search targets fromTS - slack, the forward scan stops
// only at toTS + slack, and membership is decided by the record's own
// timestamp rather than by where the scan started or stopped. Five minutes is
// the reference series' own bin width — the coarsest granularity anything
// downstream reasons at — and costs one bin of extra reading.
const rangeSlackSeconds = 300.0

// rangeProbeGrain is where the binary search stops halving and hands over to
// the forward scan. 64 KB is the scan's own buffer size: narrowing below one
// buffered read buys nothing.
const rangeProbeGrain = 64 << 10

// rangeProbeLines bounds how many complete records one probe will read looking
// for a timestamp. Effectively one in a real transcript; the bound only keeps a
// probe that landed in a run of malformed lines from turning into a whole-file
// read.
const rangeProbeLines = 8

// rangeLookbehindBytes bounds the backward search for the last human prompt at
// or before fromTS.
//
// A bound is needed because the distance to the previous human prompt is
// unbounded in BYTES even when it is small in TIME: human prompts are separated
// by the agent's output, and measured on the five largest transcripts on one
// machine reaching the previous prompt costs up to 6.6 MB. 16 MB clears that by
// ~2.4x. ⚠️ This is NOT idTailBytes reintroduced: it bounds how far back ONE
// leading prompt may be looked for, not which part of the file the method can
// see at all. A range in the first 4 MB of a 90 MB transcript is answered in
// full either way.
const rangeLookbehindBytes = 16 << 20

// PromptIDsInRange answers the range question for Claude-Code-shaped JSONL
// (Claude Code and Cowork).
//
// THE ALGORITHM, and why it is not a scan from byte zero. Transcripts are
// chronological and append-only, so the start of a range is BINARY-SEARCHABLE:
//
//  1. Halve the file looking for the first complete record at or after
//     fromTS - slack (~11 probes on a 90 MB file, each reading one line).
//  2. Forward-scan from there, collecting human prompt ids, and STOP as soon as
//     a timestamp passes toTS + slack. Cost is then proportional to the RANGE,
//     not to the file.
//  3. If that scan saw no human prompt before fromTS, search BACKWARDS from the
//     start offset in doubling chunks for the last one — the leading
//     at-or-before prompt `covers` needs.
//
// Robustness, all of it deliberate: a line with no parseable timestamp is
// skipped rather than fatal, an unparseable line is skipped by the same filter
// the tail scan uses, and nothing assumes STRICT monotonicity — the bracket is
// generous and every membership decision is made against the record's own
// timestamp. An inconclusive probe sends the search LEFT, which can only make
// the start offset earlier, never later; reading a little too much is a cost,
// starting too late is a silent loss.
//
// The human-prompt filter is watch.HumanPromptID for the same load-bearing
// reason RecentUserPromptIDs uses it: the daemon owns exactly one definition of
// "a human prompt", so the sidecar's mapping stays over PROMPTS rather than
// over turns. Duplicate ids collapse to their FIRST occurrence (~7% of prompt
// ids span several lines), so a repeat cannot define a zero-length episode that
// steals the real one's span.
func (r *ClaudeReader) PromptIDsInRange(path string, fromTS, toTS float64, n int) []string {
	if n <= 0 || toTS < fromTS {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	size := st.Size()
	start := firstOffsetAtOrAfter(f, size, fromTS-rangeSlackSeconds)

	br, ok := recordReader(f, start, size)
	if !ok {
		return nil
	}
	var inRange []datedID
	seen := map[string]bool{}
	beforeID, beforeTS := "", math.Inf(-1)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: a trailing partial line is not a complete record
		}
		ts, hasTS := lineTimestamp(line)
		if hasTS && ts > toTS+rangeSlackSeconds {
			// Past the range and past the jitter bracket. This is the stop that
			// makes the cost proportional to the range rather than to the file.
			break
		}
		id, ok := humanPromptIn(line)
		if !ok || !hasTS || seen[id] {
			continue
		}
		switch {
		case ts < fromTS:
			// A candidate for the leading at-or-before prompt. Marked seen so
			// the same id reappearing inside the range cannot be counted twice:
			// it is one prompt, and its first occurrence is what dates it.
			seen[id] = true
			if ts >= beforeTS {
				beforeTS, beforeID = ts, id
			}
		case ts <= toTS:
			seen[id] = true
			inRange = append(inRange, datedID{ts: ts, id: id})
		}
	}
	// ASCENDING BY INSTANT, not by file position, and stably. The two differ on
	// exactly the out-of-order lines this method refuses to assume away (9 in
	// 9,937 measured turns), and an id list that claims to be ascending had
	// better be.
	sort.SliceStable(inRange, func(i, j int) bool { return inRange[i].ts < inRange[j].ts })
	if beforeID == "" && start > 0 {
		// The bracket held no earlier prompt, so look further back. Only ever
		// reached when the range opens in the middle of a long episode, which
		// is exactly the case this leading id exists for.
		beforeID = lastHumanPromptBefore(f, start, fromTS)
	}

	// Ascending, leading prompt first — it is by construction the earliest.
	// Truncation drops the NEWEST ids rather than the leading one, because the
	// leading one is the only id that cannot be recovered from a later sweep's
	// range. With a 64-id budget against a measured 3.8 human prompts an hour
	// this is unreachable in practice; it is stated so the choice is not
	// accidental.
	out := make([]string, 0, n)
	if beforeID != "" {
		out = append(out, beforeID)
	}
	for _, d := range inRange {
		if len(out) >= n {
			break
		}
		out = append(out, d.id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// datedID is an id with the instant it was found at — kept only long enough to
// order the answer. The instant is never published; the ids are.
type datedID struct {
	ts float64
	id string
}

// humanPromptIn applies the cheap pre-filter and then the authoritative human
// prompt filter, exactly as the tail scan does.
//
// The pre-filter earns its keep: in Claude Code a TOOL RESULT is also a
// `"type":"user"` line, and tool results are where a transcript's bytes are.
// Skipping them unparsed is the same trick the sidecar's own tail parser uses.
// Both substrings assume the compact form Claude Code writes; a pretty-printed
// transcript falls through to watch.HumanPromptID, which is authoritative — the
// filter can only cost work, never correctness.
func humanPromptIn(line string) (string, bool) {
	if !strings.Contains(line, `"type":"user"`) || strings.Contains(line, `"tool_result"`) {
		return "", false
	}
	id, ok := watch.HumanPromptID([]byte(line))
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// tsKey is the raw-line form of the field, matched as a substring so a
// timestamp can be read WITHOUT parsing the record. That matters twice: a
// tool_result line can be megabytes, and — the invariant — nothing here should
// ever hold a decoded message.
const tsKey = `"timestamp":"`

// lineTimestamp reads a record's instant as epoch seconds, or reports that the
// line has none. It parses ONLY the timestamp: no message, no content, no text.
func lineTimestamp(line string) (float64, bool) {
	i := strings.Index(line, tsKey)
	if i < 0 {
		return 0, false
	}
	rest := line[i+len(tsKey):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, rest[:j])
	if err != nil {
		return 0, false
	}
	return float64(t.UnixNano()) / 1e9, true
}

// recordReader reads the section [off, end) assuming off is a RECORD BOUNDARY,
// so the first line it yields is a complete record rather than a fragment. Used
// with firstOffsetAtOrAfter's answer, which is always either 0 or the start of a
// record it parsed.
//
// ⚠️ Deliberately NOT the same as arbitraryReader below, and the distinction is
// load-bearing: dropping the first line at a known boundary would silently skip
// exactly one record, and that record is the likeliest candidate for the leading
// at-or-before prompt (the search stops on records EARLIER than the target).
func recordReader(f *os.File, off, end int64) (*bufio.Reader, bool) {
	if off > end {
		return nil, false
	}
	return bufio.NewReaderSize(io.NewSectionReader(f, off, end-off), 64*1024), true
}

// arbitraryReader reads [off, end) from an offset that may land mid-record, so
// it discards the first (possibly partial) line. A record straddling off is, by
// construction, covered by whatever reads the bytes before off.
func arbitraryReader(f *os.File, off, end int64) (*bufio.Reader, bool) {
	br, ok := recordReader(f, off, end)
	if !ok {
		return nil, false
	}
	if off > 0 {
		if _, err := br.ReadString('\n'); err != nil {
			return nil, false
		}
	}
	return br, true
}

// firstOffsetAtOrAfter binary-searches for a byte offset from which a forward
// scan cannot miss a record at or after target. The answer is always a RECORD
// BOUNDARY (0, or the start of a record the search parsed) and is conservative:
// it may sit earlier than the true boundary (costing a little reading), never
// later (which would silently drop a prompt).
func firstOffsetAtOrAfter(f *os.File, size int64, target float64) int64 {
	lo, hi := int64(0), size
	for hi-lo > rangeProbeGrain {
		mid := lo + (hi-lo)/2
		ts, at, ok := probeTimestamp(f, mid, size)
		if ok && ts < target {
			// Everything up to `at` is earlier than the target, so the scan may
			// start there. `at` >= mid > lo, so the search always progresses.
			lo = at
			continue
		}
		// Either the probe is inside the range already, or it told us nothing.
		// Both send the search left, which is the safe direction.
		hi = mid
	}
	return lo
}

// probeTimestamp reads the first complete record at or after `at` that carries a
// parseable timestamp, returning that timestamp and the record's own offset. It
// does its own reading rather than going through readerFrom because it must
// TRACK the offset it hands back, which means knowing how long the discarded
// partial line was.
func probeTimestamp(f *os.File, at, size int64) (float64, int64, bool) {
	if at > size {
		return 0, 0, false
	}
	br := bufio.NewReaderSize(io.NewSectionReader(f, at, size-at), 64*1024)
	pos := at
	if at > 0 {
		s, err := br.ReadString('\n')
		if err != nil {
			return 0, 0, false
		}
		pos += int64(len(s))
	}
	for i := 0; i < rangeProbeLines; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			return 0, 0, false
		}
		off := pos
		pos += int64(len(line))
		if ts, ok := lineTimestamp(line); ok {
			return ts, off, true
		}
	}
	return 0, 0, false
}

// lastHumanPromptBefore searches backwards from end for the last human prompt
// at or before fromTS, in doubling sections.
//
// Each section is read from its start all the way to `end` rather than to the
// previous section's start. That is deliberate: a record straddling a section
// boundary would otherwise be dropped by BOTH sections (as a partial head in
// one and an unterminated tail in the other), which is a prompt lost silently.
// Re-reading costs at most ~2x the final section, and the whole search is
// bounded by rangeLookbehindBytes.
func lastHumanPromptBefore(f *os.File, end int64, fromTS float64) string {
	for span := int64(256 << 10); ; span *= 2 {
		lo := end - span
		if lo < 0 {
			lo = 0
		}
		if id := lastHumanPromptIn(f, lo, end, fromTS); id != "" {
			return id
		}
		if lo == 0 || span >= rangeLookbehindBytes {
			return ""
		}
	}
}

// lastHumanPromptIn returns the id of the latest human prompt in [lo, end)
// whose instant is at or before fromTS.
func lastHumanPromptIn(f *os.File, lo, end int64, fromTS float64) string {
	br, ok := arbitraryReader(f, lo, end)
	if !ok {
		return ""
	}
	best, bestTS := "", math.Inf(-1)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		ts, hasTS := lineTimestamp(line)
		if !hasTS || ts > fromTS {
			continue
		}
		id, ok := humanPromptIn(line)
		if !ok {
			continue
		}
		if ts >= bestTS {
			bestTS, best = ts, id
		}
	}
	return best
}
