package review

import (
	"os"
	"path/filepath"
	"testing"
)

// The production-beat round's entry points. Like the beat and series rounds', each SKIPS unless
// what it needs is present, and none of them touches the model server: the beats already exist, so
// reviewing them needs no inference at all.
//
//	REVIEW_PROD_CORPUS     overrides the source document (default docs/qwen-beat-inputs-and-outputs.md)
//	REVIEW_PROD_EMIT_DIR   where to cut a round; TestEmitProdRound skips without it
//	REVIEW_PROD_ROUND      the round name recorded in the key and manifest (default p1)
//	REVIEW_PROD_DIR        a round directory to score; TestScoreProdRound skips without it
//	REVIEW_R1_SCORE        r1's score.json, for the dimension comparison; optional but the point
//
// ⚠️ Pass those directories as ABSOLUTE paths. `go test` sets each test's working directory to the
// package directory, so a relative path is resolved against internal/agent/enrich/llmstudy/review/
// and not against the shell's cwd. Round r1 lost a scoring run to exactly that.

func prodCorpusOrSkip(t *testing.T) ProdCorpus {
	t.Helper()
	p, _, _ := prodCorpusWithDigestOrSkip(t)
	return p
}

func prodCorpusWithDigestOrSkip(t *testing.T) (ProdCorpus, string, string) {
	t.Helper()
	path := ProdCorpusPathFromEnv(repoRoot)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("production-beat document not present at %s: %v", path, err)
	}
	p, sum, err := LoadProdCorpus(path)
	if err != nil {
		t.Fatalf("LoadProdCorpus(%s): %v", path, err)
	}
	return p, path, sum
}

func TestEmitProdRound(t *testing.T) {
	dir := os.Getenv("REVIEW_PROD_EMIT_DIR")
	if dir == "" {
		t.Skip("set REVIEW_PROD_EMIT_DIR to cut a production-beat round")
	}
	p, path, sum := prodCorpusWithDigestOrSkip(t)
	em, err := EmitProd(dir, os.Getenv("REVIEW_PROD_ROUND"), p, path, sum)
	if err != nil {
		t.Fatalf("EmitProd: %v", err)
	}
	real, synth := ProdSampleCoverage(p, em.Sample)
	t.Logf("round %s in %s", em.Round, em.Dir)
	t.Logf("packets %d = genuine %d + planted %d + clean duplicates %d",
		em.Manifest.Count, em.Key.Counts.Genuine, em.Key.Counts.Planted, em.Key.Counts.CleanDuplicates)
	for _, class := range MutationClasses {
		t.Logf("  planted %-24s %d", class, em.Key.Counts.PlantedByClass[string(class)])
	}
	t.Logf("the sample covers %d real conversations and %d hand-authored sessions, out of %d and %d in the corpus",
		real, synth, len(p.SessionsBy(PopulationReal)), len(p.SessionsBy(PopulationSynthetic)))
	t.Logf("absences carried: %d windows lost to subject anchoring, %d generation failures in all",
		p.Counts.SubjectLadderLosses, len(p.Failures))
	t.Logf("guard reach: %d of %d kept entries name nothing checkable",
		p.Counts.UnconstrainedEntries, p.Counts.KeptEntries)
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

func TestScoreProdRound(t *testing.T) {
	raw := os.Getenv("REVIEW_PROD_DIR")
	if raw == "" {
		t.Skip("set REVIEW_PROD_DIR to score a production-beat round")
	}
	dir, how, err := ResolveRoundDir(repoRoot, raw)
	if err != nil {
		t.Fatalf("REVIEW_PROD_DIR: %v", err)
	}
	t.Logf("scoring %s (%s)", dir, how)

	r1 := ""
	if raw := os.Getenv("REVIEW_R1_SCORE"); raw != "" {
		r1, how, err = resolveScoreFile(repoRoot, raw)
		if err != nil {
			t.Fatalf("REVIEW_R1_SCORE: %v", err)
		}
		t.Logf("comparing against r1's score at %s (%s)", r1, how)
	} else {
		t.Log("REVIEW_R1_SCORE unset: the dimension comparison is omitted rather than guessed at")
	}

	s, err := ScoreProdRound(dir, r1)
	if err != nil {
		t.Fatalf("ScoreProdRound: %v", err)
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
