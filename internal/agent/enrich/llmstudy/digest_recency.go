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
// Comparison is on distinctiveTerms, NOT on bare word overlap — but ⚠️ the immunity an
// earlier version of this doc claimed from that ("DISTINCTIVE terms only, never bare word
// overlap") is FALSE, and it is false in the direction that makes this check look clean.
// distinctiveToken admits any lowercase word of 7+ characters that is not in its stopword
// lists, and ordinary English is full of those. Reproduced directly: a synopsis entirely
// about one subject (a ledger reconciliation), measured against a recent window about an
// unrelated one (dropdown opacity), yields recentHits=2 — on the words "remains" and
// "whether" — and this function only reports lag when recentHits == 0. Two ordinary English
// words shared between any two passages are enough to certify a synopsis current.
//
// So T11 and T12 have the SAME root cause, and Part 7 diagnosed distinctiveTerms for T12
// only. **T11's 0.0% is close to a tautology of the tokeniser, not a floor**: it is not
// evidence that synopses are current, and it must not be read as support for the design's
// currency prediction. It is recorded as "not established" alongside T12 in Part 7.
//
// Measuring bare word overlap is the error that made unverified identifiers read 22.6%,
// leakage read ~100 per sweep, and plurals count as fabrication — and this function is a
// partial instance of that error rather than a counterexample to it. Fixing it needs a real
// distinctiveness rule (see distinctiveToken's note); the stopword-case fix landed alongside
// this comment narrows the hole but does not close it — neither "remains" nor "whether" is
// in either stopword list at any casing.
//
// A verdict is withheld unless the opening actually offers something to match.
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
// underscored names, digits, internal capitals, and long uncommon words.
//
// ⚠️ It does NOT share Identifiers' notion of "distinctive", and an earlier version of this
// doc claimed it did ("the same notion the identifier gate uses, so the two checks agree
// about what a specific is"). They disagree, materially: Identifiers admits a token only if
// strongIdentifier(tok) OR it is capitalised AND mid-sentence, so ordinary lowercase English
// never enters it; distinctiveToken additionally admits ANY lowercase word of 7+ characters.
// That is why a beat's "subject terms" come out as gerunds and adverbs (T12) and why a
// synopsis can match an unrelated window on two English words (T11 — see SynopsisLag).
// Do not reason about one from the other.
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

// distinctiveToken is the shared test: a strong identifier, or a term the local corpus shows is
// SPECIFIC rather than ordinary.
//
// ⚠️ THE ">=7 CHARACTERS" CLAUSE IS GONE. It was the documented mechanical root of four defects
// (see docfreq.go for all four and for the measured DF table), and no stopword list could close
// it because the offending words — failure, control, question, remains, whether — are content
// words, not function words.
//
// The three routes, in order, and why each exists:
//
//  1. stopWord / digestCommonWord, case-insensitively. Kept: cheap, and right about the words it
//     names. The case-insensitive lookup was itself a fix — digestStopWords was built for
//     Identifiers, which only offers capitalised tokens, so every key is capitalised and a
//     case-sensitive lookup missed the lowercase forms this function sees. Measured,
//     distinctiveToken("Currently") was false while distinctiveToken("currently") was true.
//  2. strongIdentifier, unchanged and unconditional. A path, a dotted filename, a versioned or
//     snake_case token names one thing whatever its frequency — the spec keeps this as an
//     independent sufficient condition, and it is also the whole of the rule during cold start.
//  3. document frequency below dfMaxFraction, reached only when the table is REPRESENTATIVE.
//     Otherwise the answer is no: cold start falls back to the narrow rule, never the broad one,
//     because admitting too few subjects is recoverable and poisoning a block labelled
//     authoritative is not.
//
// Still deliberately scoped to this function plus weakProperNoun (session_record.go), which now
// consults the same table for the same reason: those two decide SessionRecord.Subjects, the
// headline number this change is judged on. Identifiers (digest_check.go) is untouched — it feeds
// the retain-list and the T2/T4 metrics, and moving it would re-baseline what those measure for
// no defect anyone has observed.
func distinctiveToken(tok string) bool {
	tok = trimTermPunct(tok)
	if len(tok) < dfMinTermLen || stopWord(tok) || digestCommonWord(strings.ToLower(tok)) {
		return false
	}
	if strongIdentifier(tok) {
		return true
	}
	return corpusDistinctive(tok)
}

// corpusDistinctive is route 3: is this term rare enough across the local corpus to name a
// subject? False during cold start, which is the narrow direction on purpose.
func corpusDistinctive(tok string) bool {
	df := documentFrequency()
	if !df.representative() {
		return false
	}
	return df.fraction(trimTermPunct(tok)) < dfMaxFraction
}

