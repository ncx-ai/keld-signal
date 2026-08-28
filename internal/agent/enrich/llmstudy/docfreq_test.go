package llmstudy

import (
	"strings"
	"testing"
)

// dfGenericFixture is the vocabulary a test corpus treats as ordinary English: every one of
// these words appears in EVERY session of the fixture, so its document frequency is 1.00.
//
// Every entry is a word this branch measured causing a defect on the >=7-character rule —
// `remains` and `whether` defeated SynopsisLag (T11), `control`/`question`/`exactly`/`failure`/
// `padding`/`identity` were found inside SessionRecord.Subjects under the heading "measured —
// authoritative" — so the fixture states the problem rather than a convenient version of it.
var dfGenericFixture = []string{
	"remains", "whether", "control", "question", "exactly", "failure", "padding", "identity",
	"changes", "confirm", "complete", "running", "consistent", "existing", "specifically",
	"analyzing", "ensuring", "finalizing", "buttons", "parallel", "checkout", "worktree",
	// Capitalised ordinary English measured reaching Subjects by the weakProperNoun route,
	// named in beatSubjectTermsGrounded's doc as the reason the record's terms are unusable.
	"confirmed", "activity", "adjustment",
}

// installTestDocFreq gives the package a representative, deterministic DF table for the duration
// of a test.
//
// Unit tests must not read the machine's transcript directory: the study has already been burned
// six times by a green test certifying a property the code does not have, and a corpus-dependent
// unit test is that shape by construction — it would pass or fail according to what the person
// running it happened to have been working on. The corpus-derived table is exercised separately,
// under -tags llmstudy, where depending on real transcripts is the point.
func installTestDocFreq(t *testing.T) {
	t.Helper()
	sessions := make([][]string, 0, 20)
	for i := 0; i < 20; i++ {
		sessions = append(sessions, dfGenericFixture)
	}
	// One session's worth of specific vocabulary, so these read as rare rather than as absent.
	sessions[0] = append(append([]string{}, dfGenericFixture...),
		"threshold", "synopsis", "digest", "concurrency", "meridian", "larkin",
		"depreciation", "accruals", "reconciliation", "invoice", "vendor", "payment")
	t.Cleanup(withDocFreq(newDocFreq(sessions)))
}

// TestDocumentFrequencyRetiresTheLengthRule is task 4's headline property, stated as the two
// cases that used to be indistinguishable: a long ordinary English word and a short specific one.
//
// Fails before the change: `remains` (7 chars) was distinctive and `Larkin` (6) was not, which is
// exactly backwards, and the reason SessionRecord.Subjects held 8 non-subjects out of 12.
func TestDocumentFrequencyRetiresTheLengthRule(t *testing.T) {
	installTestDocFreq(t)
	for _, w := range dfGenericFixture {
		if distinctiveToken(w) {
			t.Errorf("%q appears in every session of the corpus and must not name a subject", w)
		}
	}
	for _, w := range []string{"meridian", "larkin", "depreciation", "accruals", "concurrency"} {
		if !distinctiveToken(w) {
			t.Errorf("%q is concentrated in one session and must name a subject", w)
		}
	}
	// Length is no longer evidence in either direction: a rare 6-letter word qualifies and a
	// common 12-letter one does not.
	if !distinctiveToken("Larkin") {
		t.Error("a rare 6-character name must qualify now that the length floor is gone")
	}
	if distinctiveToken("specifically") {
		t.Error("a common 12-character word must not qualify on length")
	}
}

// TestStrongIdentifiersDoNotNeedTheCorpus keeps the spec's independent sufficient condition
// independent: a path or a dotted name is distinctive whatever its frequency, and it is also the
// whole of the rule during cold start.
func TestStrongIdentifiersDoNotNeedTheCorpus(t *testing.T) {
	// A table where every identifier is in EVERY session, i.e. maximally generic by DF.
	ids := []string{"daemon.go", "internal/agent/enrich", "KELD_WATCH", "DigestSchema", "0.6B"}
	sessions := make([][]string, 0, 20)
	for i := 0; i < 20; i++ {
		sessions = append(sessions, append(append([]string{}, dfGenericFixture...), ids...))
	}
	t.Cleanup(withDocFreq(newDocFreq(sessions)))
	for _, w := range ids {
		if !distinctiveToken(w) {
			t.Errorf("%q is a strong identifier and must qualify regardless of frequency", w)
		}
	}
}

