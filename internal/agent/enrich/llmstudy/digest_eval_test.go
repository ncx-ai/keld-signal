//go:build llmstudy

// Live digest harness. Requires a llama-server.
//
//	DIGEST_URL=http://127.0.0.1:8095 go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run DigestSizing -v -timeout 60m
package llmstudy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/llmstudy/digeststore"
)

// projectFromPath recovers a readable project name from a Claude Code transcript
// path, whose parent directory encodes the working directory (e.g.
// "-home-dg-keld-keld-signal"). Gives the digest a real "working in" anchor without
// inventing one.
func projectFromPath(p string) string {
	d := filepath.Base(filepath.Dir(p))
	d = strings.TrimPrefix(d, "-")
	parts := strings.Split(d, "-")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// TestDigestSizing is verification test 6: can a budget-fitting model write a usable
// digest at all? Free generation is the capability class where Qwen3-0.6B collapsed
// to a single value, so this must be answered before any prompt tuning — tuning
// against the wrong model is wasted work.
func TestDigestSizing(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8095"
	}
	// Spread across projects rather than taking the first N in path order, which drew all
	// 14 sessions from keld-atlas and reported one project's results as a corpus.
	files := StratifiedTranscripts()
	if len(files) == 0 {
		t.Skip("no transcripts")
	}

	o := DefaultMineOpts()
	o.K = 12 // wider than classification uses: a digest needs more material
	l := NewLlama(url)

	tried, ok := 0, 0
	for _, f := range files {
		if tried >= 4 {
			break
		}
		ws, err := Mine(f, o)
		if err != nil || len(ws) < 8 {
			continue
		}
		ocs, err := Outcomes(f, o)
		if err != nil || len(ocs) != len(ws) {
			continue
		}
		idx := len(ws) - 1
		w := ws[idx]
		facts := FactsFrom(Extract(w), ocs[:idx+1]).
			WithWindow(w).
			WithPlace("", "", projectFromPath(f))
		tried++

		d, err := l.CreateDigestWithView("work session", Render(w), RenderSessionView(w), facts.Block())
		if err != nil {
			t.Errorf("[%d] call failed: %v", tried, err)
			continue
		}
		problems := ValidateDigest(d)
		if len(problems) > 0 {
			t.Errorf("[%d] malformed: %v", tried, problems)
			continue
		}
		ok++
		t.Logf("═══ digest %d — %s ═══", tried, filepath.Base(f))
		t.Logf("  FACTS GIVEN: %s", strings.ReplaceAll(strings.TrimSpace(facts.Block()), "\n", " | "))
		t.Logf("  done:       %s", clipLog(d.Done))
		t.Logf("  happened:   %s", clipLog(d.Happened))
		t.Logf("  structure:  %s", clipLog(d.Structure))
		t.Logf("  current:    %s", clipLog(d.Current))
		t.Logf("  why:        %s", clipLog(d.Why))
		t.Logf("  next:       %s", clipLog(d.Next))
		for i, s := range d.Insights {
			t.Logf("  insight[%d]: %s", i, clipLog(s))
		}
		for i, s := range d.Unresolved {
			t.Logf("  unresolved[%d]: %s", i, clipLog(s))
		}
		// Thresholds 2 and 7, per digest.
		if LooksFabricatedUnresolved(d, facts, Render(w)) {
			t.Logf("  ⚠ FABRICATED unresolved (threshold 7)")
		}
		if UsesUnresolvedSentinel(d) {
			t.Logf("  ✓ used the sentinel (nothing open)")
		}
		if leak := LeakedPromptWords(d, Render(w)); len(leak) > 0 {
			t.Logf("  ⚠ PROMPT LEAK: %v (instruction vocabulary absent from the session)", leak)
		}
		if bad := UnverifiedIdentifiers(d, Render(w)); len(bad) > 0 {
			t.Logf("  ⚠ unverified specifics: %v", bad)
		}
	}
	t.Logf("structural validity: %d/%d", ok, tried)
	if tried > 0 && ok != tried {
		t.Errorf("threshold 1 requires 100%% structural validity, got %d/%d", ok, tried)
	}
}

func clipLog(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 260 {
		return s[:260] + "…"
	}
	return s
}

// reportWindows are the window indices a report is produced at. Four steps, four apart, on a
// 16-window prefix — unchanged from the pre-beat sweep so the report-level thresholds are
// measured under the same spacing the earlier configurations were, and only the INPUTS changed.
var reportWindows = []int{4, 8, 12, 15}

// recovered runs fn under a recover(), reporting whether it panicked.
//
// Required, not defensive: assertPromptWithinBudget panics BY DESIGN when a prompt would exceed
// the budget or starve the conversation window, and a panic in a test binary kills the process
// — which on a multi-hour sweep means losing every measurement taken so far rather than one
// digest. A recovered panic is counted and reported as a first-class metric (see panics below),
// because a run with silent losses is precisely what made earlier numbers in this study
// meaningless. Same pattern TestRealCorpusPromptsNeverTripTheBackstop already uses.
func recovered(t *testing.T, what string, fn func()) bool {
	panicked := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
				t.Logf("  ⚠ RECOVERED PANIC (%s): %v", what, r)
			}
		}()
		fn()
	}()
	return panicked
}

