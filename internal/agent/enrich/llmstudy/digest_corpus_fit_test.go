//go:build llmstudy

// Real-corpus prompt-fit probe. Needs no model — it assembles prompts only.
//
//	go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run RealCorpus -v
package llmstudy

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// corpusFitSessions is how many mined sessions the probe covers. 20 is what the round-3
// review used to find the byte/rune unit mismatch (6 of 293 refine steps panicking), so the
// same figure keeps the two measurements comparable. KELD_CORPUS_FIT_SESSIONS widens it —
// more real sessions is the only honest way to make this probe more sensitive, and it is the
// lever to reach for rather than inventing pressure the corpus does not contain.
func corpusFitSessions() int {
	if v := os.Getenv("KELD_CORPUS_FIT_SESSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 20
}

// TestRealCorpusPromptsNeverTripTheBackstop is the acceptance probe for task-7b: EVERY
// synthetic worst case on this branch has been wrong, five times, in the same way — a test
// certified a bound the code did not enforce. So the last word is not a construction at all.
// It is the real corpus.
//
// It mines transcripts off this machine (StratifiedTranscripts, Mine at the sweep's K=12,
// Observe, FactsFrom — the sweep's own call path, at the sweep's own parameters), assembles
// BOTH prompt paths at every step of every session, and asserts the backstop never fires.
// A panic here is a sweep that aborts partway, which is why "zero" is the bar rather than
// "rare".
//
// Why real text and not another synthetic construction: the defect this probe exists to
// catch is invisible to ASCII. fitDiscretionary reserved in RUNES while the assembly charged
// b.Len() in BYTES, so the shortfall was exactly the multi-byte excess of the assembled
// prefix — em dashes, arrows and emoji, 8-18 bytes' worth in a real mined session view, none
// of it present in a construction built from strings.Repeat("a", n).
//
// Measured, on this machine's whole qualifying corpus (29 sessions, 1,087 steps, 2,174
// prompts): 0 panics, tightest window margin +20 runes of content over the 1,600 floor,
// largest prompt 14,000 of 14,000. Reverting the byte->rune fix in the two assemblies
// reproduces the defect through this probe — 1 refine step panics and the tightest margin
// drops to +8 — which is how the fix was verified rather than argued for. (The round-3 review
// found 6 of 293 with a heavier prev/record construction of its own; the rate differs, the
// defect does not.)
//
// The MARGIN is reported, not just the count. A single worst-case figure has understated the
// risk four times on this branch, and the tightest margin over a thousand real steps is the
// honest statement of how much room the floor actually has: +20 runes is real but thin, and
// it is thin because fitDiscretionary hands the window exactly its reserve and no more.
//
// Skips when the corpus is absent, so it is a probe an operator runs on a machine that has
// one — it is under the llmstudy tag with the rest of the harness and never runs in `go
// test ./...`.
func TestRealCorpusPromptsNeverTripTheBackstop(t *testing.T) {
	files := StratifiedTranscripts()
	if len(files) == 0 {
		t.Skip("no transcripts on this machine")
	}
	o := DefaultMineOpts()
	o.K = 12 // the sweep's own K, not DefaultMineOpts' classification width

	var (
		sessions, steps    int
		refinePanics       int
		createPanics       int
		tightestRefine     = DefaultPromptCharBudget
		tightestCreate     = DefaultPromptCharBudget
		worstRefineTotal   int
		worstCreateTotal   int
		tightestRefineWhen string
		tightestCreateWhen string
	)
	for _, f := range files {
		if sessions >= corpusFitSessions() {
			break
		}
		ws, e1 := Mine(f, o)
		ocs, e2 := Outcomes(f, o)
		if e1 != nil || e2 != nil || len(ws) < 16 || len(ws) != len(ocs) {
			continue
		}
		sessions++
		project := projectFromPath(f)

		rec := SessionRecord{}
		var beats []Beat
		var prevSrc string
		for idx, w := range ws {
			sig := Extract(w)
			src := Render(w)
			view := RenderSessionView(w)
			shifted := idx > 0 && SubjectShifted(prevSrc, src)
			rec = rec.Observe(w, sig).WithProject(project)
			// The record the sweep will hand over carries a focus and its turning points, so
			// a probe that omits them measures a lighter prompt than the sweep sends. The
			// turning points are REAL (noted on a measured SubjectShifted, which is what
			// TriggerFocusShift means); the focus LABELS are a stand-in sized like a real
			// pair, because the EWMA focus comes from the classification pipeline, which this
			// offline probe does not run — flagged rather than passed off as measured.
			rec = rec.WithFocus("engineering", "software-development", 0.87)
			if shifted {
				rec = rec.NoteTurningPoint(idx, TriggerFocusShift)
			}
			facts := FactsFrom(sig, ocs[:idx+1]).WithWindow(w).WithPlace("", "", project)
			// A beat per step, built from the step's own real text rather than generated by
			// a model: the beat ladder's SIZE is what the budget cares about, and BeatCap is
			// what bounds it either way. Real text so the multi-byte content is real.
			beats = append(beats, Beat{
				Ordinal:        idx + 1,
				Text:           clipProse(strings.TrimSpace(strings.ReplaceAll(src, "\n", " ")), BeatCap),
				ChangedSubject: shifted,
			})
			steps++

			// Create path.
			func() {
				defer func() {
					if r := recover(); r != nil {
						createPanics++
						t.Errorf("CREATE PANIC s%d i%d: %v", sessions, idx, r)
					}
				}()
				p, window := createPromptAndWindow("work session", src, view, facts.Block())
				total, margin := len([]rune(p)), windowMargin(window)
				if margin < tightestCreate {
					tightestCreate, tightestCreateWhen = margin, sprintStep(sessions, idx, project)
				}
				if total > worstCreateTotal {
					worstCreateTotal = total
				}
			}()

			// Refine path, from a prior report built out of this session's own real text.
			func() {
				defer func() {
					if r := recover(); r != nil {
						refinePanics++
						t.Errorf("REFINE PANIC s%d i%d: %v", sessions, idx, r)
					}
				}()
				why := TriggerVolume
				if shifted {
					why = TriggerFocusShift
				}
				in := RefineInput{
					SessionLabel: "work session",
					Record:       rec,
					Beats:        beats,
					SessionView:  view,
					NewTurns:     src,
					Why:          why,
				}
				p, window := updatePromptAndWindow(corpusPrev(ws, idx), in)
				total, margin := len([]rune(p)), windowMargin(window)
				if margin < tightestRefine {
					tightestRefine, tightestRefineWhen = margin, sprintStep(sessions, idx, project)
				}
				if total > worstRefineTotal {
					worstRefineTotal = total
				}
			}()
			prevSrc = src
		}
	}

	if sessions == 0 {
		t.Skip("no session in the corpus met the probe's minimum length")
	}
	t.Logf("real corpus: %d sessions, %d steps, both prompt paths per step (%d prompts)",
		sessions, steps, steps*2)
	t.Logf("PANICS: refine %d, create %d (the bar is zero — a panic aborts a sweep)",
		refinePanics, createPanics)
	t.Logf("tightest window margin over the floor: refine %s, create %s",
		marginReport(tightestRefine, tightestRefineWhen), marginReport(tightestCreate, tightestCreateWhen))
	t.Logf("largest assembled prompt: refine %d runes, create %d (budget %d)",
		worstRefineTotal, worstCreateTotal, DefaultPromptCharBudget)
	if refinePanics+createPanics > 0 {
		t.Errorf("%d of %d real steps could not assemble a prompt — a sweep would abort part "+
			"way through, not lose one digest", refinePanics+createPanics, steps*2)
	}
}

// windowMargin is how far a window's CONTENT sits above MinTurnChars — the same quantity the
// backstop checks (notice excluded; see promptBudgetViolation). Reported as a margin rather
// than a length so "how close did the real corpus come to the floor" is answerable directly.
//
// An UNCLIPPED window (a genuinely short conversation, no notice) is not measured against the
// floor at all — the floor is a promise about how much room fitTurns is GIVEN when it has to
// clip, not a padding requirement on a session that had little to say. Those steps report the
// budget's full width so they cannot become the reported minimum.
func windowMargin(window string) int {
	content, clipped := strings.CutPrefix(window, omittedNotice)
	if !clipped {
		return DefaultPromptCharBudget
	}
	return len([]rune(content)) - MinTurnChars
}

// marginReport keeps "never clipped" distinguishable from a huge margin: an unclipped window
// is not a comfortable one, it is a step the floor never applied to, and reporting it as a
// number would make the corpus look safer than it was measured to be.
func marginReport(margin int, when string) string {
	if margin == DefaultPromptCharBudget {
		return "never clipped on any step (no conversation exceeded its room)"
	}
	return fmt.Sprintf("%+d (%s)", margin, when)
}

func sprintStep(session, idx int, project string) string {
	return fmt.Sprintf("s%d i%d %s", session, idx, project)
}

// corpusPrev builds the prior report a refinement at step idx would be given, out of the
// session's OWN real text.
//
// The point is that every rune the prompt pays for is real: the retain-list comes from
// Identifiers over real prose (so real identifier shapes and real density), the open items
// are real sentences, and the multi-byte characters are the ones the corpus actually
// contains. Sections are filled to their caps from the windows already consumed, which is
// what a steady-state prev looks like after CapSections has run on it.
func corpusPrev(ws []Window, idx int) Digest {
	var seen strings.Builder
	for i := 0; i <= idx && i < len(ws); i++ {
		seen.WriteString(Render(ws[i]))
		seen.WriteString("\n")
	}
	prose := strings.TrimSpace(seen.String())
	if prose == "" {
		return Digest{}
	}
	d := Digest{
		Synopsis:  clipProse(prose, DefaultSynopsisCap),
		Done:      clipProse(prose, DefaultProseCap),
		Happened:  clipProse(prose, DefaultHappenedCap),
		Structure: clipProse(prose, DefaultStructureCap),
		Current:   clipProse(prose, DefaultProseCap),
		Why:       clipProse(prose, DefaultProseCap),
		Next:      clipProse(prose, DefaultProseCap),
	}
	// Real sentences as list entries, at the count and per-entry length the stored report
	// is actually capped to.
	sentences := strings.FieldsFunc(prose, func(r rune) bool { return r == '.' || r == '\n' })
	for i := 0; i < DefaultListCap && i < len(sentences); i++ {
		s := clipProse(strings.TrimSpace(sentences[len(sentences)-1-i]), DefaultListEntryCap)
		if s == "" {
			continue
		}
		d.Insights = append(d.Insights, s)
		d.Unresolved = append(d.Unresolved, s)
	}
	if len(d.Unresolved) == 0 {
		d.Unresolved = []string{"none - the work reached a stopping point"}
	}
	return d
}
