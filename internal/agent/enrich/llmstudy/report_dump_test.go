//go:build llmstudy

package llmstudy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReportDump records EVERY input and EVERY output of every report in a both-arms sweep,
// so the reports can be judged by the person the design is for rather than through a
// threshold count.
//
// The beat tier already had such an artifact (TestBeatDump, docs/qwen-inputs-and-outputs.md)
// and the report tier did not — which is why report quality has never been human-judged on
// this branch. A count cannot say whether a report reads correctly, and every quality figure
// in this study is a string heuristic over the same prose.
//
// It shows each input SEPARATELY and by its truth status, because that separation is the
// design:
//
//   - the measured record — counted on device, authoritative;
//   - the beat series as SelectBeats/RenderBeats actually rendered it — model-generated,
//     indicative;
//   - the retain-list the refinement is obliged to carry, with its count and the entries
//     boundRetainList dropped;
//   - the open items handed back for a verdict;
//   - the whole-session view and the conversation window AS INCLUDED, with rune counts and
//     any omission notice verbatim (a silently shorter input is the defect one level up —
//     see the delimiter convention in AGENTS.md);
//   - the refresh reason that fired, and separately what the prompt was told (the arms differ
//     in exactly that);
//   - the assembled prompt, measured;
//   - the report.
//
// Nothing is generated for the dump that the sweep does not generate: session selection,
// window geometry, beat cadence, record accumulation and both prompt builders are the sweep's
// own (see TestDigestRefineQuality). The one deliberate deviation is that both arms are run in
// ONE process, session-major, so a reader sees the same session's two arms together; the arms
// keep independent records, beat series and windowers, and beats are regenerated per arm
// rather than shared, since rec.NoteTurningPoint is only called for a report that succeeded
// and a lost report in one arm would otherwise silently change the other arm's beats.
//
// Failures are recorded in place, with the attempt count: a refusal, an exhausted retry, a
// recovered backstop panic and an empty-open-list substitution are all part of what happened.
//
//	REPORT_DUMP=/path/out.json DIGEST_URL=http://127.0.0.1:8099 \
//	  KELD_STUDY_CORPUS_ROOT=<pinned snapshot> KELD_STUDY_SESSION_ID=<id> DIGEST_SESSIONS=14 \
//	  go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run ReportDump -v -timeout 480m
//
// REPORT_DUMP_DRY=1 lists the sessions it would run and writes nothing — the cheap way to
// confirm the corpus really is pinned before spending hours of inference.
// REPORT_DUMP_SKIP_CORPUS=1 runs only the two hand-authored non-engineering sessions.
func TestReportDump(t *testing.T) {
	out := os.Getenv("REPORT_DUMP")
	if out == "" {
		t.Skip("set REPORT_DUMP")
	}
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	dry := os.Getenv("REPORT_DUMP_DRY") != ""

	o := DefaultMineOpts()
	o.K = 12
	l := NewLlama(url)
	beatTurns := BeatTurnsFromEnv()

	// The document-frequency table decides which terms count as subjects. Reported, and
	// recorded in the dump, because a cold-start table measures the fallback rather than the
	// rule — the same reason the sweep reports it.
	df := InitDocFreqFromCorpus()
	t.Logf("DOC FREQUENCY  %d sessions, %d distinct terms, representative=%v (root %s)",
		df.sessions, len(df.count), df.representative(), corpusRoot())

	// Sessions, in the order they are dumped. The two hand-authored non-engineering
	// transcripts come first and are NOT counted against the corpus session budget: the
	// audience requirement is that a non-technical org admin can read the work out of Atlas,
	// and a dump of nothing but code sessions cannot support a judgement about that.
	type source struct {
		path, label, kind string
	}
	var sources []source
	for _, f := range []string{
		"testdata/nontech/finance-close.jsonl",
		"testdata/nontech/marketing-launch.jsonl",
	} {
		sources = append(sources, source{f, strings.TrimSuffix(filepath.Base(f), ".jsonl"),
			"hand-authored non-engineering"})
	}
	if os.Getenv("REPORT_DUMP_SKIP_CORPUS") == "" {
		// The sweep's own selection: stratified across projects, with this harness's own
		// session first when it can be identified. Both read corpusRoot(), so a pinned
		// snapshot pins session selection too — which it did not before ad55212.
		files := StratifiedTranscripts()
		if me := ThisSessionTranscript(); me != "" {
			keep := []string{me}
			for _, f := range files {
				if f != me {
					keep = append(keep, f)
				}
			}
			files = keep
		}
		kept := 0
		for _, f := range files {
			if kept >= sessionBudget() {
				break
			}
			ws, e1 := Mine(f, o)
			ocs, e2 := Outcomes(f, o)
			deltas, e3 := sessionDeltas(f, o)
			if e1 != nil || e2 != nil || e3 != nil || len(ws) < 16 || len(ws) != len(ocs) || len(deltas) != len(ws) {
				continue
			}
			sources = append(sources, source{f, filepath.Base(f), "corpus (engineering)"})
			kept++
		}
	}

	arms := []struct {
		name   string
		anchor bool
	}{{"anchor ON", true}, {"anchor OFF", false}}

	var run runDump
	run.CorpusRoot = corpusRoot()
	run.SessionID = os.Getenv("KELD_STUDY_SESSION_ID")
	run.Commit = os.Getenv("REPORT_DUMP_COMMIT")
	run.Model = "Qwen3-4B-Instruct-2507 Q4_K_M"
	run.Budget = DefaultPromptCharBudget
	run.WindowFloor = MinTurnChars
	run.BeatTurns = beatTurns
	run.ReportWindows = reportWindows
	run.MineK = o.K
	run.DFSessions = df.sessions
	run.DFTerms = len(df.count)
	run.DFRepresentative = df.representative()
	run.MaxBeatSelection = MaxBeatSelection
	run.SessionViewCap = SessionViewCap
	run.RetainCap = retainListMaxCount
	run.RetainRuneCap = retainListMaxTotal

	for si, src := range sources {
		ws, e1 := Mine(src.path, o)
		ocs, e2 := Outcomes(src.path, o)
		deltas, e3 := sessionDeltas(src.path, o)
		if e1 != nil || e2 != nil || e3 != nil || len(ws) <= reportWindows[len(reportWindows)-1] ||
			len(ws) != len(ocs) || len(deltas) != len(ws) {
			t.Logf("skip %s: %v %v %v (%d windows)", src.label, e1, e2, e3, len(ws))
			continue
		}
		project := strings.TrimSuffix(filepath.Base(src.path), ".jsonl")
		if src.kind != "hand-authored non-engineering" {
			project = projectFromPath(src.path)
		}
		sd := sessionDump{
			Index: si + 1, Label: src.label, Path: src.path, Project: project,
			Kind: src.kind, Windows: len(ws),
		}
		t.Logf("SESSION %d  %s  (%s, %d windows, project %s)\n    %s",
			sd.Index, sd.Label, sd.Kind, sd.Windows, sd.Project, sd.Path)
		if dry {
			run.Sessions = append(run.Sessions, sd)
			continue
		}

		for _, arm := range arms {
			ad := armDump{Name: arm.name, Anchor: arm.anchor}
			var rec SessionRecord
			var beats []Beat
			var bw BeatWindower
			var cur Digest
			var haveCur bool
			var prevReportSrc string
			var pending []beatEvent

			stepAt := map[int]int{}
			for s, idx := range reportWindows {
				stepAt[idx] = s
			}

			for idx := 0; idx <= reportWindows[len(reportWindows)-1]; idx++ {
				w := ws[idx]
				turns := Render(w)
				view := RenderSessionView(w)
				// Asserted rather than assumed, as the sweep does: a misaligned delta would
				// mean the record describes a different turn from the prompt, silently.
				if deltas[idx].PromptID != w.PromptID {
					t.Fatalf("%s idx %d: delta/window misalignment (%q vs %q)",
						src.label, idx, deltas[idx].PromptID, w.PromptID)
				}
				rec = rec.Observe(deltas[idx], Extract(deltas[idx])).WithProject(project)

				// A beat every beatTurns user turns, on the beat tier's own contiguous
				// window — the sweep's geometry exactly.
				if (idx+1)%beatTurns == 0 {
					bwin := bw.Next(deltas, idx)
					ev := beatEvent{
						WindowIndex: idx, SpanTurns: bwin.SpanTurns, KeptTurns: bwin.KeptTurns,
						OverlapTurns: bwin.OverlapTurns, TotalRunes: bwin.TotalRunes,
						Prompt: BeatPrompt(rec.Block(), bwin.Rendered),
					}
					before := l.Attempts()
					var text string
					var berr error
					if recovered(t, fmt.Sprintf("beat s%d %s i%d", sd.Index, arm.name, idx), func() {
						text, berr = l.GenerateBeat(rec.Block(), bwin.Rendered)
					}) {
						ev.Panicked = true
					} else if berr != nil {
						ev.Err = berr.Error()
					} else {
						var kept bool
						beats, kept = AppendBeat(beats, text, GroundOf(bwin.Window))
						ev.Text, ev.Kept = text, kept
						if kept {
							ev.Ordinal = beats[len(beats)-1].Ordinal
							ev.ChangedSubject = beats[len(beats)-1].ChangedSubject
						}
					}
					ev.Attempts = l.Attempts() - before
					pending = append(pending, ev)
				}

				step, isReport := stepAt[idx]
				if !isReport {
					continue
				}

				rd := reportDump{
					Step: step, WindowIndex: idx, Beats: pending,
					Record: rec.Block(), RecordPopulated: rec.Populated(),
					BeatsAccumulated: beats,
					NewTurnsFull:     turns, ViewFull: view,
				}
				pending = nil
				facts := FactsFrom(Extract(w), ocs[:idx+1]).WithWindow(w).WithPlace("", "", project)
				rd.Facts = facts.Block()

				// The measured trigger is arm-independent (the confound fix); only what the
				// prompt is TOLD is gated on the arm.
				reason := TriggerVolume
				if step > 0 && SubjectShifted(prevReportSrc, turns) {
					reason = TriggerFocusShift
				}
				why := reason
				if !arm.anchor {
					why = TriggerVolume
				}
				rd.Reason, rd.Why = string(reason), string(why)

				if step > 0 {
					named := Identifiers(cur)
					bounded := boundRetainList(named)
					rd.RetainNamed = len(named)
					rd.RetainList = bounded
					rd.RetainRunes = retainListJoinedLen(bounded)
					for _, n := range named {
						if !hasTerm(bounded, n) {
							rd.RetainDropped = append(rd.RetainDropped, n)
						}
					}
					rd.OpenItemsIn = priorOpenItems(cur)
					rd.PrevUnresolved = cur.Unresolved
				}

				var d Digest
				var err error
				attemptsBefore := l.Attempts()
				subsBefore := l.EmptyUnresolvedSubstitutions()
				panicked := recovered(t, fmt.Sprintf("s%d %s step%d", sd.Index, arm.name, step), func() {
					if step == 0 {
						p, window := createPromptAndWindow("work session", turns, view, facts.Block())
						rd.Prompt, rd.Window = p, window
						rd.ViewIncluded = viewIncludedIn(p, view, createViewHeader)
						d, err = l.CreateDigestWithView("work session", turns, view, facts.Block())
						return
					}
					in := RefineInput{
						SessionLabel: "work session", Record: rec, Beats: beats,
						SessionView: view, NewTurns: turns, Why: why,
					}
					p, window := updatePromptAndWindow(cur, in)
					rd.Prompt, rd.Window = p, window
					rd.ViewIncluded = viewIncludedIn(p, view, viewHeader)
					rd.BeatsShown = beatsShownIn(p, beats)
					rd.BeatsRendered = beatsRenderedIn(p, beats)
					d, err = l.RefineFrom(cur, in)
				})
				rd.Attempts = l.Attempts() - attemptsBefore
				rd.EmptyUnresolvedSubstituted = l.EmptyUnresolvedSubstitutions() > subsBefore
				rd.Kind = map[bool]string{true: "create", false: "refine"}[step == 0]
				rd.PromptRunes = runeLen(rd.Prompt)
				rd.WindowRunes = runeLen(rd.Window)
				rd.WindowMargin = windowMargin(rd.Window)
				rd.WindowClipped = strings.HasPrefix(rd.Window, omittedNotice)

				switch {
				case panicked:
					rd.Panicked = true
				case err != nil:
					rd.Err = err.Error()
				default:
					rd.Digest = &d
					rd.Malformed = ValidateDigest(d)
					if haveCur {
						rd.diffAgainst(cur)
					}
					rec = rec.NoteTurningPoint(step+1, reason)
					cur, haveCur = d, true
				}
				t.Logf("  s%d %s step%d (window %d): kind=%s prompt=%d/%d window=%d margin=%+d "+
					"beats=%d/%d retain=%d attempts=%d err=%q panic=%v",
					sd.Index, arm.name, step, idx, rd.Kind, rd.PromptRunes, DefaultPromptCharBudget,
					rd.WindowRunes, rd.WindowMargin, rd.BeatsShown, len(beats), len(rd.RetainList),
					rd.Attempts, rd.Err, rd.Panicked)
				ad.Reports = append(ad.Reports, rd)
				prevReportSrc = turns
			}
			// Beats generated after the last report have no report to attach to; they are
			// still part of what happened, so they are carried rather than dropped.
			ad.TrailingBeats = pending
			sd.Arms = append(sd.Arms, ad)
		}
		run.Sessions = append(run.Sessions, sd)

		// Written after every session, so a run interrupted hours in still yields the
		// sessions it finished instead of nothing.
		if err := writeJSON(out, run); err != nil {
			t.Fatal(err)
		}
	}

	if err := writeJSON(out, run); err != nil {
		t.Fatal(err)
	}
	var reports, failures int
	for _, s := range run.Sessions {
		for _, a := range s.Arms {
			for _, r := range a.Reports {
				reports++
				if r.Digest == nil {
					failures++
				}
			}
		}
	}
	t.Logf("wrote %s: %d sessions, %d reports, %d failed", out, len(run.Sessions), reports, failures)
}

