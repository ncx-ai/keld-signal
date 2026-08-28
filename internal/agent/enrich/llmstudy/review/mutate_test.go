package review

import (
	"strings"
	"testing"
)

func fixtureMutation(mod func(*Mutation)) Mutation {
	m := Mutation{
		ID: "t01", Class: FabricatedIdentifier,
		Session: "First session", Ordinal: 1,
		Original:    "widget.go",
		Replacement: "sprocket.go",
		Absent:      []string{"sprocket.go"},
		Note:        "the file in the window is widget.go",
	}
	if mod != nil {
		mod(&m)
	}
	return m
}

func TestApplyPlantsTheMutationAndLeavesTheEvidenceAlone(t *testing.T) {
	c := parseFixture(t)
	p, err := Apply(c, fixtureMutation(nil))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if p.Item.Output != "Fixing sprocket.go. It now compiles." {
		t.Errorf("mutated output = %q", p.Item.Output)
	}
	if p.SpanStart != 7 || p.SpanEnd != 18 {
		t.Errorf("span = %d..%d, want 7..18", p.SpanStart, p.SpanEnd)
	}
	if len(p.Signature) != 1 || p.Signature[0] != "sprocket.go" {
		t.Errorf("signature = %v, want [sprocket.go]", p.Signature)
	}
	src, _ := c.Find("First session", 1)
	if p.Item.Record != src.Record || p.Item.Window != src.Window {
		t.Error("a mutation must rewrite the statement only; the evidence moved")
	}
	if p.Source.Output != src.Output {
		t.Error("the genuine output was not preserved on the planted item")
	}
}

// The absence check is the load-bearing one. A "fabricated" identifier that is actually in the
// window is a clean item recorded in the key as planted, and it would score as a reviewer
// failure on every round forever — the exact shape of the defect this branch documents, where
// a test certified a property the data did not have.
func TestApplyRefusesAnAbsentTokenTheEvidenceActuallyContains(t *testing.T) {
	c := parseFixture(t)
	// widget.go is in beat 2's RECORD (recurring subjects) though not in its window, so this
	// also pins that the check reads both inputs and not only the window.
	m := fixtureMutation(func(m *Mutation) {
		m.Ordinal, m.Original, m.Replacement, m.Absent = 2, "gadget.go", "widget.go", []string{"widget.go"}
	})
	_, err := Apply(c, m)
	if err == nil {
		t.Fatal("a token present in the record was accepted as absent")
	}
	if !strings.Contains(err.Error(), "window or record") {
		t.Errorf("error does not say where the token was found: %v", err)
	}
}

func TestApplyRefusesAnAbsentTokenTheReplacementDoesNotIntroduce(t *testing.T) {
	c := parseFixture(t)
	_, err := Apply(c, fixtureMutation(func(m *Mutation) { m.Absent = []string{"flywheel.go"} }))
	if err == nil || !strings.Contains(err.Error(), "not in the replacement") {
		t.Fatalf("err = %v, want a complaint that the token is not introduced", err)
	}
}

func TestApplyRefusesASpanThatIsNotExactlyOneOccurrence(t *testing.T) {
	c := parseFixture(t)
	// "." appears three times in "Fixing widget.go. It now compiles." — a span like that makes "the
	// exact span mutated" untrue, and the scorer's located-the-defect test keys on it.
	_, err := Apply(c, fixtureMutation(func(m *Mutation) {
		m.Original, m.Replacement, m.Absent = ".", " on sprocket.go.", []string{"sprocket.go"}
	}))
	if err == nil || !strings.Contains(err.Error(), "occurs 3 times") {
		t.Fatalf("err = %v, want a complaint about 3 occurrences", err)
	}
	_, err = Apply(c, fixtureMutation(func(m *Mutation) { m.Original = "flywheel.go" }))
	if err == nil || !strings.Contains(err.Error(), "occurs 0 times") {
		t.Fatalf("err = %v, want a complaint about 0 occurrences", err)
	}
}

