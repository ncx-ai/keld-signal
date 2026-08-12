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

// TestBeatArmsDump generates BOTH beat arms over the SAME windows and records every input and
// every output of every pass.
//
//   - control: the current fused prompt, unchanged — "what you are working on, and where it has
//     got to", one inference over the window and the record.
//   - split: three narrow passes — entities (what is being worked on, typed), events (what this
//     window shows happened), composition (the prose, from those two and the record only).
//
// There is deliberately no description-only arm: with no movement at all the series stops being
// a narrative, which is the requirement.
//
// Nothing here judges the arms. A separate metric is being built and the outputs are reviewed
// blind afterwards, so this test records what was generated and what failed, and computes only
// figures that are counts of its own observations (attempts, sentences, runes, which pass burned
// them, which terms a beat used that its notes did not hold).
//
// The record is shared by both arms rather than accumulated twice, and that is a confound
// removed rather than a shortcut: Observe and WithProject are the only writers on this path
// (NoteTurningPoint needs a report, and no reports are generated here), so the two arms are
// given byte-identical records by construction.
//
//	BEAT_ARMS_DUMP=/path/out.json DIGEST_URL=http://127.0.0.1:8099 \
//	  KELD_STUDY_CORPUS_ROOT=<pinned snapshot> KELD_STUDY_SESSION_ID=<id> \
//	  BEAT_ARMS_SESSIONS=2 BEAT_ARMS_WINDOWS=30 \
//	  go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run BeatArmsDump -v -timeout 300m
//
// BEAT_ARMS_DRY=1 lists the sessions and windows it would run and writes nothing.
func TestBeatArmsDump(t *testing.T) {
	out := os.Getenv("BEAT_ARMS_DUMP")
	if out == "" {
		t.Skip("set BEAT_ARMS_DUMP")
	}
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	dry := os.Getenv("BEAT_ARMS_DRY") != ""
	l := NewLlama(url)
	o := DefaultMineOpts()
	o.K = 12
	beatTurns := BeatTurnsFromEnv()
	lastWindow := envInt("BEAT_ARMS_WINDOWS", 30)

	df := InitDocFreqFromCorpus()
	t.Logf("DOC FREQUENCY  %d sessions, %d distinct terms, representative=%v (root %s)",
		df.sessions, len(df.count), df.representative(), corpusRoot())

	// The two hand-authored non-engineering transcripts lead and are not counted against the
	// corpus budget: the audience requirement is that a non-technical reader can follow the
	// series, and an artifact of nothing but code sessions cannot support a judgement about it.
	type source struct{ path, label, kind string }
	var sources []source
	for _, f := range []string{
		"testdata/nontech/finance-close.jsonl",
		"testdata/nontech/marketing-launch.jsonl",
	} {
		sources = append(sources, source{f, strings.TrimSuffix(filepath.Base(f), ".jsonl"),
			"hand-authored non-engineering"})
	}
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
		if kept >= envInt("BEAT_ARMS_SESSIONS", 2) {
			break
		}
		if ws, err := Mine(f, o); err != nil || len(ws) < 16 {
			continue
		}
		sources = append(sources, source{f, filepath.Base(f), "corpus (engineering)"})
		kept++
	}

	run := beatArmsRun{
		Model:            "Qwen3-4B-Instruct-2507 Q4_K_M",
		ServerFlags:      os.Getenv("BEAT_ARMS_SERVER_FLAGS"),
		CorpusRoot:       corpusRoot(),
		SessionID:        os.Getenv("KELD_STUDY_SESSION_ID"),
		Commit:           os.Getenv("BEAT_ARMS_COMMIT"),
		BeatTurns:        beatTurns,
		MineK:            o.K,
		WindowChars:      BeatWindowChars,
		BeatCap:          BeatCap,
		OverlapPct:       beatOverlapPct,
		DFSessions:       df.sessions,
		DFTerms:          len(df.count),
		DFRepresentative: df.representative(),
		CandidateCap:     beatEntityCandidateCap,
	}

	for si, src := range sources {
		ws, e1 := Mine(src.path, o)
		deltas, e2 := sessionDeltas(src.path, o)
		if e1 != nil || e2 != nil || len(deltas) != len(ws) || len(ws) < beatTurns {
			t.Logf("skip %s: %v %v (%d windows)", src.label, e1, e2, len(ws))
			continue
		}
		project := strings.TrimSuffix(filepath.Base(src.path), ".jsonl")
		if src.kind != "hand-authored non-engineering" {
			project = RepoFromTranscriptPath(src.path)
		}
		last := lastWindow
		if last > len(ws)-1 {
			last = len(ws) - 1
		}
		sd := beatArmsSession{
			Index: si + 1, Label: src.label, Path: src.path, Project: project,
			Kind: src.kind, Windows: len(ws), WalkedTo: last,
		}
		t.Logf("SESSION %d  %s  (%s, %d windows, walking to %d, project %s)\n    %s",
			sd.Index, sd.Label, sd.Kind, sd.Windows, last, sd.Project, sd.Path)
		if dry {
			run.Sessions = append(run.Sessions, sd)
			continue
		}

		var rec SessionRecord
		var bw BeatWindower
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
			p := beatArmsPoint{
				WindowIndex: idx, SpanTurns: bwin.SpanTurns, KeptTurns: bwin.KeptTurns,
				OverlapTurns: bwin.OverlapTurns, OverlapRunes: bwin.OverlapRunes,
				Dropped: bwin.Dropped(), TotalRunes: bwin.TotalRunes,
				Clipped: strings.Contains(bwin.Rendered, beatOmittedNotice),
				Record:  rec.Block(), Window: bwin.Rendered,
				UserTurn: GroundOf(bwin.Window).Turn,
			}

			// CONTROL first, then split, every time — a fixed order so a difference between the
			// arms cannot be an artifact of which one warmed the server's cache.
			c := beatArmsControl{Prompt: BeatPrompt(p.Record, p.Window)}
			before := l.Attempts()
			var raw, text string
			var err error
			if recovered(t, fmt.Sprintf("control s%d i%d", sd.Index, idx), func() {
				raw, text, err = l.generateBeat(p.Record, p.Window)
			}) {
				c.Panicked = true
			} else if err != nil {
				c.Err = err.Error()
			} else {
				c.Raw, c.Text = raw, text
				c.ProgressClaims = beatProgressClaims(text)
				c.Ungrounded = ungroundedTerms(text, c.Prompt)
			}
			c.Attempts = l.Attempts() - before
			p.Control = c

			var s BeatSplit
			if recovered(t, fmt.Sprintf("split s%d i%d", sd.Index, idx), func() {
				s, _ = l.GenerateBeatSplit(rec, p.Window)
			}) {
				p.SplitPanicked = true
			}
			p.Split = s

			t.Logf("  s%d i%d  window %d runes | control %d attempt(s) err=%q | "+
				"split %d/%d/%d attempt(s) failed=%q entities=%d events=%d ungrounded=%v",
				sd.Index, idx, p.TotalRunes, c.Attempts, c.Err,
				s.EntityAttempts, s.EventAttempts, s.ComposeAttempts, s.Which(),
				len(s.Entities), len(s.Events), s.Ungrounded)
			sd.Beats = append(sd.Beats, p)
		}
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

	// Counts of this run's own observations. No comparison, no ranking: the arms are reviewed
	// blind against a metric this test does not own.
	var beats, cFail, cPanic, cRetry, sFail, sPanic, sRetry int
	byPass := map[string]int{}
	for _, s := range run.Sessions {
		for _, b := range s.Beats {
			beats++
			switch {
			case b.Control.Panicked:
				cPanic++
			case b.Control.Err != "":
				cFail++
			}
			if b.Control.Attempts > 1 {
				cRetry++
			}
			switch {
			case b.SplitPanicked:
				sPanic++
			case b.Split.Failed():
				sFail++
				byPass[b.Split.Which()]++
			}
			if b.Split.Attempts() > 3 {
				sRetry++
			}
		}
	}
	t.Logf("BEATS ASKED %d per arm", beats)
	t.Logf("CONTROL  generated %d  failed %d  recovered panics %d  more than one attempt %d",
		beats-cFail-cPanic, cFail, cPanic, cRetry)
	t.Logf("SPLIT    generated %d  failed %d %v  recovered panics %d  more than three attempts %d",
		beats-sFail-sPanic, sFail, byPass, sPanic, sRetry)
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