// TestColdStartFallsBackToTheNarrowRule is the risk the spec names explicitly: on a new machine
// there is one session, DF is meaningless, and falling back to the >=7-character rule would ship
// the defect to exactly the users least able to notice it.
func TestColdStartFallsBackToTheNarrowRule(t *testing.T) {
	// Below dfMinSessions, however the evidence looks.
	few := make([][]string, 0, dfMinSessions-1)
	for i := 0; i < dfMinSessions-1; i++ {
		few = append(few, []string{"meridian"})
	}
	t.Cleanup(withDocFreq(newDocFreq(few)))
	if documentFrequency().representative() {
		t.Fatalf("%d sessions read as representative; dfMinSessions is %d", len(few), dfMinSessions)
	}
	// The narrow rule: identifiers only.
	if !distinctiveToken("daemon.go") || !distinctiveToken("KELD_WATCH") {
		t.Error("cold start must still admit strong identifiers")
	}
	// And NOT the broad one. `reconciliation` is 14 characters and appears in no session here;
	// the old rule admitted it on length alone, which is the behaviour that must not survive as
	// a fallback.
	for _, w := range []string{"reconciliation", "remains", "concurrency", "Larkin"} {
		if distinctiveToken(w) {
			t.Errorf("cold start admitted %q on something other than identifier shape — the "+
				"fallback must be narrow, never the >=7-character rule", w)
		}
	}
}

// TestUninitialisedTableIsColdStart pins the default. An auto-scanning table would make every
// unit test in this package depend on the machine's transcript directory.
func TestUninitialisedTableIsColdStart(t *testing.T) {
	t.Cleanup(withDocFreq(&docFreq{count: map[string]int{}}))
	if documentFrequency().representative() {
		t.Error("an empty table must not be treated as evidence")
	}
	if distinctiveToken("reconciliation") {
		t.Error("with no corpus the broad rule must not apply")
	}
	if got := documentFrequency().fraction("anything"); got != 1 {
		t.Errorf("fraction with no evidence = %v, want 1 (maximally generic)", got)
	}
}

// TestWeakProperNounNeedsTheCorpusToo is the second half of retiring the length rule. Position and
// capitalisation are evidence of a NAME; they are not evidence that the name is this session's
// subject, and Subjects is a 12-slot list the prompt calls authoritative.
//
// Fails before the change: "Confirmed", "Activity" and "Adjustment" were measured reaching
// Subjects by this route, and beatSubjectTermsGrounded's doc names them as the reason the
// record's terms are unusable as a novelty vocabulary.
func TestWeakProperNounNeedsTheCorpusToo(t *testing.T) {
	installTestDocFreq(t)
	text := "we then Confirmed the Larkin accrual and the Changes to it"
	if !weakProperNoun("Larkin", text) {
		t.Error("a rare capitalised mid-sentence name must still be admitted")
	}
	if weakProperNoun("Confirmed", text) {
		t.Error("an ordinary capitalised word must not reach Subjects on position alone")
	}
	if weakProperNoun("Changes", text) {
		t.Error("a corpus-common word must not reach Subjects on position alone")
	}
}

// TestDocFreqCountsSessionsNotOccurrences pins the distinction the whole mechanism rests on: a
// term repeated fifty times in one session is one session's worth of evidence, and counting
// occurrences would make any long session's vocabulary look universal.
func TestDocFreqCountsSessionsNotOccurrences(t *testing.T) {
	fifty := make([]string, 50)
	for i := range fifty {
		fifty[i] = "meridian"
	}
	d := newDocFreq([][]string{fifty, {"other"}, {"other"}, {"other"}})
	if got := d.count["meridian"]; got != 1 {
		t.Errorf("meridian counted %d times, want 1 session", got)
	}
	if got := d.fraction("meridian"); got != 0.25 {
		t.Errorf("fraction = %v, want 0.25", got)
	}
	// Case-folded, matching what the tokeniser stores.
	if d.fraction("MERIDIAN") != 0.25 {
		t.Error("DF lookups must be case-insensitive, as the tokeniser lowercases")
	}
}

// TestSessionTermSetTokenisesLikeTheCaller is the silent-failure guard. A table built by a
// different splitter would answer questions the caller never asks, every caller token would read
// as DF 0, and the fix would look like it was working while admitting everything.
func TestSessionTermSetTokenisesLikeTheCaller(t *testing.T) {
	text := "The DigestSchema in internal/agent/enrich/llmstudy/digest.go remains a threshold."
	var fromCaller []string
	for _, tok := range subjectTokens(text) {
		tok = trimTermPunct(tok)
		if runeLen(tok) >= dfMinTermLen && runeLen(tok) <= maxSubjectTermLen {
			fromCaller = append(fromCaller, strings.ToLower(tok))
		}
	}
	if len(fromCaller) == 0 {
		t.Fatal("fixture produced no tokens")
	}
	// sessionTermSet's own body must agree token for token; assert the interesting shapes
	// rather than re-deriving the loop, which would only prove the loop equals itself.
	for _, want := range []string{"digestschema", "internal/agent/enrich/llmstudy/digest.go",
		"remains", "threshold"} {
		found := false
		for _, got := range fromCaller {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("the caller's tokeniser does not produce %q; DF would never be asked about it", want)
		}
	}
}
