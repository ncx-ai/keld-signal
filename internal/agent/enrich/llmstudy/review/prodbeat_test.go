package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// prodFixtureDoc is a miniature of the production-beat document. It carries every shape that breaks
// a naive parser and every shape the round has to handle:
//
//   - a conversation window whose verbatim content contains markdown headings and a "---" rule;
//   - a beat whose output carries a DROP MARKER, which is the anchoring guard's verdict on it and
//     must not reach a packet;
//   - the prose the harness prints under an output (what each entry was checked on), which is
//     annotation and not statement;
//   - two GENERATION FAILED blocks with different rules, one of them the subject-anchoring loss the
//     round must carry as an absence;
//   - a hand-authored session labelled SYNTHETIC;
//   - the "What was generated" tally the parser cross-checks itself against.
const prodFixtureDoc = "# Beat generation: inputs and outputs\n\nBlurb.\n\n---\n\n" +
	"# alpha.jsonl\n\n*real transcript (engineering) · 8 mined windows*\n\n" +
	"Transcript: `/tmp/alpha.jsonl` · project `alpha` · walked to window 8\n\n---\n\n" +
	"## Beat 1 (window 4 of 8) · 1 attempt(s)\n\n" +
	"Window geometry: 13 turns in the stride since the previous beat.\n\n" +
	"### Input 1 — measured record (counted on device — authoritative)\n\n" +
	"```\ncounts: turns=1 user_turns=1 tool_calls=0 corrections=0\nprojects: alpha\nrecurring subjects: widget.go\n```\n\n" +
	"### Input 2 — conversation window (40 runes — evidence)\n\n" +
	"```\nuser: fix widget.go please\n\n## A heading inside the window\n\n---\n\nassistant: done, and the retry loop is gone\n```\n\n" +
	"### Output\n\n> the widget.go fix\n> - widget.go was edited to remove the retry loop\n> - the change was confirmed done in the same turn\n\n" +
	"What each kept entry was checked on: `widget.go`, `(names nothing checkable)`.\n\n---\n\n" +
	"## Beat at window 9 — GENERATION FAILED after 5 attempt(s)\n\n" +
	"```\nretry: gave up after 5 attempt(s): invalid beat: the subject line carries no term occurring in the window or the record: \"the daily-jar rewrite\"\n```\n\n---\n\n" +
	"## Beat 2 (window 14 of 8) · 2 attempt(s)\n\n" +
	"### Input 1 — measured record (counted on device — authoritative)\n\n" +
	"```\ncounts: turns=9 user_turns=3 tool_calls=4 corrections=1\nprojects: alpha\nrecurring subjects: widget.go, gadget.go, alpha/cmd\n```\n\n" +
	"### Input 2 — conversation window (60 runes — evidence)\n\n" +
	"```\nuser: now gadget.go\nassistant: gadget.go updated and building\n```\n\n" +
	"### Output\n\n> the gadget.go update\n> - gadget.go was updated and left building\n> - the alpha/cmd entry point was read for context\n" +
	"> [1 entry dropped: it names something that occurs nowhere in this window or the record]\n\n" +
	"**Dropped by the anchoring guard (1):** shown so the decision can be checked.\n\n---\n\n" +
	"# books-close\n\n*SYNTHETIC — hand-authored non-engineering · 4 mined windows*\n\n---\n\n" +
	"## Beat 1 (window 4 of 4) · 1 attempt(s)\n\n" +
	"### Input 1 — measured record (counted on device — authoritative)\n\n" +
	"```\ncounts: turns=2 user_turns=1 tool_calls=0 corrections=0\nprojects: books\nrecurring subjects: ledger.csv\n```\n\n" +
	"### Input 2 — conversation window (30 runes — evidence)\n\n" +
	"```\nuser: close out the ledger from ledger.csv\n```\n\n" +
	"### Output\n\n> the period close on ledger.csv\n> - the ledger for the period was asked to be closed out\n> - ledger.csv was named as the source\n\n---\n\n" +
	"## Beat at window 9 — GENERATION FAILED after 5 attempt(s)\n\n" +
	"```\nretry: gave up after 5 attempt(s): invalid beat: entry \"…\" is 235 runes, over the cap of 200\n```\n\n---\n\n" +
	"# What was generated\n\n" +
	"- sessions 2; beats asked 5, generated 3, failed 2, recovered panics 0\n" +
	"- entries dropped by the anchoring guard (a specific occurring in neither the window nor the record): 1 of 7 offered, across 1 of 3 beats\n" +
	"- kept entries naming NO specific, so unconstrained by the guard: 2 of 6 kept\n" +
	"- beats re-requested for an unanchored SUBJECT: 1 beats, 5 attempts; still failing after the ladder: 1; stored beats whose subject carries no term from the evidence: 0 of 3\n"