// beatsShownIn reports how many of the accumulated beats the assembled prompt actually carries.
//
// fitDiscretionary shrinks the beat SELECTION when the budget is under pressure, so "the report
// was given the beats" is a claim about the assembled prompt, not about len(beats). Derived by
// asking the real SelectBeats/RenderBeats for each candidate size and testing containment,
// rather than parsing the prompt back out — a parser would have to re-guess the section
// boundaries, which is the landmark-searching mistake the backstop was already fixed for once.
func beatsShownIn(prompt string, all []Beat) int {
	for k := len(all); k > 0; k-- {
		if s := RenderBeats(SelectBeats(all, k)); s != "" && strings.Contains(prompt, s) {
			return len(SelectBeats(all, k))
		}
	}
	return 0
}

// TestDigestRefineQuality measures the report thresholds over real sessions, running the actual
// story-rollup path — a SessionRecord accumulated from the transcript, a beat series generated
// every BeatTurnsFromEnv() user turns, and RefineFrom reading both — and persisting the record,
// the beats and the digests so a run is replayable.
//
// Nothing here embeds the previous report's prose: that is the design under measurement. The
// legacy CreateDigestWithView/RefineDigestWithReason path this sweep used to drive built no
// record and generated no beats, which is why T12 could only print UNMEASURED.
func TestDigestRefineQuality(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	// Spread across projects rather than taking the first N in path order, which drew all
	// 14 sessions from keld-atlas and reported one project's results as a corpus. This
	// session's own transcript sat at index 51 of 59 and was never sampled.
	files := StratifiedTranscripts()
	if me := ThisSessionTranscript(); me != "" {
		keep := make([]string, 0, len(files)+1)
		keep = append(keep, me)
		for _, f := range files {
			if f != me {
				keep = append(keep, f)
			}
		}
		// Deduplicated: it IS reachable from StratifiedTranscripts, so a bare prepend would
		// spend two of the session budget's slots on the same transcript and report it as two.
		files = keep
	}

	store, err := digeststore.Open(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	o := DefaultMineOpts()
	o.K = 12
	l := NewLlama(url)
	beatTurns := BeatTurnsFromEnv()

	var (
		attempted, failed        int
		stale, openItems         int
		completedCurrent         int
		restated                 int
		lagging, lagDen          int
		lagJudged, lagAbstained  int
		shifts                   int
		digests, malformed       int
		ids, unverified, leaks   int
		withCorrections, stamped int
		cleanRuns, fabricated    int
		retNum, retDen           int
		// T4 split by whether the "fact" is identifier-shaped at all. Identifiers() is
		// position-aware over PROSE, so its output legitimately includes bare capitalised words
		// ("Code", "Run", "Atlas") alongside real specifics ("daemon.go", "agentcfg.Info"). A
		// report dropping "Run" is not the failure T4 exists to detect, and four metrics in this
		// study have already reported a large number that turned out to be ordinary English —
		// so the rate is split by the code's OWN rule (strongIdentifier) rather than left as one
		// number that mixes the two populations.
		retStrongNum, retStrongDen int
		retWeakNum, retWeakDen     int
		sentinelUsed               int
		nextIDs, fabricatedNext    int
		// Beat accounting. "Generated" is what the model was asked for and returned;
		// "kept" is what AppendBeat stored; "discarded" is what it dropped as a restatement
		// of the previous beat. If most of what a 3-turn cadence generates is discarded, the
		// cadence is wrong for real sessions — that is a finding about the design, not a
		// nuisance, so both halves are reported rather than only the series length.
		beatAsks, beatsGenerated   int
		beatsKept, beatsDiscarded  int
		beatErrs                   int
		beatsChangedSubject        int
		beatsShownTotal, beatSteps int
		beatRatioSum, beatRatioMax float64
		beatRatioN                 int
		// T12: BeatContradictsRecord returns (terms, checked). checked==false is an
		// ABSTENTION (the record holds fewer than minConsistencyEvidence subjects), and a rate
		// computed as flagged/generated rather than flagged/checked would score every
		// early-session beat as consistent and deflate the metric.
		t12Checked, t12Flagged, t12Abstained int
		// Recovered panics, by path. Reported as a metric, not a log line.
		panicsCreate, panicsRefine, panicsBeat int
		// T4 diagnosis. A retention rate on its own cannot distinguish the two failures that
		// matter, and they have opposite fixes: the retain-list never OFFERED the fact (a
		// channel defect — boundRetainList evicted it, or it had already fallen out of the
		// prior report so Identifiers could not see it) versus the retain-list offered it and
		// the model dropped it anyway (a prompt/model defect). The retain-list is the ONLY
		// channel carrying a prior report's specifics into a refinement now that no prose is
		// embedded, so which of the two it is decides whether the cap moves or the instruction
		// does.
		retainOffered, retainEvicted, retainAlreadyGone int
		retainMaxCount, retainMaxRunes                  int
		retainCountBound, retainRunesBound              int
		// Prompt geometry actually observed, so "the budget and ctx can come back down" is
		// answerable from the run rather than from the worst-case construction.
		tightestRefine     = DefaultPromptCharBudget
		tightestCreate     = DefaultPromptCharBudget
		tightestRefineWhen string
		tightestCreateWhen string
		worstPrompt        int
		worstPromptWhen    string
	)
	sessions := 0
	for _, f := range files {
		if sessions >= sessionBudget() {
			break
		}
		ws, e1 := Mine(f, o)
		ocs, e2 := Outcomes(f, o)
		// Disjoint per-user-prompt deltas for the RECORD only; the prompts still read Mine's
		// windows. See sessionDeltas for why the record cannot be fed Mine's overlapping,
		// K-truncated windows.
		deltas, e3 := sessionDeltas(f, o)
		if e1 != nil || e2 != nil || e3 != nil || len(ws) < 16 || len(ws) != len(ocs) || len(deltas) != len(ws) {
			continue
		}
		sessions++
		sid := filepath.Base(f)
		project := projectFromPath(f)

		var cur Digest
		var injected []string
		var firstSrc, prevReportSrc string
		var rec SessionRecord
		var beats []Beat
		// The verification reference must be every window consumed so far. Scoring a
		// refined digest against only the newest window counts correct carry-forward
		// as fabrication — which is what an earlier run of this harness did, reporting
		// 53.8% unverified identifiers against a true rate of 1-2 per digest.
		var seenSrc strings.Builder
		// Which report step, if any, each window index produces.
		stepAt := map[int]int{}
		for s, idx := range reportWindows {
			stepAt[idx] = s
		}
		// EVERY window up to the last report is walked now, not only the four report windows.
		// The record folds each one and beats are generated on a turn cadence between reports,
		// so the material the model sees is no longer confined to the report windows — and the
		// verification reference below has to grow with it or correct carry-through gets
		// scored as fabrication all over again.
		for idx := 0; idx <= reportWindows[len(reportWindows)-1]; idx++ {
			w := ws[idx]
			src := Render(w)
			view := RenderSessionView(w)

			// Mine and sessionDeltas both emit one entry per user record of the same parse, so
			// they align by construction. Asserted rather than assumed: if they ever diverge,
			// the record would be describing a different turn from the prompt, silently.
			if deltas[idx].PromptID != w.PromptID {
				t.Fatalf("session %d idx %d: delta/window misalignment (%q vs %q)",
					sessions, idx, deltas[idx].PromptID, w.PromptID)
			}
			rec = rec.Observe(deltas[idx], Extract(deltas[idx])).WithProject(project)
			// WithFocus is deliberately NOT called. The EWMA focus comes from the
			// classification pipeline, which this harness does not run, and writing a
			// plausible-looking focus into a block the prompt labels "measured —
			// authoritative" is the fabricated-record failure Task 5 was corrected for.
			// Populated() therefore omits "focus", which is exactly what it exists to do:
			// the spine reads as partial rather than as a settled focus nobody measured.

			seenSrc.WriteString(src)
			seenSrc.WriteString("\n")
			// The reference must contain EVERYTHING the model was shown, including the
			// coarse whole-session view. Omitting it scored correct carry-through from that
			// view as fabrication and took unverified identifiers from 0.6% to 8.5% — the
			// same defect as scoring a refined digest against only the newest window.
			seenSrc.WriteString(view)
			seenSrc.WriteString("\n")
			// The record's own rendering is shown to the model verbatim, so its subject terms
			// and project paths are legitimately available to be carried into a report.
			//
			// Beat TEXT is deliberately NOT added. Beats are model output: adding them would
			// let a specific INVENTED in a beat count as verified when the report repeats it,
			// which is precisely the fabrication chain T2 exists to catch. A beat that
			// invents a name should cost T2 (and T12), not be laundered by it.
			cumulative := seenSrc.String() + rec.Block()

			// A beat every beatTurns USER turns. Mine returns exactly one window per user
			// prompt (see its doc), so the window ordinal IS the user-turn count — no
			// separate tally, and no summing Signals.UserTurns across overlapping windows,
			// which would count the same turn a dozen times.
			if (idx+1)%beatTurns == 0 {
				beatAsks++
				var text string
				var berr error
				if recovered(t, fmt.Sprintf("beat s%d i%d", sessions, idx), func() {
					text, berr = l.GenerateBeat(rec.Block(), src)
				}) {
					panicsBeat++
				} else if berr != nil {
					beatErrs++
					t.Logf("  beat s%d i%d failed: %v", sessions, idx, berr)
				} else {
					beatsGenerated++
					// T12, on the beat as generated — including one about to be discarded as a
					// restatement, because a contradiction is a property of the model's output
					// and not of whether the series happened to keep it.
					if terms, checked := BeatContradictsRecord(text, rec); checked {
						t12Checked++
						if len(terms) > 0 {
							t12Flagged++
							t.Logf("  BEAT-CONTRADICTS-RECORD s%d i%d: terms=%v | beat=%s | subjects=%v",
								sessions, idx, terms, clipLog(text), rec.Subjects)
						}
					} else {
						t12Abstained++
					}
					before := len(beats)
					// The overlap ratio beatsRestate thresholds at insightMatchRatio, recorded
					// for the PREVIOUS beat before AppendBeat decides. A discard rate of zero is
					// only a finding about the CADENCE if the comparator was anywhere near
					// firing; if the largest ratio a real session ever produces is far below the
					// threshold, the finding is instead that consecutive beats genuinely differ
					// (or that the comparator cannot see a restatement at all). Mirrors
					// beatsRestate's own arithmetic over beatSignificantWords for REPORTING
					// only — beatsRestate itself remains the decision, and is not touched.
					if before > 0 {
						r := beatOverlapRatio(text, beats[before-1].Text)
						beatRatioSum += r
						beatRatioN++
						if r > beatRatioMax {
							beatRatioMax = r
						}
					}
					var kept bool
					beats, kept = AppendBeat(beats, text)
					if kept {
						beatsKept++
						b := beats[len(beats)-1]
						// Every kept beat, in full. The beat series is the design's claim to be
						// "directly presentable" history, and a reader cannot judge that from a
						// count — nor tell an accurate beat that T12 flagged from an inaccurate
						// one, which is the whole question T12 turned out to raise.
						t.Logf("  BEAT s%d i%d [%d]%s %s", sessions, idx, b.Ordinal,
							map[bool]string{true: " (subject changed)", false: ""}[b.ChangedSubject],
							clipLog(b.Text))
						if b.ChangedSubject {
							beatsChangedSubject++
						}
						if err := store.PutBeat(sid, digeststore.BeatRow{
							Ordinal: b.Ordinal, CreatedTS: int64(idx),
							ChangedSubject: b.ChangedSubject, Text: b.Text,
						}); err != nil {
							t.Errorf("put beat: %v", err)
						}
					} else {
						beatsDiscarded++
						prevText := ""
						if before > 0 {
							prevText = beats[before-1].Text
						}
						// The discarded item, never just the count: "most beats are
						// restatements" is only a finding about the cadence if the pairs
						// really do say the same thing.
						t.Logf("  BEAT-DISCARDED-AS-RESTATEMENT s%d i%d: new=%s | prev=%s",
							sessions, idx, clipLog(text), clipLog(prevText))
					}
				}
			}

			step, isReport := stepAt[idx]
			if !isReport {
				continue
			}
			facts := FactsFrom(Extract(w), ocs[:idx+1]).WithWindow(w).WithPlace("", "", project)
			if step == 0 {
				firstSrc = src
			}
			// Stand-in for the production focus-shift trigger, which needs the EWMA focus
			// this harness does not compute. Compared against the window of the PREVIOUS
			// REPORT, not the previous window: the question the trigger answers is whether
			// the work moved since the last report, and comparing adjacent windows would
			// measure something else and break comparability with the earlier runs.
			reason := TriggerVolume
			if anchorEnabled() && step > 0 && SubjectShifted(prevReportSrc, src) {
				reason = TriggerFocusShift
				shifts++
			}
			// T4 diagnosis, computed BEFORE the call and from the same functions the prompt
			// builder uses, so it describes the retain-list this refinement actually carried
			// rather than a reconstruction of it.
			if step > 0 && len(injected) > 0 {
				named := Identifiers(cur)
				bounded := boundRetainList(named)
				if n := len(bounded); n > retainMaxCount {
					retainMaxCount = n
				}
				if n := retainListJoinedLen(bounded); n > retainMaxRunes {
					retainMaxRunes = n
				}
				if len(named) > retainListMaxCount {
					retainCountBound++
				}
				if retainListJoinedLen(tailN(named, retainListMaxCount)) > retainListMaxTotal {
					retainRunesBound++
				}
				for _, f := range injected {
					switch {
					case hasTerm(bounded, f):
						retainOffered++
					case hasTerm(named, f):
						retainEvicted++
						t.Logf("  RETAIN-EVICTED s%d step%d: %q dropped by boundRetainList "+
							"(list %d entries / %d runes from %d named; caps %d / %d)",
							sessions, step, f, len(bounded), retainListJoinedLen(bounded),
							len(named), retainListMaxCount, retainListMaxTotal)
					default:
						retainAlreadyGone++
						t.Logf("  RETAIN-ALREADY-GONE s%d step%d: %q is no longer in the prior "+
							"report at all, so no channel could carry it", sessions, step, f)
					}
				}
			}
			var d Digest
			var err error
			attempted++
			// One recover() per step covering BOTH the prompt assembly done for measurement
			// and the model call, because assertPromptWithinBudget fires during assembly and
			// either way the cost must be one lost digest, not the run.
			panicked := recovered(t, fmt.Sprintf("s%d step%d", sessions, step), func() {
				if step == 0 {
					p, window := createPromptAndWindow("work session", src, view, facts.Block())
					if m := windowMargin(window); m < tightestCreate {
						tightestCreate, tightestCreateWhen = m, sprintStep(sessions, idx, project)
					}
					if n := len([]rune(p)); n > worstPrompt {
						worstPrompt, worstPromptWhen = n, "create "+sprintStep(sessions, idx, project)
					}
					d, err = l.CreateDigestWithView("work session", src, view, facts.Block())
					return
				}
				in := RefineInput{
					SessionLabel: "work session",
					Record:       rec,
					Beats:        beats,
					SessionView:  view,
					NewTurns:     src,
					Why:          reason,
				}
				p, window := updatePromptAndWindow(cur, in)
				if m := windowMargin(window); m < tightestRefine {
					tightestRefine, tightestRefineWhen = m, sprintStep(sessions, idx, project)
				}
				if n := len([]rune(p)); n > worstPrompt {
					worstPrompt, worstPromptWhen = n, "refine "+sprintStep(sessions, idx, project)
				}
				// How many beats the prompt actually carried, so a series silently shrunk by
				// budget pressure is visible instead of assumed away.
				beatSteps++
				beatsShownTotal += beatsShownIn(p, beats)
				d, err = l.RefineFrom(cur, in)
			})
			if panicked {
				failed++
				if step == 0 {
					panicsCreate++
				} else {
					panicsRefine++
				}
				continue
			}
			if err != nil {
				failed++
				t.Logf("session %d step %d: %v", sessions, step, err)
				continue
			}
			digests++
			if p := ValidateDigest(d); len(p) > 0 {
				malformed++
				t.Logf("session %d step %d malformed: %v", sessions, step, p)
			}
			ids += len(Identifiers(d))
			// Log the flagged items, not just the count. Both of these gates have
			// reported large numbers that turned out to be ordinary English, and a bare
			// count gives no way to tell a real defect from a measurement artifact.
			if bad := UnverifiedIdentifiers(d, cumulative); len(bad) > 0 {
				unverified += len(bad)
				t.Logf("  UNVERIFIED s%d step%d: %v", sessions, step, bad)
			}
			// T13: T2 (unverified identifiers) reads the whole digest, so a fabrication
			// confined to `next` is diluted by every other section's identifiers rather than
			// scored against its own. Observed directly in real output: a next inventing
			// schema field names ("tool name, call id, input, output, timestamp") never
			// discussed anywhere in the conversation. T7 inspects only `unresolved`, so
			// nothing caught it. Denominator is identifiers found IN NEXT ONLY, so the rate
			// answers "how often does next specifically fabricate", not "how often does the
			// whole digest".
			nextIDs += len(Identifiers(Digest{Next: d.Next}))
			if fab := FabricatedNext(d, cumulative); len(fab) > 0 {
				fabricatedNext += len(fab)
				t.Logf("  FABRICATED-NEXT s%d step%d: %v", sessions, step, fab)
			}
			if lk := LeakedPromptWords(d, cumulative); len(lk) > 0 {
				leaks += len(lk)
				t.Logf("  LEAK s%d step%d: %q", sessions, step, lk)
			}
			if UsesUnresolvedSentinel(d) {
				sentinelUsed++
			}
			// T8: an open item the report itself contradicts. T7 only catches a blocker
			// with no basis at all and scores a stale one as passing, though a reader
			// acting on it wastes the same effort.
			if st := StaleUnresolved(d); len(st) > 0 {
				stale += len(st)
				t.Logf("  STALE s%d step%d: %v", sessions, step, st)
			}
			openItems += len(d.Unresolved)
			// T11 (synopsis lag). SynopsisLag ABSTAINS by returning lag=false when the opening
			// is not represented in the synopsis by at least minLagEvidence terms — there is
			// then no evidence that the synopsis is backward-looking rather than merely
			// general. A rate over every refinement therefore counts each abstention as a
			// pass, which is the identical false-confidence failure the T12 denominator was
			// corrected for, and it is not hypothetical here: the ledger records the
			// untrimmed-token bug producing abstentions on a synopsis that WAS lagging, so the
			// previously reported 14.3% was itself partly abstentions. Both denominators are
			// carried so the judged rate is the result and the abstention count is visible
			// beside it rather than hidden inside it.
			if step > 0 {
				lagDen++
				rh, eh, lag := SynopsisLag(d, firstSrc, src)
				switch {
				case eh >= minLagEvidence:
					lagJudged++
					if lag {
						lagging++
						t.Logf("  SYNOPSIS-LAGS s%d step%d (recent=%d early=%d): %s",
							sessions, step, rh, eh, clipLog(d.Synopsis))
					}
				default:
					lagAbstained++
					t.Logf("  SYNOPSIS-LAG-ABSTAINED s%d step%d (early=%d < %d, recent=%d): %s",
						sessions, step, eh, minLagEvidence, rh, clipLog(d.Synopsis))
				}
			}
			if SynopsisRestatesAnotherSection(d) {
				restated++
				t.Logf("  SYNOPSIS-RESTATES s%d step%d: %s", sessions, step, clipLog(d.Synopsis))
			}
			if CurrentDescribesCompletion(d) {
				completedCurrent++
				t.Logf("  CURRENT-IS-DONE s%d step%d: %s", sessions, step, clipLog(d.Current))
			}
			if facts.Corrections > 0 || facts.CorrectedTurns > 0 {
				withCorrections++
				if LooksRubberstamped(d, facts) {
					stamped++
					t.Logf("  RUBBERSTAMP s%d step%d (corr=%d): %s",
						sessions, step, facts.Corrections, clipLog(d.Happened))
				}
			} else {
				cleanRuns++
				if LooksFabricatedUnresolved(d, facts, cumulative) {
					fabricated++
					t.Logf("  FABRICATED s%d step%d: %v", sessions, step, d.Unresolved)
				}
			}

			// A report that fired because direction changed is a turning point, recoverable
			// from the record as a FACT by every later report rather than inferred from prose.
			// Noted after the report succeeded, not when the reason was chosen: a step that
			// produced nothing did not turn anything.
			rec = rec.NoteTurningPoint(step+1, reason)

			body, _ := json.Marshal(d)
			sig, _ := json.Marshal(facts)
			if err := store.Put(digeststore.Record{
				SessionID: sid, Seq: step + 1, CreatedTS: int64(step),
				SchemaVersion: DigestSchemaVersion, Model: "qwen3-4b",
				Trigger: string(reason), FromPromptID: w.PromptID,
				ToPromptID: w.PromptID, Turns: facts.Turns,
				Signals: string(sig), Body: string(body),
			}); err != nil {
				t.Errorf("store: %v", err)
			}
			// Current state, overwritten, tagged with the seq that last consumed it — so a run
			// is replayable from the store alone rather than only from this process's memory.
			// Note SessionRecord's unexported fields (hasFocus, freq) do not survive JSON: the
			// row is the measured state a reader sees, not a resumable accumulator.
			recBody, _ := json.Marshal(rec)
			if err := store.PutSessionRecord(sid, step+1, string(recBody)); err != nil {
				t.Errorf("store record: %v", err)
			}

			if step == 0 {
				injected = Identifiers(d)
				if len(injected) > 6 {
					injected = injected[:6]
				}
			}
			cur = d
			prevReportSrc = src
		}
		if len(injected) > 0 {
			retNum += RetainedFacts(cur, injected)
			retDen += len(injected)
			// Same survival test as RetainedFacts, partitioned. Uses proseHay via lostFacts so
			// the two cannot disagree about what "survived" means.
			lost := lostFacts(cur, injected)
			for _, f := range injected {
				survived := !hasTerm(lost, f)
				if strongIdentifier(f) {
					retStrongDen++
					if survived {
						retStrongNum++
					}
					continue
				}
				retWeakDen++
				if survived {
					retWeakNum++
				}
			}
			// RetainedFacts (T4) returns a bare survival count. Log which of the injected
			// facts did NOT survive — the same "print what was flagged, not just the rate"
			// requirement as every other threshold here, and the direction a regression in
			// this metric actually needs: knowing a fact was lost is only useful if it says
			// which one.
			if lost := lostFacts(cur, injected); len(lost) > 0 {
				t.Logf("  LOST-FACT s%d: %v", sessions, lost)
			}
		}
		// The store must actually hold the trail the drift metric replays — and now also the
		// beats and the record, or "a run is replayable" is a claim about memory rather than
		// about the database.
		if h, err := store.History(sid); err != nil || len(h) == 0 {
			t.Errorf("history missing for %s: %v", sid, err)
		}
		if rows, err := store.Beats(sid); err != nil || len(rows) != len(beats) {
			t.Errorf("beats not persisted for %s: %d rows for %d beats (%v)", sid, len(rows), len(beats), err)
		}
		if _, _, ok, err := store.SessionRecord(sid); err != nil || !ok {
			t.Errorf("session record not persisted for %s: %v", sid, err)
		}
		changedHere := 0
		for _, b := range beats {
			if b.ChangedSubject {
				changedHere++
			}
		}
		t.Logf("─── session %d %s: beats kept=%d (subject changes=%d) record=%v counts=turns:%d/user:%d/corr:%d subjects=%v",
			sessions, project, len(beats), changedHere, rec.Populated(),
			rec.Turns, rec.UserTurns, rec.Corrections, rec.Subjects)
	}

	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return float64(n) / float64(d) * 100
	}
	arm := "anchor ON (SubjectShifted stand-in — known to fire on nearly every refinement, so " +
		"effectively anchor-always, NOT a measurement of the gated design)"
	if !anchorEnabled() {
		arm = "anchor OFF (no recency anchor on any refinement — the arm the design's T11 prediction is about)"
	}
	t.Logf("ARM: %s", arm)
	t.Logf("sessions=%d attempted=%d produced=%d failed=%d", sessions, attempted, digests, failed)
	// T1 counts SUCCEEDED/ATTEMPTED, not valid/produced. An earlier version measured
	// the latter and reported 100%% while silently dropping 5 of 20 attempts to
	// truncated JSON — a dropped digest is worse than a malformed one, not exempt.
	t.Logf("T1 usable digests        %.1f%% of %d attempts  (want 100%%)", pct(digests-malformed, attempted), attempted)
	t.Logf("T2 unverified identifiers %.1f%% of %d  (want <=2%%)", pct(unverified, ids), ids)
	t.Logf("T3 rubberstamped          %.1f%% of %d correction-bearing  (want <=10%%)", pct(stamped, withCorrections), withCorrections)
	t.Logf("T4 retention to final     %.1f%% of %d  (want >=90%%)", pct(retNum, retDen), retDen)
	t.Logf("    split: identifier-shaped specifics %.1f%% of %d; bare capitalised words %.1f%% of "+
		"%d — only the first population is what T4 exists to protect",
		pct(retStrongNum, retStrongDen), retStrongDen, pct(retWeakNum, retWeakDen), retWeakDen)
	t.Logf("T7 fabricated unresolved  %.1f%% of %d clean runs  (want <=10%%)", pct(fabricated, cleanRuns), cleanRuns)
	t.Logf("T8 stale open items       %.1f%% of %d open items  (want <=2%%)", pct(stale, openItems), openItems)
	t.Logf("T9 current-is-completed   %.1f%% of %d  (want <=5%%)", pct(completedCurrent, digests), digests)
	t.Logf("T10 synopsis restates      %.1f%% of %d  (want <=5%%)", pct(restated, digests), digests)
	if lagJudged == 0 {
		t.Logf("T11 synopsis lags          NO VERDICT — all %d refinements abstained (the synopsis "+
			"never echoed the opening by %d terms), so 0%% would mean 'unmeasured', not 'clean'",
			lagDen, minLagEvidence)
	} else {
		t.Logf("T11 synopsis lags          %.1f%% of %d JUDGED refinements  (want <=10%%; %d of %d "+
			"abstained for want of opening evidence)",
			pct(lagging, lagJudged), lagJudged, lagAbstained, lagDen)
	}
	t.Logf("    anchor fired on %d of %d refinements (measured subject shift)", shifts, lagDen)
	// T12, measurable for the first time now that this sweep builds the record and the beats.
	// The DENOMINATOR is t12Checked, not the number of beats: BeatContradictsRecord abstains
	// while the record holds fewer than minConsistencyEvidence subjects, and dividing by all
	// generated beats would score every abstention as consistent and deflate the rate. The
	// abstentions are printed alongside, because "checked 4 of 70" is a different result from
	// "checked 70 of 70" at the same percentage.
	if t12Checked == 0 {
		t.Logf("T12 beat-vs-record         NO VERDICT — %d beats generated, the record never held "+
			"%d subjects, so every beat abstained (not a clean result)",
			beatsGenerated, minConsistencyEvidence)
	} else {
		t.Logf("T12 beat-vs-record         %.1f%% of %d CHECKED beats  (want <=5%%; %d abstained "+
			"on a thin record, %d generated in total)",
			pct(t12Flagged, t12Checked), t12Checked, t12Abstained, beatsGenerated)
	}
	t.Logf("T13 fabricated next        %.1f%% of %d next-only identifiers  (want <=5%%)", pct(fabricatedNext, nextIDs), nextIDs)
	t.Logf("   prompt leaks %d; sentinel used %d/%d", leaks, sentinelUsed, digests)

	// Recovered panics, as a metric rather than a log line. A run with silent losses is what
	// made earlier numbers in this study meaningless, and every rate above is computed over a
	// denominator these panics reduce — so a non-zero count here invalidates the run's
	// comparability, not just the step it happened on.
	t.Logf("RECOVERED PANICS          %d total (create %d, refine %d, beat %d)  — the bar is zero",
		panicsCreate+panicsRefine+panicsBeat, panicsCreate, panicsRefine, panicsBeat)
	if panicsCreate+panicsRefine+panicsBeat > 0 {
		t.Errorf("%d prompt assemblies tripped the budget backstop; the sweep continued but the "+
			"rates above are measured over a reduced denominator",
			panicsCreate+panicsRefine+panicsBeat)
	}

	// Beat accounting. The generated/discarded split is the direct test of whether a 3-turn
	// cadence suits real sessions: a series that discards most of what it generates is paying
	// for inference that adds nothing to the history.
	t.Logf("BEATS  asked %d, generated %d, kept %d, discarded as restatement %d (%.1f%% of "+
		"generated), errors %d, cadence every %d user turns",
		beatAsks, beatsGenerated, beatsKept, beatsDiscarded, pct(beatsDiscarded, beatsGenerated),
		beatErrs, beatTurns)
	t.Logf("   of the kept beats, %d changed the subject (%.1f%%)", beatsChangedSubject, pct(beatsChangedSubject, beatsKept))
	if beatRatioN > 0 {
		t.Logf("   consecutive-beat overlap ratio: mean %.3f, max %.3f over %d pairs, against the "+
			"%.2f threshold beatsRestate discards at — a max far below it means the beats really "+
			"do differ, not that the cadence is loose",
			beatRatioSum/float64(beatRatioN), beatRatioMax, beatRatioN, insightMatchRatio)
	}
	if beatSteps > 0 {
		t.Logf("   refinements carried %.1f beats on average (cap %d); a lower number than the "+
			"series length means fitDiscretionary shrank the selection",
			float64(beatsShownTotal)/float64(beatSteps), MaxBeatSelection)
	}

	// Prompt geometry as OBSERVED, which is what "can the budget and ctx come back down"
	// actually needs. The margin is over MinTurnChars of window CONTENT (notice excluded) —
	// see windowMargin; an unclipped window reports the budget's full width so it cannot
	// become the reported minimum.
	// T4's mechanism, not just its rate. retainOffered counts (refinement, injected fact) pairs
	// where the retain-list DID name the fact: a loss under a high offered count is the model
	// ignoring an explicit instruction, and no cap increase can fix that.
	t.Logf("RETAIN-LIST  offered %d, evicted by the cap %d, already gone from the prior report %d "+
		"(of %d refinement x fact pairs)", retainOffered, retainEvicted, retainAlreadyGone,
		retainOffered+retainEvicted+retainAlreadyGone)
	t.Logf("   largest list observed: %d entries / %d runes (caps %d / %d); the count cap bound on "+
		"%d refinements, the rune cap on %d",
		retainMaxCount, retainMaxRunes, retainListMaxCount, retainListMaxTotal,
		retainCountBound, retainRunesBound)

	t.Logf("PROMPT  largest %d runes of %d budget (%s)", worstPrompt, DefaultPromptCharBudget, worstPromptWhen)
	t.Logf("   tightest window margin over the %d-rune floor: refine %s, create %s",
		MinTurnChars, marginReport(tightestRefine, tightestRefineWhen),
		marginReport(tightestCreate, tightestCreateWhen))
}

