package llmstudy

import (
	"sort"
	"strings"
)

// SynopsisLag reports a synopsis grounded in the session's opening while ignoring its
// present, returning the distinctive-term hits against each so a verdict is never a bare
// boolean.
//
// This is the one failure a reader cannot detect unaided, and the one no existing threshold
// catches. Measured on this session's own transcript, the synopsis described a branch
// discussed forty windows earlier while the newest turns were about something else. Nothing
// in it was fabricated (T7 passes) and nothing contradicted itself (T8 passes) — it was
// simply out of date, and a reader with no other view believes it.
//
// Comparison is on DISTINCTIVE terms only, never bare word overlap. Ordinary English is
// shared by any two passages about anything, and measuring it is the error that made
// unverified identifiers read 22.6%, leakage read ~100 per sweep, and plurals count as
// fabrication. A verdict is withheld unless the opening actually offers something to match.
func SynopsisLag(d Digest, earlySrc, recentSrc string) (recentHits, earlyHits int, lag bool) {
	syn := distinctiveTerms(d.Synopsis)
	if len(syn) == 0 {
		return 0, 0, false
	}
	early, recent := distinctiveTerms(earlySrc), distinctiveTerms(recentSrc)
	for t := range syn {
		if recent[t] {
			recentHits++
		}
		if early[t] {
			earlyHits++
		}
	}
	// Abstain unless the opening is genuinely represented: with fewer hits than this there is
	// not enough evidence to call a synopsis backward-looking rather than merely general.
	if earlyHits < minLagEvidence {
		return recentHits, earlyHits, false
	}
	return recentHits, earlyHits, recentHits == 0
}

// minLagEvidence is how much of the opening a synopsis must echo before its silence about
// the present counts as lag.
const minLagEvidence = 2

// distinctiveTerms reduces text to terms that identify a subject: paths, dotted or
// underscored names, digits, internal capitals, and long uncommon words. The same notion of
// "distinctive" the identifier gate uses, so the two checks agree about what a specific is.
//
// Neither existing tokeniser fits, so this one is its own. wordsOf keeps only [a-z0-9], so
// every uppercase letter is a separator and it shreds exactly the tokens that matter here —
// "DigestSchema" becomes "igest"+"chema" (it is correct for its callers only because they all
// lowercase first). identifierPat goes the other way: it matches dotted, hyphenated and
// Capitalised tokens but no ordinary lowercase word, and excludes "/", so neither "extracts"
// nor "eval/mine" survives. Subject vocabulary needs both kinds.
//
// Emits the TRIMMED spelling, not the raw token subjectTokens produced. subjectTokens keeps
// '.', '-', '_', '/' attached to a token rather than splitting on them (they are part of what
// makes a path or identifier an identifier), so a subject noun sitting right before a
// sentence-ending period comes out as "threshold.", not "threshold". distinctiveToken already
// trims that punctuation to decide whether the token qualifies at all — but it only returned a
// bool, so the untrimmed original was what callers actually stored. Comparing that against
// Subjects/Projects (a clean word list, never punctuated) meant a sentence-final subject could
// never match its own record: BeatContradictsRecord would flag "threshold." against a record
// that plainly holds "threshold". Trimming here, once, is what every caller (SynopsisLag,
// SubjectShifted, BeatContradictsRecord) needs and previously lacked.
func distinctiveTerms(s string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range subjectTokens(s) {
		if !distinctiveToken(tok) {
			continue
		}
		out[strings.ToLower(trimTermPunct(tok))] = true
	}
	return out
}

// trimTermPunct strips the punctuation subjectTokens leaves attached to a token's ends —
// the same cutset distinctiveToken already trims internally to judge a token's length and
// stopword status, factored out so distinctiveTerms can store the identical trimmed spelling
// instead of the raw one.
func trimTermPunct(tok string) string {
	return strings.Trim(tok, ".-_/")
}

// distinctiveToken is the shared test: a strong identifier, or a word long enough to be
// subject vocabulary rather than glue.
func distinctiveToken(tok string) bool {
	tok = trimTermPunct(tok)
	if len(tok) < 4 || digestStopWords[tok] || digestCommonWord(strings.ToLower(tok)) {
		return false
	}
	return strongIdentifier(tok) || len(tok) >= 7
}

// RecentSubjects returns distinctive terms from the last n USER turns.
//
// User turns only, and the newest ones: they are where a change of subject is announced. The
// assistant's reply elaborates whatever it was handed, so including it dilutes the signal
// with the previous topic's vocabulary.
func RecentSubjects(w Window, n int) []string {
	if n <= 0 {
		n = 1
	}
	var texts []string
	for i := len(w.Turns) - 1; i >= 0 && len(texts) < n; i-- {
		if w.Turns[i].Role == RoleUser {
			texts = append(texts, w.Turns[i].Text)
		}
	}
	if len(texts) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, tok := range subjectTokens(t) {
			if !distinctiveToken(tok) {
				continue
			}
			k := strings.ToLower(tok)
			if seen[k] {
				continue
			}
			seen[k] = true
			// Original spelling, not the lowercased key: an identifier a reader would
			// recognise is worth handing over as written. But trimmed, not raw: tok still
			// carries whatever punctuation subjectTokens left attached to its ends (a
			// sentence-final subject comes out "DigestSchema."), and this value is spliced
			// straight into the model's recency anchor by recentSubjectsOf — unlike
			// distinctiveTerms' map keys, nothing downstream of RecentSubjects trims it.
			// trimTermPunct only strips leading/trailing punctuation, so casing is untouched.
			out = append(out, trimTermPunct(tok))
			if len(out) == maxRecentSubjects {
				return out
			}
		}
	}
	return out
}

