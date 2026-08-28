package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureRound emits a round from the fixture corpus with a fixture calibration set, so the
// emission path is exercised without the owner's untracked document. The real calibration set
// (Mutations, CleanDuplicates) is validated against the real corpus by TestRealCorpus… which
// skips when the document is absent.
func fixtureRound(t *testing.T) (Emission, Corpus) {
	t.Helper()
	c := parseFixture(t)
	saveM, saveD := Mutations, CleanDuplicates
	Mutations = []Mutation{
		{
			ID: "f01", Class: FabricatedIdentifier,
			Session: "First session", Ordinal: 1,
			Original: "widget.go", Replacement: "sprocket.go",
			Absent: []string{"sprocket.go"},
			Note:   "widget.go is what the window names",
		},
		{
			ID: "f02", Class: SubjectDrift,
			Session: "First session", Ordinal: 2,
			Original: "gadget.go", Replacement: "the ledger.csv close",
			Absent: []string{"ledger.csv"}, DrawnFrom: "Second session",
			Note: "attributes the work to the accounting session's subject",
		},
	}
	CleanDuplicates = []struct {
		Session string
		Ordinal int
	}{{"Second session", 1}}
	t.Cleanup(func() { Mutations, CleanDuplicates = saveM, saveD })

	em, err := Emit(t.TempDir(), "fixture", c, "fixture-corpus.md", "deadbeef", 1)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	return em, c
}

func TestEmitWritesOnePacketPerItemPlusPlantedAndDuplicates(t *testing.T) {
	em, c := fixtureRound(t)
	want := len(c.Items()) + len(Mutations) + len(CleanDuplicates)
	if em.Manifest.Count != want {
		t.Fatalf("packets = %d, want %d", em.Manifest.Count, want)
	}
	if em.Key.Counts.Genuine != 3 || em.Key.Counts.Planted != 2 || em.Key.Counts.CleanDuplicates != 1 {
		t.Errorf("counts = %+v", em.Key.Counts)
	}
	files, err := filepath.Glob(filepath.Join(em.PacketsDir, "PKT-*.md"))
	if err != nil || len(files) != want {
		t.Fatalf("packet files = %d (%v), want %d", len(files), err, want)
	}
	// The manifest is in id order and the ids are hashes, so the listing order carries no
	// information about kind. Assert the order is sorted and that kinds are interleaved
	// somewhere other than one block at the end.
	for i := 1; i < len(em.Manifest.Packets); i++ {
		if em.Manifest.Packets[i-1].ID >= em.Manifest.Packets[i].ID {
			t.Fatalf("manifest is not in id order at %d", i)
		}
	}
	if len(em.Key.Entries) != want {
		t.Fatalf("key entries = %d, want %d", len(em.Key.Entries), want)
	}
	for i, e := range em.Key.Entries {
		if e.PacketID != em.Manifest.Packets[i].ID {
			t.Fatalf("key entry %d is %s but manifest row %d is %s — the key and the manifest are not aligned",
				i, e.PacketID, i, em.Manifest.Packets[i].ID)
		}
	}
}

