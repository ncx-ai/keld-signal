//go:build llmstudy

package llmstudy

import (
	"strings"
	"testing"
	"unicode"
)

// beatHeldOutSessions are the transcripts the prompt's worked examples were read from. The beat
// sweep skips them when it selects its corpus, so no beat is ever scored on material the model
// was shown an answer for.
//
// Keyed on the transcript's base name, which is the session id: the corpus root is pinnable
// (KELD_STUDY_CORPUS_ROOT) so a path would not survive re-pinning, while the id identifies the
// same session wherever the snapshot sits.
var beatHeldOutSessions = map[string]bool{
	"c2019c5e-7578-442f-95a4-d191687153a3.jsonl": true,
	"aa59ef4c-53f5-4ca7-bb9f-4a9b6c750eac.jsonl": true,
	"51476fbe-9c7e-449a-a19f-10806932d326.jsonl": true,
}

// TestBeatExamplesAreHeldOut is the contamination check, run against the corpus the sweep would
// actually select.
//
// ⚠️ IT VERIFIES THE SEPARATION MECHANICALLY, WHICH IS THE WHOLE POINT. The examples this
// replaces were held out by intention and were not held out in fact: two were invented out of a
// hand-authored session that was itself in the corpus, so `fa-register.csv` stood in the
// instructions and in that session's window at the same time. Nothing failed, because nothing
// checked. The rule is now a test:
//
//   - no example's SUBJECT LINE may occur in any eval session's window or measured record;
//   - no NAMED IDENTIFIER any example uses may occur there either — a strong identifier (a path,
//     a dotted filename, a snake_case or versioned token, anything carrying a digit or an
//     internal capital) or a capitalised term, i.e. exactly the tokens that name one thing.
//
// Ordinary English is deliberately NOT covered, and that is not a loophole: requiring "written",
// "left" or "picked" to be absent from a 12-session corpus is impossible, and this package has
// already paid for measures that flagged ordinary English — "Key", "Initial" and "e.g" at 22.6%.
// What contaminates a run is a NAME shared between the instructions and the material, because a
// name is what the anchoring guard would then accept as if it had been read from the evidence.
//
// Occurrence is case-insensitive SUBSTRING, the lenient direction: it fails on a term the corpus
// merely contains, so "seat_capex_split" would fail against "seat_capex_split_v2" too.
//
//	KELD_STUDY_CORPUS_ROOT=<pinned snapshot> \
//	  go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run BeatExamplesAreHeldOut -v
func TestBeatExamplesAreHeldOut(t *testing.T) {
	o := DefaultMineOpts()
	beatTurns := BeatTurnsFromEnv()

	// The sweep's own selection, run rather than re-implemented — including the fork/resume
	// dedupe, which changes WHICH sessions are chosen: a duplicate dropped is another transcript
	// pulled in behind it, and a session the sweep reads that this check never saw is exactly the
	// contamination this test exists to refuse.
	corpus := selectBeatCorpus(t, StratifiedTranscripts(), o, beatEvalSessions, beatEvalWindows)
	corpus = append(corpus, "testdata/nontech/finance-close.jsonl",
		"testdata/nontech/marketing-launch.jsonl")
	if len(corpus) < 3 {
		t.Skip("no pinned corpus (set KELD_STUDY_CORPUS_ROOT)")
	}

	// The evidence is what the sweep actually shows the model: every beat window it would
	// generate over, and the measured record as it stood at each of them. Not the raw transcript
	// — a term the sweep never puts in front of the model cannot contaminate its answer.
	var evidence strings.Builder
	for _, f := range corpus {
		ws, e1 := Mine(f, o)
		deltas, e2 := sessionDeltas(f, o)
		if e1 != nil || e2 != nil || len(deltas) != len(ws) {
			t.Fatalf("%s: %v %v", f, e1, e2)
		}
		last := beatEvalWindows
		if last > len(ws)-1 {
			last = len(ws) - 1
		}
		var rec SessionRecord
		var bw BeatWindower
		for idx := 0; idx <= last; idx++ {
			rec = rec.Observe(deltas[idx], Extract(deltas[idx]))
			if (idx+1)%beatTurns != 0 {
				continue
			}
			if w := bw.Next(deltas, idx); w.Rendered != "" {
				evidence.WriteString(w.Rendered)
				evidence.WriteString("\n")
				evidence.WriteString(rec.Block())
				evidence.WriteString("\n")
			}
		}
	}
	hay := strings.ToLower(evidence.String())
	t.Logf("checked against %d sessions, %d runes of window and record evidence",
		len(corpus), runeLen(hay))

	for _, ex := range beatExamples {
		if strings.Contains(hay, strings.ToLower(ex.Subject)) {
			t.Errorf("example subject %q occurs in the eval corpus (read from %s)",
				ex.Subject, ex.Source)
		}
		for _, term := range beatExampleNames(ex) {
			if strings.Contains(hay, strings.ToLower(term)) {
				t.Errorf("example names %q, which occurs in the eval corpus (example %q, read from %s)",
					term, ex.Subject, ex.Source)
			}
		}
	}
}

// beatEvalSessions and beatEvalWindows mirror the sweep's own defaults, so the check covers the
// corpus the sweep selects rather than a differently-sized one.
const (
	beatEvalSessions = 12
	beatEvalWindows  = 30
)

// beatExampleNames returns the terms in an example that NAME something: strong identifiers at any
// length, and capitalised tokens. Tokenisation is subjectTokens', so a path or a dotted filename
// arrives whole rather than as fragments that would match anything.
func beatExampleNames(ex beatExample) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range append([]string{ex.Subject}, ex.Events...) {
		for _, tok := range subjectTokens(s) {
			term := trimTermPunct(tok)
			if term == "" || seen[strings.ToLower(term)] {
				continue
			}
			if strongIdentifier(term) || unicode.IsUpper([]rune(term)[0]) {
				seen[strings.ToLower(term)] = true
				out = append(out, term)
			}
		}
	}
	return out
}

// TestBeatExamplesNameSomething guards the check above against passing vacuously: an example set
// that named nothing would satisfy the hold-out test trivially and teach the model nothing about
// using the session's own words. Each example must name at least one thing, and the second must
// still be the nothing-was-finished shape — one entry, no completion in it — since modelling the
// empty answer is what stops the model inventing progress.
func TestBeatExamplesNameSomething(t *testing.T) {
	if len(beatExamples) < 3 {
		t.Fatalf("expected at least 3 worked examples, have %d", len(beatExamples))
	}
	for _, ex := range beatExamples {
		if names := beatExampleNames(ex); len(names) == 0 {
			t.Errorf("example %q names nothing, so the hold-out check passes vacuously on it",
				ex.Subject)
		}
		if ex.Source == "" {
			t.Errorf("example %q records no source transcript", ex.Subject)
		}
	}
	if n := len(beatExamples[1].Events); n != 1 {
		t.Errorf("the second example is the nothing-was-finished shape and must carry one entry, has %d", n)
	}
}
