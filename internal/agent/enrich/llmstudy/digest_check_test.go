package llmstudy

import "testing"

// The identifier gate bounds fabricated SPECIFICS. It cannot bound fabricated
// judgements, which is why the spec keeps a human review gate.
func TestUnverifiedIdentifiersFlagsInventedSpecifics(t *testing.T) {
	src := "user: reconcile the March ledger for Northwind\nassistant: opened ledger-2026-03.csv\n"
	d := Digest{
		Done:     "Reconciled the March ledger for Northwind using ledger-2026-03.csv.",
		Happened: "Also cross-checked against Globex and invoice-99812.pdf.",
	}
	found := map[string]bool{}
	for _, b := range UnverifiedIdentifiers(d, src) {
		found[b] = true
	}
	if !found["Globex"] || !found["invoice-99812.pdf"] {
		t.Errorf("invented specifics not flagged: %v", UnverifiedIdentifiers(d, src))
	}
	if found["Northwind"] || found["ledger-2026-03.csv"] {
		t.Errorf("real specifics wrongly flagged: %v", UnverifiedIdentifiers(d, src))
	}
}

// Rubberstamping: corrections occurred, yet the report names no friction anywhere.
func TestLooksRubberstampedWhenFrictionIsOmitted(t *testing.T) {
	facts := DigestFacts{Turns: 20, UserTurns: 8, Corrections: 3, CorrectedTurns: 2}
	glowing := Digest{
		Done: "Everything was completed smoothly.", Happened: "All steps succeeded first time.",
		Structure: "Two parts, both in place.", Unresolved: []string{"nothing is open"},
	}
	if !LooksRubberstamped(glowing, facts) {
		t.Error("a glowing report with 3 corrections must be flagged")
	}
	honest := Digest{
		Done: "Completed the reconciliation.", Happened: "Two attempts were reversed after totals disagreed.",
		Structure: "Two parts.", Unresolved: []string{"April still to do"},
	}
	if LooksRubberstamped(honest, facts) {
		t.Error("a report naming the friction must not be flagged")
	}
}

// With no corrections there is nothing to under-report, so nothing is flagged.
func TestLooksRubberstampedIsSilentWithoutCorrections(t *testing.T) {
	d := Digest{Done: "Done.", Happened: "Went fine.", Unresolved: []string{"nothing open"}}
	if LooksRubberstamped(d, DigestFacts{Turns: 6, UserTurns: 2}) {
		t.Error("no corrections means no rubberstamping to detect")
	}
}

func TestRetainedFactsCountsSurvivors(t *testing.T) {
	after := Digest{Done: "contacted Northwind about the totals"}
	if got := RetainedFacts(after, []string{"ledger-2026-03.csv", "Northwind"}); got != 1 {
		t.Fatalf("RetainedFacts = %d, want 1 (Northwind survived, the file did not)", got)
	}
}

// Ordinary English must not be mistaken for invented proper nouns, or the gate's
// signal is drowned by false positives.
func TestIdentifiersIgnoreSentenceStarters(t *testing.T) {
	d := Digest{Done: "The work is done. However, there were issues. Also nothing is open."}
	for _, id := range Identifiers(d) {
		if id == "The" || id == "However" || id == "Also" || id == "Nothing" {
			t.Errorf("stop word %q treated as an identifier", id)
		}
	}
}

// The sentinel gives "nothing is open" a first-class expression so the required
// field can be honoured without inventing a blocker.
func TestUsesUnresolvedSentinel(t *testing.T) {
	if !UsesUnresolvedSentinel(Digest{Unresolved: []string{UnresolvedSentinel}}) {
		t.Error("the canonical sentinel must be recognised")
	}
	if !UsesUnresolvedSentinel(Digest{Unresolved: []string{"None - nothing remains"}}) {
		t.Error("recognition must be case-insensitive on the marker")
	}
	if UsesUnresolvedSentinel(Digest{Unresolved: []string{"needs further testing"}}) {
		t.Error("a real-looking item must not read as the sentinel")
	}
	if UsesUnresolvedSentinel(Digest{Unresolved: []string{UnresolvedSentinel, "and also X"}}) {
		t.Error("the sentinel must stand alone; mixing it with items is not 'nothing open'")
	}
}

// The exact observed failure: a session the source called "Fixed and verified live"
// produced speculative blockers that appeared nowhere in the conversation.
func TestLooksFabricatedUnresolvedCatchesTheObservedFailure(t *testing.T) {
	src := "assistant: Fixed and verified live. 9 pass. Committing to main.\nuser: commit to main\n"
	facts := DigestFacts{Turns: 13, UserTurns: 1} // no corrections
	fabricated := Digest{Unresolved: []string{
		"The resolver's behavior in different environments needs further testing.",
		"The exact impact of the 5-minute cache TTL is not fully understood.",
	}}
	if !LooksFabricatedUnresolved(fabricated, facts, src) {
		t.Error("speculative blockers on verified work must be flagged")
	}
	if LooksFabricatedUnresolved(Digest{Unresolved: []string{UnresolvedSentinel}}, facts, src) {
		t.Error("the sentinel must never be flagged as fabrication")
	}
}

// Where the work genuinely hit friction, open items are expected and must not be
// flagged — otherwise the metric punishes honest reporting.
func TestLooksFabricatedUnresolvedSilentWhenFrictionOccurred(t *testing.T) {
	src := "assistant: verified live\n"
	facts := DigestFacts{Turns: 13, UserTurns: 4, Corrections: 2}
	d := Digest{Unresolved: []string{"The retry path may need further testing."}}
	if LooksFabricatedUnresolved(d, facts, src) {
		t.Error("with real corrections, open items are expected")
	}
}

// And where the source never claims completion, tentative items are legitimate.
func TestLooksFabricatedUnresolvedSilentWhenSourceDoesNotClaimDone(t *testing.T) {
	src := "user: start looking into the resolver\nassistant: reading the config\n"
	d := Digest{Unresolved: []string{"The approach may need further testing."}}
	if LooksFabricatedUnresolved(d, DigestFacts{Turns: 4}, src) {
		t.Error("unfinished work may legitimately carry tentative open items")
	}
}

// Observed failure: an instruction example leaked verbatim into a report about an
// unrelated session, and the identifier gate could not see it because the leaked
// words were lowercase common nouns.
func TestLeakedPromptWordsCatchesInstructionBleed(t *testing.T) {
	src := "assistant: fixed the Manage column width\nuser: merge to main\n"
	d := Digest{Unresolved: []string{"The resolver was changed to follow redirects, but no further action is needed."}}
	got := LeakedPromptWords(d, src)
	if len(got) == 0 {
		t.Fatal("instruction vocabulary absent from the source must be flagged")
	}
	// And it must stay silent when the word is genuinely in the conversation.
	realSrc := "assistant: the resolver now follows redirects\n"
	if l := LeakedPromptWords(d, realSrc); len(l) != 0 {
		t.Errorf("must not flag words the source actually contains: %v", l)
	}
}
