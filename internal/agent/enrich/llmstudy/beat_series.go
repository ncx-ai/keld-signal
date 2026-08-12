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

// AppendBeat stores a beat. Ordinals are contiguous over stored beats.
//
// ⚠️ TWO SIGNALS WERE REMOVED FROM HERE, BOTH BECAUSE THEY WERE MEASURED. The restatement
// suppression (BeatSaysNothingNew) discarded 0 of 70 beats — inert, and paid for with a
// generation each time it might have fired. ChangedSubject fired on 41 of 42 refinements and then
// on 0 of 46 packets, because it measured window adjacency rather than subject change; it is no
// longer computed, and nothing downstream samples on it (see SelectBeats). A near-duplicate beat
// is now STORED: a window that repeated itself is a fact about the session, and suppressing it
// deleted that fact rather than reporting it.
//
// Bounding is fitBeatText, not ClipBeat. A bulleted beat holds no sentence terminators, so the
// prose clip would return "" for every beat; whole entries are dropped instead and the drop is
// marked. AppendBeat is the second gate on that bound (generateBeat is the first), so a caller
// assembling beat text some other way cannot get an over-cap beat into the series.
func AppendBeat(prev []Beat, text string, g BeatGround) ([]Beat, bool) {
	text = fitBeatText(text, BeatCap)
	if runeLen(text) < BeatMinRunes {
		return prev, false
	}
	return append(prev, Beat{Ordinal: len(prev) + 1, Text: text}), true
}

// AppendBeatDraft stores a generated beat whole — its subject and entries beside the rendered
// text — so a reader of the series is not left re-parsing the rendering to recover the fields the
// model actually answered with.
func AppendBeatDraft(prev []Beat, d BeatDraft) ([]Beat, bool) {
	out, ok := AppendBeat(prev, d.Text, BeatGround{})
	if !ok {
		return prev, false
	}
	b := &out[len(out)-1]
	b.Subject, b.Events = d.Subject, d.Events
	return out, true
}

// beatSubjectTermsGrounded is what a beat's subject is judged on: the things the beat names, plus
// the things the USER TURN that prompted its window names.
//
// The grounded half exists because the beat's half is not always there. A beat is written by the
// model and can be entirely abstract ("Designing a schema-enforced digest that prevents
// rubberstamping"); the turn that prompted the window is raw transcript, so it names what the
// person actually asked about. Both halves go through the SAME admission rule (beatSubjectTerms),
// so nothing lowercase and ordinary enters by either route — the grounding widens the evidence,
// not the vocabulary.
//
// The grounded half is capped at maxBeatGroundTerms. A user turn can be a pasted file or diff, and
// an unbounded ground would let one paste supply a term set large enough to decide the flag on its
// own — the same "a count cap without a length cap is not a bound" reasoning maxSubjectTermLen
// records for the record's subjects. Measured over the corpus the real turns yielded 0-4 terms, so
// the cap binds only on the pathological case it exists for.
func beatSubjectTermsGrounded(text string, g BeatGround) map[string]bool {
	terms := beatSubjectTerms(text)
	for i, t := range beatSubjectTermList(g.Turn) {
		if i == maxBeatGroundTerms {
			break
		}
		terms[t] = true
	}
	return terms
}

// maxBeatGroundTerms bounds the grounded half of a beat's term set. Set at the same scale as
// maxRecentSubjects (10), which bounds the same kind of thing for the same reason.
const maxBeatGroundTerms = 8

