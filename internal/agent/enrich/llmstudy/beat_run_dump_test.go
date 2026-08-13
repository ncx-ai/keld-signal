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

// TestBeatRunDump generates the production beat over the study corpus and records every input and
// every output, so a blind review round can follow without re-running inference.
//
// ONE ARM. The two-arm harness this replaces compared the fused prompt against a three-pass split;
// both are superseded (docs/superpowers/specs/2026-08-12-production-beat-design.md), so there is
// nothing left to compare and a comparison would only invite a verdict this harness has no
// standing to make.
//
// Nothing here judges anything. Every number it prints is a count over its own observations —
// beats asked, generated, failed, attempts, entries dropped by the anchoring guard, turn coverage,
// prompt size against the budget. The quality question belongs to the blind round.
//
// ⚠️ NO LATENCY OR RAM FIGURE IS RECORDED, DELIBERATELY. The viability case for this model rests
// on CPU-only measurements at documented flags (`--cache-ram` and `--no-repack` load-bearing);
// this run is CPU-only but at a thread count no laptop deployment would use and without
// `--no-repack`, so any resource number taken from it would misrepresent the case. That question
// needs its own run at the viability flags.
//
//	BEAT_RUN_DUMP=/path/out.json DIGEST_URL=http://127.0.0.1:8099 \
//	  KELD_STUDY_CORPUS_ROOT=<pinned snapshot> KELD_STUDY_SESSION_ID=<id> \
//	  BEAT_RUN_SESSIONS=2 BEAT_RUN_WINDOWS=30 \
//	  go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run BeatRunDump -v -timeout 300m
//
// BEAT_RUN_DRY=1 lists the sessions and windows it would run and writes nothing.
func TestBeatRunDump(t *testing.T) {
	out := os.Getenv("BEAT_RUN_DUMP")
	if out == "" {
		t.Skip("set BEAT_RUN_DUMP")
	}
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	dry := os.Getenv("BEAT_RUN_DRY") != ""
	l := NewLlama(url)
	o := DefaultMineOpts()
	beatTurns := BeatTurnsFromEnv()
	lastWindow := envInt("BEAT_RUN_WINDOWS", 30)

	df := InitDocFreqFromCorpus()
	t.Logf("DOC FREQUENCY  %d sessions, %d distinct terms, representative=%v (root %s)",
		df.sessions, len(df.count), df.representative(), corpusRoot())

	// ⚠️ REAL TRANSCRIPTS ARE THE CORPUS; THE SYNTHETIC PAIR IS A LABELLED MINORITY CHECK.
	//
	// The run this replaces drew two of its four sessions from hand-authored non-engineering
	// transcripts, so "19 of 19 generated on the first attempt" rested substantially on short,
	// clean, invented material. The difficulty lives in the real sessions — long tool outputs,
	// pasted code, interruptions, corrections, a user redirecting mid-task — and a figure that
	// averages the two says nothing about either. So the real sessions lead and are the
	// majority, the two hand-authored ones are kept and marked SYNTHETIC wherever they appear,
	// and every figure is reported once for each population as well as overall.
	//
	// The synthetic pair is kept rather than dropped because the project owner's requirement is
	// that this work for accountants and marketers as well as for codegen, and the pinned corpus
	// holds only Claude Code transcripts. It is the only non-engineering check available — now
	// as a control read against the real majority, not as half the evidence.
	type source struct {
		path, label, kind string
		synthetic         bool
	}
	var sources []source
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
		if kept >= envInt("BEAT_RUN_SESSIONS", 12) {
			break
		}
		// The sessions the prompt's worked examples were read from are held out, so no beat is
		// scored on material the model was shown an answer for. See beatExamples.
		if beatHeldOutSessions[filepath.Base(f)] {
			t.Logf("held out of the corpus (a worked example was read from it): %s", filepath.Base(f))
			continue
		}
		if ws, err := Mine(f, o); err != nil || len(ws) < 16 {
			continue
		}
		sources = append(sources, source{path: f, label: filepath.Base(f),
			kind: "real transcript (engineering)"})
		kept++
	}
	for _, f := range []string{
		"testdata/nontech/finance-close.jsonl",
		"testdata/nontech/marketing-launch.jsonl",
	} {
		sources = append(sources, source{path: f,
			label: strings.TrimSuffix(filepath.Base(f), ".jsonl"),
			kind:  "SYNTHETIC — hand-authored non-engineering", synthetic: true})
	}

	run := beatRun{
		Model:            "Qwen3-4B-Instruct-2507 Q4_K_M",
		ServerFlags:      os.Getenv("BEAT_RUN_SERVER_FLAGS"),
		CorpusRoot:       corpusRoot(),
		SessionID:        os.Getenv("KELD_STUDY_SESSION_ID"),
		Commit:           os.Getenv("BEAT_RUN_COMMIT"),
		BeatTurns:        beatTurns,
		WindowChars:      BeatWindowChars,
		BeatCap:          BeatCap,
		EventCap:         beatEventMaxRunes,
		EventMaxCount:    beatEventMaxCount,
		PromptBudget:     BeatPromptCharBudget,
		DFSessions:       df.sessions,
		DFTerms:          len(df.count),
		DFRepresentative: df.representative(),
	}

	for si, src := range sources {
		ws, e1 := Mine(src.path, o)
		deltas, e2 := sessionDeltas(src.path, o)
		if e1 != nil || e2 != nil || len(deltas) != len(ws) || len(ws) < beatTurns {
			t.Logf("skip %s: %v %v (%d windows)", src.label, e1, e2, len(ws))
			continue
		}
		project := strings.TrimSuffix(filepath.Base(src.path), ".jsonl")
		if !src.synthetic {
			project = RepoFromTranscriptPath(src.path)
		}
		last := lastWindow
		if last > len(ws)-1 {
			last = len(ws) - 1
		}
		sd := beatRunSession{
			Index: si + 1, Label: src.label, Path: src.path, Project: project,
			Kind: src.kind, Synthetic: src.synthetic, Windows: len(ws), WalkedTo: last,
		}
		t.Logf("SESSION %d  %s  (%s, %d windows, walking to %d, project %s)\n    %s",
			sd.Index, sd.Label, sd.Kind, sd.Windows, last, sd.Project, sd.Path)
		if dry {
			run.Sessions = append(run.Sessions, sd)
			continue
		}

		var rec SessionRecord
		var bw BeatWindower
		var cov BeatCoverage
		var beats []Beat
		for idx := 0; idx <= last; idx++ {
			if deltas[idx].PromptID != ws[idx].PromptID {
				t.Fatalf("%s idx %d: delta/window misalignment (%q vs %q)",
					src.label, idx, deltas[idx].PromptID, ws[idx].PromptID)
			}
			rec = rec.Observe(deltas[idx], Extract(deltas[idx])).WithProject(project)
			if (idx+1)%beatTurns != 0 {
				continue
			}
			bwin := bw.Next(deltas, idx)
			if bwin.Rendered == "" {
				continue
			}
			cov.Add(bwin)
			p := beatRunPoint{
				WindowIndex: idx, SpanTurns: bwin.SpanTurns, KeptTurns: bwin.KeptTurns,
				Dropped: bwin.Dropped(), TotalRunes: bwin.TotalRunes, Holed: bwin.Holed(),
				Record: rec.Block(), Window: bwin.Rendered,
				UserTurn: GroundOf(bwin.Window).Turn,
			}
			p.Prompt = BeatPrompt(p.Record, p.Window)
			p.PromptRunes = runeLen(p.Prompt)

			before := l.Attempts()
			var d BeatDraft
			var err error
			if recovered(t, fmt.Sprintf("beat s%d i%d", sd.Index, idx), func() {
				d, err = l.generateBeat(p.Record, p.Window)
			}) {
				p.Panicked = true
			} else if err != nil {
				p.Err = err.Error()
			} else {
				p.Subject, p.Events = d.Subject, d.Events
				p.Unanchored, p.Overflowed, p.Anchors = d.Unanchored, d.Overflowed, d.Anchors
				p.Unverified = d.Unverified
				p.SubjectAnchored, p.Raw, p.Text = d.SubjectAnchored, d.Raw, d.Text
				var stored bool
				beats, stored = AppendBeatDraft(beats, d)
				if stored {
					p.Ordinal = beats[len(beats)-1].Ordinal
				}
			}
			p.Attempts = l.Attempts() - before

			t.Logf("  s%d i%d  window %d runes (prompt %d of %d) | %d attempt(s) err=%q | "+
				"%d entries, %d unanchored, %d over cap, %d anchored only in the record, "+
				"%d unverified identifier(s)",
				sd.Index, idx, p.TotalRunes, p.PromptRunes, BeatPromptCharBudget, p.Attempts,
				p.Err, len(p.Events), len(p.Unanchored), len(p.Overflowed), p.RecordOnlyAnchors(),
				len(p.Unverified))
			sd.Beats = append(sd.Beats, p)
		}
		sd.Record = rec.Block()
		sd.Coverage = beatRunCoverage{
			SpanTurns: cov.SpanTurns, KeptTurns: cov.KeptTurns, UnreadTurns: cov.UnreadTurns(),
			Windows: cov.Windows, Holed: cov.Holed, LargestRunes: cov.LargestRunes,
			TurnCoverage: cov.TurnCoverage(),
		}
		t.Logf("   windows: %d turns spanned, %d read (%.1f%%), %d read by NO window; "+
			"%d of %d windows carry a hole marker; largest window %d runes",
			cov.SpanTurns, cov.KeptTurns, cov.TurnCoverage(), cov.UnreadTurns(),
			cov.Holed, cov.Windows, cov.LargestRunes)
		run.Sessions = append(run.Sessions, sd)
		// Written after every session, so an interrupted run still leaves everything it did.
		if blob, err := json.MarshalIndent(run, "", "  "); err == nil {
			os.WriteFile(out, blob, 0o600)
		}
	}

	blob, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, line := range run.tally().lines() {
		t.Log(line)
	}
	t.Logf("wrote %s", out)
}

