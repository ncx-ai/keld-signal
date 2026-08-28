package review

import (
	"os"
	"path/filepath"
	"testing"
)

// The series round's three entry points. Like the beat round's, each SKIPS unless what it needs is
// present, and none of them touches the model server: the beats already exist, so measuring whether
// they read as a narrative needs no inference at all.
//
//	REVIEW_CORPUS            overrides the source document (default docs/qwen-inputs-and-outputs.md)
//	REVIEW_SERIES_EMIT_DIR   where to cut a series round; TestEmitSeriesRound skips without it
//	REVIEW_SERIES_ROUND      the round name recorded in the key and manifest (default s1)
//	REVIEW_SERIES_DIR        a series round to score; TestScoreSeriesRound skips without it
//	REVIEW_BEAT_DIR          a per-beat round to cross-tabulate against; optional
//
// ⚠️ Pass those directories as ABSOLUTE paths. `go test` sets each test's working directory to the
// package directory, so a relative path is resolved against internal/agent/enrich/llmstudy/review/
// and not against the shell's cwd. Round r1 lost a scoring run to exactly that. ResolveRoundDir
// tries cwd and then the repository root as a courtesy and logs which it used, but an absolute path
// is the only one that cannot be misread.

func seriesOrSkip(t *testing.T) ([]Series, Corpus, string, string, int) {
	t.Helper()
	c, path, sum, skipped := corpusOrSkip(t)
	all, err := BuildSeries(c)
	if err != nil {
		t.Fatalf("BuildSeries: %v", err)
	}
	return all, c, path, sum, skipped
}

// The series calibration set is authored against the three real timelines, so this is where its
// claims are checked against them: every permutation a real permutation with a real junction, every
// spliced beat foreign here and real there, every renamed entity absent from the corpus and present
// in the measured record, every dropped run interior, contiguous and covering a marked subject
// change, and every invented arc asserting a conclusion in words the session never used.
func TestTheSeriesCalibrationSetAppliesToTheRealCorpus(t *testing.T) {
	all, c, _, _, skipped := seriesOrSkip(t)
	t.Logf("corpus: %d sessions, %d statements, %d skipped (no output)", len(c.Sessions), len(c.Items()), skipped)
	for _, s := range all {
		t.Logf("series %-40q %d beats, record from %d blocks, %d subjects",
			s.SessionTitle, len(s.Beats), s.Record.DerivedFrom, len(s.Record.Subjects))
	}
	if len(all) < 2 {
		t.Fatalf("%d series; a series round needs at least two so contamination has a donor", len(all))
	}
	byClass := map[SeriesMutationClass]int{}
	bySession := map[string]int{}
	for _, m := range SeriesMutations {
		p, err := ApplySeries(c, all, m)
		if err != nil {
			t.Errorf("%s: %v", m.ID, err)
			continue
		}
		byClass[m.Class]++
		bySession[m.Session]++
		t.Logf("%s %-28s %-40q %d beats, positions %v, signature %v",
			m.ID, m.Class, m.Session, len(p.Series.Beats), p.Positions, p.Signature)
	}
	for _, class := range SeriesMutationClasses {
		if byClass[class] < 2 {
			t.Errorf("class %s has %d planted items; two is the minimum that can distinguish a blind spot from an off item", class, byClass[class])
		}
	}
	for _, title := range SeriesCleanDuplicates {
		if _, err := FindSeries(all, title); err != nil {
			t.Errorf("clean duplicate %q: %v", title, err)
		}
	}
	// Stated as a log line rather than an assertion, because it is a fact about the corpus and not
	// a property to enforce: with three timelines, every packet is one of three sessions.
	t.Logf("plants per session: %v — over only %d real timelines", bySession, len(all))
}

func TestEmitSeriesRound(t *testing.T) {
	dir := os.Getenv("REVIEW_SERIES_EMIT_DIR")
	if dir == "" {
		t.Skip("set REVIEW_SERIES_EMIT_DIR to cut a series round")
	}
	_, c, path, sum, skipped := seriesOrSkip(t)
	em, err := EmitSeries(dir, os.Getenv("REVIEW_SERIES_ROUND"), c, path, sum, skipped)
	if err != nil {
		t.Fatalf("EmitSeries: %v", err)
	}
	t.Logf("round %s in %s", em.Round, em.Dir)
	t.Logf("packets %d = clean %d + planted %d + clean duplicates %d, cut from %d real timelines",
		em.Manifest.Count, em.Key.Counts.Clean, em.Key.Counts.Planted,
		em.Key.Counts.CleanDuplicates, em.Key.Counts.SourceSeries)
	for _, class := range SeriesMutationClasses {
		t.Logf("  planted %-28s %d", class, em.Key.Counts.PlantedByClass[string(class)])
	}
	t.Logf("packets by session: %v", em.Key.Counts.SeriesBySession)
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

func TestScoreSeriesRound(t *testing.T) {
	raw := os.Getenv("REVIEW_SERIES_DIR")
	if raw == "" {
		t.Skip("set REVIEW_SERIES_DIR to score a series round")
	}
	dir, how, err := ResolveRoundDir(repoRoot, raw)
	if err != nil {
		t.Fatalf("REVIEW_SERIES_DIR: %v", err)
	}
	t.Logf("scoring %s (%s)", dir, how)

	beatDir := ""
	if raw := os.Getenv("REVIEW_BEAT_DIR"); raw != "" {
		beatDir, how, err = ResolveRoundDir(repoRoot, raw)
		if err != nil {
			t.Fatalf("REVIEW_BEAT_DIR: %v", err)
		}
		t.Logf("cross-tabulating against the per-beat round at %s (%s)", beatDir, how)
	} else {
		t.Log("REVIEW_BEAT_DIR unset: the series-versus-beat table is omitted rather than guessed at")
	}

	s, err := ScoreSeriesRound(
		filepath.Join(dir, "withheld", "answer-key.json"),
		filepath.Join(dir, "packets"),
		filepath.Join(dir, "verdicts"),
		beatDir,
	)
	if err != nil {
		t.Fatalf("ScoreSeriesRound: %v", err)
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
