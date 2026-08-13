package review

import (
	"os"
	"testing"
	"unicode/utf8"
)

// TestDumpProdCorpus prints the parsed production-beat corpus so the calibration set can be
// authored against the real statements rather than against a memory of them. It asserts nothing
// beyond the parse succeeding; every claim the calibration set makes is checked by
// TestTheProdCalibrationSetAppliesToTheRealCorpus against the real evidence.
func TestDumpProdCorpus(t *testing.T) {
	if os.Getenv("REVIEW_DUMP") == "" {
		t.Skip("set REVIEW_DUMP=1 to print the parsed production-beat corpus")
	}
	p := prodCorpusOrSkip(t)
	t.Logf("sessions %d, beats %d, failures %d", len(p.Corpus.Sessions), len(p.Corpus.Items()), len(p.Failures))
	t.Logf("tally: asked %d generated %d failed %d; unconstrained %d of %d kept; ladder losses %d",
		p.Counts.BeatsAsked, p.Counts.BeatsGenerated, p.Counts.BeatsFailed,
		p.Counts.UnconstrainedEntries, p.Counts.KeptEntries, p.Counts.SubjectLadderLosses)
	for _, s := range p.Corpus.Sessions {
		t.Logf("== %s [%s] %d beats", s.Title, p.Population[s.Title], len(s.Items))
		for _, it := range s.Items {
			t.Logf("  beat %d (window %d of %d) %d runes, %d events, %d dropped\n%s",
				it.Ordinal, it.WindowIndex, it.WindowCount, utf8.RuneCountInString(it.Output),
				len(ProdEvents(it.Output)), len(it.DroppedEntries), it.Output)
		}
	}
	for _, f := range p.Failures {
		t.Logf("FAILURE %s window %d after %d attempts [%s]: %s", f.Session, f.WindowIndex, f.Attempts, f.Rule, f.Reason)
	}

	sample, err := SampleProdGenuine(p, ProdGenuineReal, ProdGenuineSynthetic)
	if err != nil {
		t.Fatalf("SampleProdGenuine: %v", err)
	}
	inSample := map[string]bool{}
	for _, it := range sample {
		inSample[itemKey(it)] = true
	}
	r, s := ProdSampleCoverage(p, sample)
	t.Logf("SAMPLE %d items over %d real + %d synthetic sessions", len(sample), r, s)
	for _, it := range sample {
		t.Logf("  SAMPLE %s#%d [%s] %d runes\n%s", it.SessionTitle, it.Ordinal, it.Population,
			utf8.RuneCountInString(it.Output), it.Output)
	}
	for _, it := range p.Corpus.Items() {
		if !inSample[itemKey(it)] {
			t.Logf("  RESERVE %s#%d [%s] %d runes\n%s", it.SessionTitle, it.Ordinal, it.Population,
				utf8.RuneCountInString(it.Output), it.Output)
		}
	}
}