// The packet is the whole contract with a reviewer: evidence and statement, nothing else.
// Asserted STRUCTURALLY — the file on disk must equal a render over exactly those three
// strings — so no field added later can reach a packet whatever a substring search looks for.
// The substring scan below it is the second, weaker check, and it is there because the source
// document's own headings ("Beat 5 (window 25 of 72) · marked SUBJECT CHANGED") are the
// provenance most likely to be copied through by accident.
func TestAPacketIsExactlyTheEvidenceAndTheStatement(t *testing.T) {
	em, c := fixtureRound(t)
	if len(em.Leak.Structural) != 0 {
		t.Fatalf("structural mismatches: %v", em.Leak.Structural)
	}
	byID := map[string]KeyEntry{}
	for _, e := range em.Key.Entries {
		byID[e.PacketID] = e
	}
	for _, mp := range em.Manifest.Packets {
		raw, err := os.ReadFile(filepath.Join(em.PacketsDir, mp.File))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		e := byID[mp.ID]
		src, err := c.Find(e.SourceSession, e.SourceOrdinal)
		if err != nil {
			t.Fatal(err)
		}
		statement := src.Output
		if e.Kind == KindPlanted {
			for _, m := range Mutations {
				if m.ID == e.MutationID {
					p, err := Apply(c, m)
					if err != nil {
						t.Fatal(err)
					}
					statement = p.Item.Output
				}
			}
		}
		want := renderPacket(Packet{ID: mp.ID, Record: src.Record, Window: src.Window, Output: statement})
		if body != want {
			t.Errorf("%s is not a render over its evidence and statement alone", mp.File)
		}
		for _, forbidden := range []string{
			"Beat ", "window 0 of", "SUBJECT CHANGED", "clean_duplicate", "mutation",
			"First session", "Second session", string(e.MutationClass),
		} {
			if forbidden == "" {
				continue
			}
			if strings.Contains(body, forbidden) {
				t.Errorf("%s leaks %q", mp.File, forbidden)
			}
		}
		if !strings.HasPrefix(body, "# Review packet "+mp.ID+"\n") {
			t.Errorf("%s does not open with its own id", mp.File)
		}
	}
}

func TestEmittedPacketsAreGreppedForEveryAnswerKeyValue(t *testing.T) {
	em, _ := fixtureRound(t)
	if len(em.Leak.Hits) != 0 {
		t.Fatalf("leak hits: %v", em.Leak.Hits)
	}
	// The check has to be looking for something. An empty checked set would make "no hits" a
	// tautology, which is how a metric ends up measuring the format.
	if len(em.Leak.Checked) < 5 {
		t.Fatalf("only %d values checked: %v", len(em.Leak.Checked), em.Leak.Checked)
	}
	var sawSession, sawClass, sawKind bool
	for _, v := range em.Leak.Checked {
		switch v {
		case "First session":
			sawSession = true
		case string(SubjectDrift):
			sawClass = true
		case string(KindPlanted):
			sawKind = true
		}
	}
	if !sawSession || !sawClass || !sawKind {
		t.Errorf("the grep set is missing a provenance field: session=%v class=%v kind=%v", sawSession, sawClass, sawKind)
	}
	// And every exclusion carries a reason, so nothing is quietly dropped.
	for _, ex := range em.Leak.Excluded {
		if strings.TrimSpace(ex.Reason) == "" {
			t.Errorf("excluded %q with no reason", ex.Value)
		}
	}
	// The withheld directory sits outside the packets directory.
	if strings.HasPrefix(em.KeyPath, em.PacketsDir) {
		t.Errorf("the answer key is inside the packets directory: %s", em.KeyPath)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(em.KeyPath), "README.md")); err != nil {
		t.Errorf("withheld README missing: %v", err)
	}
}