// envInt reads a positive integer from the environment, or returns def.
func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// beatRun is the whole recorded run: the configuration it was generated under, then the sessions.
type beatRun struct {
	Model       string `json:"model"`
	ServerFlags string `json:"server_flags"`
	CorpusRoot  string `json:"corpus_root"`
	SessionID   string `json:"session_id"`
	Commit      string `json:"commit"`

	BeatTurns     int `json:"beat_turns"`
	WindowChars   int `json:"window_chars"`
	BeatCap       int `json:"beat_cap"`
	EventCap      int `json:"event_cap"`
	EventMaxCount int `json:"event_max_count"`
	PromptBudget  int `json:"prompt_budget"`

	DFSessions       int  `json:"df_sessions"`
	DFTerms          int  `json:"df_terms"`
	DFRepresentative bool `json:"df_representative"`

	Sessions []beatRunSession `json:"sessions"`
}

type beatRunSession struct {
	Index   int    `json:"index"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Project string `json:"project"`
	Kind    string `json:"kind"`
	// Synthetic marks a hand-authored session. It rides every session so no figure taken from
	// this dump can mix invented material into a real one without saying so.
	Synthetic bool            `json:"synthetic"`
	Windows   int             `json:"windows"`
	WalkedTo  int             `json:"walked_to"`
	Record    string          `json:"record"`
	Coverage  beatRunCoverage `json:"coverage"`
	Beats     []beatRunPoint  `json:"beats"`
}

// beatRunCoverage is the geometry's own figures, which are properties of the SEQUENCE of windows
// and so cannot be read off any single beat.
type beatRunCoverage struct {
	SpanTurns    int     `json:"span_turns"`
	KeptTurns    int     `json:"kept_turns"`
	UnreadTurns  int     `json:"unread_turns"`
	Windows      int     `json:"windows"`
	Holed        int     `json:"holed_windows"`
	LargestRunes int     `json:"largest_window_runes"`
	TurnCoverage float64 `json:"turn_coverage_pct"`
}

// beatRunPoint is one beat window and what the pass made of it.
type beatRunPoint struct {
	WindowIndex int  `json:"window_index"`
	Ordinal     int  `json:"ordinal,omitempty"`
	SpanTurns   int  `json:"span_turns"`
	KeptTurns   int  `json:"kept_turns"`
	Dropped     int  `json:"dropped_turns"`
	TotalRunes  int  `json:"total_runes"`
	Holed       bool `json:"holed"`

	Record   string `json:"record"`
	Window   string `json:"window"`
	UserTurn string `json:"user_turn"`

	Prompt      string `json:"prompt"`
	PromptRunes int    `json:"prompt_runes"`

	Attempts int    `json:"attempts"`
	Err      string `json:"err,omitempty"`
	Panicked bool   `json:"panicked,omitempty"`

	Subject    string   `json:"subject,omitempty"`
	Events     []string `json:"events,omitempty"`
	Unanchored []string `json:"unanchored,omitempty"`
	Overflowed []string `json:"overflowed,omitempty"`
	// Anchors is where each kept entry was anchored. An entry anchored only in the RECORD is the
	// seam signal: its antecedent fell on the other side of a window boundary.
	Anchors []BeatAnchor `json:"anchors,omitempty"`
	// Unverified are the strong identifiers the stored beat uses that occur nowhere in the
	// evidence. Recorded, never enforced.
	Unverified      []string `json:"unverified,omitempty"`
	SubjectAnchored bool     `json:"subject_anchored"`
	Raw             string   `json:"raw,omitempty"`
	Text            string   `json:"text,omitempty"`
}

// Failed reports a beat that produced no text, whether by error or by panic.
func (p beatRunPoint) Failed() bool { return p.Panicked || p.Err != "" }

// RecordOnlyAnchors counts the entries anchored in the measured record and NOT in this beat's own
// window — the seam signal, and the one figure that says what dropping window overlap cost.
func (p beatRunPoint) RecordOnlyAnchors() int {
	var n int
	for _, a := range p.Anchors {
		if !a.InWindow {
			n++
		}
	}
	return n
}