// stopWord is digestStopWords looked up without regard to case. The map's keys are
// capitalised (see digestStopWords), so a lowercase token needs its initial upcased to reach
// them; the rest of the token's spelling is left alone, since a key like "It" must not be
// matched by an unrelated all-caps identifier ("IT_HOME" arrives as one token because
// subjectTokens keeps '_' attached, and strongIdentifier claims it anyway).
func stopWord(tok string) bool {
	if digestStopWords[tok] {
		return true
	}
	if tok == "" {
		return false
	}
	r := []rune(strings.ToLower(tok))
	r[0] = []rune(strings.ToUpper(string(r[0])))[0]
	return digestStopWords[string(r)]
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
			// A single over-long token is dropped, not clipped — see maxSubjectTermLen.
			// maxRecentSubjects bounds how MANY terms reach the live anchor; without this
			// it bounds nothing about how large the anchor actually is.
			if len([]rune(tok)) > maxSubjectTermLen {
				continue
			}
			// Keyed on the TRIMMED spelling, matching what is actually emitted below —
			// task-7b fix round 3: this was still `strings.ToLower(tok)` (the RAW,
			// punctuated token) even after the emitted value itself was fixed to trim,
			// so "DigestSchema." and "DigestSchema," (or "DigestSchema." repeated across
			// two turns) hashed to different keys and both survived, handing the live
			// recency anchor an exact duplicate the dedup was supposed to prevent.
			// resolveSubjectTerm runs on BOTH the key and the emitted value, for the same
			// reason the trim does: a worktree checkout path and the repository it is a
			// checkout of are the same subject, and the anchor should say the repository (see
			// record_paths.go). Keyed on the resolved spelling so the path and a bare mention
			// of the repository do not both take a slot.
			term := resolveSubjectTerm(trimTermPunct(tok))
			k := strings.ToLower(term)
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
			out = append(out, term)
			if len(out) == maxRecentSubjects {
				return out
			}
		}
	}
	return out
}

// subjectTokens splits text into candidate subject terms, preserving case and keeping the
// characters that make an identifier an identifier — including the thousands separators that
// make an amount one number.
func subjectTokens(s string) []string {
	// ONE tokeniser, not two agreeing loops: subjectTokenSpans (beat_series.go) is the
	// implementation and this returns its spans as strings. It used to be a FieldsFunc over the
	// shared subjectTokenRune predicate, which was equivalent only as long as the rule fitted in
	// a per-rune class — and the thousands-separator rule does not, since a ',' is a token
	// character only between digits. Two loops, one of them context-free, is exactly how the two
	// would have drifted.
	spans := subjectTokenSpans(s)
	out := make([]string, 0, len(spans))
	for _, m := range spans {
		out = append(out, s[m[0]:m[1]])
	}
	return out
}

// maxRecentSubjects bounds the anchor. It is a nudge toward the present, not a vocabulary
// the report must exhaust.
const maxRecentSubjects = 10

// maxSubjectTermLen bounds ONE subject term's length, wherever a subject term is emitted
// into a prompt: the recency anchor (RecentSubjects, capped in COUNT by maxRecentSubjects)
// and the session record (SessionRecord.Subjects, capped in count by MaxRecordSubjects).
//
// A count cap without a length cap is not a bound. subjectTokens deliberately preserves
// [A-Za-z0-9._/-] — that is what keeps a path or a dotted name in one piece — so a single
// base64url blob, JWT, data-URI fragment or long trace id pasted into a real transcript
// arrives as ONE token of unbounded length. Measured directly through Observe with a
// 1,000-rune blob in one user turn: a single Subjects entry of 1,025 runes, a
// SessionRecord.Block() of 1,102, and (per the round-2 review) an 18,173-rune prompt from
// the same shape once the other sections are populated — i.e. this one dimension can
// exceed the entire prompt budget on its own. The backstop (assertPromptWithinBudget)
// catches that as a violation, which is correct but is a hard failure; bounding the term
// itself is the prevention.
//
// A term over the cap is DROPPED, not clipped. Clipping an identifier mid-name manufactures
// a specific that never appeared — the same reasoning boundRetainList's doc gives for
// dropping whole entries — and both callers feed a model that is told every named specific
// is real.
//
// ⚠️ 64 DOES drop real paths, and the "54 runes is the longest" claim this doc used to make
// was wrong. Re-measured against the repository rather than from memory:
//
//	longest tracked .go path      57  internal/agent/enrich/llmstudy/digest_consistency_test.go
//	longest tracked path, any     83  docs/superpowers/specs/2026-07-05-keld-agent-loadtest-and-memory-eviction-design.md
//	(the 54-rune capability_eval_test.go path is merely the one a test happens to use)
//
// So every design/spec path in docs/ — the paths this branch's own sessions discuss most —
// is silently deleted from both the anchor and the record. That matters because it is a
// SECOND mechanism removing named specifics, alongside the one T4 measures at 50.0%/56.2%
// (see DefaultListEntryCap's doc and Part 7); a term the record never holds is a term no
// consistency check can ground a beat against, and T12's tool-name-dominated subject lists
// are the visible symptom.
//
// Raising it is NOT free, and that was measured rather than assumed. Both callers are
// count-capped (maxRecentSubjects 10, MaxRecordSubjects 12), so the cap multiplies by 22 in
// the worst case. At 96 — enough to admit an 83-rune docs path —
// TestWorstCasePromptOnBothPaths/refine goes to exactly 14,000 of 14,000 with a 1-rune
// window margin, and the CREATE arm PANICS on the backstop: window content 1,336 against
// the 1,600 floor. There is no room at the current budget. Widening this therefore needs
// either a larger budget/ctx (which the corpus probe already says cannot come down, so it
// would have to go up) or a smaller count cap, and either is a measured change, not a
// constant edit. Left at 64 with the cost recorded.
const maxSubjectTermLen = 64

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
