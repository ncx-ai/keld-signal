package llmstudy

import (
	"regexp"
	"strings"
	"unicode"
)

// Verbatim anchoring: every SPECIFIC an entry names must occur, verbatim, in that entry's own
// window or in the measured record. (The "at least one term of any kind" form of this rule is
// retired — it fired 0 of 274 and accepted `each` as an anchor; see the narrowing below.)
//
// ⚠️ THIS IS A FACT TEST, NOT A JUDGEMENT. Substring presence is checkable and cannot be wrong
// about what it measured; it is the standard set after four metrics on this branch turned out to
// be measuring ordinary English rather than the thing they were named for — unverified
// identifiers flagged "Key", "Initial" and "e.g" at 22.6%, leak detection flagged only the
// sentinel the model is instructed to emit, plurals scored as fabrication, and T1 reported 100%
// while silently dropping 5 of 20 digests. The guard says one thing: this entry contains nothing
// the evidence contains.
//
// WHAT COUNTS AS A TERM, and why each part of it is set where it is:
//
//   - The tokenisation is subjectTokens', so '/', '.', '_' and '-' stay attached and a path, a
//     dotted filename or a thousands-separated amount arrives whole ("app/main.py",
//     "fa-register.csv", "1,650.55") instead of as fragments that would match anything.
//   - A token is a term when it is at least beatAnchorMinRunes long and is not an English
//     function word (insightStopWord's closed list, plus digestStopWords via stopWord) — OR when
//     it is a strongIdentifier, at any length, because "pb-4" and "0.22" name one thing each.
//   - Case is IGNORED on both sides: a beat writing "Atlas" about a window that wrote "atlas" has
//     named the thing, and a case-sensitive test would report a fabrication.
//   - Punctuation at a term's ends is trimmed (trimTermPunct), so "exporter." matches "exporter".
//   - Occurrence is SUBSTRING, via VerifyTopics — the same verbatim gate the publish path uses
//     for spans. Substring rather than whole-word is the lenient direction on purpose: "export"
//     anchors against "exporter", and the failure this guard exists to catch is an entry with
//     NOTHING behind it, not a morphological variant. Scoring plurals as fabrication is a mistake
//     this study has already made and paid for.
//
// ⚠️ IT IS DELIBERATELY *NOT* distinctiveToken, AND THAT IS A MEASUREMENT RATHER THAN A
// PREFERENCE. The first cut used it — the package's document-frequency gate for "does this term
// name a subject" — and probing it against the pinned 33-session corpus table showed it is the
// wrong instrument for THIS question in both directions:
//
//	export 0.485, rows 0.636, empty 0.758, workflow 0.727, range 0.394  -> NOT distinctive
//	continued 0.000, discussed 0.091, identified 0.242                  -> distinctive
//
// So "the export came back empty for a date range holding no rows" — an honest entry, every noun
// of it in the window — would have been DROPPED because its nouns are popular in an engineering
// corpus, while "the issue was discussed and identified" would have anchored on a bare verb. DF
// measures whether a term names a subject ACROSS SESSIONS; anchoring asks whether this entry uses
// THIS window's words. A term's corpus popularity is not evidence about either, and a drop
// decided by it is a drop decided by ordinary English.
//
// WHAT HAPPENS TO A FAILING ENTRY: it is DROPPED and the drop is MARKED in the beat text
// (renderBeat), and the dropped entries are carried on the BeatDraft so the sweep can print them.
// The beat is NOT failed — one unanchored entry is no reason to lose the rest — unless every
// entry fails, which is a generation with nothing of the evidence in it and is re-requested at a
// wider temperature.
//
// TWO FACTS ARE RECORDED BESIDE THE DECISION, because the decision alone answers neither question
// the review round asks:
//
//   - WHICH SIDE anchored the entry. An entry anchored only in the record and not in its own
//     window is the signature of an event whose antecedent fell on the other side of a window
//     boundary — the measurable cost of disjoint windows, and the thing that decides whether the
//     fix is a small marked context prefix or nothing at all.
//   - The strong IDENTIFIERS a beat uses that do NOT occur in the evidence
//     (unverifiedSpecifics). Recorded, never dropped: the narrow form of the identifier check,
//     over strong identifiers alone, so it cannot flag "Key", "Initial" or "e.g" the way the
//     22.6% measurement did.