func parseProdFixture(t *testing.T) ProdCorpus {
	t.Helper()
	p, err := ParseProdCorpus(prodFixtureDoc)
	if err != nil {
		t.Fatalf("ParseProdCorpus: %v", err)
	}
	return p
}

func TestProdParserReadsSubjectEventsPopulationsAndFailures(t *testing.T) {
	p := parseProdFixture(t)
	if len(p.Corpus.Sessions) != 2 {
		t.Fatalf("sessions = %d, want 2", len(p.Corpus.Sessions))
	}
	if p.Population["alpha.jsonl"] != PopulationReal || p.Population["books-close"] != PopulationSynthetic {
		t.Errorf("populations = %v", p.Population)
	}
	if got := len(p.Corpus.Items()); got != 3 {
		t.Fatalf("beats = %d, want 3", got)
	}
	one := p.Corpus.Sessions[0].Items[0]
	// The statement is the subject line and the entries, joined on NEWLINES. Joining on spaces —
	// which is right for r1's prose — would flatten a list into one unreadable line.
	want := "the widget.go fix\n- widget.go was edited to remove the retry loop\n- the change was confirmed done in the same turn"
	if one.Output != want {
		t.Errorf("output = %q\nwant %q", one.Output, want)
	}
	if ProdSubject(one.Output) != "the widget.go fix" {
		t.Errorf("subject = %q", ProdSubject(one.Output))
	}
	if got := ProdEvents(one.Output); len(got) != 2 || got[0] != "widget.go was edited to remove the retry loop" {
		t.Errorf("events = %v", got)
	}
	if !strings.Contains(one.Window, "## A heading inside the window") {
		t.Error("the window's own markdown heading split the beat")
	}
	// The harness's annotation under an output is not part of the statement.
	if strings.Contains(one.Output, "checked on") {
		t.Errorf("the checked-on annotation reached the statement: %q", one.Output)
	}

	two := p.Corpus.Sessions[0].Items[1]
	if len(two.DroppedEntries) != 1 {
		t.Fatalf("dropped entries = %v, want the one drop marker", two.DroppedEntries)
	}
	if strings.Contains(two.Output, "dropped") {
		t.Errorf("the anchoring guard's drop marker reached the statement: %q", two.Output)
	}

	if len(p.Failures) != 2 {
		t.Fatalf("failures = %d, want 2", len(p.Failures))
	}
	if p.Failures[0].Rule != FailureSubjectUnanchored || p.Failures[0].Attempts != 5 || p.Failures[0].WindowIndex != 9 {
		t.Errorf("failure 0 = %+v", p.Failures[0])
	}
	if p.Failures[0].Session != "alpha.jsonl" || p.Failures[0].Population != PopulationReal {
		t.Errorf("failure 0 is attributed to %q/%q", p.Failures[0].Session, p.Failures[0].Population)
	}
	if p.Failures[1].Rule != FailureEntryCap {
		t.Errorf("failure 1 rule = %q", p.Failures[1].Rule)
	}
	if p.Counts.UnconstrainedEntries != 2 || p.Counts.KeptEntries != 6 || p.Counts.SubjectLadderLosses != 1 {
		t.Errorf("tally = %+v", p.Counts)
	}
}

