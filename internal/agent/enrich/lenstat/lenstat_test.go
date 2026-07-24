package lenstat_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/lenstat"
)

func testCfg(dir string) lenstat.Config {
	return lenstat.Config{
		Path: filepath.Join(dir, "prompt-lengths.json"),
		// Deliberately wide clamps so formula tests exercise mu+2sigma itself
		// rather than a clamp.
		Floor: 1, Ceiling: 100000, MinSample: 5,
	}
}

// Until the sample is representative, the cap must be the LIBERAL ceiling —
// sizing truncation from two or three prompts would constrain the window based
// on noise.
func TestCapIsLiberalCeilingDuringWarmup(t *testing.T) {
	tr := lenstat.New(testCfg(t.TempDir()))
	for i := 0; i < 4; i++ { // one short of MinSample
		tr.Observe(10)
	}
	if got := tr.Cap(); got != 100000 {
		t.Fatalf("Cap() = %d during warmup, want the ceiling 100000", got)
	}
}

// Once representative, the cap is mu + 2*sigma over observed prompt lengths.
func TestCapIsMeanPlusTwoSigma(t *testing.T) {
	tr := lenstat.New(testCfg(t.TempDir()))
	for _, n := range []int{100, 200, 300, 400, 500} {
		tr.Observe(n)
	}
	// mean 300; sample sd = sqrt(25000) = 158.11; mu+2sigma = 616.23
	want := int(math.Round(300 + 2*math.Sqrt(25000)))
	if got := tr.Cap(); got != want {
		t.Fatalf("Cap() = %d, want %d (mu+2sigma)", got, want)
	}
}

// The memory budget is a hard invariant: mu+2sigma knows nothing about RAM, so a
// population of very long prompts must not push the cap past the ceiling.
func TestCapNeverExceedsCeiling(t *testing.T) {
	cfg := testCfg(t.TempDir())
	cfg.Ceiling = 1800
	tr := lenstat.New(cfg)
	for _, n := range []int{5000, 20000, 40000, 60000, 80000} {
		tr.Observe(n)
	}
	if got := tr.Cap(); got != 1800 {
		t.Fatalf("Cap() = %d, want it clamped to the ceiling 1800", got)
	}
}

// A population of uniformly short prompts drives mu+2sigma near zero; the floor
// keeps the window from becoming over-restrictive for the occasional long prompt.
func TestCapNeverBelowFloor(t *testing.T) {
	cfg := testCfg(t.TempDir())
	cfg.Floor = 512
	tr := lenstat.New(cfg)
	for i := 0; i < 10; i++ {
		tr.Observe(8)
	}
	if got := tr.Cap(); got != 512 {
		t.Fatalf("Cap() = %d, want the floor 512", got)
	}
}

// Zero variance must not produce a NaN/zero cap.
func TestCapWithZeroVarianceIsMean(t *testing.T) {
	cfg := testCfg(t.TempDir())
	cfg.Floor = 1
	tr := lenstat.New(cfg)
	for i := 0; i < 6; i++ {
		tr.Observe(700)
	}
	if got := tr.Cap(); got != 700 {
		t.Fatalf("Cap() = %d, want 700 (mean, sigma=0)", got)
	}
}

// The distribution must survive a daemon restart, or every restart re-enters
// warmup and the cap never converges.
func TestStatsPersistAcrossReload(t *testing.T) {
	cfg := testCfg(t.TempDir())
	tr := lenstat.New(cfg)
	for _, n := range []int{100, 200, 300, 400, 500} {
		tr.Observe(n)
	}
	before := tr.Cap()
	if err := tr.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	reloaded := lenstat.New(cfg)
	if got := reloaded.Cap(); got != before {
		t.Fatalf("Cap() after reload = %d, want %d", got, before)
	}
}

// A missing or corrupt stats file must degrade to warmup, never fail the daemon.
func TestCorruptStatsFileFallsBackToWarmup(t *testing.T) {
	cfg := testCfg(t.TempDir())
	if err := writeFile(cfg.Path, "{not json"); err != nil {
		t.Fatal(err)
	}
	tr := lenstat.New(cfg)
	if got := tr.Cap(); got != cfg.Ceiling {
		t.Fatalf("Cap() = %d on corrupt state, want the ceiling %d", got, cfg.Ceiling)
	}
}

// Observe counts word tokens, which is the unit gliner2's max_len takes.
func TestWordsCountsWhitespaceTokens(t *testing.T) {
	if got := lenstat.Words("  refactor   the auth\tmiddleware\nnow "); got != 5 {
		t.Fatalf("Words() = %d, want 5", got)
	}
	if got := lenstat.Words("   "); got != 0 {
		t.Fatalf("Words() on blank = %d, want 0", got)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