func writeJSON(path string, v any) error {
	blob, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, blob, 0o600)
}

// runDump and the types below are the on-disk shape of the dump. Everything the artifact
// states is read from here, so a claim in the artifact is a claim about a recorded
// observation rather than about the code's intent.
type runDump struct {
	Commit           string        `json:"commit"`
	CorpusRoot       string        `json:"corpus_root"`
	SessionID        string        `json:"session_id"`
	Model            string        `json:"model"`
	Budget           int           `json:"prompt_char_budget"`
	WindowFloor      int           `json:"window_floor"`
	BeatTurns        int           `json:"beat_every_user_turns"`
	ReportWindows    []int         `json:"report_windows"`
	MineK            int           `json:"mine_k"`
	DFSessions       int           `json:"df_sessions"`
	DFTerms          int           `json:"df_terms"`
	DFRepresentative bool          `json:"df_representative"`
	MaxBeatSelection int           `json:"max_beat_selection"`
	SessionViewCap   int           `json:"session_view_cap"`
	RetainCap        int           `json:"retain_list_max_count"`
	RetainRuneCap    int           `json:"retain_list_max_runes"`
	Sessions         []sessionDump `json:"sessions"`
}

type sessionDump struct {
	Index   int       `json:"index"`
	Label   string    `json:"label"`
	Path    string    `json:"path"`
	Project string    `json:"project"`
	Kind    string    `json:"kind"`
	Windows int       `json:"windows"`
	Arms    []armDump `json:"arms"`
}