// The parser cross-checks itself against the document's own tally, because a parse that silently
// disagrees with the artifact it reads is a measurement of neither.
func TestProdParserRefusesADocumentThatDisagreesWithItsOwnTally(t *testing.T) {
	doc := strings.Replace(prodFixtureDoc, "generated 3, failed 2", "generated 9, failed 2", 1)
	if _, err := ParseProdCorpus(doc); err == nil {
		t.Fatal("a document claiming 9 generated beats and carrying 3 was accepted")
	}
	doc = strings.Replace(prodFixtureDoc, "still failing after the ladder: 1", "still failing after the ladder: 4", 1)
	if _, err := ParseProdCorpus(doc); err == nil {
		t.Fatal("a document claiming 4 subject-anchoring losses and carrying 1 was accepted")
	}
	doc = strings.Replace(prodFixtureDoc, "- kept entries naming NO specific, so unconstrained by the guard: 2 of 6 kept\n", "", 1)
	if _, err := ParseProdCorpus(doc); err == nil {
		t.Fatal("a document with no guard-reach line was accepted, so the caveat could go missing in silence")
	}
}

// prodFixtureRound emits a round from the fixture with a fixture calibration set, so the emission
// path is exercised without the real document. The real calibration set is validated against the
// real corpus by TestTheProdCalibrationSetAppliesToTheRealCorpus.
func prodFixtureRound(t *testing.T) (ProdEmission, ProdCorpus) {
	t.Helper()
	p := parseProdFixture(t)
	saveM, saveD, saveR, saveS := ProdMutations, ProdCleanDuplicates, ProdGenuineReal, ProdGenuineSynthetic
	ProdMutations = []Mutation{
		{
			ID: "x01", Class: FabricatedIdentifier,
			Session: "alpha.jsonl", Ordinal: 2,
			Original: "gadget.go was updated", Replacement: "sprocket.go was updated",
			Absent: []string{"sprocket.go"},
			Note:   "gadget.go is what the window names",
		},
	}
	ProdCleanDuplicates = []struct {
		Session string
		Ordinal int
	}{{"books-close", 1}}
	ProdGenuineReal, ProdGenuineSynthetic = 1, 1
	t.Cleanup(func() {
		ProdMutations, ProdCleanDuplicates = saveM, saveD
		ProdGenuineReal, ProdGenuineSynthetic = saveR, saveS
	})

	em, err := EmitProd(t.TempDir(), "fixture", p, "fixture-prod.md", "deadbeef")
	if err != nil {
		t.Fatalf("EmitProd: %v", err)
	}
	return em, p
}

// The packet is the whole contract with a reviewer: evidence and statement, nothing else. Asserted
// STRUCTURALLY — the file must equal a render over exactly those three strings — so no field added
// later can reach a packet whatever a substring search looks for.
func TestAProdPacketIsExactlyTheEvidenceAndTheStatement(t *testing.T) {
	em, _ := prodFixtureRound(t)
	if len(em.Leak.Structural) != 0 {
		t.Fatalf("structural mismatches: %v", em.Leak.Structural)
	}
	if len(em.Leak.Hits) != 0 {
		t.Fatalf("leak hits: %v", em.Leak.Hits)
	}
	if len(em.Leak.Checked) < 5 {
		t.Fatalf("only %d values checked: %v — an empty grep set makes 'no hits' a tautology", len(em.Leak.Checked), em.Leak.Checked)
	}
	for _, mp := range em.Manifest.Packets {
		raw, err := os.ReadFile(filepath.Join(em.PacketsDir, mp.File))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		for _, forbidden := range []string{
			"Beat ", "window 4 of", "attempt(s)", "entry dropped", "SYNTHETIC", "synthetic",
			"clean_duplicate", "planted", "alpha.jsonl", "books-close", "GENERATION FAILED",
			"anchoring guard", "checked on",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s leaks %q", mp.File, forbidden)
			}
		}
		if !strings.HasPrefix(body, "# Review packet "+mp.ID+"\n") {
			t.Errorf("%s does not open with its own id", mp.File)
		}
		// It must still say how to read a subject-plus-entries statement, or the reviewer is
		// guessing at the format.
		if !strings.Contains(body, "names the subject of the work") {
			t.Errorf("%s does not orient the reviewer to the statement's layout", mp.File)
		}
	}
}