// subjectTokens splits text into candidate subject terms, preserving case and keeping the
// characters that make an identifier an identifier.
func subjectTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '/' || r == '.' || r == '_' || r == '-':
			return false
		}
		return true
	})
}

// maxRecentSubjects bounds the anchor. It is a nudge toward the present, not a vocabulary
// the report must exhaust.
const maxRecentSubjects = 10

// SubjectShifted reports whether two windows are about substantially different things,
// measured on shared distinctive terms.
//
// A STAND-IN for the production trigger, not the trigger itself. Production fires
// TriggerFocusShift off the EWMA focus the classification pipeline maintains, which this
// harness does not run. Without a stand-in the gated configuration could only be reasoned
// about — and the ungated one was measured to cost fact retention (97.4% -> 88.3%) and to
// double fabricated open items (4.1% -> 10.2%), so which configuration ships is exactly the
// question that needs measuring rather than arguing.
func SubjectShifted(prevSrc, curSrc string) bool {
	prev, cur := distinctiveTerms(prevSrc), distinctiveTerms(curSrc)
	if len(prev) < minShiftEvidence || len(cur) < minShiftEvidence {
		return false
	}
	shared := 0
	for t := range cur {
		if prev[t] {
			shared++
		}
	}
	// Continuity is the default. A shift is called only when the overlap is genuinely thin,
	// because mislabelling a routine refresh as a shift pays the anchor's cost for nothing.
	return float64(shared)/float64(len(cur)) < shiftOverlapFloor
}

// MISCALIBRATED, and left in place with this note rather than silently tuned. On a sweep
// whose refinement windows sit four apart it fired on 42 of 42 refinements: consecutive
// windows that far apart share little distinctive vocabulary even within one subject, so thin
// overlap does not distinguish a shift from ordinary progress. The gated configuration is
// therefore UNMEASURED — the run labelled "gated" was anchor-always with different wording.
//
// Calibrating it needs the production signal it stands in for (the EWMA focus argmax), not a
// different threshold here; two windows of the same work can share almost no terms.
const (
	minShiftEvidence  = 4
	shiftOverlapFloor = 0.25
)

// BeatContradictsRecord reports a beat whose subject is absent from the measured record.
// Abstains (checked=false) when the record holds too little to check against, because a
// verdict on thin evidence is how every earlier version of a check like this over-reported.
//
// checked is a SEPARATE return, not folded into terms, because abstention and "checked and
// found nothing" must not collapse to the same nil. A caller computing a rate as
// flagged/checked needs to know which digests to put in the denominator; a caller that only
// looked at terms==nil cannot tell "nothing wrong" from "nothing measured" apart, and every
// early-session beat (where the record has not yet accumulated minConsistencyEvidence
// subjects) would silently count as consistent — the same false-confidence failure this
// study exists to catch, just moved one level up from "no items logged" to "no verdict logged".
//
// Applied to beats rather than to a report paraphrase because beats are dense and cheap: the
// check runs dozens of times per session instead of once an hour, so a drifting subject is
// caught near where it started rather than after it has been folded into a report.
func BeatContradictsRecord(beat string, r SessionRecord) (terms []string, checked bool) {
	if len(r.Subjects) < minConsistencyEvidence || strings.TrimSpace(beat) == "" {
		return nil, false
	}
	subjectTerms := distinctiveTerms(beat)
	if len(subjectTerms) == 0 {
		return nil, false
	}
	hay := strings.ToLower(strings.Join(append(append([]string{}, r.Subjects...), r.Projects...), " "))
	for t := range subjectTerms {
		if strings.Contains(hay, t) || inflectionPresent(t, t, hay) {
			return nil, true // one grounded subject term is enough; this is a contradiction check
		}
	}
	out := make([]string, 0, len(subjectTerms))
	for t := range subjectTerms {
		out = append(out, t)
	}
	sort.Strings(out)
	return out, true
}

// minConsistencyEvidence is how many measured subjects the record must hold before its
// silence about a beat's subject counts as a contradiction rather than a thin record.
const minConsistencyEvidence = 3

// FabricatedNext reports named specifics in `next` that the conversation never mentions.
// Observed in real output as a next inventing schema field names; T7 inspects only
// `unresolved`, so nothing caught it.
func FabricatedNext(d Digest, source string) []string {
	return UnverifiedIdentifiers(Digest{Next: d.Next}, source)
}
