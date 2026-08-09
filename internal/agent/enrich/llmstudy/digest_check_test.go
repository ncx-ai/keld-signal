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