// A packet must round-trip through the SAME parser r1's packets use, because the scorer's
// "the quote is in the EVIDENCE, not the statement" check is that parser.
func TestAProdPacketParsesBackToItsThreeParts(t *testing.T) {
	em, _ := prodFixtureRound(t)
	raw, err := os.ReadFile(filepath.Join(em.PacketsDir, em.Manifest.Packets[0].File))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePacket(string(raw))
	if err != nil {
		t.Fatalf("ParsePacket: %v", err)
	}
	if p.ID != em.Manifest.Packets[0].ID || p.Record == "" || p.Window == "" || p.Output == "" {
		t.Fatalf("round trip lost a part: %+v", p)
	}
	if !strings.Contains(p.Output, "\n- ") {
		t.Errorf("the statement lost its entry markers on the round trip: %q", p.Output)
	}
}

// A clean duplicate has to be indistinguishable from its twin, or it measures the packaging rather
// than the reviewer.
func TestAProdCleanDuplicateIsByteIdenticalToItsTwinApartFromTheID(t *testing.T) {
	em, _ := prodFixtureRound(t)
	file := map[string]string{}
	for _, p := range em.Manifest.Packets {
		b, err := os.ReadFile(filepath.Join(em.PacketsDir, p.File))
		if err != nil {
			t.Fatal(err)
		}
		file[p.ID] = string(b)
	}
	found := 0
	for _, e := range em.Key.Entries {
		if e.Kind != KindCleanDuplicate {
			continue
		}
		found++
		if e.DuplicateOf == "" {
			t.Fatalf("%s names no twin", e.PacketID)
		}
		got := strings.Replace(file[e.PacketID], e.PacketID, e.DuplicateOf, 1)
		if got != file[e.DuplicateOf] {
			t.Errorf("%s is not identical to its twin %s", e.PacketID, e.DuplicateOf)
		}
	}
	if found == 0 {
		t.Fatal("no clean duplicate in the round")
	}
}

// Emission is deterministic: a round that cannot be regenerated identically cannot be re-scored,
// and re-scoring is how a disputed verdict gets settled.
func TestEmitProdIsDeterministic(t *testing.T) {
	em1, p := prodFixtureRound(t)
	em2, err := EmitProd(t.TempDir(), "fixture", p, "fixture-prod.md", "deadbeef")
	if err != nil {
		t.Fatalf("second EmitProd: %v", err)
	}
	a, _ := json.Marshal(em1.Manifest)
	b, _ := json.Marshal(em2.Manifest)
	if string(a) != string(b) {
		t.Fatalf("two emissions of the same corpus differ:\n%s\n%s", a, b)
	}
}