type armDump struct {
	Name          string       `json:"name"`
	Anchor        bool         `json:"anchor"`
	Reports       []reportDump `json:"reports"`
	TrailingBeats []beatEvent  `json:"trailing_beats,omitempty"`
}

// beatEvent is one beat generation — including one that failed, which is why the error and the
// attempt count are fields rather than a log line.
type beatEvent struct {
	WindowIndex    int    `json:"window_index"`
	Ordinal        int    `json:"ordinal,omitempty"`
	SpanTurns      int    `json:"span_turns"`
	KeptTurns      int    `json:"kept_turns"`
	OverlapTurns   int    `json:"overlap_turns"`
	TotalRunes     int    `json:"total_runes"`
	Prompt         string `json:"prompt"`
	Text           string `json:"text,omitempty"`
	Kept           bool   `json:"kept"`
	ChangedSubject bool   `json:"changed_subject"`
	Attempts       int    `json:"attempts"`
	Err            string `json:"err,omitempty"`
	Panicked       bool   `json:"panicked,omitempty"`
}

type reportDump struct {
	Step        int    `json:"step"`
	Kind        string `json:"kind"`
	WindowIndex int    `json:"window_index"`

	// Inputs, by truth status. Record and Facts are counted on device; BeatsRendered is
	// model-generated; the rest is transcript text as included.
	Record           string      `json:"record"`
	RecordPopulated  []string    `json:"record_populated"`
	Facts            string      `json:"facts"`
	Beats            []beatEvent `json:"beats_generated_since_previous_report,omitempty"`
	BeatsAccumulated []Beat      `json:"beats_accumulated,omitempty"`
	BeatsShown       int         `json:"beats_shown_in_prompt"`
	BeatsRendered    string      `json:"beats_rendered_in_prompt,omitempty"`

	RetainNamed   int      `json:"retain_list_named"`
	RetainList    []string `json:"retain_list,omitempty"`
	RetainRunes   int      `json:"retain_list_runes"`
	RetainDropped []string `json:"retain_list_dropped,omitempty"`

	OpenItemsIn    []string `json:"open_items_in,omitempty"`
	PrevUnresolved []string `json:"prev_unresolved,omitempty"`

	ViewFull     string `json:"view_full"`
	ViewIncluded string `json:"view_included"`
	NewTurnsFull string `json:"new_turns_full"`
	// Window is fitTurns' own output — the assembly's return value, not a landmark search
	// of the finished prompt (see assertPromptWithinBudget's doc for why that distinction
	// is load-bearing).
	Window        string `json:"window_included"`
	WindowRunes   int    `json:"window_runes"`
	WindowClipped bool   `json:"window_clipped"`
	WindowMargin  int    `json:"window_margin"`

	Reason string `json:"trigger_measured"`
	Why    string `json:"trigger_told_to_prompt"`

	Prompt      string `json:"prompt"`
	PromptRunes int    `json:"prompt_runes"`

	Digest                     *Digest  `json:"digest,omitempty"`
	Malformed                  []string `json:"malformed,omitempty"`
	Err                        string   `json:"err,omitempty"`
	Panicked                   bool     `json:"panicked,omitempty"`
	Attempts                   int      `json:"attempts"`
	EmptyUnresolvedSubstituted bool     `json:"empty_unresolved_substituted,omitempty"`

	// What changed from the previous report of the same arm and session. Computed with the
	// harness's OWN survival test (lostFacts, i.e. proseHay) so the artifact and the T4
	// figure cannot disagree about what "still appears" means.
	FactsObliged  []string `json:"facts_obliged,omitempty"`
	FactsRetained []string `json:"facts_retained,omitempty"`
	FactsDropped  []string `json:"facts_dropped,omitempty"`
	ItemsKept     []string `json:"items_kept,omitempty"`
	ItemsClosed   []string `json:"items_closed,omitempty"`
	ItemsAdded    []string `json:"items_added,omitempty"`
}

