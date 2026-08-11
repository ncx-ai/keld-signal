package llmstudy

import (
	"os"
	"strconv"
	"strings"
	"unicode"
)

// BeatCap bounds one beat, and it is MEASURED, not chosen.
//
// At 200 it was not a backstop at all, it was the shape of the output: 46 of 47 beats over
// three real sessions ended in an ellipsis and the median count of COMPLETE sentences was
// zero, because lengths clustered at 187-199 and every one of them was guillotined mid-clause.
// A cap that the answer always hits is not bounding an outlier, it is truncating the answer.
//
// So the figure comes from what a two-to-three-sentence standup answer actually costs. Over the
// same three sessions (n=47, Qwen3-4B-Instruct-2507, the prompt below), unclipped generations
// ran min 110 / median 355 / p90 519 / max 733 runes, and the byte the SECOND sentence ends on
// — the boundary that has to survive for a beat to read as an answer rather than a headline —
// ran median 329 / p95 473 / max 505. 512 covers that maximum: every two-sentence answer in the
// corpus survives whole, and a third sentence survives whenever it fits (7 of the 13 beats that
// wrote one). Clipping past it drops the trailing sentence rather than half of it — see
// ClipBeat.
//
// It is not free. The beat ladder is a discretionary claimant on the digest prompt budget
// (fitDiscretionary, digest_refine.go), so MaxBeatSelection x BeatCap went from ~2,400 runes to
// ~6,144; under pressure the report reads fewer beats instead of shorter ones. That is the right
// trade — a report cannot use a beat that stops mid-clause at all.
const BeatCap = 512

// BeatMinRunes is the floor below which a "complete sentence" is not an answer to the question.
// A beat that clips down to "Fixed." is structurally valid and says nothing, so it is treated
// as a failed generation and re-requested rather than stored. Set below the shortest real beat
// measured (110 runes) with margin, so it rejects degenerate output without rejecting a
// genuinely terse answer.
const BeatMinRunes = 60

// Beat is one cheap statement of what the work is about, derived from its own window.
type Beat struct {
	Ordinal        int    `json:"ordinal"`
	Text           string `json:"text"`
	ChangedSubject bool   `json:"changed_subject"`
}

// BeatPrompt asks the cheap question. Deliberately NOT given a previous beat: a beat reads the
// transcript and the measured record only, which is what keeps the series free of a chain
// along which drift could compound.
func BeatPrompt(record, window string) string {
	var b strings.Builder
	b.WriteString("State what the work is about, in one to three sentences.\n\n")
	b.WriteString("SESSION RECORD (measured — authoritative):\n")
	b.WriteString(record)
	b.WriteString("\nRECENT CONVERSATION:\n")
	b.WriteString(window)
	b.WriteString(`
Rules:
  - Say what the work is ABOUT — the subject and its purpose. Not a list of actions taken.
  - Use the record to place the work. An action is only meaningful as part of something.
  - Every noun must come from the conversation or the record above. Nothing in these
    instructions is subject matter.
  - No preamble, no headings. One to three sentences of plain prose.

Respond with JSON only.
`)
	return b.String()
}

// BeatSchema constrains the response to one required string.
func BeatSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"beat": map[string]any{"type": "string", "minLength": digestMinProse},
		},
		"required":             []string{"beat"},
		"additionalProperties": false,
	}
}

// ClipBeat bounds a beat AT A SENTENCE BOUNDARY, dropping any trailing incomplete sentence.
// It returns "" when no complete sentence fits, which is a failed generation, not a beat.
//
// This is deliberately not clipProse. clipProse is written for the digest's carried prose, where
// losing content is worse than an ellipsis (see its doc: an earlier sentence-preferring version
// measurably deleted evidence), so it cuts on a word boundary and marks the wound. A beat is the
// opposite case: it is one short answer read whole by a person, nothing downstream re-expands it,
// and a beat that stops mid-clause is not a smaller beat — it is unreadable. So the trailing
// fragment goes, and if that leaves nothing there is nothing to store.
//
// A cap of 0 or less means "no cap": the trailing-fragment rule still applies, because a model
// that stops mid-clause on its own produces exactly the same defect as a clip that does.
func ClipBeat(s string, cap int) string {
	s = strings.TrimSpace(s)
	// A beat may arrive already carrying clipProse's marker (a re-clip, or a model copying one
	// out of the record). It is the marker of the defect being fixed here, so it is removed
	// along with the fragment it marks.
	for strings.HasSuffix(s, "…") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "…"))
	}
	r := []rune(s)
	if cap > 0 && len(r) > cap {
		r = r[:cap]
	}
	end := lastSentenceStop(r)
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(string(r[:end]))
}