// A leak must actually be detectable, or "no hits" says nothing.
func TestTheProdLeakCheckFindsALeakWhenThereIsOne(t *testing.T) {
	em, _ := prodFixtureRound(t)
	victim := filepath.Join(em.PacketsDir, em.Manifest.Packets[0].File)
	body, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, append(body, []byte("\n\n(source: alpha.jsonl)\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	bodies := map[string]Packet{}
	for _, e := range em.Key.Entries {
		raw, err := os.ReadFile(filepath.Join(em.PacketsDir, e.PacketID+".md"))
		if err != nil {
			t.Fatal(err)
		}
		p, err := ParsePacket(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		bodies[e.PacketID] = p
	}
	// The tampered packet's parsed content still equals what it should be apart from the appended
	// line, so the structural check is what has to catch this one.
	rep, err := checkNoProdLeak(em.PacketsDir, em.Key, bodies)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Hits) == 0 {
		t.Error("the leak check passed a packet naming its source session")
	}
	if len(rep.Structural) == 0 {
		t.Error("the structural check passed a packet with a line appended to it")
	}
}

// The round's dispatch plan is the coordinator's whole worklist.
func TestTheProdDispatchPlanCoversEveryPacketTwice(t *testing.T) {
	em, _ := prodFixtureRound(t)
	b, err := os.ReadFile(filepath.Join(em.Dir, "dispatch-plan.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	rows, seen := 0, map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) != 4 {
			t.Fatalf("row %q has %d columns", line, len(cols))
		}
		seen[cols[0]]++
		rows++
	}
	if rows != em.Manifest.Count*ReviewersPerPacket {
		t.Errorf("plan rows = %d, want %d", rows, em.Manifest.Count*ReviewersPerPacket)
	}
	for id, n := range seen {
		if n != ReviewersPerPacket {
			t.Errorf("%s dispatched %d times, want %d", id, n, ReviewersPerPacket)
		}
	}
}

// The round README is where the "few sources" caveat and the two absences have to be readable
// before anybody dispatches anything.
func TestTheProdRoundReadmeCarriesTheSourceCountAndBothAbsences(t *testing.T) {
	em, _ := prodFixtureRound(t)
	b, err := os.ReadFile(filepath.Join(em.Dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{
		"distinct real conversations",
		"name nothing checkable",
		"produced no beat at all",
		"REVIEW_R1_SCORE",
		"USE ABSOLUTE PATHS",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the round README does not carry %q", want)
		}
	}
	// It must NOT carry the composition: how many packets are planted is a base rate, and a
	// reviewer who wandered in and read it would review differently.
	for _, forbidden := range []string{"planted", "clean duplicate", "genuine"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Errorf("the round README leaks the composition (%q)", forbidden)
		}
	}
}

// The reviewer prompt is the rubric. If it drifts from r1's, the comparison this round exists to
// make is between two different measurements.
func TestTheProdReviewerPromptCarriesR1sRubricVerbatim(t *testing.T) {
	prod, r1 := ProdDispatchPrompt(), DispatchPrompt()
	for _, dim := range Dimensions {
		if !strings.Contains(prod, "`"+dim+"`") {
			t.Errorf("the prompt does not name dimension %s", dim)
		}
	}
	// The wording of every dimension, and of the evidence requirement, is r1's. These are the
	// sentences that decide what a verdict means.
	for _, sentence := range []string{
		"does the statement assert progress, quality or completion that",
		"could a non-technical org administrator read this and say what",
		"would the person doing this work accept it as an",
		"is the statement specific in the terms *this* work uses?",
		"is every claim in the statement traceable to the window or the record?",
		"a span copied **verbatim** from Evidence 1 or Evidence 2 in the packet",
		"a list of strings you claim appear **nowhere** in Evidence 1 or Evidence 2",
	} {
		if !strings.Contains(r1, sentence) {
			t.Fatalf("the r1 prompt no longer contains %q — this test is comparing against the wrong text", sentence)
		}
		if !strings.Contains(prod, sentence) {
			t.Errorf("the production prompt has diverged from r1's rubric at %q", sentence)
		}
	}
	for _, class := range MutationClasses {
		if !strings.Contains(prod, "`"+string(class)+"`") {
			t.Errorf("the prompt does not name defect class %s", class)
		}
	}
	// And it must not tell the reviewer what produced the statement, or that anything else did.
	for _, forbidden := range []string{"production", "redesign", "the old ", "previous design", "fused"} {
		if strings.Contains(strings.ToLower(afterRule(prod)), forbidden) {
			t.Errorf("the dispatched half of the prompt tells the reviewer about the design (%q)", forbidden)
		}
	}
}

// afterRule is the half of the prompt that is actually pasted into a reviewer: everything after the
// coordinator's instructions and the "---" rule.
func afterRule(s string) string {
	if i := strings.Index(s, "\n---\n"); i >= 0 {
		return s[i:]
	}
	return s
}