// diffAgainst records what this report did with the previous one's facts and open items.
//
// The fact set is the retain-list the prompt actually carried — the entries the report was
// explicitly obliged to keep — so a drop here is a drop of something named, not of something
// the model was never shown. Survival is lostFacts', so it is the same test T4 reports.
func (r *reportDump) diffAgainst(prev Digest) {
	if r.Digest == nil {
		return
	}
	r.FactsObliged = r.RetainList
	if len(r.FactsObliged) > 0 {
		lost := lostFacts(*r.Digest, r.FactsObliged)
		for _, f := range r.FactsObliged {
			if hasTerm(lost, f) {
				r.FactsDropped = append(r.FactsDropped, f)
			} else {
				r.FactsRetained = append(r.FactsRetained, f)
			}
		}
	}
	for _, item := range prev.Unresolved {
		if hasTerm(r.Digest.Unresolved, item) {
			r.ItemsKept = append(r.ItemsKept, item)
		} else {
			r.ItemsClosed = append(r.ItemsClosed, item)
		}
	}
	for _, item := range r.Digest.Unresolved {
		if !hasTerm(prev.Unresolved, item) {
			r.ItemsAdded = append(r.ItemsAdded, item)
		}
	}
}

// viewIncludedIn recovers the whole-session view AS THE PROMPT CARRIES IT.
//
// By containment of a real clipSessionViewFor/clipLines output, not by parsing the prompt
// between two headings: the view sits next to arbitrary conversation text, and locating a
// section by landmark is the mistake assertPromptWithinBudget was already fixed for once (a
// window quoting a heading moved the measured boundary in both directions). Same technique
// beatsShownIn uses, and it cannot report a string the assembly did not write, because every
// candidate is produced by the same clipping function the assembly calls.
func viewIncludedIn(prompt, view, header string) string {
	v := strings.TrimSpace(view)
	if v == "" {
		return ""
	}
	if strings.Contains(prompt, header+v) {
		return v
	}
	for room := SessionViewCap; room >= 240; room-- {
		if c := clipLines(v, room); c != "" && strings.Contains(prompt, header+c) {
			return c
		}
	}
	return ""
}

// beatsRenderedIn returns the beat series exactly as the prompt carries it, by the same
// containment technique beatsShownIn counts with.
func beatsRenderedIn(prompt string, all []Beat) string {
	for k := len(all); k > 0; k-- {
		if s := RenderBeats(SelectBeats(all, k)); s != "" && strings.Contains(prompt, s) {
			return s
		}
	}
	return ""
}
