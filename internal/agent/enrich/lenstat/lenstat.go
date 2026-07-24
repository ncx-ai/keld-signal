// Package lenstat sizes enrichment's input truncation from the machine's own
// prompt-length distribution instead of a fixed guess.
//
// Why truncation is needed at all: GLiNER2 is a transformer, so a single
// inference's transient activation memory grows with sequence length — measured
// on a 20-core host at roughly 40 MB + 0.55 MB per word token on top of the
// model's ~2.4 GB resident cost. Left unbounded (gliner2's max_len defaults to
// None, i.e. no truncation) one long prompt allocates a multi-GB spike, which is
// what drove the sidecar's RSS oscillation from ~2.7 GB to ~5.7 GB.
//
// Why adaptive: a hardcoded cap is either wasteful (sized for the worst case,
// so every ordinary prompt pays worst-case memory) or over-restrictive (sized
// for the common case, so long prompts are silently gutted). Instead we track
// the streaming mean and variance of observed prompt lengths (Welford, so no
// history is retained) and truncate at mu + 2*sigma — the window that covers
// ~97.7% of this machine's prompts in full.
//
// Two clamps bound that estimate:
//
//   - Ceiling: mu+2sigma is a quality estimator with no notion of memory. On a
//     machine where large pastes are routine, both mu and sigma inflate and the
//     estimate can exceed the RAM budget. The ceiling expresses that budget in
//     tokens and is a hard invariant, not a tuning preference.
//   - Floor: a population of uniformly short prompts drives mu+2sigma toward
//     zero, which would gut the occasional long prompt. The floor means the
//     adaptive cap can only ever widen the window, never over-constrain it.
//
// Only lengths are recorded — never prompt text — so the persisted state
// carries no more information than a histogram of integers.
package lenstat

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// Defaults. The ceiling is MEASURED, not estimated: each cap was swept against
// the real gliner2-large worker (2 torch threads, MALLOC_ARENA_MAX=2, 3-call
// bursts saturating the cap) and its peak RSS recorded, against a 4096 MB total
// sidecar budget with ~105 MB for the FastAPI parent + resource tracker:
//
//	max_len   worker peak   total     latency/call
//	    512       3043 MB   3148 MB       3.5 s
//	    768       3215 MB   3319 MB       5.8 s
//	   1024       3416 MB   3521 MB       8.6 s
//	   1280       3666 MB   3771 MB      11.9 s
//	   1536       3870 MB   3975 MB      15.9 s
//	   1800       4305 MB   4410 MB      30.8 s   <- over budget; hard-killed
//
// Those figures come from synthetic /classify calls. The LIVE pipeline is
// heavier: it also issues /extract with entity labels, and prepends context
// preambles, so real peaks run several hundred MB above the synthetic curve at
// the same cap. Measured in the running daemon at a 1024 cap: peak 3872 MB
// (total ~3977 MB) — inside the 4096 MB budget, but only ~120 MB of margin and
// only ~74 MB below the worker hard limit, close enough that ordinary variance
// would start tripping mid-job kills.
//
// So the default is 768, calibrated on the LIVE measurement rather than the
// synthetic one. Verified in the running daemon on the heaviest real op
// (/extract, 5 entity labels + 5 sensitivity labels, input far exceeding the
// cap): peak 3238 MB, total ~3343 MB — 753 MB of margin under the 4096 MB budget,
// at 9.2 s for that call. Cost is superlinear in BOTH memory and time, so raising
// this is not a free "less truncation" win.
//
// Note what this implies: a 4 GB total budget against a ~2.7 GB resident model
// leaves ~1.2 GB of transient headroom, which buys roughly 768 word tokens in the
// real pipeline. A materially larger window needs a larger budget or a smaller /
// quantized model — not a bigger cap.
//
// Most prompts are far shorter than the cap, so mu+2sigma normally lands well
// below it and neither the memory nor the latency worst case is reached.
const (
	DefaultFloor     = 512 // never truncate below this, however short prompts are
	DefaultCeiling   = 768 // live-measured memory-budget bound, in word tokens
	DefaultMinSample = 200 // observations before the estimate is trusted
	sigmaMultiple    = 2.0 // mu + 2*sigma ⇒ ~97.7% coverage
)

