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
//
// SubjectTerms is what ChangedSubject was decided on — the names the beat and its grounded turn
// between them put on the table (see beatChangedSubject). Carried on the beat rather than
// recomputed, because the comparison is against the ACCUMULATED series and the grounded half of
// an earlier beat's terms cannot be recovered from its text.
type Beat struct {
	Ordinal        int      `json:"ordinal"`
	Text           string   `json:"text"`
	ChangedSubject bool     `json:"changed_subject"`
	SubjectTerms   []string `json:"subject_terms,omitempty"`
}

// BeatGround is the grounded, non-model-authored context one beat was generated from.
//
// Turn is the user turn that prompted the window. It is the one statement of what a window is
// about that no model wrote, which is what makes it usable where the beat itself names nothing:
// see beatChangedSubject.
//
// SessionRecord.Subjects is deliberately NOT here, and that is a measured rejection rather than an
// omission. Folding the record's accumulated subject terms into the same novelty test was tried
// over the three corpus sessions and made the signal WORSE — 40.7% against the 48.1% baseline,
// turning four subject changes that read as genuine (the enrichment-arrival investigation, the KPI
// card, the spend-card toggle, the test email) into "unchanged". The reason is structural: Subjects
// is cumulative and capped at MaxRecordSubjects by frequency, so it is dominated by terms every
// earlier beat has already been credited with, and adding it inflates the denominator of the
// novelty ratio with things that are already seen by construction. It also carries ordinary
// English into the term set by a route beatSubjectTerms refuses ("Confirmed", "Activity",
// "Adjustment" reach Subjects via weakProperNoun, and standing alone in a list there is no
// sentence position left to judge them by).
type BeatGround struct {
	Turn string
}

// GroundOf builds the grounded context for a beat over w. The window's LAST user turn is the turn
// that prompted it — Mine returns exactly one window per user prompt (see its doc), so that turn is
// the question this beat is answering.
func GroundOf(w Window) BeatGround {
	for i := len(w.Turns) - 1; i >= 0; i-- {
		if w.Turns[i].Role == RoleUser {
			return BeatGround{Turn: w.Turns[i].Text}
		}
	}
	return BeatGround{}
}

