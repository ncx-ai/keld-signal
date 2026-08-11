package llmstudy

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// MaxBeatSelection caps how many beats a report reads. At BeatCap runes each that is ~6,144
// worst case — it was ~2,400 while BeatCap was 200, and BeatCap was raised to 512 because at 200
// essentially every beat was cut mid-clause (see its doc) — against a fully-capped
// CarryForward-equivalent JSON embed, measured at 6,339 runes (not the ~4,742 an average real
// session showed — that figure was typical, not worst-case). Beats are also the one discretionary
// claimant besides the whole-session view: fitDiscretionary (digest_refine.go) shrinks the
// selection below MaxBeatSelection when the budget is under pressure, so this is the ceiling, not
// a guarantee — and after the cap rise that shrinking is the ordinary case rather than the
// exception, which is the price of a beat that reads as an answer.
const MaxBeatSelection = 12

// AppendBeat stores a beat unless it restates the previous one, marking whether it changed the
// subject. Ordinals are contiguous over STORED beats, so a discarded restatement leaves no gap.
//
// ChangedSubject is the signal the report samples on, and it is measured here by comparing
// against the accumulated beats rather than taken from the classification pipeline's EWMA
// focus — which the digest path does not run. The EWMA is better where available; this makes
// the signal usable without it.
//
// The restatement test uses beatsRestate, not the shared insightsMatch — see its doc for why.
// The subject-change test does NOT: see beatChangedSubject for what replaced it and why.
//
// Clipping is ClipBeat, not clipProse: a stored beat ends at a sentence boundary or it is not
// stored at all. AppendBeat is the second gate on that invariant (GenerateBeat is the first), so
// a caller that assembles beat text some other way cannot get a fragment into the series.
func AppendBeat(prev []Beat, text string) ([]Beat, bool) {
	text = ClipBeat(text, BeatCap)
	if text == "" {
		return prev, false
	}
	if BeatSaysNothingNew(text, prev) {
		return prev, false
	}
	changed := beatChangedSubject(text, prev)
	return append(prev, Beat{Ordinal: len(prev) + 1, Text: text, ChangedSubject: changed}), true
}

// beatChangedSubject reports whether a beat is about something no earlier beat was about.
//
// What it replaced carried NO information. The previous rule ran beatsRestate — the
// near-duplicate test, at insightMatchRatio 0.8 — against every prior beat and called the
// subject changed unless one of them matched. But a beat that restates its predecessor has
// already been discarded by then, so every beat reaching the test was a non-restatement by
// construction, and at 0.8 on short varied prose nothing else matched either: measured over
// three real sessions, 47 of 47 beats came out ChangedSubject=true. SelectBeats samples on this
// flag, so beat selection for the expensive report was choosing on a constant.
//
// The rule now is novelty of NAMED SUBJECTS against the accumulated series: more than half of
// the things this beat names are things no earlier beat named, and at least two of them are. A
// return to an earlier subject is therefore not a change (its names are already in the union),
// which is the semantics the old doc claimed and the old code could not deliver.
//
// It is a LOWER BOUND, and the abstention is the reason. A beat that names nothing concrete —
// "designing a schema-enforced digest that prevents rubberstamping" is a real one — yields too
// few subject terms to judge, and is reported unchanged rather than guessed at, on the same
// "continuity is the default" principle SubjectShifted uses. Measured: 17 of 47 beats abstained
// that way, so genuine changes are missed. What is NOT permitted is the other error, an
// undiscriminating signal: see beatSubjectTerms for why the obvious vocabulary is unusable here.
func beatChangedSubject(text string, prev []Beat) bool {
	if len(prev) == 0 {
		return true // the first beat establishes the subject
	}
	terms := beatSubjectTerms(text)
	if len(terms) < minBeatSubjectTerms {
		return false
	}
	seen := map[string]bool{}
	for _, b := range prev {
		for t := range beatSubjectTerms(b.Text) {
			seen[t] = true
		}
	}
	novel := 0
	for t := range terms {
		if !seen[t] {
			novel++
		}
	}
	return novel >= minNovelBeatTerms &&
		float64(novel)/float64(len(terms)) >= beatSubjectNoveltyFloor
}

