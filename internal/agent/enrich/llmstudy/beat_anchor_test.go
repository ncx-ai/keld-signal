package llmstudy

import (
	"strings"
	"testing"
)

// The guard's whole claim is that it measures a FACT. These are the facts it must get right, and
// the non-facts it must not start measuring.
func TestBeatAnchorIsOccurrenceNotJudgement(t *testing.T) {
	const evidence = "user: open fa-register.csv and check it against the depreciation schedule\n" +
		"assistant: I read app/main.py and reran the exporter for Meridian\n"
	cases := []struct {
		name  string
		entry string
		want  string // the anchoring term, or "" for unanchored
	}{
		{"path", "app/main.py was read end to end", "app/main.py"},
		{"dotted filename", "fa-register.csv was opened", "fa-register.csv"},
		{"case differs", "FA-Register.csv was opened", "FA-Register.csv"},
		{"trailing punctuation", "the exporter wrote app/main.py.", "app/main.py"},
		{"named in neither", "the Ganymede migration was signed off", ""},
		{"ordinary english only", "the work was reviewed and then continued", ""},
	}
	for _, c := range cases {
		got := beatAnchor(c.entry, evidence)
		if (got == "") != (c.want == "") || (c.want != "" && !strings.EqualFold(got, c.want)) {
			t.Errorf("%s: beatAnchor(%q) = %q, want %q", c.name, c.entry, got, c.want)
		}
	}
}

// Occurrence is a SUBSTRING test, which is the lenient direction and deliberately so: scoring a
// plural or a morphological variant as a fabrication is a mistake this study has already made.
func TestBeatAnchorAcceptsAMorphologicalVariant(t *testing.T) {
	const evidence = "user: rerun the exporter for fa-register.csv\n"
	if got := beatAnchor("the exporter was rerun against fa-register.csv", evidence); got == "" {
		t.Error("an entry naming a term the evidence contains was reported unanchored")
	}
}

// The term rule is distinctiveToken, so with a representative corpus table a rare ordinary word
// anchors and a common one does not — and neither decision is a judgement about the sentence.
func TestBeatAnchorTermsUseTheCorpusDistinctivenessGate(t *testing.T) {
	sessions := [][]string{{"depreciation", "control", "question"}}
	for i := 0; i < dfMinSessions; i++ {
		sessions = append(sessions, []string{"control", "question", "failure"})
	}
	restore := withDocFreq(newDocFreq(sessions))
	defer restore()

	terms := map[string]bool{}
	for _, t := range beatAnchorTerms("the depreciation question was raised under control") {
		terms[strings.ToLower(t)] = true
	}
	if !terms["depreciation"] {
		t.Errorf("the rare term must anchor, got %v", terms)
	}
	for _, common := range []string{"control", "question"} {
		if terms[common] {
			t.Errorf("%q appears in most sessions of the table and must not anchor", common)
		}
	}
	// Cold start (no corpus table) is the conservative direction: strong identifiers only.
	restore2 := withDocFreq(nil)
	defer restore2()
	if got := beatAnchorTerms("the depreciation question was raised"); len(got) != 0 {
		t.Errorf("with no corpus evidence only strong identifiers may anchor, got %v", got)
	}
	if got := beatAnchorTerms("fa-register.csv was opened"); len(got) != 1 {
		t.Errorf("a strong identifier must anchor without corpus evidence, got %v", got)
	}
}

// The split is per entry, and it reports which term each survivor was anchored by — a guard whose
// decision cannot be checked is a guard nobody can audit.
func TestAnchorBeatEventsSplitsPerEntryAndNamesTheAnchor(t *testing.T) {
	const evidence = "user: open fa-register.csv\nassistant: read app/main.py\n"
	kept, dropped, anchors := anchorBeatEvents([]string{
		"fa-register.csv was opened",
		"the Ganymede migration was signed off",
		"app/main.py was read",
	}, evidence)
	if len(kept) != 2 || len(dropped) != 1 || len(anchors) != 2 {
		t.Fatalf("kept %v dropped %v anchors %v", kept, dropped, anchors)
	}
	if anchors[0] != "fa-register.csv" || anchors[1] != "app/main.py" {
		t.Errorf("anchors do not name the terms the entries were kept on: %v", anchors)
	}
	if !strings.Contains(dropped[0], "Ganymede") {
		t.Errorf("the wrong entry was dropped: %v", dropped)
	}
}