// A class whose defining property is not in the mutation is worse than no calibration item:
// it counts against the reviewer for missing something that was never planted.
func TestApplyRefusesAClassThatDoesNotDoWhatItsNameSays(t *testing.T) {
	c := parseFixture(t)
	blocker := fixtureMutation(func(m *Mutation) {
		m.Class, m.Absent = InventedBlocker, nil
		m.Original, m.Replacement = "It now compiles.", "It now compiles cleanly."
	})
	if _, err := Apply(c, blocker); err == nil || !strings.Contains(err.Error(), "obstacle") {
		t.Errorf("blocker without an obstacle: err = %v", err)
	}
	completion := blocker
	completion.Class = UnobservableCompletion
	if _, err := Apply(c, completion); err == nil || !strings.Contains(err.Error(), "assert progress") {
		t.Errorf("completion without a completion claim: err = %v", err)
	}
	drift := fixtureMutation(func(m *Mutation) {
		m.Class, m.Replacement, m.Absent = SubjectDrift, "ledger.csv", []string{"ledger.csv"}
	})
	if _, err := Apply(c, drift); err == nil || !strings.Contains(err.Error(), "name the session") {
		t.Errorf("drift with no source session: err = %v", err)
	}
	drift.DrawnFrom = "First session"
	if _, err := Apply(c, drift); err == nil || !strings.Contains(err.Error(), "DIFFERENT session") {
		t.Errorf("drift drawn from its own session: err = %v", err)
	}
	drift.DrawnFrom = "Second session"
	if _, err := Apply(c, drift); err != nil {
		t.Errorf("a real drift from the accounting session was refused: %v", err)
	}
	drift.Replacement, drift.Absent = "flywheel.go", []string{"flywheel.go"}
	if _, err := Apply(c, drift); err == nil || !strings.Contains(err.Error(), "sourceless specific") {
		t.Errorf("drift onto a subject no session has: err = %v", err)
	}
}

// Register and length are half of "indistinguishable from a genuine item". A planted item
// twice the length of everything around it is caught for its shape, and the round then
// measures nothing.
func TestApplyRefusesAMutationThatChangesTheRegister(t *testing.T) {
	c := parseFixture(t)
	long := "sprocket.go, and the assembly harness, and the calibration jig, and the ledger of every part it touches"
	_, err := Apply(c, fixtureMutation(func(m *Mutation) { m.Replacement = long }))
	if err == nil || !strings.Contains(err.Error(), "spotted for its length") {
		t.Fatalf("err = %v, want a length complaint", err)
	}
}

// The repository's rule: text read as language ends at a logical delimiter. A planted item
// that trails off mid-clause carries a second defect the packaging introduced.
func TestApplyRefusesAMutationLeftMidSentence(t *testing.T) {
	c := parseFixture(t)
	_, err := Apply(c, fixtureMutation(func(m *Mutation) {
		m.Class, m.Absent = InventedBlocker, nil
		m.Original, m.Replacement = "It now compiles.", "It cannot be built until"
	}))
	if err == nil || !strings.Contains(err.Error(), "sentence boundary") {
		t.Fatalf("err = %v, want a boundary complaint", err)
	}
}

func TestApplyRefusesAReplacementWithNothingQuotableInIt(t *testing.T) {
	c := parseFixture(t)
	// A three-rune substitute: absent from the evidence, so the class check passes, but the
	// signature rule keeps only words of four runes or more, so no reviewer could be credited
	// with quoting it. That is a real limit on the calibration set — a very short fabricated
	// identifier cannot be planted — and it is stated here rather than discovered later.
	_, err := Apply(c, fixtureMutation(func(m *Mutation) {
		m.Replacement, m.Absent = "cog", []string{"cog"}
	}))
	if err == nil || !strings.Contains(err.Error(), "no new word") {
		t.Fatalf("err = %v, want a signature complaint", err)
	}
}

func TestSignatureIsVocabularyNewToTheWholeStatement(t *testing.T) {
	// "store" is already in the statement, so a reviewer quoting it has not located anything;
	// only "stalled" is evidence that they read the planted clause.
	got := signatureOf("The sweep uses the store and is running.", "is now stalled waiting on the store")
	if len(got) != 2 || got[0] != "stalled" || got[1] != "waiting" {
		t.Fatalf("signature = %v, want [stalled waiting]", got)
	}
}

func TestApplyRejectsAnUnknownClassOrAMissingNote(t *testing.T) {
	c := parseFixture(t)
	if _, err := Apply(c, fixtureMutation(func(m *Mutation) { m.Class = "vibes" })); err == nil {
		t.Error("unknown class accepted")
	}
	if _, err := Apply(c, fixtureMutation(func(m *Mutation) { m.Note = "" })); err == nil {
		t.Error("mutation with no note accepted")
	}
}