func sortedTerms(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for t := range m {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
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
// It under-reported, and two changes close most of that gap. Both are stated as measurements
// below, because the previous version's abstention was 17 of 47 beats and was about to be read off
// a dashboard timeline as "no milestone here".
//
//  1. THE TERM SET IS GROUNDED. It is no longer only what the model wrote: the user turn that
//     prompted the window contributes its named subjects too (beatSubjectTermsGrounded), so a beat
//     that describes its work abstractly can still be judged on what the person asked for. This is
//     what catches the shift the flat version missed in this repo's own session — beat 1 is about
//     finding CPU-friendly alternatives to GLiNER2, beat 2 is about removing a prompt-truncation
//     confound from the study design, and the beat halves alone shared enough vocabulary
//     ({gliner2, llm} against {gliner2, ner, hugging, face, cpu-friendly}) that only one term was
//     novel. The turn ("I installed lamma.cpp") supplies the second.
//
//  2. ONE NOVEL STRONG IDENTIFIER IS ENOUGH. minNovelBeatTerms exists because a lone weak
//     proper-noun candidate (a capitalised mid-sentence word) is thin evidence. A STRONG
//     identifier is not thin: a path, a dotted filename, a versioned or snake_case token names one
//     thing and nothing else. Requiring two of them made a beat about `border-1px` on the
//     compliance flags a continuation of a beat about `pb-4` on the activity rows.
//
//     ⚠️ strongIdentifier is applied to the term as STORED, i.e. lowercased, so its internal-
//     capitalisation branch cannot fire here: "ConfirmDialog" and "CSV" arrive as "confirmdialog"
//     and "csv" and count as WEAK on this route. That is deliberate and it is the conservative
//     direction — the single-name route admits only the forms that survive lowercasing
//     unambiguously (paths, dotted filenames, digits, snake_case) — but it is a real limit, and it
//     is why a lone new CamelCase component name still needs a second novel term.
//
// It is still a LOWER BOUND, and the abstention is still the reason: when neither the beat nor its
// turn names anything concrete — a beat about "a schema-enforced digest that prevents
// rubberstamping" answering "How does it look so far?" — there is no grounded subject to compare,
// and continuity is the default (the principle SubjectShifted follows). Measured over the three
// corpus sessions, 4 of 27 beats name nothing at all and 2 more name only one already-seen or weak
// term, against 13 of 27 that the text-only rule could not judge. What is NOT permitted is the
// other error, an undiscriminating signal: see beatSubjectTerms for why the obvious vocabulary is
// unusable here.
func beatChangedSubject(terms map[string]bool, prev []Beat) bool {
	if len(prev) == 0 {
		return true // the first beat establishes the subject
	}
	if len(terms) == 0 {
		return false // nothing named, on either side: abstain
	}
	seen := map[string]bool{}
	for _, b := range prev {
		for _, t := range b.SubjectTerms {
			seen[t] = true
		}
	}
	novel, strongNovel := 0, 0
	for t := range terms {
		if seen[t] {
			continue
		}
		novel++
		if strongIdentifier(t) {
			strongNovel++
		}
	}
	if float64(novel)/float64(len(terms)) < beatSubjectNoveltyFloor {
		return false
	}
	return novel >= minNovelBeatTerms || strongNovel >= 1
}

// The novelty rule's constants, calibrated on the corpus (three real sessions) rather than chosen.
// The floor at "more than half" and 2 novel terms gave 22 of 47 on the original corpus and 13 of 27
// on the regenerated one; with the grounded term set and the single-strong-identifier route above,
// the same constants give 19 of 27 (70.4%) — 8/12, 3/4, 8/11 across the three sessions — and the
// verdicts still stand up to reading: three consecutive beats about one CSV export mark the first
// and not the other two, four consecutive beats about one team-budget display mark only the first,
// and a jump to an unrelated component marks. Sweeping the floor from 0.4 to 0.7 moved the
// text-only rate 49% -> 32%, so the signal is not balanced on the threshold; the floor is set at
// "more than half" because that is a statement about the beat rather than a tuned number.
//
// The floor is what bounds the strong-identifier route: with novel/total >= 0.5 required, a single
// novel term can only carry a set of at most two, so "one new filename" marks a change in a beat
// that names one or two things and never in a beat that names five.
const (
	beatSubjectNoveltyFloor = 0.5
	minNovelBeatTerms       = 2
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
	for _, t := range beatSubjectTermList(s) {
		out[t] = true
	}
	return out
}

// beatSubjectTermList is beatSubjectTerms in ORDER OF APPEARANCE, deduplicated. Order matters to
// exactly one caller: the grounded half of a beat's term set is capped (maxBeatGroundTerms), and a
// cap applied to a Go map iteration would pick a different eight terms on every run.
func beatSubjectTermList(s string) []string {
	var out []string
	seen := map[string]bool{}
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
		k := strings.ToLower(tok)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

// subjectTokenSpans is THE tokenisation, reported as byte spans; subjectTokens is a thin wrapper
// that returns the strings. It is spans-first because the position-aware capital test needs to
// know WHERE a token sits — a capital at a sentence start is English, the same capital
// mid-sentence is a name — and because the comma rule below needs a token's neighbours, which a
// per-rune predicate cannot see.
//
// The two used to be separate loops over a shared rune class. They agreed, but only by
// inspection, and the class alone can no longer express the rule.
func subjectTokenSpans(s string) [][2]int {
	var out [][2]int
	start := -1
	for i, r := range s {
		// A ',' is a separator everywhere EXCEPT between the digits of one number. Without
		// that exception "1,400.00" is tokenised as "1" and "400.00", and the fragment is what
		// gets stored: measured on testdata/nontech/finance-close.jsonl, six of the twelve
		// slots in SessionRecord.Subjects — the block the prompt labels "measured —
		// authoritative" — held amounts that were never written (400.00 for 1,400.00, 200.00
		// for 1,200.00, 629.60 for 412,629.60, ...). Nothing downstream could catch it: the
		// verbatim gate is a SUBSTRING test and "400.00" is a substring of "1,400.00", so a
		// fractured amount passes every check the record has, and a beat anchoring against the
		// record inherits a false number under a heading that says it is measured.
		if subjectTokenRune(r) || (start >= 0 && r == ',' && thousandsSeparatorAt(s, i)) {
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

// thousandsSeparatorAt reports whether the ',' at byte offset i separates the digit groups of a
// single number: a digit immediately before it and EXACTLY three digits after it.
//
// Exactly three, not "some digits", so the rule stays a thousands-separator rule rather than a
// general "commas join numbers" rule: "1,400.00" and "412,629.60" are one token each, while
// "1,2,3" — a list, a CSV row, a tuple — still splits the way it reads. Both sides are required,
// so a clause-ending comma ("posted 148.00, then reversed") and a date ("March 5, 2026") are
// untouched: a comma followed by a space is not followed by three digits.
//
// Bytes rather than runes deliberately: every character it tests is ASCII, so a byte compare
// cannot mis-index a multi-byte rune — it can only fail to match one, which is the safe direction.
func thousandsSeparatorAt(s string, i int) bool {
	if i == 0 || i+3 >= len(s) {
		return false
	}
	if s[i-1] < '0' || s[i-1] > '9' {
		return false
	}
	for j := i + 1; j <= i+3; j++ {
		if s[j] < '0' || s[j] > '9' {
			return false
		}
	}
	// A fourth digit means the groups are not thousands (e.g. "12,34567"), so this comma is not
	// a separator and the token splits there.
	return i+4 >= len(s) || s[i+4] < '0' || s[i+4] > '9'
}

// subjectTokenRune is the character class every subject token is built from. The ',' exception is
// NOT here: it depends on a token's neighbours, and this predicate sees one rune.
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

// SelectBeats chooses which beats a report reads: the first, the most recent, and even spacing
// between them to fill the cap.
//
// ⚠️ IT NO LONGER SAMPLES ON ChangedSubject. That flag was the middle term of this selection and
// it carried no information — 41 of 42 true in the round that set it up, then 0 of 46 in the
// round that measured it against a reader — so the "interesting" beats it picked were whichever
// ones the flag happened to be true for. Even spacing over a disjoint, contiguous window sequence
// is a statement about the transcript rather than about a heuristic's mood.
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

// RenderBeats renders the series oldest-first, one beat per line.
//
// The "(subject changed)" annotation is gone with the flag that produced it (see SelectBeats).
// It only ever said what a retired heuristic thought; a reader of the rendered series can see a
// change of subject in the subjects themselves, which are the first thing each line names.
func RenderBeats(sel []Beat) string {
	if len(sel) == 0 {
		return ""
	}
	var b strings.Builder
	for _, x := range sel {
		b.WriteString(fmt.Sprintf("[%d] %s\n", x.Ordinal, oneLine(x.Text)))
	}
	return b.String()
}

// oneLine flattens a beat so one entry stays one line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