// beatOverlapRatio is the quantity beatsRestate compares against insightMatchRatio, exposed for
// reporting. Read-only: beatsRestate keeps the decision, and this exists because "0 beats were
// discarded" is uninterpretable without knowing whether the comparator came close.
func beatOverlapRatio(a, b string) float64 {
	wa, wb := beatSignificantWords(a), beatSignificantWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	larger := len(wa)
	if len(wb) > larger {
		larger = len(wb)
	}
	return float64(shared) / float64(larger)
}

// hasTerm reports whether a list names exactly this term, case-insensitively. Exact-element,
// not substring: the retain-list carries whole identifiers and the question being asked is
// "was this specific one named", which a substring test would answer yes to for any longer
// entry that happens to contain it.
func hasTerm(list []string, term string) bool {
	for _, x := range list {
		if strings.EqualFold(x, term) {
			return true
		}
	}
	return false
}

// lostFacts is RetainedFacts (T4) with the miss list exposed. RetainedFacts itself returns
// only a survival count — useful for the rate, useless for diagnosing a regression, which is
// exactly the "print what was flagged, not just the count" gap the other thresholds in this
// sweep were already fixed for. Shares proseHay (digest_check.go) with RetainedFacts rather
// than rebuilding the same join here — the two had drifted into separate copies once already.
func lostFacts(after Digest, facts []string) []string {
	hay := proseHay(after)
	var out []string
	for _, f := range facts {
		if !strings.Contains(hay, strings.ToLower(f)) {
			out = append(out, f)
		}
	}
	return out
}