// lastSentenceStop returns the rune index just past the terminator that ends the last COMPLETE
// sentence in r, or -1 when r holds none.
//
// Stricter than lastSentenceEnd (digest_insights.go), which counts every '.', '!' and '?'. That
// is safe for the word-boundary clip it serves, but as the rule that decides where a beat ENDS
// it would cut inside the identifiers beats are full of: a bare "any period" rule ends a
// sentence in the middle of "turn-row.tsx" or "2.9 GB" or "atlas.keld.co". So a terminator
// counts only when what follows it is whitespace or the end of the string — which is what makes
// a period a sentence end rather than punctuation inside a token — and closing quotes and
// brackets are stepped over so a sentence ending in a quoted term is not missed. A short
// abbreviation list covers the remaining case, "e.g." and friends, where a period IS followed by
// a space mid-sentence.
func lastSentenceStop(r []rune) int {
	stops := sentenceStops(r)
	if len(stops) == 0 {
		return -1
	}
	return stops[len(stops)-1]
}

// sentenceStops returns the rune index just past every sentence-ending terminator in r. Callers
// that need only the last one use lastSentenceStop; the full list is what counts a beat's
// complete sentences, which is the shape figure the dump harness reports.
func sentenceStops(r []rune) []int {
	var out []int
	for i := 0; i < len(r); i++ {
		if !sentenceTerminator(r[i]) || abbreviationBefore(r, i) {
			continue
		}
		j := i + 1
		for j < len(r) && (sentenceTerminator(r[j]) || sentenceCloser(r[j])) {
			j++
		}
		if j == len(r) || unicode.IsSpace(r[j]) {
			out = append(out, j)
			i = j - 1
		}
	}
	return out
}

func sentenceTerminator(c rune) bool { return c == '.' || c == '!' || c == '?' }

// sentenceCloser is the punctuation allowed to sit between a terminator and the space after it,
// so a sentence ending on a quoted or parenthesised term still ends. Straight and curly alike.
func sentenceCloser(c rune) bool {
	switch c {
	case '"', '\'', ')', ']', '’', '”', '»':
		return true
	}
	return false
}

// beatAbbreviations are the tokens whose trailing period is not a sentence end even though a
// space follows it. Short by design: every entry is a false sentence break that would cut a beat
// mid-clause, and a wrong entry silently costs a real sentence end.
var beatAbbreviations = map[string]bool{
	"e.g": true, "i.e": true, "eg": true, "ie": true, "etc": true, "vs": true,
	"cf": true, "al": true, "fig": true, "approx": true, "no": true,
	"dr": true, "mr": true, "mrs": true, "ms": true, "prof": true,
}

// abbreviationBefore reports whether the terminator at i closes a known abbreviation. The token
// is read back over letters, digits and interior periods so "e.g" arrives whole rather than as
// its final letter.
func abbreviationBefore(r []rune, i int) bool {
	j := i - 1
	for j >= 0 && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '.') {
		j--
	}
	return beatAbbreviations[strings.ToLower(string(r[j+1:i]))]
}

// GenerateBeat produces one beat, or an error.
//
// Shape is validated INSIDE callValid's retry loop, which is the whole mechanism: a generation
// that holds no complete sentence within the cap is re-requested, not emitted as a fragment and
// not silently dropped. Note what is deliberately absent — a max_tokens bound. The response is
// JSON-schema-constrained, so a token limit cuts the string mid-value and the object never
// closes; brevity is asked for in the prompt and enforced here by shape, never by truncating
// the decode.
func (l *Llama) GenerateBeat(record, window string) (string, error) {
	_, kept, err := l.generateBeat(record, window)
	return kept, err
}

// generateBeat is GenerateBeat plus the unclipped generation, which the measurement harnesses
// need in order to report what the cap actually costs (see BeatCap).
func (l *Llama) generateBeat(record, window string) (raw, kept string, err error) {
	var out struct {
		Beat string `json:"beat"`
	}
	err = l.callValid(BeatPrompt(record, window), BeatSchema(), &out, func() error {
		raw, kept = strings.TrimSpace(out.Beat), ClipBeat(out.Beat, BeatCap)
		switch {
		case raw == "":
			return firstProblem([]string{"beat is empty"})
		case kept == "":
			return firstProblem([]string{"beat holds no complete sentence within " +
				strconv.Itoa(BeatCap) + " runes"})
		case len([]rune(kept)) < BeatMinRunes:
			return firstProblem([]string{"beat is " + strconv.Itoa(len([]rune(kept))) +
				" runes after dropping its incomplete tail, under the floor of " +
				strconv.Itoa(BeatMinRunes)})
		}
		return nil
	})
	if err != nil {
		return raw, "", err
	}
	return raw, kept, nil
}

// BeatSaysNothingNew reports a beat that restates the most recent one.
//
// Compared on significant words via beatsRestate — the same style of test that collapses
// duplicate insights, but with wider stemming: a restatement arrives reworded rather than
// identical, and a beat reworks the same verb (gerund/nominalisation) far more often than a
// full insight does, so insightsMatch's plural-only stemming is too weak here (see beatStem
// in beat_series.go). Only the most recent beat is compared: a subject the session RETURNS to
// later is genuine history and should appear again.
func BeatSaysNothingNew(text string, prev []Beat) bool {
	if len(prev) == 0 {
		return false
	}
	return beatsRestate(text, prev[len(prev)-1].Text)
}

// BeatTurnsFromEnv reads KELD_DIGEST_BEAT_TURNS, defaulting to 3 user turns.
func BeatTurnsFromEnv() int {
	if v := os.Getenv("KELD_DIGEST_BEAT_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 3
}
