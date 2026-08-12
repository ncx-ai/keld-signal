//go:build llmstudy

package llmstudy

import (
	"strings"
	"testing"
)

// TestCorpusBeatWindowGeometry is the real-corpus probe for the beat-window design, and it
// reports the four quantities that design is judged on IN COUNTS: how many turns any beat window
// reads, how much of a window the previous beat also read, how large the assembled beat prompt
// gets against its budget, and how many times the budget backstop fires.
//
// Counts, not only rates, deliberately. Every denominator here moves — the two geometries span
// the same turns but keep different numbers of them, and the old geometry's window is a different
// size — and a rate over a moving denominator is what produced five unreadable verdicts on this
// branch already.
//
// THE OLD GEOMETRY IS COMPUTED, NOT REMEMBERED. A beat fired at user prompt idx was handed
// Mine's window at idx — the last K = 12 TURNS — while its stride was every record since the
// previous beat. Coverage is scored as the spec words it, "turns appearing in at least one beat
// window", by matching each stride turn against the turns of EVERY mined beat window of that
// session as a multiset. That method can only overstate the old number (two identical tool lines
// in different strides can match each other), which is the safe direction for a probe whose
// conclusion is that the new number is higher.
//
// Offline: no model, no generation. Prompt assembly is deterministic, so the budget and the
// backstop are measured over EVERY beat window of the walked prefix rather than over the sampled
// few a generation run can afford — and the panic count means what it says, since assembling the
// prompt is where the panic lives.
func TestCorpusBeatWindowGeometry(t *testing.T) {
	files := StratifiedTranscripts()
	if me := ThisSessionTranscript(); me != "" {
		files = append([]string{me}, files...)
	}
	o := DefaultMineOpts()
	o.K = 12
	cadence := BeatTurnsFromEnv()

	// Coverage, in turns. spanTurns is every turn of the walked prefix: the strides are
	// contiguous and disjoint by construction, so their union IS the prefix and anything neither
	// geometry keeps is read by nothing.
	var spanTurns, oldCovered, newKept int
	// Overlap, in runes of rendered window, for consecutive beats within a session.
	var oldOverlapRunes, oldPairWindowRunes int
	var newOverlapRunes, newWindowRunes, newPrevSpanRunes, newPrevWindowRunes int
	var oldWindowRunes, windows, pairs int
	// The budget, in runes of assembled prompt, and the backstop's own count.
	var oldWorstPrompt, newWorstPrompt, panics int
	var newWorstWhere string
	// Holes: strides the bound could not carry whole. Every one must be MARKED — a silently
	// skipped span is the defect the whole change exists to remove.
	var holed, holedMarked, droppedTurns int
	// What 100% coverage would actually cost, in beats. See the report line.
	var beatsAt100 int
	sessions := 0
	for _, f := range files {
		if sessions >= 14 {
			break
		}
		ws, e1 := Mine(f, o)
		deltas, e2 := sessionDeltas(f, o)
		if e1 != nil || e2 != nil || len(ws) < 16 || len(ws) != len(deltas) {
			continue
		}
		sessions++
		project := projectFromPath(f)

		// Pass 1: the mined windows the OLD geometry would have handed to beats, as a multiset of
		// rendered turns, so "in at least one beat window" is scored over all of them.
		seen := map[string]int{}
		var beatIdx []int
		for idx := 0; idx < 16; idx++ {
			if (idx+1)%cadence != 0 {
				continue
			}
			beatIdx = append(beatIdx, idx)
			for _, tn := range ws[idx].Turns {
				seen[string(tn.Role)+":"+tn.Text]++
			}
		}

		// Pass 2: walk the prefix, accumulating the record the prompt really carries.
		var rec SessionRecord
		var bw BeatWindower
		prevDelta := 0
		prevOld := -1
		for idx := 0; idx < 16; idx++ {
			rec = rec.Observe(deltas[idx], Extract(deltas[idx])).WithProject(project)
			if (idx+1)%cadence != 0 {
				continue
			}
			// The stride: every delta since the previous beat.
			var span []Turn
			for i := prevDelta; i <= idx; i++ {
				span = append(span, deltas[i].Turns...)
			}
			prevDelta = idx + 1
			spanTurns += len(span)
			for _, tn := range span {
				k := string(tn.Role) + ":" + tn.Text
				if seen[k] > 0 {
					seen[k]--
					oldCovered++
				}
			}

			// OLD: the mined window, and the beat prompt built from it.
			oldRendered := Render(ws[idx])
			oldWindowRunes += runeLen(oldRendered)
			if prevOld >= 0 {
				oldOverlapRunes += sharedTurnRunes(ws[idx].Turns, ws[prevOld].Turns)
				oldPairWindowRunes += runeLen(oldRendered)
			}
			prevOld = idx
			if n := runeLen(BeatPrompt(rec.Block(), oldRendered)); n > oldWorstPrompt {
				oldWorstPrompt = n
			}

			// NEW: the contiguous window, and the beat prompt built from it. The assembly is
			// inside a recover because assertBeatPromptWithinBudget PANICS — that is the backstop
			// being counted, not an accident being swallowed.
			b := bw.Next(deltas, idx)
			newSpanCheck := len(span)
			if b.SpanTurns != newSpanCheck {
				t.Errorf("%s idx %d: the windower spans %d turns, the stride holds %d — the "+
					"windows are not contiguous with the strides they are measured against",
					project, idx, b.SpanTurns, newSpanCheck)
			}
			newKept += b.KeptTurns
			newWindowRunes += b.TotalRunes
			newOverlapRunes += b.OverlapRunes
			newPrevSpanRunes += b.PrevSpanRunes
			newPrevWindowRunes += b.PrevWindowRunes
			if b.OverlapTurns > 0 {
				pairs++
			}
			windows++
			// The bound, on real multi-byte transcripts rather than an ASCII fixture.
			if b.TotalRunes > BeatWindowChars {
				t.Errorf("%s idx %d: window is %d runes over the %d bound",
					project, idx, b.TotalRunes-BeatWindowChars, BeatWindowChars)
			}
			if d := b.Dropped(); d > 0 {
				holed++
				droppedTurns += d
				if strings.Contains(b.Rendered, beatOmittedNotice) {
					holedMarked++
				} else {
					t.Errorf("%s idx %d: %d turns dropped and the window does not say so",
						project, idx, d)
				}
			}
			var p string
			if func() (panicked bool) {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				p = BeatPrompt(rec.Block(), b.Rendered)
				return false
			}() {
				panics++
				t.Errorf("%s idx %d: the beat prompt backstop fired — a window bounded at %d "+
					"runes assembled past the %d-rune budget", project, idx, BeatWindowChars,
					BeatPromptCharBudget)
				continue
			}
			if n := runeLen(p); n > newWorstPrompt {
				newWorstPrompt, newWorstWhere = n, project
			}
			// What 100% coverage costs, measured rather than argued: the number of beats this
			// stride would need if a beat also fired whenever the uncovered stride reached the
			// bound. The reserve is the overlap the next window would carry.
			usable := BeatWindowChars - BeatWindowChars*beatOverlapPct/100
			need := 1
			if r := turnsRuneLen(span); r > usable {
				need = (r + usable - 1) / usable
			}
			beatsAt100 += need
		}
	}
	if windows == 0 {
		t.Skip("no transcripts")
	}

	pc := func(a, b int) float64 {
		if b == 0 {
			return 0
		}
		return 100 * float64(a) / float64(b)
	}
	t.Logf("sessions=%d beat windows=%d (cadence every %d user prompts, first 16 windows of each "+
		"session — the sweep's prefix)", sessions, windows, cadence)

	// 1. Turn coverage, as counts.
	t.Logf("TURN COVERAGE  %d turns spanned by the beats' strides", spanTurns)
	t.Logf("   OLD (K=%d mined window): %d turns read, %d read by NO window  (%.1f%%)",
		o.K, oldCovered, spanTurns-oldCovered, pc(oldCovered, spanTurns))
	t.Logf("   NEW (contiguous, %d-rune bound): %d turns read, %d read by NO window  (%.1f%%)",
		BeatWindowChars, newKept, spanTurns-newKept, pc(newKept, spanTurns))
	t.Logf("   holes: %d of %d windows dropped turns (%d turns), %d of %d marked in the window",
		holed, windows, droppedTurns, holedMarked, holed)
	// 100% is what the spec demands and what ctx 8192 refuses. State the price in beats.
	t.Logf("   100%% coverage at this bound would take %d beats instead of %d (%.1fx the "+
		"inference), firing an extra beat whenever the uncovered stride reaches the bound",
		beatsAt100, windows, float64(beatsAt100)/float64(windows))

	// 2. Consecutive-window overlap, as counts of runes.
	t.Logf("CONSECUTIVE-WINDOW OVERLAP")
	t.Logf("   OLD: %d of %d window runes were also in the previous beat's window (%.1f%%), over "+
		"%d pairs", oldOverlapRunes, oldPairWindowRunes, pc(oldOverlapRunes, oldPairWindowRunes),
		windows-sessions)
	t.Logf("   NEW: %d of %d window runes carried forward (%.1f%% of the window; %.1f%% of what "+
		"the previous beat READ — the spec's 25-30%%; %.1f%% of its whole stride, reserve %d%%), "+
		"over %d pairs with any overlap",
		newOverlapRunes, newWindowRunes, pc(newOverlapRunes, newWindowRunes),
		pc(newOverlapRunes, newPrevWindowRunes), pc(newOverlapRunes, newPrevSpanRunes),
		beatOverlapPct, pairs)
	t.Logf("   mean window: OLD %d runes, NEW %d runes", oldWindowRunes/windows, newWindowRunes/windows)

	// 3. The budget, and the backstop.
	t.Logf("BEAT PROMPT  largest assembled: OLD %d runes, NEW %d runes (%s), of the %d-rune "+
		"budget — %d runes of headroom; backstop fired %d times over %d assemblies",
		oldWorstPrompt, newWorstPrompt, newWorstWhere, BeatPromptCharBudget,
		BeatPromptCharBudget-newWorstPrompt, panics, windows)

	if newKept <= oldCovered {
		t.Errorf("the new geometry reads %d turns against the old %d — contiguous coverage is the "+
			"whole point of the change", newKept, oldCovered)
	}
	if newOverlapRunes == 0 {
		t.Error("consecutive windows share no ground; ChangedSubject is comparing disjoint texts")
	}
	if panics != 0 {
		t.Errorf("%d assemblies tripped the budget backstop", panics)
	}
}

// sharedTurnRunes is how much of a's rendered length is text that also appears in b, matched as a
// multiset of whole turns. It is the only way to measure overlap between two MINED windows: they
// are built independently from the record stream, so there is no index to intersect, and turn text
// is what a model would actually be re-reading.
func sharedTurnRunes(a, b []Turn) int {
	have := map[string]int{}
	for _, tn := range b {
		have[string(tn.Role)+":"+tn.Text]++
	}
	n := 0
	for _, tn := range a {
		k := string(tn.Role) + ":" + tn.Text
		if have[k] > 0 {
			have[k]--
			n += renderedTurnLen(tn)
		}
	}
	return n
}