// ⚠️ THE EVIDENCE IS THE CONVERSATION, NOT THE SCAFFOLDING WE WRAPPED IT IN. Both inputs carry
// words this harness put there — the window's per-turn role labels ("assistant: ") and its hole
// marker ("[turns since the previous update omitted to fit the context ...]"), the record's own
// field labels ("recurring subjects:", "corrections=") — and every one of those words is present
// in EVERY window by construction. An entry anchoring on "assistant", "context" or "corrections"
// would be anchoring on the instrument, which is precisely the defect that made leak detection
// worthless: it flagged only the sentinel the model was instructed to emit. So anchorEvidence
// strips the scaffolding before the occurrence test runs, and the guard only ever matches against
// text a person or a tool actually produced.
//
// beatAnchorMinRunes is the shortest ordinary word that may anchor an entry. Below it the token
// is a fragment or a function word and its occurrence in a 16,000-rune window says nothing; a
// strongIdentifier bypasses it, since "pb-4" is not a fragment.
const beatAnchorMinRunes = 4

// BeatAnchor is where one entry was anchored.
type BeatAnchor struct {
	// Term is the term that anchored it, "" when nothing did.
	Term string `json:"term"`
	// InWindow is true when the term occurs in the beat's own window. When it is false and Term
	// is non-empty, the entry is anchored ONLY in the measured record — the seam signal.
	InWindow bool `json:"in_window"`
	// Specifics is how many specifics the entry carried. Zero says the entry is UNCONSTRAINED —
	// it named nothing that could be checked, so it passed without being evidence of anything.
	// Reported rather than folded into Term, because "kept because everything it named is in the
	// evidence" and "kept because it named nothing" are different facts about a beat, and a
	// reader counting the second one is counting how often the guard had nothing to say.
	Specifics int `json:"specifics"`
}

// beatAnchorIn returns where s is anchored, given its window and the measured record. The window
// is tried first so an entry anchored by both is reported against the window, which is the
// ordinary case and the one that carries no signal.
func beatAnchorIn(s, window, record string) BeatAnchor {
	terms := beatAnchorTerms(s)
	if len(terms) == 0 {
		return BeatAnchor{}
	}
	if kept, _ := VerifyTopics(terms, windowEvidence(window)); len(kept) > 0 {
		return BeatAnchor{Term: kept[0], InWindow: true}
	}
	if kept, _ := VerifyTopics(terms, recordEvidence(record)); len(kept) > 0 {
		return BeatAnchor{Term: kept[0]}
	}
	return BeatAnchor{}
}