// sessionDeltas returns one Window per user prompt like Mine does, but holding ONLY the
// records that are NEW since the previous user prompt — disjoint slices whose union is the
// whole record stream, in the same order and with the same PromptIDs as Mine's windows.
//
// This exists because SessionRecord.Observe SUMS what it is given
// (r.Turns += s.Turns, r.Corrections += s.Corrections, mergeToolCounts adds), and Mine's
// windows OVERLAP: each carries up to K=12 context turns before its target, so consecutive
// windows share ~11 turns. Folding Mine's windows straight into the record therefore counts
// most turns about a dozen times, and the record is the one input the refine prompt labels
// "measured — authoritative". digestRules tells the model that corrections in particular are a
// measured fact its prose must be consistent with, so an inflated correction count is not a
// cosmetic error: it pushes the model toward reporting friction, which is exactly what T3
// (rubberstamping) scores, and in the direction that makes T3 look BETTER than the truth.
// Measured on this corpus before this fix, over 16 windows: turns 190 vs 51 (3.7x),
// tool_calls 87 vs 22 (4.0x), corrections 20 vs 5 (4.0x) on the first session, with the same
// shape on every other session probed.
//
// Observe's own unit test pairs it with disjoint two-turn windows (turns=4 from 2+2), so
// disjoint input is the contract it was written against; Mine's overlap is the harness's
// problem to solve, not a reason to change shared accumulation code.
//
// Built from records() — the same parse Mine and Outcomes share, so the three cannot disagree
// about what counts as a conversational record — and through appendTurn/elideCode/clip so a
// delta's turns are rendered and collapsed identically to a mined window's. Tool-run collapse
// (the "(xN)" marker Extract and mergeToolCounts both read) therefore still applies within a
// delta; a run split across two deltas counts as two runs, which is what actually happened.
func sessionDeltas(path string, o MineOpts) ([]Window, error) {
	recs, sessionID, err := recordsAndSession(path, o)
	if err != nil {
		return nil, err
	}
	var out []Window
	prev := 0
	for i, r := range recs {
		if r.role != RoleUser {
			continue
		}
		turns := make([]Turn, 0, i-prev+1)
		for _, c := range recs[prev : i+1] {
			turns = appendTurn(turns, Turn{Role: c.role, Text: clip(elideCode(c.text), o.PerTurnChars)})
		}
		out = append(out, Window{
			SessionID: sessionID, PromptID: r.id,
			Target: clip(elideCode(r.text), o.PerTurnChars), Turns: turns,
		})
		prev = i + 1
	}
	return out, nil
}