// Config parameterizes a Tracker. Zero fields fall back to the defaults.
type Config struct {
	Path      string // persisted state; empty disables persistence
	Floor     int
	Ceiling   int
	MinSample int
}

func (c Config) withDefaults() Config {
	if c.Floor <= 0 {
		c.Floor = DefaultFloor
	}
	if c.Ceiling <= 0 {
		c.Ceiling = DefaultCeiling
	}
	if c.MinSample <= 0 {
		c.MinSample = DefaultMinSample
	}
	if c.Floor > c.Ceiling {
		c.Floor = c.Ceiling // a misconfigured floor must not beat the memory bound
	}
	return c
}

// state is the persisted Welford accumulator: count, running mean, and sum of
// squared deviations. No prompt text, no per-prompt history.
type state struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean"`
	M2   float64 `json:"m2"`
}

// Tracker accumulates prompt-length statistics and derives the truncation cap.
// Safe for concurrent use.
type Tracker struct {
	cfg Config
	mu  sync.Mutex
	s   state
}

// New builds a Tracker, restoring persisted stats when Config.Path exists. A
// missing or unreadable state file is not an error: the tracker simply starts in
// warmup, because failing to size truncation must never fail enrichment.
func New(cfg Config) *Tracker {
	t := &Tracker{cfg: cfg.withDefaults()}
	t.load()
	return t
}

// FromEnv builds a Tracker with env-tunable policy:
// KELD_ENRICH_TOKEN_FLOOR, KELD_ENRICH_TOKEN_CEILING, KELD_ENRICH_LEN_MIN_SAMPLE.
func FromEnv(path string) *Tracker {
	return New(Config{
		Path:      path,
		Floor:     envInt("KELD_ENRICH_TOKEN_FLOOR", DefaultFloor),
		Ceiling:   envInt("KELD_ENRICH_TOKEN_CEILING", DefaultCeiling),
		MinSample: envInt("KELD_ENRICH_LEN_MIN_SAMPLE", DefaultMinSample),
	})
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

func (t *Tracker) load() {
	if t.cfg.Path == "" {
		return
	}
	b, err := os.ReadFile(t.cfg.Path)
	if err != nil {
		return
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil || s.N < 0 {
		return // corrupt: stay in warmup rather than trust garbage
	}
	t.s = s
}

// Observe records one prompt's length in word tokens (see Words). Values <= 0
// are ignored so an unresolvable prompt cannot skew the distribution toward
// truncating everything.
func (t *Tracker) Observe(tokens int) {
	if tokens <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Welford: constant memory, numerically stable, retains no history.
	t.s.N++
	delta := float64(tokens) - t.s.Mean
	t.s.Mean += delta / float64(t.s.N)
	t.s.M2 += delta * (float64(tokens) - t.s.Mean)
}

// Cap returns the truncation length in word tokens to apply to the next
// inference: the liberal ceiling until the sample is representative, then
// mu + 2*sigma clamped to [Floor, Ceiling].
func (t *Tracker) Cap() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.s.N < t.cfg.MinSample {
		return t.cfg.Ceiling // liberal to start: learn before constraining
	}
	variance := 0.0
	if t.s.N > 1 {
		variance = t.s.M2 / float64(t.s.N-1) // sample variance
	}
	if variance < 0 || math.IsNaN(variance) {
		variance = 0
	}
	cap := int(math.Round(t.s.Mean + sigmaMultiple*math.Sqrt(variance)))
	if cap < t.cfg.Floor {
		return t.cfg.Floor
	}
	if cap > t.cfg.Ceiling {
		return t.cfg.Ceiling
	}
	return cap
}

// Save persists the accumulator. Writes via a temp file + rename so a crash
// mid-write cannot leave truncated JSON that would reset the distribution.
func (t *Tracker) Save() error {
	if t.cfg.Path == "" {
		return nil
	}
	t.mu.Lock()
	snapshot := t.s
	t.mu.Unlock()

	b, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.cfg.Path), 0o700); err != nil {
		return err
	}
	tmp := t.cfg.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.cfg.Path)
}

// Words counts whitespace-separated tokens — the unit gliner2's max_len takes,
// so the statistics and the cap are in the same units. Cheap by design: it runs
// on every prompt and must not need a tokenizer.
func Words(s string) int {
	n, inWord := 0, false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			inWord = false
		default:
			if !inWord {
				n++
				inWord = true
			}
		}
	}
	return n
}
