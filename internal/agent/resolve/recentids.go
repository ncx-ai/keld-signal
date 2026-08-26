package resolve

import (
	"bufio"
	"io"
	"os"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/watch"
)

// RecentIDReader is an optional capability: return the IDS of the human prompts
// preceding currentPromptID, newest-first, up to n. Readers that can't provide
// history omit it.
//
// ⚠️ THE SIBLING OF RecentReader, AND THE DIFFERENCE IS THE POINT. RecentReader
// returns prompt TEXT, because it exists to fill a model's context window on
// this device. This one returns ids and nothing else, because its consumer is
// the sidecar: the ids ride a POST /blocks request so the block digest can map
// EPISODES onto blocks (see sidecar/app/analysis/blocks.py's `covers`), and raw
// prompt text must never cross that call. The two are deliberately separate
// methods rather than one method with a flag — a flag is a text leak one wrong
// argument away.
type RecentIDReader interface {
	RecentUserPromptIDs(transcriptPath, currentPromptID string, n int) []string
}

// RecentPromptIDs returns up to n prior human prompt IDS (newest-first) for the
// source, or nil when the source has no history reader or inputs are empty.
// Best-effort: this is decoration on a facet, never a reason to fail a job.
//
// NEVER TEXT. See RecentIDReader.
func RecentPromptIDs(source, transcriptPath, currentPromptID string, n int) []string {
	if n <= 0 || transcriptPath == "" {
		return nil
	}
	r, ok := readers[source]
	if !ok {
		return nil
	}
	rr, ok := r.(RecentIDReader)
	if !ok {
		return nil
	}
	return rr.RecentUserPromptIDs(transcriptPath, currentPromptID, n)
}

// idTailBytes bounds how much of a transcript's tail the ids scan reads. It is
// 128x recentTailBytes, and that is a MEASURED correction rather than caution.
//
// The two scans want different things from the same file. RecentPrompts wants
// the last three prompts' text and 128 KB is generous for that. This one wants
// every human prompt inside the current block of work, and human prompts are
// separated by the AGENT's output — which is where a transcript's bytes are.
// Measured over the five largest Claude Code transcripts on one machine (20-90
// MB): the last 128 KB holds 0, 0, 1, 1 and 6 human prompts, and reaching the
// five most recent costs up to 6.6 MB. A 20-minute window of the tail — the
// widest a block can be — costs up to 1.6 MB. So at 128 KB this scan would
// usually return NOTHING and the episode mapping would be silently empty on
// exactly the autonomous sessions it exists to describe.
//
// 16 MB clears the worst measured figure by ~2.4x while still refusing to read
// a 90 MB transcript whole. It is a second, INDEPENDENT bound on top of the
// caller's count (see enrich's recentPromptIDBudget): the count says how many
// ids are useful, this says how much file that is allowed to cost, and whichever
// binds first wins. Cost, measured on a 16 MB synthetic transcript of realistic
// shape: see TestRecentPromptIDsCostOnAFullBudgetTail.
const idTailBytes = 16 << 20

// RecentUserPromptIDs tail-scans the transcript (bounded window) for HUMAN
// prompts, excludes currentPromptID, and returns up to n ids newest-first. Like
// RecentUserPrompts it deliberately does NOT use or advance the append-only
// cursor from Read — history re-reads a bounded tail so current-prompt reads
// stay correct. Any error yields nil.
//
// The filter is watch.HumanPromptID, not this file's own userPrompt, and that is
// load-bearing rather than tidy. userPrompt asks "is this a user line with
// text", which admits the injected/caveat (`isMeta`) and subagent (`isSidechain`)
// records the UserPromptSubmit hook never fires for. Those records ARE in the
// sidecar's turn index, so they would resolve to an instant and become episodes
// of their own — the mapping would then be over turns rather than over prompts,
// which is the exact failure the daemon-owns-the-filter split exists to prevent.
//
// Duplicate ids are collapsed to their FIRST occurrence: one promptId can span
// several transcript lines (measured, ~7% of them do), and a repeat would define
// a zero-length episode that steals the real one's span.
func (r *ClaudeReader) RecentUserPromptIDs(path, currentPromptID string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	var off int64
	if st.Size() > idTailBytes {
		off = st.Size() - idTailBytes
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	br := bufio.NewReaderSize(f, 64*1024)
	if off > 0 {
		// Drop the first (possibly partial) line so we only parse complete records.
		if _, err := br.ReadString('\n'); err != nil {
			return nil
		}
	}
	var ids []string
	seen := map[string]bool{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: trailing partial line not consumed
		}
		// A cheap pre-filter before the JSON parse, and it earns its keep: in
		// Claude Code a TOOL RESULT is also a `"type":"user"` line, and tool
		// results are where a transcript's bytes are. Skipping them unparsed is
		// the same trick the sidecar's own tail parser uses
		// (sidecar/app/analysis/transcript.py:turns_in). Both substrings assume
		// the compact form Claude Code writes; a pretty-printed transcript would
		// fall through this filter to watch.HumanPromptID, which is authoritative
		// — the filter can only cost work, never correctness. (`tool_result` is
		// rejected outright rather than kept when a `tool_use` sits beside it,
		// because a HUMAN prompt line never carries either.)
		if !strings.Contains(line, `"type":"user"`) || strings.Contains(line, `"tool_result"`) {
			continue
		}
		id, ok := watch.HumanPromptID([]byte(line))
		if !ok || id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	out := make([]string, 0, n)
	for i := len(ids) - 1; i >= 0 && len(out) < n; i-- {
		if ids[i] == currentPromptID {
			continue
		}
		out = append(out, ids[i])
	}
	return out
}
