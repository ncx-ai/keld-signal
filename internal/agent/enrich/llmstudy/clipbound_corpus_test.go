//go:build llmstudy

package llmstudy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorpusClipSitesHaveADelimiter measures how often each turn-text bound actually fires on
// the real corpus and whether a delimiter is available inside it — the evidence behind
// clipbound.go's figures, kept as a test rather than quoted from a transcript so the numbers
// can be re-derived instead of trusted.
//
// It ASSERTS the property compliance rests on: at PerTurnChars no over-length turn lacks both a
// sentence end and a line break, so nothing is dropped at that budget. If that ever stops
// holding, clipTurn starts discarding whole turns and this says so rather than leaving it to be
// noticed in a rate.
func TestCorpusClipSitesHaveADelimiter(t *testing.T) {
	files := StratifiedTranscripts()
	o := DefaultMineOpts()
	o.K = 12
	var (
		turns, over, haveSentence, haveLine, haveNeither int
		lostToLine, lostToSentence                       int
		toolArgs, toolOver                               int
		digestOver, digestHaveLine, digestHaveSentence   int
	)
	n := 0
	for _, f := range files {
		if n >= 20 {
			break
		}
		recs, err := records(f, MineOpts{K: 12, PerTurnChars: 0, WindowChars: 0})
		if err != nil {
			continue
		}
		if len(recs) < 40 {
			continue
		}
		n++
		for _, r := range recs {
			txt := elideCode(r.text)
			turns++
			if r.role == RoleTool {
				toolArgs++
				if len([]rune(txt)) > 80 {
					toolOver++
				}
			}
			rr := []rune(txt)
			if len(rr) > o.PerTurnChars {
				over++
				head := string(rr[:o.PerTurnChars])
				si := lastSentenceStop([]rune(head))
				li := strings.LastIndexByte(head, '\n')
				switch {
				case si > 0:
					haveSentence++
					lostToSentence += o.PerTurnChars - si
				case li > 0:
					haveLine++
					lostToLine += o.PerTurnChars - len([]rune(head[:li]))
				default:
					haveNeither++
				}
			}
			if len(rr) > digestClip {
				digestOver++
				head := string(rr[:digestClip])
				if lastSentenceStop([]rune(head)) > 0 {
					digestHaveSentence++
				} else if strings.LastIndexByte(head, '\n') > 0 {
					digestHaveLine++
				}
			}
		}
	}
	t.Logf("sessions=%d turns=%d", n, turns)
	t.Logf("PerTurnChars=%d: %d turns over (%.1f%%); of those sentence-end available %d, "+
		"line-break-only %d, NEITHER %d", o.PerTurnChars, over, 100*float64(over)/float64(turns),
		haveSentence, haveLine, haveNeither)
	if haveSentence > 0 {
		t.Logf("   mean runes given up to reach a sentence end: %.0f", float64(lostToSentence)/float64(haveSentence))
	}
	if haveLine > 0 {
		t.Logf("   mean runes given up to reach a line break: %.0f", float64(lostToLine)/float64(haveLine))
	}
	t.Logf("digestClip=%d: %d over; sentence %d, line-only %d, neither %d", digestClip,
		digestOver, digestHaveSentence, digestHaveLine, digestOver-digestHaveSentence-digestHaveLine)
	t.Logf("tool lines: %d, over 80 runes: %d", toolArgs, toolOver)
	if haveNeither > 0 {
		t.Errorf("%d of %d over-length turns have neither a sentence end nor a line break inside "+
			"PerTurnChars, so clipTurn drops them whole — clipbound.go's affordability claim no "+
			"longer holds on this corpus", haveNeither, over)
	}
}