// beatArmsRun is the whole recorded run: the configuration it was generated under, then the
// sessions.
type beatArmsRun struct {
	Model       string `json:"model"`
	ServerFlags string `json:"server_flags"`
	CorpusRoot  string `json:"corpus_root"`
	SessionID   string `json:"session_id"`
	Commit      string `json:"commit"`

	BeatTurns    int `json:"beat_turns"`
	MineK        int `json:"mine_k"`
	WindowChars  int `json:"window_chars"`
	BeatCap      int `json:"beat_cap"`
	OverlapPct   int `json:"overlap_pct"`
	CandidateCap int `json:"candidate_cap"`

	DFSessions       int  `json:"df_sessions"`
	DFTerms          int  `json:"df_terms"`
	DFRepresentative bool `json:"df_representative"`

	Sessions []beatArmsSession `json:"sessions"`
}

type beatArmsSession struct {
	Index    int             `json:"index"`
	Label    string          `json:"label"`
	Path     string          `json:"path"`
	Project  string          `json:"project"`
	Kind     string          `json:"kind"`
	Windows  int             `json:"windows"`
	WalkedTo int             `json:"walked_to"`
	Beats    []beatArmsPoint `json:"beats"`
}

// beatArmsPoint is one beat window and what each arm made of it.
type beatArmsPoint struct {
	WindowIndex  int  `json:"window_index"`
	SpanTurns    int  `json:"span_turns"`
	KeptTurns    int  `json:"kept_turns"`
	OverlapTurns int  `json:"overlap_turns"`
	OverlapRunes int  `json:"overlap_runes"`
	Dropped      int  `json:"dropped_turns"`
	TotalRunes   int  `json:"total_runes"`
	Clipped      bool `json:"clipped"`

	Record   string `json:"record"`
	Window   string `json:"window"`
	UserTurn string `json:"user_turn"`

	Control       beatArmsControl `json:"control"`
	Split         BeatSplit       `json:"split"`
	SplitPanicked bool            `json:"split_panicked,omitempty"`
}

// beatArmsControl is the fused arm's one prompt and one answer.
type beatArmsControl struct {
	Prompt         string   `json:"prompt"`
	Raw            string   `json:"raw,omitempty"`
	Text           string   `json:"text,omitempty"`
	Attempts       int      `json:"attempts"`
	Err            string   `json:"err,omitempty"`
	Panicked       bool     `json:"panicked,omitempty"`
	ProgressClaims []string `json:"progress_claims,omitempty"`
	Ungrounded     []string `json:"ungrounded,omitempty"`
}
