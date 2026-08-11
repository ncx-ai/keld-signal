package llmstudy

import (
	"strings"
	"testing"
)

// Measured on this session's own 44-window transcript: the synopsis described work the
// session had left behind while the newest turns were about something else entirely. T7 and
// T8 both pass such a report, because nothing in it is fabricated or self-contradictory —
// it is simply out of date, which is the one failure a reader cannot detect unaided.
func TestSynopsisLagIsDetected(t *testing.T) {
	early := "user: the eval/mine module extracts a moving window and elides code with ElideCode\n"
	recent := "user: add a synopsis section answering what the work is about, not a kanban view\n"

	stale := Digest{Synopsis: "The work focuses on the eval/mine module's moving window and ElideCode policy."}
	if _, _, lag := SynopsisLag(stale, early, recent); !lag {
		t.Error("a synopsis grounded only in the opening was not flagged")
	}
	current := Digest{Synopsis: "The work is adding a synopsis section so a report answers what the work is about."}
	if _, _, lag := SynopsisLag(current, early, recent); lag {
		t.Error("a synopsis grounded in the newest turns was flagged")
	}
	// A synopsis spanning both is correct — it should keep the subject AND be current.
	both := Digest{Synopsis: "Work on the eval/mine moving window has moved to adding a synopsis section."}
	if _, _, lag := SynopsisLag(both, early, recent); lag {
		t.Error("a synopsis covering origin and present was flagged")
	}
}

// Too little distinctive vocabulary to judge must not produce a verdict. Every previous
// version of a check like this over-reported by ruling on thin evidence.
func TestSynopsisLagAbstainsOnThinEvidence(t *testing.T) {
	if _, _, lag := SynopsisLag(Digest{Synopsis: "The work continues."}, "user: hi\n", "user: ok\n"); lag {
		t.Error("a verdict was returned on almost no evidence")
	}
}

// The deterministic anchor: distinctive terms from the newest user turns, handed to the
// model rather than left for it to infer. This is the pattern that fixed fact retention and
// open-item closure; inference alone failed in the costly direction.
func TestRecentSubjectsAreExtractedAndHandedOver(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleUser, "look at the eval/mine module"},
		{RoleAssistant, "reading it"},
		{RoleUser, "now add a synopsis section to DigestSchema, not a kanban view"},
	}}
	subs := RecentSubjects(w, 1)
	joined := strings.Join(subs, " ")
	if !strings.Contains(joined, "DigestSchema") {
		t.Fatalf("distinctive term from the newest user turn missing: %v", subs)
	}
	if strings.Contains(joined, "eval/mine") {
		t.Errorf("a term from an OLDER turn leaked into the recent anchor: %v", subs)
	}
	p := DigestUpdatePromptWithReason(Digest{Done: "x"}, "work session", Render(w), "", "counts: turns=3\n", TriggerNone)
	if !strings.Contains(p, "DigestSchema") {
		t.Error("refine prompt does not hand over the latest subjects")
	}
}

// RecentSubjects called distinctiveToken (which trims only internally, to decide whether a
// token qualifies) but stored the RAW token, so a subject sitting at the end of a sentence
// kept its terminal punctuation glued on. recentSubjectsOf splices the result straight into
// a live prompt (THE LATEST TURNS ARE ABOUT: ...), so the model was shown "DigestSchema."
// with the period attached. Sibling distinctiveTerms had the identical bug, fixed via
// trimTermPunct in d717ea3; this is the same fix applied to RecentSubjects' emitted value.
func TestRecentSubjectsTrimsTerminalPunctuation(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleUser, "We need to extend DigestSchema."},
	}}
	subs := RecentSubjects(w, 1)
	joined := strings.Join(subs, " ")
	if strings.Contains(joined, "DigestSchema.") {
		t.Errorf("emitted subject kept its terminal punctuation: %v", subs)
	}
	if !strings.Contains(joined, "DigestSchema") {
		t.Fatalf("expected a trimmed DigestSchema in the output: %v", subs)
	}
}

// TestRecentSubjectsBoundEachTermsLength pins maxSubjectTermLen on the anchor side.
// maxRecentSubjects bounds how MANY terms reach the live "THE LATEST TURNS ARE ABOUT"
// block, which bounds nothing about its size: subjectTokens keeps a base64/dotted blob
// together as one token, so ten of them is ten thousand runes spliced into a prompt.
// The path-shaped assertion is the over-correction guard — the anchor's whole job is
// handing over recognisable identifiers, and a 54-rune source path is one.
func TestRecentSubjectsBoundEachTermsLength(t *testing.T) {
	const realPath = "internal/agent/enrich/llmstudy/capability_eval_test.go"
	w := Window{Turns: []Turn{
		{RoleUser, "decode " + blobToken() + " then look at " + realPath},
	}}
	subs := RecentSubjects(w, 1)
	for _, s := range subs {
		if n := len([]rune(s)); n > maxSubjectTermLen {
			t.Errorf("anchor term is %d runes, over the %d-rune cap: %.40q...", n, maxSubjectTermLen, s)
		}
	}
	if !strings.Contains(strings.Join(subs, " "), realPath) {
		t.Errorf("a genuine %d-rune path-shaped term must still reach the anchor, got %v",
			len([]rune(realPath)), subs)
	}
}

// task-7b fix round 3 (minor G): the fix above trimmed the EMITTED value but left the
// dedup key as `strings.ToLower(tok)` — the RAW, still-punctuated token — so a subject
// appearing once bare ("DigestSchema") and once sentence-final ("DigestSchema.") hashed
// to two different keys despite both trimming to the identical emitted string, and both
// survived into the live recency anchor as an exact duplicate. Two user turns are used so
// both spellings are actually scanned (RecentSubjects walks the newest n user turns).
func TestRecentSubjectsSeenKeyMatchesTheEmittedTrimmedValue(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleUser, "DigestSchema needs work"},
		{RoleAssistant, "ok"},
		{RoleUser, "back to DigestSchema."},
	}}
	subs := RecentSubjects(w, 2)
	count := 0
	for _, s := range subs {
		if s == "DigestSchema" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("DigestSchema should be deduped to one entry regardless of trailing "+
			"punctuation on the raw token, got %d occurrences in %v", count, subs)
	}
}