// A leak must actually be detectable, or "no hits" says nothing. This plants the session title
// into a packet after emission and re-runs the check.
func TestTheLeakCheckFindsALeakWhenThereIsOne(t *testing.T) {
	em, _ := fixtureRound(t)
	victim := filepath.Join(em.PacketsDir, em.Manifest.Packets[0].File)
	body, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, append(body, []byte("\n\n(source: First session)\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	bodies := map[string]Packet{}
	for _, e := range em.Key.Entries {
		p, err := packetOf(em, e.PacketID)
		if err != nil {
			t.Fatal(err)
		}
		bodies[e.PacketID] = p
	}
	rep, err := checkNoLeak(em.PacketsDir, em.Key, bodies)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Hits) == 0 {
		t.Fatal("the leak check passed a packet naming its source session")
	}
	if len(rep.Structural) == 0 {
		t.Error("the structural check passed a packet with a line appended to it")
	}
}

// packetOf recovers what a packet SHOULD contain, from the file as emitted before tampering.
func packetOf(em Emission, id string) (Packet, error) {
	for _, e := range em.Key.Entries {
		if e.PacketID != id {
			continue
		}
		c, _, err := ParseCorpus(fixtureDoc)
		if err != nil {
			return Packet{}, err
		}
		src, err := c.Find(e.SourceSession, e.SourceOrdinal)
		if err != nil {
			return Packet{}, err
		}
		out := src.Output
		if e.Kind == KindPlanted {
			for _, m := range Mutations {
				if m.ID == e.MutationID {
					p, err := Apply(c, m)
					if err != nil {
						return Packet{}, err
					}
					out = p.Item.Output
				}
			}
		}
		return Packet{ID: id, Record: src.Record, Window: src.Window, Output: out}, nil
	}
	return Packet{}, fmt.Errorf("no packet %s", id)
}

func TestPlantedEntriesRecordTheExactSpanAndTheDuplicatePointsAtItsTwin(t *testing.T) {
	em, _ := fixtureRound(t)
	var planted, dup int
	for _, e := range em.Key.Entries {
		switch e.Kind {
		case KindPlanted:
			planted++
			if e.MutatedSpan == "" || e.ReplacedSpan == "" || len(e.Signature) == 0 {
				t.Errorf("%s: planted entry without a recorded span or signature: %+v", e.PacketID, e)
			}
			if len(e.SpanRunes) != 2 || e.SpanRunes[1] <= e.SpanRunes[0] {
				t.Errorf("%s: empty span %v", e.PacketID, e.SpanRunes)
			}
			if e.MutationClass == "" || e.MutationID == "" || e.MutationNote == "" {
				t.Errorf("%s: planted entry missing its class, id or note", e.PacketID)
			}
		case KindCleanDuplicate:
			dup++
			if e.DuplicateOf == "" {
				t.Errorf("%s: clean duplicate does not name its twin", e.PacketID)
			}
			if e.MutationClass != "" {
				t.Errorf("%s: a clean duplicate carries a mutation class", e.PacketID)
			}
		}
	}
	if planted != 2 || dup != 1 {
		t.Fatalf("planted=%d dup=%d", planted, dup)
	}
}

// A clean duplicate has to be indistinguishable from its twin, or it measures the packaging
// rather than the reviewer.
func TestACleanDuplicateIsByteIdenticalToItsTwinApartFromTheID(t *testing.T) {
	em, _ := fixtureRound(t)
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
		got := strings.Replace(file[e.PacketID], e.PacketID, e.DuplicateOf, 1)
		if got != file[e.DuplicateOf] {
			t.Errorf("%s is not identical to its twin %s", e.PacketID, e.DuplicateOf)
		}
	}
	if found == 0 {
		t.Fatal("no clean duplicate in the round")
	}
}

func TestEmitIsDeterministic(t *testing.T) {
	em1, c := fixtureRound(t)
	em2, err := Emit(t.TempDir(), "fixture", c, "fixture-corpus.md", "deadbeef", 1)
	if err != nil {
		t.Fatalf("second Emit: %v", err)
	}
	a, _ := json.Marshal(em1.Manifest)
	b, _ := json.Marshal(em2.Manifest)
	if string(a) != string(b) {
		t.Fatalf("two emissions of the same corpus differ:\n%s\n%s", a, b)
	}
}

func TestTheDispatchPlanCoversEveryPacketTwice(t *testing.T) {
	em, _ := fixtureRound(t)
	b, err := os.ReadFile(filepath.Join(em.Dir, "dispatch-plan.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	seen := map[string]int{}
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
	if !strings.Contains(string(b), "verdicts/") {
		t.Error("the plan does not name where verdicts go")
	}
}

func TestTheEmittedRoundCarriesTheReviewerPrompt(t *testing.T) {
	em, _ := fixtureRound(t)
	b, err := os.ReadFile(filepath.Join(em.Dir, "reviewer-dispatch.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != DispatchPrompt() {
		t.Error("the emitted prompt is not the embedded one")
	}
	for _, want := range []string{"{{PACKET_FILE}}", "{{REVIEWER}}", "{{VERDICT_FILE}}", "\"faithful\"", "absent"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the prompt is missing %q", want)
		}
	}
}
