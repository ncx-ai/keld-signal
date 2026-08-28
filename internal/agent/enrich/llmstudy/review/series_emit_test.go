package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureSeriesRound emits a series round from the fixture corpus with a fixture calibration set, so
// the emission path is exercised without the owner's untracked document. The real set
// (SeriesMutations, SeriesCleanDuplicates) is validated against the real corpus by
// TestTheSeriesCalibrationSetAppliesToTheRealCorpus, which skips when the document is absent.
func fixtureSeriesRound(t *testing.T) (SeriesEmission, Corpus) {
	t.Helper()
	c := fixtureCorpus()
	saveM, saveD := SeriesMutations, SeriesCleanDuplicates
	SeriesMutations = []SeriesMutation{
		{ID: "f01", Class: OrderShuffle, Session: "Alpha session", Order: []int{1, 2, 4, 3, 5},
			Note: "the bench beat is shown before the cache work it benchmarks"},
		{ID: "f02", Class: EntitySwap, Session: "Alpha session",
			Pairs: []SwapPair{{From: "widgetd", To: "intaked"}},
			Note:  "the service the record counts is renamed throughout"},
		{ID: "f03", Class: CrossSessionContamination, Session: "Alpha session",
			DonorSession: "Beta session", DonorOrdinal: 2, InsertAt: 3,
			Foreign: []string{"depreciation", "Larkspur"},
			Note:    "an accounting beat spliced into a software timeline"},
		{ID: "f04", Class: DroppedMiddle, Session: "Alpha session", Remove: []int{3},
			Note: "removes the marked turn to the cache work"},
		{ID: "f05", Class: InventedArc, Session: "Alpha session",
			Replacement: "The widgetd release is complete and signed off by the platform reviewers.",
			Note:        "the real final beat says the release notes are unfinished"},
	}
	SeriesCleanDuplicates = []string{"Beta session"}
	t.Cleanup(func() { SeriesMutations, SeriesCleanDuplicates = saveM, saveD })

	em, err := EmitSeries(t.TempDir(), "fixture-s", c, "fixture-corpus.md", "deadbeef", 1)
	if err != nil {
		t.Fatalf("EmitSeries: %v", err)
	}
	return em, c
}

func TestEmitSeriesWritesOnePacketPerTimelinePlusPlantedAndDuplicates(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	want := 2 + len(SeriesMutations) + len(SeriesCleanDuplicates)
	if em.Manifest.Count != want {
		t.Fatalf("packets = %d, want %d", em.Manifest.Count, want)
	}
	if em.Key.Counts.Clean != 2 || em.Key.Counts.Planted != 5 || em.Key.Counts.CleanDuplicates != 1 {
		t.Errorf("counts = %+v", em.Key.Counts)
	}
	if em.Key.Counts.SourceSeries != 2 {
		t.Errorf("source series = %d, want 2 — the count the whole round's strength rests on", em.Key.Counts.SourceSeries)
	}
	files, err := filepath.Glob(filepath.Join(em.PacketsDir, "SER-*.md"))
	if err != nil || len(files) != want {
		t.Fatalf("packet files = %d (%v), want %d", len(files), err, want)
	}
	for i := 1; i < len(em.Manifest.Packets); i++ {
		if em.Manifest.Packets[i-1].ID >= em.Manifest.Packets[i].ID {
			t.Fatalf("manifest is not in id order at %d", i)
		}
	}
	for i, e := range em.Key.Entries {
		if e.PacketID != em.Manifest.Packets[i].ID {
			t.Fatalf("key entry %d is %s but manifest row %d is %s — key and manifest are not aligned",
				i, e.PacketID, i, em.Manifest.Packets[i].ID)
		}
	}
}

func TestEveryEmittedSeriesPacketIsTheRecordAndTheBeatsAlone(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	files, _ := filepath.Glob(filepath.Join(em.PacketsDir, "SER-*.md"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		body := string(b)
		p, err := ParseSeriesPacket(body)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
			continue
		}
		if body != renderSeriesPacket(p) {
			t.Errorf("%s is not a render over its own record and beats", filepath.Base(f))
		}
		for _, forbidden := range []string{
			"Alpha session", "Beta session", "series_planted", "order_shuffle", "mutation",
			"SUBJECT CHANGED", "user: ",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains provenance %q", filepath.Base(f), forbidden)
			}
		}
	}
}

func TestEmittedSeriesPacketsAreGreppedForEveryAnswerKeyValue(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	if len(em.Leak.Hits) != 0 || len(em.Leak.Structural) != 0 {
		t.Fatalf("hits=%v structural=%v", em.Leak.Hits, em.Leak.Structural)
	}
	if len(em.Leak.Checked) == 0 {
		t.Fatal("nothing was grepped for, so the check proves nothing")
	}
	if em.Leak.Scanned != em.Manifest.Count {
		t.Errorf("scanned %d packets, emitted %d", em.Leak.Scanned, em.Manifest.Count)
	}
	// Every exclusion carries a reason: a check that silently narrows what it looks for is the
	// failure this harness is replacing.
	for _, x := range em.Leak.Excluded {
		if strings.TrimSpace(x.Reason) == "" {
			t.Errorf("excluded value %q has no reason", x.Value)
		}
	}
	// And the withheld key is not inside the packets directory.
	if strings.HasPrefix(em.KeyPath, em.PacketsDir) {
		t.Errorf("the answer key %s is inside the packets directory", em.KeyPath)
	}
}

