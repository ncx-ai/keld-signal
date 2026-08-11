//go:build llmstudy

package llmstudy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestBeatDump prints the actual beat series for real sessions, in order, so a human can
// judge whether the series tracks the work. Beats are the cheap frequent pass; the whole
// design rests on them being individually sound and collectively a trajectory.
//
// It also reports the SHAPE of what came back, per beat and in aggregate: the unclipped
// generation and its length, how many complete sentences survived the cap, and how many distinct
// opening phrasings the set used. Those are the figures that exposed the defect this harness was
// blind to — 46 of 47 beats ending mid-clause and 46 of 47 opening with the same four words —
// and they were invisible because nothing but the text was recorded.
//
//	BEAT_DUMP=/path/out.json DIGEST_URL=http://127.0.0.1:8099 \
//	  KELD_STUDY_SESSION_ID=<id> go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run BeatDump -v -timeout 60m
func TestBeatDump(t *testing.T) {
	out := os.Getenv("BEAT_DUMP")
	if out == "" {
		t.Skip("set BEAT_DUMP")
	}
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	l := NewLlama(url)

	type beatOut struct {
		Ordinal        int      `json:"ordinal"`
		ChangedSubject bool     `json:"changed_subject"`
		SubjectTerms   []string `json:"subject_terms"`
		AtWindow       int      `json:"at_window"`
		Sentences      int      `json:"sentences"`
		Runes          int      `json:"runes"`
		RawRunes       int      `json:"raw_runes"`
		Clipped        bool     `json:"clipped"`
		// ProgressClaims is the overall-progress characterisation the beat makes, if any. It
		// must be empty on every beat: the check runs inside GenerateBeat's retry loop, so a
		// non-empty entry here means an offending generation reached the series anyway.
		ProgressClaims []string `json:"progress_claims,omitempty"`
		UserTurn       string   `json:"user_turn"`
		Text           string   `json:"text"`
		Raw            string   `json:"raw"`
	}
	type sessOut struct {
		Label      string    `json:"label"`
		Windows    int       `json:"windows"`
		Record     string    `json:"record"`
		Beats      []beatOut `json:"beats"`
		Asked      int       `json:"asked"`
		Kept       int       `json:"kept"`
		Restated   int       `json:"discarded_as_restatement"`
		Failed     int       `json:"failed"`
		EveryTurns int       `json:"every_turns"`
	}

	var sources []struct{ path, label string }
	if me := ThisSessionTranscript(); me != "" {
		sources = append(sources, struct{ path, label string }{me, "THIS SESSION (you lived it — judge accuracy directly)"})
	}
	sources = append(sources, struct{ path, label string }{
		"testdata/nontech/finance-close.jsonl", "Month-end close (hand-authored accounting)"})
	for _, f := range StratifiedTranscripts() {
		if strings.Contains(f, "keld-atlas") {
			if ws, err := Mine(f, DefaultMineOpts()); err == nil && len(ws) >= 16 {
				sources = append(sources, struct{ path, label string }{f, "Real engineering session (" + filepath.Base(f)[:8] + ")"})
				break
			}
		}
	}

	o := DefaultMineOpts()
	o.K = 12
	var all []sessOut

	for _, src := range sources {
		ws, err := Mine(src.path, o)
		if err != nil || len(ws) < 8 {
			t.Logf("skip %s", src.label)
			continue
		}
		every := BeatTurnsFromEnv()
		s := sessOut{Label: src.label, Windows: len(ws), EveryTurns: every}
		proj := strings.TrimSuffix(filepath.Base(src.path), ".jsonl")

		var rec SessionRecord
		var beats []Beat
		for i, w := range ws {
			rec = rec.Observe(w, Extract(w)).WithProject(proj)
			if i%every != 0 {
				continue
			}
			s.Asked++
			// generateBeat, not GenerateBeat, for the unclipped generation: the cap can only
			// be judged against what it clipped (see BeatCap).
			raw, text, err := l.generateBeat(rec.Block(), Render(w))
			if err != nil {
				s.Failed++
				t.Logf("  beat at window %d failed: %v", i, err)
				continue
			}
			var stored bool
			// The user turn that prompted this window is grounded, non-model-authored
			// evidence of what the window is about, and it is what ChangedSubject falls back
			// on when the beat names nothing concrete (see beatSubjectTermsGrounded).
			ground := GroundOf(w)
			beats, stored = AppendBeat(beats, text, ground)
			if !stored {
				s.Restated++
				t.Logf("  DISCARDED as restatement at window %d: %s", i, text)
				continue
			}
			b := beats[len(beats)-1]
			turn := ground.Turn
			if len(turn) > 150 {
				turn = turn[:150] + "…"
			}
			s.Beats = append(s.Beats, beatOut{
				Ordinal:        b.Ordinal,
				ChangedSubject: b.ChangedSubject,
				SubjectTerms:   b.SubjectTerms,
				ProgressClaims: beatProgressClaims(b.Text),
				AtWindow:       i,
				Sentences:      countBeatSentences(b.Text),
				Runes:          len([]rune(b.Text)),
				RawRunes:       len([]rune(raw)),
				Clipped:        b.Text != strings.TrimSpace(raw),
				UserTurn:       turn,
				Text:           b.Text,
				Raw:            raw,
			})
		}
		s.Kept = len(beats)
		s.Record = rec.Block()
		all = append(all, s)
		var chg int
		for _, b := range beats {
			if b.ChangedSubject {
				chg++
			}
		}
		t.Logf("%s: %d windows, %d asked, %d kept, %d restated, %d failed (every %d turns), "+
			"changed_subject %d/%d", src.label, len(ws), s.Asked, s.Kept, s.Restated, s.Failed,
			every, chg, len(beats))
	}

	blob, _ := json.MarshalIndent(all, "", "  ")
	if err := os.WriteFile(out, blob, 0o600); err != nil {
		t.Fatal(err)
	}

	// The acceptance figures, computed here rather than by eye: the defect they catch passed
	// every length threshold in the suite.
	var midSentence, changed, n, claiming, abstained int
	var runes, raws []int
	sentences := map[int]int{}
	openings := map[string]int{}
	for _, s := range all {
		for _, b := range s.Beats {
			n++
			if lastSentenceStop([]rune(b.Text)) != len([]rune(b.Text)) {
				midSentence++
			}
			if b.ChangedSubject {
				changed++
			}
			if len(b.ProgressClaims) > 0 {
				claiming++
				t.Errorf("beat %d claims unobservable progress %v: %s",
					b.Ordinal, b.ProgressClaims, b.Text)
			}
			// A beat whose own text and whose prompting turn between them name nothing
			// concrete cannot be judged for a subject change, and is reported unchanged. The
			// count is the honest size of ChangedSubject's remaining lower bound.
			if len(b.SubjectTerms) == 0 {
				abstained++
			}
			sentences[b.Sentences]++
			runes = append(runes, b.Runes)
			raws = append(raws, b.RawRunes)
			openings[strings.ToLower(strings.Join(strings.Fields(b.Text)[:min(4, len(strings.Fields(b.Text)))], " "))]++
		}
	}
	if n == 0 {
		t.Fatalf("no beats generated; wrote %s", out)
	}
	sort.Ints(runes)
	sort.Ints(raws)
	var dist []string
	for k := 0; k <= 6; k++ {
		if sentences[k] > 0 {
			dist = append(dist, strconv.Itoa(k)+" sentence(s): "+strconv.Itoa(sentences[k]))
		}
	}
	t.Logf("n=%d  ending mid-sentence: %d (%.1f%%)  changed_subject: %d (%.1f%%)",
		n, midSentence, 100*float64(midSentence)/float64(n), changed, 100*float64(changed)/float64(n))
	t.Logf("claiming unobservable progress: %d (%.1f%%)  changed_subject abstentions: %d (%.1f%%)",
		claiming, 100*float64(claiming)/float64(n), abstained, 100*float64(abstained)/float64(n))
	t.Logf("sentences per beat: %s", strings.Join(dist, ", "))
	t.Logf("distinct openings (first four words): %d of %d", len(openings), n)
	t.Logf("kept runes: min=%d median=%d max=%d | unclipped: min=%d median=%d max=%d",
		runes[0], runes[len(runes)/2], runes[len(runes)-1], raws[0], raws[len(raws)/2], raws[len(raws)-1])
	t.Logf("wrote %s", out)
}

// countBeatSentences counts the complete sentences in a stored beat, by the same rule that
// decides where one ends.
func countBeatSentences(s string) int { return len(sentenceStops([]rune(s))) }
