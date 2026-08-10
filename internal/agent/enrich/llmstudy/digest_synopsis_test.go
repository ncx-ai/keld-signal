package llmstudy

import (
	"strings"
	"testing"
)

// The eight original sections are a decomposed status board: answering "what is this work
// about" required a reader to assemble it from why + structure + done. The synopsis is the
// standalone answer, and it comes first.
func TestSynopsisIsRequiredAndLeads(t *testing.T) {
	sc := DigestSchema()
	props := sc["properties"].(map[string]any)
	if props["synopsis"] == nil {
		t.Fatal("schema has no synopsis")
	}
	req := sc["required"].([]string)
	if req[0] != "synopsis" {
		t.Errorf("synopsis must lead the required list, got %q first", req[0])
	}
	if len(ValidateDigest(Digest{Done: "d", Happened: "h", Structure: "s",
		Current: "c", Why: "w", Next: "n", Unresolved: []string{UnresolvedSentinel}})) == 0 {
		t.Error("a digest with no synopsis must be reported as incomplete")
	}
}

// A synopsis that merely restates `why` has added nothing — the failure mode a synthesis
// section invites, given every other section is told to be distinct.
func TestSynopsisRestatingWhyIsDetected(t *testing.T) {
	why := "To ensure the March financial statements are accurate and compliant with policy."
	if !SynopsisRestatesAnotherSection(Digest{Synopsis: why, Why: why}) {
		t.Error("a verbatim restatement of why was not detected")
	}
	if !SynopsisRestatesAnotherSection(Digest{
		Synopsis: "To ensure that the March financial statements are accurate and compliant with the policy.",
		Why:      why}) {
		t.Error("a reworded restatement of why was not detected")
	}
	good := Digest{
		Synopsis: "This is a month-end close for the Meridian entity. Seven adjusting journals " +
			"are posted and the bank reconciles; the trial balance review is the last step.",
		Why:  why,
		Done: "Bank reconciliation complete at 412,629.60 with seven journals posted.",
	}
	if SynopsisRestatesAnotherSection(good) {
		t.Error("a genuine synopsis was flagged as a restatement")
	}
}

// SynopsisRestatesAnotherSection routes through the same insightsMatch as MergeInsights and
// StaleUnresolved, so a possessive rewording between synopsis and why must be caught here too.
func TestSynopsisRestatementDetectedAcrossPossessive(t *testing.T) {
	why := "The work is reconciling the March ledger for Meridian."
	synopsis := "Work continues reconciling Meridian's March ledger."
	if !SynopsisRestatesAnotherSection(Digest{Synopsis: synopsis, Why: why}) {
		t.Error("a possessive rewording was not detected as a restatement of why")
	}
}

// The whole-session view must actually reach the prompt. It was built by SessionDigest and
// then never used, so a synopsis could only ever describe the newest window — a month-end
// close would be summarised as "clearing the suspense account".
func TestSessionViewReachesBothPrompts(t *testing.T) {
	w := Window{
		Turns:  []Turn{{RoleUser, "clear the suspense account to sundry"}},
		Digest: []Turn{{RoleUser, "close out March for the Meridian entity"}},
	}
	view := RenderSessionView(w)
	if !strings.Contains(view, "Meridian") {
		t.Fatalf("session view missing its content: %q", view)
	}
	create := DigestCreatePromptWithView("work session", Render(w), view, "counts: turns=2\n")
	if !strings.Contains(create, "Meridian") {
		t.Error("create prompt omits the whole-session view")
	}
	upd := DigestUpdatePromptWithView(Digest{Done: "x"}, "work session", Render(w), view, "counts: turns=2\n")
	if !strings.Contains(upd, "Meridian") {
		t.Error("refine prompt omits the whole-session view")
	}
}

// The session view competes for the same prompt budget, so it must be counted in the
// overhead rather than added on top of an already-full prompt.
func TestSessionViewStaysInsideTheBudget(t *testing.T) {
	huge := strings.Repeat("user: filler turn about the close\n", 3000)
	view := strings.Repeat("user: an early turn about the Meridian close\n", 200)
	p := DigestCreatePromptWithView("work session", huge, view, "counts: turns=900\n")
	if got := len([]rune(p)); got > DefaultPromptCharBudget {
		t.Errorf("prompt %d runes exceeds budget %d", got, DefaultPromptCharBudget)
	}
}

// Synopsis is carried forward: the stable half of it is what the work IS, and rederiving
// that from a late window is exactly how it would drift onto the last topic discussed.
func TestSynopsisIsCarriedForward(t *testing.T) {
	c := CarryForward(Digest{Synopsis: "a month-end close for Meridian", Current: "x", Why: "y", Next: "z"})
	if c.Synopsis == "" {
		t.Error("synopsis must be carried into the refinement")
	}
	if c.Current != "" || c.Why != "" || c.Next != "" {
		t.Error("present-state sections must still be dropped")
	}
}

// A coined compound whose parts are all in the source is generalisation, not fabrication.
// This was the dominant flagged class once the synopsis existed.
func TestCompoundsBuiltFromSourceWordsAreVerified(t *testing.T) {
	src := "we need this to be efficient on CPU and to use OAuth for the Signal to Atlas hop"
	d := Digest{Synopsis: "A CPU-efficient, OAuth-first design for the Signal-to-Atlas path."}
	if bad := UnverifiedIdentifiers(d, src); len(bad) > 0 {
		t.Errorf("compounds built from source vocabulary were flagged: %v", bad)
	}
	// A compound with a part the source never mentions is still caught — but only where the
	// token reaches the gate at all. "Kubernetes-first" would not: a leading capital is not
	// INTERNAL caps, so it is a weak token, and weak tokens are excluded when hyphenated.
	// The rule therefore bites on compounds carrying an all-caps part, like this one.
	d2 := Digest{Synopsis: "An AWS-first rollout."}
	if bad := UnverifiedIdentifiers(d2, src); len(bad) == 0 {
		t.Error("a compound with an ungrounded part must still be flagged")
	}
}

// The friction vocabulary must be stems: the noun "reversal" is precisely what the list
// exists to find, and spelling out "reverse"/"reversed" missed it.
func TestFrictionVocabularyCatchesReversal(t *testing.T) {
	f := DigestFacts{Corrections: 1}
	d := Digest{Happened: "A reversal in interpretation followed the user's correction."}
	if LooksRubberstamped(d, f) {
		t.Error("a report naming a reversal was scored as rubberstamped")
	}
}