// TestCorpusToolArgumentsExceedTheirCap measures how often toolLine's bound fires, and on which
// argument key. This is the measurement that made the whole rule urgent: a path cut short is a
// false path, and a command cut short is a false command that SessionRecord.Observe then stores
// as a verbatim-verified subject.
func TestCorpusToolArgumentsExceedTheirCap(t *testing.T) {
	files := StratifiedTranscripts()
	byKey := map[string]int{}
	overByKey := map[string]int{}
	n := 0
	for _, f := range files {
		if n >= 20 {
			break
		}
		lines, err := rawLines(f)
		if err != nil || len(lines) < 40 {
			continue
		}
		n++
		for _, l := range lines {
			var m msg
			if jsonUnmarshal(l.Message, &m) != nil {
				continue
			}
			var blocks []block
			if jsonUnmarshal(m.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				for _, k := range toolArgKeys {
					raw, ok := b.Input[k]
					if !ok {
						continue
					}
					var v string
					if jsonUnmarshal(raw, &v) != nil || v == "" {
						continue
					}
					if k == "file_path" || k == "path" || k == "notebook_path" {
						v = baseName(v)
					}
					byKey[k]++
					if runeLen(v) > 80 {
						overByKey[k]++
					}
					break
				}
			}
		}
	}
	t.Logf("sessions=%d", n)
	for k := range byKey {
		t.Logf("  %-14s seen %5d, over 80 runes %5d (%.1f%%)", k, byKey[k], overByKey[k],
			100*float64(overByKey[k])/float64(byKey[k]))
	}
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func baseName(p string) string            { return filepath.Base(p) }

func rawLines(path string) ([]line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		if l.IsSidechain || l.IsMeta || l.IsCompactSummary {
			continue
		}
		out = append(out, l)
	}
	return out, sc.Err()
}

// TestCorpusDelimiterCutIsAffordable re-derives clipAllowancePct's figures: what a
// delimiter-respecting cut costs against the rune-count cut it replaces, per site, over the
// sweep's own corpus.
//
// It ASSERTS affordability rather than only logging it, because the first measured pair of sweeps
// under a strictly-retreating rule regressed four thresholds in the anchor-ON arm — the cut was
// throwing away 40% of the coarse session view — and a convention honoured at that price is the
// trade this study exists to refuse. If the forward allowance ever stops paying for itself on a
// real corpus, that is a finding, not a rounding error.
func TestCorpusDelimiterCutIsAffordable(t *testing.T) {
	files := StratifiedTranscripts()
	if me := ThisSessionTranscript(); me != "" {
		files = append([]string{me}, files...)
	}
	o := DefaultMineOpts()
	type acc struct{ old, now, drops int }
	var view, turn acc
	n := 0
	for _, f := range files {
		if n >= 14 {
			break
		}
		recs, err := records(f, o)
		if err != nil || len(recs) < 40 {
			continue
		}
		n++
		for _, r := range recs {
			txt := elideCode(r.text)
			for _, tc := range []struct {
				cap int
				a   *acc
			}{{digestClip, &view}, {o.PerTurnChars, &turn}} {
				if runeLen(txt) <= tc.cap {
					continue
				}
				tc.a.old += runeLen(clip(txt, tc.cap))
				got := clipTurn(txt, tc.cap)
				tc.a.now += runeLen(got)
				if got == elisionMark {
					tc.a.drops++
				}
			}
		}
	}
	if n == 0 {
		t.Skip("no transcripts")
	}
	for _, x := range []struct {
		name string
		a    acc
	}{{"coarse view (digestClip)", view}, {"mined turns (PerTurnChars)", turn}} {
		delta := 100 * float64(x.a.now-x.a.old) / float64(x.a.old)
		t.Logf("%-28s rune-count cut %d runes -> delimiter cut %d (%+.1f%%), whole-turn drops %d",
			x.name, x.a.old, x.a.now, delta, x.a.drops)
		// -5% is the affordability bar: the delimiter cut may cost a little, never a lot. Under
		// a strictly-retreating rule the view measured -40.3%.
		if delta < -5 {
			t.Errorf("%s: the delimiter cut costs %+.1f%% of the text — clipAllowancePct is no "+
				"longer paying for itself on this corpus", x.name, delta)
		}
	}
}