// The novelty rule's constants, calibrated on the 47-beat corpus (three real sessions) rather
// than chosen: at floor 0.5 / 2 novel terms the flag fires on 22 of 47, and the verdicts stand up
// to reading — three consecutive beats about one CSV export mark the first and not the other two,
// four consecutive beats about one team-budget display mark only the first, and a jump to an
// unrelated component marks. Sweeping the floor from 0.4 to 0.7 moves the rate 49% -> 32%, so the
// signal is not balanced on the threshold; the floor is set at "more than half" because that is a
// statement about the beat rather than a tuned number.
const (
	beatSubjectNoveltyFloor = 0.5
	minNovelBeatTerms       = 2
	minBeatSubjectTerms     = 2
)

// beatSubjectTerms reduces a beat to the things it NAMES: files, paths, identifiers, versions,
// and proper nouns used mid-sentence.
//
// ⚠️ It deliberately does not use distinctiveTerms, and that is the whole design. distinctiveToken
// admits any lowercase word of 7+ characters, which is documented in this package as the
// mechanical reason two other thresholds are unusable (T11's SynopsisLag certifying an unrelated
// synopsis on the strength of "remains"/"whether"; T12's beat subject terms coming out as gerunds
// and adverbs). Built on that, a novelty count would compare "specifically" against "identified"
// and report a subject change — another signal that measures English rather than subject matter.
//
// So admission is Identifiers' notion of a specific instead: a strong identifier anywhere (path,
// dotted filename, snake/SCREAMING_SNAKE, internal capital, anything carrying a digit), or a
// capitalised token that is NOT sentence-initial and not an ordinary hyphenated compound. No
// ordinary lowercase English can enter by any route. Identifiers itself is left untouched — its
// regex feeds the retain-list and the T2/T4 metrics — so this reuses its RULE (strongIdentifier +
// the position-aware capital test) over subjectTokens' tokenisation, which unlike identifierPat
// keeps '/' attached and so sees "feat/multiturn-context" and "app/main.py" whole.
//
// Two exclusions beyond that rule, both from reading the corpus:
//   - a token with no letter at all ("1,650.55", "0.22") is an amount, not a subject, and amounts
//     change in every beat about unchanged work — pure novelty noise;
//   - digestStopWords, looked up case-insensitively (via stopWord), because a capitalised opener
//     mid-sentence after a quote is still English.
func beatSubjectTerms(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range subjectTokenSpans(s) {
		tok := trimTermPunct(s[m[0]:m[1]])
		if len(tok) < 3 || stopWord(tok) || !containsLetter(tok) {
			continue
		}
		if !strongIdentifier(tok) {
			// A weak token is a proper-noun candidate only where a capital carries
			// information: capitalised, mid-sentence, and not a hyphenated compound.
			r := []rune(tok)
			if !unicode.IsUpper(r[0]) || sentenceInitial(s, m[0]) || strings.Contains(tok, "-") {
				continue
			}
		}
		out[strings.ToLower(tok)] = true
	}
	return out
}

// subjectTokenSpans is subjectTokens' tokenisation reported as byte spans, because the
// position-aware capital test needs to know WHERE a token sits — a capital at a sentence start is
// English, the same capital mid-sentence is a name. subjectTokens returns the strings only.
func subjectTokenSpans(s string) [][2]int {
	var out [][2]int
	start := -1
	for i, r := range s {
		if subjectTokenRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, [2]int{start, i})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// subjectTokenRune is subjectTokens' character class, factored out so the two cannot drift.
func subjectTokenRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '/' || r == '.' || r == '_' || r == '-':
		return true
	}
	return false
}

func containsLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// beatStem folds common morphological variants — gerund, nominalisation, plural, past tense —
// onto a shared root, wider than significantWords' plural-only stripping.
//
// Beats are a different population from the "insight" pairs significantWords/insightsMatch
// were measured on: an insight is a full observation, but a beat is <=200 runes describing one
// subject, regenerated from an overlapping window every few turns. So the same verb resurfaces
// in a different form far more often here — "reconciling" then "reconciliation" describing
// unchanged work is the ordinary case for a beat, not a contrived one — and plural-only
// stripping is too weak to catch it.
//
// "iation" is listed ahead of the plain "ation" for the irregular case: "reconcile"
// nominalises to "reconciliation", not the "reconcilation" that bare stem+ation would give,
// so stripping "ation" alone leaves a stray "i" that "reconciling" (stem+ing) never had.
//
// The paired gerund suffixes ("ating"/"icating"/"iating", each listed just ahead of the
// nominalisation it must match) are for the regular, far more common counterpart: an -ate
// verb's gerund is stem+"ating" ("migrate" -> "migrating") while its nominalisation is
// stem+"ation" ("migration") — stripping only the generic "ing" would leave "migrat" against
// "migr" and miss the pair. Ten -ate verbs were checked directly (see
// TestBeatsRestateFoldsAteVerbFamily): migrate/create/generate/operate/evaluate/terminate/
// calculate/allocate need only "ating"+"ation". communicate and negotiate are actually
// -icate/-iate verbs (the "ic"/"i" is part of the STEM, not the suffix — "commun"+"icate",
// "negot"+"iate"), so their gerund and nominalisation carry that letter through both forms
// ("communicating"/"communication", "negotiating"/"negotiation") and need the longer
// "icating"/"iating" pair checked first, or the generic "ating"/"ation" split at the wrong
// point and the two stems land one letter apart.
func beatStem(w string) string {
	for _, suf := range []string{
		"ications", "icating", "ication", "iations", // -ication / -iation, plural
		"iating", "iation", "ations", "itions", "utions",
		"ation", "ition", "ution", "ating",
		"ing", "ed", "es", "s",
	} {
		// Require at least 3 runes left so a short word ("has", "ar") is never stemmed
		// into a fragment — over-stemming risks collapsing two DIFFERENT subjects, which
		// silently drops a beat's history rather than keeping a near-duplicate.
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 3 {
			return strings.TrimSuffix(w, suf)
		}
	}
	return w
}

// beatSignificantWords is significantWords' beat-local counterpart: same stop-word and
// possessive handling, but stemmed with beatStem instead of a bare trailing-"s" strip.
func beatSignificantWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordsOf(stripPossessiveSuffix(strings.ToLower(s))) {
		if insightStopWord(w) {
			continue
		}
		if stem := beatStem(w); stem != "" {
			out[stem] = true
		}
	}
	return out
}

// beatsRestate reports whether two beats describe the same subject.
//
// This is insightsMatch's beat-local counterpart, kept as a separate function (not a change to
// insightsMatch itself) because insightMatchRatio was measured on insight pairs, a different
// population — see beatStem. The 0.8 ratio itself is reused as-is: only the stemming needed to
// change, and reusing insightMatchRatio directly (rather than a second constant) keeps that
// visible instead of letting the two ratios drift apart unnoticed.
func beatsRestate(a, b string) bool {
	wa, wb := beatSignificantWords(a), beatSignificantWords(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
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
	return float64(shared)/float64(larger) >= insightMatchRatio
}

// SelectBeats chooses which beats a report reads: the first, every subject change, the most
// recent, and even spacing to fill the cap.
func SelectBeats(all []Beat, max int) []Beat {
	if max <= 0 {
		max = MaxBeatSelection
	}
	if len(all) <= max {
		return all
	}
	if max == 1 {
		// A single slot can't hold both anchors the invariant wants (first AND latest), so
		// it holds the one a report needs more: where things stand now, not where they
		// started. Handled before the pick map is seeded, which otherwise unconditionally
		// puts both index 0 and len(all)-1 in and returns 2 regardless of max.
		return []Beat{all[len(all)-1]}
	}
	pick := map[int]bool{0: true, len(all) - 1: true}
	for i := len(all) - 2; i > 0 && len(pick) < max; i-- {
		if all[i].ChangedSubject {
			pick[i] = true
		}
	}
	if len(pick) < max {
		step := float64(len(all)-1) / float64(max-1)
		for k := 1; k < max-1 && len(pick) < max; k++ {
			pick[int(float64(k)*step)] = true
		}
	}
	idx := make([]int, 0, len(pick))
	for i := range pick {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]Beat, 0, len(idx))
	for _, i := range idx {
		out = append(out, all[i])
	}
	return out
}

// RenderBeats renders the series oldest-first, marking where the subject changed so a report
// can see the trajectory rather than only the endpoints.
func RenderBeats(sel []Beat) string {
	if len(sel) == 0 {
		return ""
	}
	var b strings.Builder
	for _, x := range sel {
		mark := ""
		if x.ChangedSubject {
			mark = " (subject changed)"
		}
		b.WriteString(fmt.Sprintf("[%d]%s %s\n", x.Ordinal, mark, oneLine(x.Text)))
	}
	return b.String()
}

// oneLine flattens a beat so one entry stays one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