// BeatPrompt asks the cheap question. Deliberately NOT given a previous beat: a beat reads the
// transcript and the measured record only, which is what keeps the series free of a chain
// along which drift could compound.
func BeatPrompt(record, window string) string {
	var b strings.Builder
	b.WriteString("You are the engineer working in the session below. A colleague asks you at " +
		"standup what you are working on. Answer them in two or three sentences: what you are " +
		"working on, and where it has got to.\n\n")
	b.WriteString("SESSION RECORD (measured — authoritative):\n")
	b.WriteString(record)
	b.WriteString("\nRECENT CONVERSATION:\n")
	b.WriteString(window)
	b.WriteString(`
Rules:
  - Answer the way a person answers that question out loud: two or three sentences,
    plainly, saying what the work is and where it stands.
  - Vary how you open, and never use a stock opener. Do not begin with any of these:
    "The work is about", "The work is", "This session", "The user", "Currently".
    Nor with any other continuation of "The work". No fixed formula at all — begin with
    the thing being worked on, named.
  - Say what the work IS — the subject and why it is being done. Not a list of actions
    taken.
  - Where it stands means where THIS WINDOW shows it standing. You have not seen the
    rest of the job, so never characterise the job as a whole — not that it is
    finished, not how far along it is, not how little is left. Phrasings like
    "nearly complete", "almost done", "only X pending" and "all that remains is"
    are forbidden. You MAY say that a specific named thing was finished when the
    conversation shows it finished.
  - Use the record to place the work. An action is only meaningful as part of something.
  - Every noun must come from the conversation or the record above. Nothing in these
    instructions is subject matter.
  - Finish every sentence. End the last one with a full stop; never trail off mid-clause.
  - No preamble, no headings, no bullets. Plain prose only.

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
//
// THE RETRY NOW DIFFERS, and that is this function's one deliberate departure from every other
// caller of callValid. Asked about a one-turn window, the model sometimes answers with an
// unpunctuated headline clause — "Syncing the README with the actual state of the world", 53
// runes, no terminator anywhere — which holds no sentence and is rejected. At temperature 0 the
// re-request returns the BYTE-IDENTICAL string, so all five attempts failed on the same string
// and the window contributed no beat: 2 of 42 asked, identically in all six sweeps of the last
// round (s6 i9 and s10 i4), recorded in no artifact but a concerns list. A 5% silent loss of the
// history the whole design leans on.
//
// beatSampling is the fix, and it is deliberately NOT a looser shape rule: the sentence-
// completeness standard is the point (see BeatCap — at 200 runes, 46 of 47 beats ended
// mid-clause and the median count of complete sentences was ZERO). What was wrong was retrying
// an unchanged request. The first attempt is still the greedy temperature-0 request, so a beat
// that succeeds first time — 40 of 42 — is byte-identical to before and the study's
// reproducibility is untouched; only a REJECTED generation is re-requested differently, and the
// seed makes even that reproducible.
func (l *Llama) GenerateBeat(record, window string) (string, error) {
	_, kept, err := l.generateBeat(record, window)
	return kept, err
}

// generateBeat is GenerateBeat plus the unclipped generation, which the measurement harnesses
// need in order to report what the cap actually costs (see BeatCap).
// beatSampling is the beat path's retry schedule: greedy first, then a widening temperature at
// a fixed seed.
//
// Attempt 0 is temperature 0 with no seed field at all, so the request is byte-identical to
// every other caller's and the 95% of beats that pass first time are unaffected. Each retry
// then raises the temperature by beatRetryTempStep — enough for the sampler to leave a
// degenerate mode, far below the level at which a 4B instruct model starts inventing (the
// prompt's "every noun must come from the conversation" rule is what T2 and T12 police, and a
// beat generated at 0.8 is scored by exactly the same gates as one generated at 0).
//
// The seed is the attempt index, not a clock or a random value: a re-run of the same sweep
// re-requests with the same seed and gets the same recovery, which is what keeps "both arms run
// twice with identical figures" true for the recovered beats as well as the greedy ones.
func beatSampling(attempt int) sampling {
	if attempt <= 0 {
		return sampling{}
	}
	return sampling{Temp: float64(attempt) * beatRetryTempStep, Seed: attempt}
}

// beatRetryTempStep is how much each retry widens sampling. With retry.DefaultPolicy's 5
// attempts the schedule is 0, 0.2, 0.4, 0.6, 0.8 — the last of those is still a conservative
// temperature for an instruct model, and the shape gates (BeatCap, BeatMinRunes,
// BeatClaimsUnobservableProgress) apply unchanged to every attempt, so a wilder sample is
// rejected rather than stored.
const beatRetryTempStep = 0.2

func (l *Llama) generateBeat(record, window string) (raw, kept string, err error) {
	var out struct {
		Beat string `json:"beat"`
	}
	err = l.callValidSampled(BeatPrompt(record, window), BeatSchema(), &out, func() error {
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
		case BeatClaimsUnobservableProgress(kept, window+"\n"+record):
			// Judged on the CLIPPED text: that is what would be stored and read, and a
			// claim in a dropped tail was never going to be published.
			return firstProblem([]string{"beat characterises overall progress the window " +
				"does not show: " + strings.Join(beatProgressClaims(kept), "; ")})
		}
		return nil
	}, beatSampling)
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

// BeatTurnsFromEnv reads KELD_DIGEST_BEAT_TURNS, defaulting to 5 user turns.
//
// Three was too often, and repetitiveness was the symptom: over three user turns there is
// frequently nothing new to say, so the model restates the same subject in slightly different
// words. Some of those restatements are caught and discarded (BeatSaysNothingNew), which is pure
// waste — the generation was paid for — and the ones just under the threshold survive as
// near-duplicates that dilute the series. Five spaces the question far enough apart that there is
// usually something to answer.
func BeatTurnsFromEnv() int {
	if v := os.Getenv("KELD_DIGEST_BEAT_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}