// windowEvidence is the rendered window with this harness's own words removed: the "role: " label
// each turn is written with, and the hole marker a bounded window leaves where turns were
// dropped. Both are in every window by construction, so a term matching one of them says nothing
// about this beat.
func windowEvidence(window string) string {
	var b strings.Builder
	for _, line := range strings.Split(window, "\n") {
		if strings.HasPrefix(line, strings.TrimSpace(beatOmittedNotice)) {
			continue
		}
		for _, r := range []Role{RoleUser, RoleAssistant, RoleTool} {
			if p := string(r) + ": "; strings.HasPrefix(line, p) {
				line = strings.TrimPrefix(line, p)
				break
			}
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// recordEvidence is the measured record's VALUES, without the field labels the block writes them
// under ("recurring subjects: ", "counts: ") or the key of each count ("user_turns=").
//
// The labels are this harness's vocabulary and appear in every record; the values are what was
// measured on device. One residual is documented rather than stripped: "settled" appears inside
// the focus line's own parenthetical, so a beat could in principle anchor on it. It is one word,
// it is not a subject, and stripping by parenthesis would eat real values.
func recordEvidence(record string) string {
	var b strings.Builder
	for _, line := range strings.Split(record, "\n") {
		if i := strings.Index(line, ": "); i >= 0 {
			line = line[i+2:]
		}
		b.WriteString(recordKeyPrefix.ReplaceAllString(line, ""))
		b.WriteString("\n")
	}
	return b.String()
}

// recordKeyPrefix matches the "key=" of a counted field, so "user_turns=10" contributes 10 rather
// than a word every record carries.
var recordKeyPrefix = regexp.MustCompile(`[A-Za-z_]+=`)

// beatAnchorTerms is the terms of one entry, deduplicated, STRONG IDENTIFIERS FIRST.
//
// The ordering changes nothing about the decision — anchoring is an OR over every term — and
// exists only so the reported anchor is the most informative one available. Reading "kept on
// `build-pkg.sh`" tells a reviewer something; reading "kept on `bank`" about the same entry does
// not, and both were true.
func beatAnchorTerms(s string) []string {
	var strong, plain []string
	seen := map[string]bool{}
	for _, tok := range subjectTokens(s) {
		t := trimTermPunct(tok)
		if t == "" {
			continue
		}
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		switch {
		case strongIdentifier(t):
			seen[k] = true
			strong = append(strong, t)
		case runeLen(t) >= beatAnchorMinRunes && !insightStopWord(k) && !stopWord(t):
			seen[k] = true
			plain = append(plain, t)
		}
	}
	return append(strong, plain...)
}

// ⚠️ THE PER-ENTRY GUARD IS NARROWED TO SPECIFICS, BECAUSE "AT LEAST ONE TERM" WAS UNFALSIFIABLE.
//
// The rule above — one token of four or more runes that is not a function word — fired 0 times
// across two sweeps, 0 of 274 entries. Its own artifact shows why: it accepted `each` as an
// anchor, on a beat that was a near-verbatim copy of the prompt's worked examples. Practically
// every English sentence carries a four-rune non-function word that a 16,000-rune window also
// carries, so the guard could be satisfied without the entry saying anything the evidence says.
// That is the ninth check on this branch whose vocabulary turned out to be ordinary English.
//
// What it was for is FABRICATED SPECIFICS, so specifics are what it now reads:
//
//   - a SPECIFIC is an identifier-shaped token (path, filename, dotted or snake_case name, flag,
//     symbol, anything carrying a digit or an internal capital), a number or an amount, or a
//     proper noun that is capitalised somewhere other than the start of its sentence;
//   - EVERY specific in an entry must occur in that entry's own window or in the measured record.
//     Not "at least one": an entry carrying two specifics of which one is invented is a
//     fabrication, and an OR would pass it on the other one;
//   - an entry carrying NO specifics is UNCONSTRAINED and passes. It cannot fabricate a specific
//     it does not have, and dropping it would be dropping ordinary English — which is the
//     mistake this branch has now made eight times.
//
// ⚠️ IT IS NOT THE DF GATE, and that is measured rather than preferred — see the table above:
// document frequency would drop "the export came back empty for that date range", every noun of
// which is in the window, while anchoring "the issue was discussed and identified" on a bare verb.
//
// The normalisation is the other half of the rule, and every part of it is a mistake this study
// has already paid for: matching is case-insensitive (a beat writing "Atlas" about a window that
// wrote "atlas" has named the thing), simple plurals and possessives are folded (scoring "KPIs"
// against a window holding "KPI" as a fabrication is exactly the 22.6% failure), punctuation at a
// term's ends is trimmed, and occurrence is SUBSTRING so a morphological variant still counts.
// The tokenisation is subjectTokens', which keeps '/', '.', '_' and '-' attached — identifierPat
// splits those, and a rule that reads "app/main.py" as "app" and "main.py" measures fragments.

// beatSpecifics returns the checkable specifics an entry names, in order and deduplicated.
func beatSpecifics(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range subjectTokenSpans(s) {
		t, ok := specificAt(s, m)
		if !ok {
			continue
		}
		k := strings.ToLower(t)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, t)
	}
	return out
}

// specificAt decides whether the token at span m is a specific, and returns its trimmed spelling.
//
// The three routes are the three shapes a fabrication takes. strongIdentifier covers the first
// (paths, dotted filenames, snake_case, internal capitals, anything with a digit) and is used
// unchanged, because it is the package's narrow rule and the one that does not flag English. A
// letterless token carrying a digit is the second — "1,650.55", "15/15", "0.22" — and it is
// separated out only because containsLetter would otherwise exclude it. The third is the
// position-aware proper noun: a capital mid-sentence is a name, the same capital at a sentence
// start is English, which is the distinction the 22.6% over-detection lacked. An ordinary
// hyphenated compound is excluded for the same reason it is in Identifiers.
func specificAt(s string, m [2]int) (string, bool) {
	tok := trimTermPunct(s[m[0]:m[1]])
	if tok == "" || stopWord(tok) {
		return "", false
	}
	if !containsLetter(tok) {
		return tok, hasDigit(tok)
	}
	if strongIdentifier(tok) {
		return tok, true
	}
	r := []rune(tok)
	if len(r) >= specificProperNounMinRunes && unicode.IsUpper(r[0]) &&
		!sentenceInitial(s, m[0]) && !strings.Contains(tok, "-") {
		return tok, true
	}
	return "", false
}

// specificProperNounMinRunes keeps two-letter capitals out of the proper-noun route. `UI` was one
// of the two noise terms the unverified-identifier measure produced over 19 beats, and the same
// token would be flagged here for the same reason.
const specificProperNounMinRunes = 3

func hasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// specificPresent reports whether a specific occurs in the evidence, normalised.
//
// Case-insensitive substring, then the plural/possessive fold. inflectionPresent is the package's
// existing rule for exactly this (see its doc: a transcript saying "KPI" and a report saying
// "KPIs" is not a fabrication), reused rather than reimplemented so the two measures cannot drift
// into disagreeing about what a plural is.
// ⚠️ A '/'-JOINED TOKEN WHOSE EVERY PART IS IN THE EVIDENCE IS NOT A FABRICATION. The tokeniser
// keeps '/' attached because that is what makes "app/main.py" one name — but a model also writes
// '/' as prose punctuation, and on the last sweep's real material that produced FIVE of the
// seventeen entries this rule would otherwise have dropped: `installer/JSON`, `start/callback`,
// `telemetry.py/_event_values`, `forest/amber`, `NULL/empty`. Every part of each is in its own
// window; only the joining is the model's. That is the same defect as scoring a plural a
// fabrication, one level up, so the parts are checked when the whole is absent. It is the lenient
// direction and it costs something real: an invented path every component of which appears
// separately now passes. The failure this guard exists to catch is a name from nowhere.
func specificPresent(term, hay string) bool {
	k := strings.ToLower(term)
	if strings.Contains(hay, k) {
		return true
	}
	if inflectionPresent(k, term, hay) {
		return true
	}
	parts := strings.Split(k, "/")
	if len(parts) < 2 {
		return false
	}
	var checked int
	for _, p := range parts {
		if runeLen(p) < 2 {
			continue
		}
		if !strings.Contains(hay, p) && !inflectionPresent(p, p, hay) {
			return false
		}
		checked++
	}
	return checked > 0
}

// anchorBeatEvents splits a beat's entries into the kept and the dropped, and reports what each
// survivor was checked on.
//
// A guard whose decision cannot be checked is a guard nobody can audit, so the term is returned
// rather than discarded: the sweep prints it, and a reader can see that an entry survived on
// "fa-register.csv" and not on "the".
func anchorBeatEvents(events []string, window, record string) (kept, dropped []string, anchors []BeatAnchor) {
	win, rec := strings.ToLower(windowEvidence(window)), strings.ToLower(recordEvidence(record))
	for _, e := range events {
		specifics := beatSpecifics(e)
		a := BeatAnchor{Specifics: len(specifics)}
		var fabricated bool
		for _, t := range specifics {
			switch {
			case specificPresent(t, win):
				if a.Term == "" || !a.InWindow {
					a.Term, a.InWindow = t, true
				}
			case specificPresent(t, rec):
				if a.Term == "" {
					a.Term = t
				}
			default:
				fabricated = true
			}
		}
		if fabricated {
			dropped = append(dropped, e)
			continue
		}
		kept = append(kept, e)
		anchors = append(anchors, a)
	}
	return kept, dropped, anchors
}

// beatFabricatedSpecifics returns the specifics of an entry that occur in neither the window nor
// the record — what anchorBeatEvents dropped it for, so the sweep can print the reason beside the
// entry rather than leaving a reader to re-derive it.
func beatFabricatedSpecifics(entry, window, record string) []string {
	win, rec := strings.ToLower(windowEvidence(window)), strings.ToLower(recordEvidence(record))
	var out []string
	for _, t := range beatSpecifics(entry) {
		if !specificPresent(t, win) && !specificPresent(t, rec) {
			out = append(out, t)
		}
	}
	return out
}

// unverifiedSpecifics returns the strong IDENTIFIERS a beat uses that do not occur in the
// evidence: a path, a dotted filename, a snake_case or versioned token, anything carrying a digit
// or an internal capital.
//
// Recorded, never enforced. STRONG IDENTIFIERS ONLY, and that narrowness is the point. The two
// wider rules available here both misfire: Identifiers' regex is what flagged "Key", "Initial"
// and "e.g" at 22.6%, and beatSubjectTermList's weak route — a capitalised token used
// mid-sentence — flags the same words whenever a model capitalises one ("the Initial Key rows"
// yields `initial`, `key`, measured). A strong identifier names one thing and nothing else, so
// its absence from the evidence is a fact about the beat rather than about English.
//
// Occurrence is the same VerifyTopics test the anchor uses, so the two facts are measured the
// same way.
func unverifiedSpecifics(s, evidence string) []string {
	hay := strings.ToLower(evidence)
	var out []string
	seen := map[string]bool{}
	for _, tok := range subjectTokens(s) {
		t := trimTermPunct(tok)
		k := strings.ToLower(t)
		if t == "" || !strongIdentifier(t) || seen[k] || runeLen(t) < unverifiedMinRunes {
			continue
		}
		seen[k] = true
		if strings.Contains(hay, k) || strings.Contains(hay, strings.TrimSuffix(k, "s")) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// unverifiedMinRunes and the singular fallback above are both from the first full sweep, where
// this measure named three terms across 19 beats and two of them were noise of exactly the kinds
// this package has already paid for: `UI` (two letters, an "internal capital" by the shared rule)
// and `CTAs` (a plural whose singular is in the window — plurals scored as fabrication is a
// mistake made here before). Reporting either as an unverified specific is the 22.6% failure in
// miniature.
const unverifiedMinRunes = 4