func TestTheSeriesLeakCheckFindsALeakWhenThereIsOne(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	var target string
	for _, e := range em.Key.Entries {
		if e.Kind == KindSeriesPlanted {
			target = e.PacketID
			break
		}
	}
	path := filepath.Join(em.PacketsDir, target+".md")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, []byte("\n\nsource: Alpha session, series_planted\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	bodies := map[string]SeriesPacket{}
	for _, e := range em.Key.Entries {
		raw, err := os.ReadFile(filepath.Join(em.PacketsDir, e.PacketID+".md"))
		if err != nil {
			t.Fatal(err)
		}
		p, err := ParseSeriesPacket(string(raw))
		if err != nil {
			// The tampered file still parses; if it did not, the structural check would catch it.
			continue
		}
		bodies[e.PacketID] = p
	}
	rep, err := checkNoSeriesLeak(em.PacketsDir, em.Key, bodies)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Hits) == 0 && len(rep.Structural) == 0 {
		t.Fatal("a packet with the session title and the kind appended was reported clean")
	}
}

func TestACleanSeriesDuplicateIsByteIdenticalToItsTwinApartFromTheID(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	var dup, twin string
	for _, e := range em.Key.Entries {
		if e.Kind == KindSeriesDuplicate {
			dup, twin = e.PacketID, e.DuplicateOf
		}
	}
	if dup == "" || twin == "" {
		t.Fatal("no clean duplicate in the round")
	}
	a, err := os.ReadFile(filepath.Join(em.PacketsDir, dup+".md"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(em.PacketsDir, twin+".md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Replace(string(a), dup, twin, 1) != string(b) {
		t.Error("the duplicate is not byte-identical to its twin apart from the id line")
	}
}

func TestEmitSeriesIsDeterministic(t *testing.T) {
	c := fixtureCorpus()
	saveM, saveD := SeriesMutations, SeriesCleanDuplicates
	SeriesMutations = []SeriesMutation{
		{ID: "f01", Class: OrderShuffle, Session: "Alpha session", Order: []int{1, 2, 4, 3, 5}, Note: "n"},
	}
	SeriesCleanDuplicates = []string{"Alpha session"}
	t.Cleanup(func() { SeriesMutations, SeriesCleanDuplicates = saveM, saveD })

	first, err := EmitSeries(t.TempDir(), "d", c, "p.md", "sum", 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EmitSeries(t.TempDir(), "d", c, "p.md", "sum", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Manifest.Packets) != len(second.Manifest.Packets) {
		t.Fatal("packet counts differ between two emissions")
	}
	for i := range first.Manifest.Packets {
		if first.Manifest.Packets[i] != second.Manifest.Packets[i] {
			t.Fatalf("emission %d differs: %+v vs %+v", i, first.Manifest.Packets[i], second.Manifest.Packets[i])
		}
	}
}

func TestTheSeriesDispatchPlanCoversEveryPacketTwiceAndTheRoundCarriesTheRubric(t *testing.T) {
	em, _ := fixtureSeriesRound(t)
	b, err := os.ReadFile(filepath.Join(em.Dir, "dispatch-plan.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		rows++
	}
	if want := em.Manifest.Count * ReviewersPerPacket; rows != want {
		t.Errorf("dispatch plan has %d rows, want %d", rows, want)
	}
	prompt, err := os.ReadFile(filepath.Join(em.Dir, "reviewer-dispatch-series.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(prompt) != SeriesDispatchPrompt() {
		t.Error("the round's copy of the rubric is not the embedded one")
	}
	for _, dim := range SeriesDimensions {
		if !strings.Contains(string(prompt), dim) {
			t.Errorf("the rubric never mentions the %q dimension the scorer reads", dim)
		}
	}
	for _, class := range SeriesMutationClasses {
		if !strings.Contains(string(prompt), string(class)) {
			t.Errorf("the rubric offers no way to name the %q class the calibration plants", class)
		}
	}
	// The round README must not disclose the composition, and must warn about the path resolution
	// that cost round r1 a scoring run.
	readme, err := os.ReadFile(filepath.Join(em.Dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), "ABSOLUTE PATH") {
		t.Error("the round README does not warn that go test resolves a relative path against the package directory")
	}
	var key SeriesAnswerKey
	kb, err := os.ReadFile(em.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(kb, &key); err != nil {
		t.Fatal(err)
	}
	if key.Counts.Packets != em.Manifest.Count {
		t.Errorf("the key on disk says %d packets, the manifest says %d", key.Counts.Packets, em.Manifest.Count)
	}
}
