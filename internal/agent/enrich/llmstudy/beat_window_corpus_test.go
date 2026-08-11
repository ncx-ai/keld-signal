//go:build llmstudy

package llmstudy

import "testing"

// TestCorpusBeatWindowCoverage is the before/after the design spec asks for and nothing
// measured: what fraction of a session's turns any beat window actually reads.
//
// The OLD geometry is computed here rather than remembered, because "currently far below 100%
// and unmeasured" is not a number. A beat fired at user prompt idx and was handed Mine's window
// at idx — the last K = 12 TURNS — while its stride was every record since the previous beat.
// Coverage was therefore min(window turns, stride turns) / stride turns, and the deduplication
// matters: consecutive mined windows overlap heavily, so a naive sum would count the same turn
// several times and flatter the old number.
func TestCorpusBeatWindowCoverage(t *testing.T) {
	files := StratifiedTranscripts()
	if me := ThisSessionTranscript(); me != "" {
		files = append([]string{me}, files...)
	}
	o := DefaultMineOpts()
	o.K = 12
	cadence := BeatTurnsFromEnv()
	var oldKept, oldSpan, newKept, newSpan int
	var oldWindowRunes, newWindowRunes, windows int
	n := 0
	for _, f := range files {
		if n >= 14 {
			break
		}
		ws, e1 := Mine(f, o)
		deltas, e2 := sessionDeltas(f, o)
		if e1 != nil || e2 != nil || len(ws) < 16 || len(ws) != len(deltas) {
			continue
		}
		n++
		var bw BeatWindower
		prev := 0
		for idx := 0; idx < 16; idx++ {
			if (idx+1)%cadence != 0 {
				continue
			}
			// The stride: every delta since the previous beat.
			span := 0
			for i := prev; i <= idx; i++ {
				span += len(deltas[i].Turns)
			}
			prev = idx + 1
			// OLD: the beat read Mine's window at idx, which holds at most K+1 turns of that
			// stride — and no more of it, whatever the stride's size.
			old := len(ws[idx].Turns)
			if old > span {
				old = span
			}
			oldSpan += span
			oldKept += old
			oldWindowRunes += runeLen(Render(ws[idx]))
			// NEW: the contiguous window.
			b := bw.Next(deltas, idx)
			newSpan += b.SpanTurns
			newKept += b.KeptTurns
			newWindowRunes += b.TotalRunes
			windows++
		}
	}
	if windows == 0 {
		t.Skip("no transcripts")
	}
	pc := func(a, b int) float64 { return 100 * float64(a) / float64(b) }
	t.Logf("sessions=%d beat windows=%d", n, windows)
	t.Logf("OLD (K=%d mined window): coverage %.1f%% of %d turns spanned; mean window %d runes",
		o.K, pc(oldKept, oldSpan), oldSpan, oldWindowRunes/windows)
	t.Logf("NEW (contiguous, %d-rune bound): coverage %.1f%% of %d turns spanned; mean window %d runes",
		BeatWindowChars, pc(newKept, newSpan), newSpan, newWindowRunes/windows)
	if pc(newKept, newSpan) <= pc(oldKept, oldSpan) {
		t.Errorf("the new geometry covers %.1f%% against the old %.1f%% — contiguous coverage is "+
			"the whole point of the change", pc(newKept, newSpan), pc(oldKept, oldSpan))
	}
}
