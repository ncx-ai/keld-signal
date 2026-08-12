package llmstudy

import "strings"

// Verbatim anchoring: every entry in a beat must carry at least one TERM that occurs, verbatim,
// in that beat's own window or in the measured record.
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
}

// beatAnchorIn returns where s is anchored, given its window and the measured record. The window
// is tried first so an entry anchored by both is reported against the window, which is the
// ordinary case and the one that carries no signal.
func beatAnchorIn(s, window, record string) BeatAnchor {
	terms := beatAnchorTerms(s)
	if len(terms) == 0 {
		return BeatAnchor{}
	}
	if kept, _ := VerifyTopics(terms, window); len(kept) > 0 {
		return BeatAnchor{Term: kept[0], InWindow: true}
	}
	if kept, _ := VerifyTopics(terms, record); len(kept) > 0 {
		return BeatAnchor{Term: kept[0]}
	}
	return BeatAnchor{}
}

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

// anchorBeatEvents splits a beat's entries into the anchored and the unanchored, and reports
// where each survivor was anchored.
//
// A guard whose decision cannot be checked is a guard nobody can audit, so the term is returned
// rather than discarded: the sweep prints it, and a reader can see that an entry survived on
// "fa-register.csv" and not on "the".
func anchorBeatEvents(events []string, window, record string) (kept, dropped []string, anchors []BeatAnchor) {
	for _, e := range events {
		if a := beatAnchorIn(e, window, record); a.Term != "" {
			kept = append(kept, e)
			anchors = append(anchors, a)
			continue
		}
		dropped = append(dropped, e)
	}
	return kept, dropped, anchors
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
	var terms []string
	seen := map[string]bool{}
	for _, tok := range subjectTokens(s) {
		t := trimTermPunct(tok)
		if t == "" || !strongIdentifier(t) || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		terms = append(terms, t)
	}
	if len(terms) == 0 {
		return nil
	}
	_, dropped := VerifyTopics(terms, evidence)
	return dropped
}
