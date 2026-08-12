package review

import (
	"os"
	"path/filepath"
	"testing"
)

// The three entry points below read or write things outside this package, so each one SKIPS
// unless what it needs is present. They are ordinary tests rather than a tagged build because
// none of them touches the model server: the packaging half of this harness needs no inference
// at all, which is the point of doing the generation once and reviewing the record of it.
//
//	REVIEW_CORPUS       overrides the source document (default docs/qwen-inputs-and-outputs.md)
//	REVIEW_EMIT_DIR     where to cut a round; TestEmitRound skips without it
//	REVIEW_ROUND        the round name recorded in the key and the manifest (default r1)
//	REVIEW_SCORE_DIR    a round directory to score; TestScoreRound skips without it
//
// The source document is the project owner's and is untracked, so no test may require it.

// repoRoot is where this package sits relative to the module root.
const repoRoot = "../../../../.."

func corpusOrSkip(t *testing.T) (Corpus, string, string, int) {
	t.Helper()
	path := CorpusPathFromEnv(repoRoot)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("source document not present at %s (it is the owner's, untracked): %v", path, err)
	}
	c, sum, skipped, err := LoadCorpus(path)
	if err != nil {
		t.Fatalf("LoadCorpus(%s): %v", path, err)
	}
	return c, path, sum, skipped
}

// The calibration set is authored against real statements, so this is where its claims are
// checked against the real evidence: every span unique, every "absent" token really absent from
// that item's window and record, every drifted subject really present in the session it was
// drawn from, every result within the length band and ending at a sentence boundary.
func TestTheCalibrationSetAppliesToTheRealCorpus(t *testing.T) {
	c, _, _, skipped := corpusOrSkip(t)
	t.Logf("corpus: %d sessions, %d statements, %d skipped (no output)", len(c.Sessions), len(c.Items()), skipped)
	byClass := map[MutationClass]int{}
	for _, m := range Mutations {
		p, err := Apply(c, m)
		if err != nil {
			t.Errorf("%s: %v", m.ID, err)
			continue
		}
		byClass[m.Class]++
		t.Logf("%s %-24s %s", m.ID, m.Class, p.Item.Output)
	}
	for _, class := range MutationClasses {
		if byClass[class] < 2 {
			t.Errorf("class %s has %d planted items; two is the minimum that can distinguish a blind spot from an off item", class, byClass[class])
		}
	}
	for _, d := range CleanDuplicates {
		if _, err := c.Find(d.Session, d.Ordinal); err != nil {
			t.Errorf("clean duplicate %s#%d: %v", d.Session, d.Ordinal, err)
		}
	}
}

func TestEmitRound(t *testing.T) {
	dir := os.Getenv("REVIEW_EMIT_DIR")
	if dir == "" {
		t.Skip("set REVIEW_EMIT_DIR to cut a round")
	}
	c, path, sum, skipped := corpusOrSkip(t)
	em, err := Emit(dir, os.Getenv("REVIEW_ROUND"), c, path, sum, skipped)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	t.Logf("round %s in %s", em.Round, em.Dir)
	t.Logf("packets %d = genuine %d + planted %d + clean duplicates %d",
		em.Manifest.Count, em.Key.Counts.Genuine, em.Key.Counts.Planted, em.Key.Counts.CleanDuplicates)
	for _, class := range MutationClasses {
		t.Logf("  planted %-24s %d", class, em.Key.Counts.PlantedByClass[string(class)])
	}
	t.Logf("answer key %s", em.KeyPath)
	t.Logf("leak check: %d values grepped, %d hits, %d coincidences in evidence, %d structural mismatches",
		len(em.Leak.Checked), len(em.Leak.Hits), len(em.Leak.Coincidences), len(em.Leak.Structural))
	for _, c := range em.Leak.Coincidences {
		t.Logf("  coincidence: %s", c)
	}
	if len(em.Leak.Hits) > 0 || len(em.Leak.Structural) > 0 {
		t.Fatalf("hits=%v structural=%v", em.Leak.Hits, em.Leak.Structural)
	}
}

func TestScoreRound(t *testing.T) {
	dir := os.Getenv("REVIEW_SCORE_DIR")
	if dir == "" {
		t.Skip("set REVIEW_SCORE_DIR to score a round")
	}
	s, err := ScoreRound(
		filepath.Join(dir, "withheld", "answer-key.json"),
		filepath.Join(dir, "packets"),
		filepath.Join(dir, "verdicts"),
	)
	if err != nil {
		t.Fatalf("ScoreRound: %v", err)
	}
	report := s.Render()
	out := filepath.Join(dir, "score.md")
	if err := os.WriteFile(out, []byte(report), 0o644); err != nil {
		t.Fatalf("writing %s: %v", out, err)
	}
	if err := writeJSON(filepath.Join(dir, "score.json"), s); err != nil {
		t.Fatalf("writing score.json: %v", err)
	}
	t.Logf("wrote %s\n\n%s", out, report)
}
