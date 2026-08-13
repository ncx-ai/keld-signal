package review

import (
	"testing"
	"unicode/utf8"
)

// The production calibration set is authored against real beats, so this is where its claims are
// checked against the real evidence: every span unique, every "absent" token really absent from
// that beat's own window and record, the drifted subject really present in the session it was drawn
// from, every result within the length band and still shaped like a subject plus entries.
func TestTheProdCalibrationSetAppliesToTheRealCorpus(t *testing.T) {
	p := prodCorpusOrSkip(t)
	t.Logf("corpus: %d sessions (%d real, %d synthetic), %d beats, %d generation failures",
		len(p.Corpus.Sessions), len(p.SessionsBy(PopulationReal)), len(p.SessionsBy(PopulationSynthetic)),
		len(p.Corpus.Items()), len(p.Failures))

	byClass := map[MutationClass]int{}
	bySession := map[string]int{}
	for _, m := range ProdMutations {
		pl, err := ApplyProd(p.Corpus, m)
		if err != nil {
			t.Errorf("%s: %v", m.ID, err)
			continue
		}
		byClass[m.Class]++
		bySession[m.Session]++
		t.Logf("%s %-24s %s#%d signature %v\n%s", m.ID, m.Class, m.Session, m.Ordinal, pl.Signature, pl.Item.Output)
	}
	for _, class := range MutationClasses {
		if byClass[class] != 1 {
			t.Errorf("class %s has %d planted items, want exactly 1 — the round is sized for one per class",
				class, byClass[class])
		}
	}
	// One plant per session is not a rule, it is what this set happens to do, and it is asserted
	// because a set that concentrated on one session would measure that session.
	for s, n := range bySession {
		if n > 1 {
			t.Errorf("session %q carries %d plants; the set is meant to span sessions", s, n)
		}
	}
	// The audience requirement is that this works for people who do not write code, and a
	// calibration set drawn only from engineering sessions cannot see a domain-blind reviewer.
	synthetic := 0
	for _, m := range ProdMutations {
		if p.Population[m.Session] == PopulationSynthetic {
			synthetic++
		}
	}
	if synthetic == 0 {
		t.Error("no plant is cut from the hand-authored pair; a domain-blind reviewer would be invisible")
	}

	sample, err := SampleProdGenuine(p, ProdGenuineReal, ProdGenuineSynthetic)
	if err != nil {
		t.Fatalf("SampleProdGenuine: %v", err)
	}
	inSample := map[string]bool{}
	for _, it := range sample {
		inSample[itemKey(it)] = true
	}
	for _, d := range ProdCleanDuplicates {
		it, err := p.Corpus.Find(d.Session, d.Ordinal)
		if err != nil {
			t.Errorf("clean duplicate %s#%d: %v", d.Session, d.Ordinal, err)
			continue
		}
		if !inSample[itemKey(it)] {
			t.Errorf("clean duplicate %s#%d is not in the genuine sample, so it has no twin in the round", d.Session, d.Ordinal)
		}
		t.Logf("duplicate %s#%d [%s] %d runes", d.Session, d.Ordinal, it.Population, utf8.RuneCountInString(it.Output))
	}
	// A plant whose source is also emitted unmutated would put the same statement in the round
	// twice, once clean and once defective, which is a different experiment.
	for _, m := range ProdMutations {
		if inSample[itemKey(Item{SessionTitle: m.Session, Ordinal: m.Ordinal})] {
			t.Errorf("plant %s is cut from %s#%d, which is also in the genuine sample", m.ID, m.Session, m.Ordinal)
		}
	}
}

// The sample is the round's whole claim to breadth, so its coverage is asserted rather than logged.
func TestTheGenuineSampleCoversEverySession(t *testing.T) {
	p := prodCorpusOrSkip(t)
	sample, err := SampleProdGenuine(p, ProdGenuineReal, ProdGenuineSynthetic)
	if err != nil {
		t.Fatalf("SampleProdGenuine: %v", err)
	}
	if len(sample) != ProdGenuineReal+ProdGenuineSynthetic {
		t.Fatalf("sample is %d items, want %d", len(sample), ProdGenuineReal+ProdGenuineSynthetic)
	}
	real, synth := ProdSampleCoverage(p, sample)
	if want := len(p.SessionsBy(PopulationReal)); real != want {
		t.Errorf("the sample covers %d of %d real sessions", real, want)
	}
	if want := len(p.SessionsBy(PopulationSynthetic)); synth != want {
		t.Errorf("the sample covers %d of %d synthetic sessions", synth, want)
	}
	// The rotation exists so the sample is not all session openings, which are systematically the
	// easiest beat in a session: the window is the start of the work and the record is small.
	firstBeats := 0
	for _, it := range sample {
		if it.Ordinal == 1 {
			firstBeats++
		}
	}
	if firstBeats == len(sample) {
		t.Error("every sampled beat is its session's first — the rotation is not rotating")
	}
	t.Logf("sample %d items over %d real + %d synthetic sessions; %d are a session's first beat",
		len(sample), real, synth, firstBeats)

	// Deterministic: a round that cannot be regenerated identically cannot be re-scored.
	again, err := SampleProdGenuine(p, ProdGenuineReal, ProdGenuineSynthetic)
	if err != nil {
		t.Fatal(err)
	}
	for i := range sample {
		if itemKey(sample[i]) != itemKey(again[i]) {
			t.Fatalf("the sample is not deterministic at %d: %s vs %s", i, itemKey(sample[i]), itemKey(again[i]))
		}
	}
}
