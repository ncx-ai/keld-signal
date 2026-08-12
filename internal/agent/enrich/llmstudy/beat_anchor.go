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
// while silently dropping 5 of 20 digests. The guard says one thing: this entry names something
// the evidence does not contain.
//
// WHAT COUNTS AS A TERM, and why each part of that is set where it is:
//
//   - The tokenisation is subjectTokens', so '/', '.', '_' and '-' stay attached and a path or a
//     dotted filename arrives whole ("app/main.py", "fa-register.csv") instead of as fragments
//     that would match anything.
//   - Admission is distinctiveToken, the package's existing shared test for "is this a specific
//     rather than ordinary English" — a strong identifier (path, dotted filename, snake_case,
//     internal capital, anything carrying a digit), or a term the local corpus's document
//     frequency shows is rare. That gate is what kept `depreciation`, `accruals`, `Meridian` and
//     `Larkin` while excluding `control`, `question` and `failure` (docfreq.go), and reusing it
//     means the anchor rule and the record's own subjects move together instead of disagreeing.
//     The alternative — any word over some length — is the >=7-character clause this package has
//     already retired as the mechanical root of four defects.
//   - Case is IGNORED on both sides, because a beat writing "Atlas" about a window that wrote
//     "atlas" has named the thing, and a case-sensitive test would report a fabrication.
//   - Punctuation at a term's ends is trimmed (trimTermPunct), so "exporter." matches "exporter".
//   - Occurrence is SUBSTRING, via VerifyTopics — the same verbatim gate the publish path uses
//     for spans. Substring rather than whole-word is the lenient direction on purpose: "export"
//     anchors against "exporter", and the failure this guard exists to catch is an entry with
//     NOTHING behind it, not a morphological variant. Scoring plurals as fabrication is a mistake
//     this study has already made and paid for.
//
// AN ENTRY WITH NO TERMS AT ALL IS UNANCHORED. It cannot be shown to name anything from the
// evidence, which is exactly what the rule requires, and the calibration below records what that
// costs in practice.
//
// WHAT HAPPENS TO A FAILING ENTRY: it is DROPPED and the drop is MARKED in the beat text
// (renderBeat), and the dropped entries are carried on the BeatDraft so the sweep can print them.
// The beat is NOT failed — one unanchored entry is no reason to lose the rest — unless every
// entry fails, which is a generation with nothing in it and is re-requested at a wider
// temperature.

// beatAnchor returns the term that anchors s in the evidence, or "" when nothing does.
func beatAnchor(s, evidence string) string {
	terms := beatAnchorTerms(s)
	if len(terms) == 0 {
		return ""
	}
	kept, _ := VerifyTopics(terms, evidence)
	if len(kept) == 0 {
		return ""
	}
	return kept[0]
}

// beatAnchorTerms is the terms of one entry, deduplicated, in order of appearance.
func beatAnchorTerms(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range subjectTokens(s) {
		t := trimTermPunct(tok)
		if t == "" || !distinctiveToken(t) {
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

// anchorBeatEvents splits a beat's entries into the anchored and the unanchored, and reports the
// term each surviving entry was anchored by.
//
// The anchors are returned rather than discarded because a guard whose decision cannot be checked
// is a guard nobody can audit: the sweep prints the term, so a reader can see that an entry
// survived on "fa-register.csv" and not on "the".
func anchorBeatEvents(events []string, evidence string) (kept, dropped, anchors []string) {
	for _, e := range events {
		if a := beatAnchor(e, evidence); a != "" {
			kept = append(kept, e)
			anchors = append(anchors, a)
			continue
		}
		dropped = append(dropped, e)
	}
	return kept, dropped, anchors
}
