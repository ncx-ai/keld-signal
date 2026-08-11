//go:build llmstudy

package llmstudy

import (
	"testing"
	"time"
)

// TestCorpusDocumentFrequencySeparatesSubjectsFromEnglish is the evidence behind
// dfMaxFraction, kept as a test so the figures in its doc can be re-derived rather than trusted.
//
// It also ASSERTS the property the threshold rests on, in the only form the measurement
// supports: every word that caused a recorded defect on the >=7-character rule is now excluded,
// and the accounting vocabulary the audience requirement turns on survives. It deliberately does
// NOT assert clean separation of the two lists, because there is none — they overlap between
// about .12 and .56, and a test claiming otherwise would be the seventh instance of this
// branch's signature defect.
func TestCorpusDocumentFrequencySeparatesSubjectsFromEnglish(t *testing.T) {
	start := time.Now()
	d := buildDocFreq(corpusRoot(), dfSampleSessions)
	t.Logf("built in %s: %d sessions, %d distinct terms", time.Since(start).Round(time.Millisecond), d.sessions, len(d.count))
	if !d.representative() {
		t.Skipf("only %d sessions on this machine; the DF rule is in cold start", d.sessions)
	}
	restore := withDocFreq(d)
	defer restore()
	// The eight words measured inside SessionRecord.Subjects, plus the T11 pair.
	for _, w := range []string{"control", "question", "exactly", "failure", "padding",
		"identity", "confirm", "changes", "remains", "whether"} {
		if distinctiveToken(w) {
			t.Errorf("%q is still distinctive at df=%.2f — it is one of the words that made "+
				"Subjects, T11 or T12 unusable", w, d.fraction(w))
		}
	}
	// The audience requirement: a rule that only works for code fails it.
	for _, w := range []string{"depreciation", "accruals", "Meridian", "Larkin"} {
		if !distinctiveToken(w) {
			t.Errorf("%q did not survive at df=%.2f — the accounting session's subjects must, "+
				"or the rule is an engineering stoplist with extra steps", w, d.fraction(w))
		}
	}
	// The full table, for the record.
	for _, group := range []struct {
		name  string
		terms []string
	}{
		{"GENERIC — the words that broke Subjects/T11/T12", []string{
			"control", "question", "exactly", "failure", "remains", "whether", "changes",
			"confirm", "padding", "identity", "windows", "latency", "complete", "running",
			"consistent", "analyzing", "ensuring", "finalizing", "specifically", "identified",
			"existing", "buttons", "password", "parallel", "checkout", "synthetic", "worktree",
		}},
		{"SPECIFIC — real subject vocabulary", []string{
			"gliner2", "lenstat", "digestschema", "boundretainlist", "subjecttokens",
			"keld-signal", "agentcfg.info", "daemon.go", "cache-ram", "llama-server",
			"notarization", "build-pkg.sh", "keld_oauth", "atlas.keld.co", "enrichment",
			"depreciation", "accruals", "meridian", "larkin", "globex", "northwind",
		}},
	} {
		t.Logf("--- %s", group.name)
		for _, w := range group.terms {
			t.Logf("   %-18s df=%3d/%d  frac=%.2f", w, d.count[w], d.sessions, d.fraction(w))
		}
	}
}