// anchorEnabled reports whether the sweep offers the recency anchor at all.
//
// Two arms, because the design makes a PREDICTION about this: that synopsis lag (T11) improves
// WITHOUT the anchor now that the framing is no longer pinned by verbatim prior prose. A single
// run cannot state that outcome either way — and the same class of plausible-mechanism
// reasoning is what failed for the anchor itself, so it has to be measured on both sides.
//
// "on" (the default) is the GATED configuration, in the only form this harness can offer it:
// SubjectShifted as a stand-in for the production EWMA focus shift. That stand-in is documented
// MISCALIBRATED at this window spacing — it fires on essentially every refinement — so the
// "on" arm is in practice anchor-always, and must be reported as such rather than as "gated".
// "off" removes the anchor entirely, which is the arm the design's prediction is about.
func anchorEnabled() bool { return os.Getenv("KELD_STUDY_ANCHOR") != "off" }

// sessionBudget sets how many sessions the sweep covers. Default 5 is a fast
// iteration loop; the reportable number needs more, because T4's denominator is
// 6 injected facts per session and at 30 a three-fact swing spans most of the
// plausible range — three configs scored 100%, 96.7% and 86.7% with overlapping
// Wilson intervals, i.e. indistinguishable.
func sessionBudget() int {
	if v := os.Getenv("DIGEST_SESSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}
