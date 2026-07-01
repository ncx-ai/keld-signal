// Package govern is the adaptive host-load governor for the ML path.
package govern

import "sync"

const (
	alpha    = 0.3 // EWMA smoothing
	highMark = 85.0
	lowMark  = 60.0
)

type Sampler interface{ CPUPercent() float64 }

type Governor struct {
	sampler Sampler
	maxConc int
	mu      sync.Mutex
	ewma    float64
	seen    bool
	tick    uint64
}

func New(sampler Sampler, maxConc int) *Governor {
	if maxConc < 1 {
		maxConc = 1
	}
	return &Governor{sampler: sampler, maxConc: maxConc}
}

func (g *Governor) Observe(sample float64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.seen {
		g.ewma, g.seen = sample, true
		return
	}
	g.ewma = alpha*sample + (1-alpha)*g.ewma
}

// Concurrency scales from maxConc (<= lowMark) down to 1 (>= highMark).
func (g *Governor) Concurrency() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case g.ewma >= highMark:
		return 1
	case g.ewma <= lowMark:
		return g.maxConc
	default:
		// linear interp between lowMark..highMark
		frac := (highMark - g.ewma) / (highMark - lowMark) // 1 at low, 0 at high
		c := 1 + int(frac*float64(g.maxConc-1))
		if c < 1 {
			c = 1
		}
		return c
	}
}

// Admit sheds a fraction of work proportional to overload above highMark.
// Admit rate = 1/keep, so a larger keep sheds more.
func (g *Governor) Admit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ewma < highMark {
		return true
	}
	// admit rate = 1/keep: larger keep means more shedding
	keep := uint64(2) // 85 <= ewma < 95: admit ~1/2
	if g.ewma >= 95 {
		keep = 4 // severe: admit ~1/4 (shed more)
	}
	g.tick++
	return g.tick%keep == 0
}

// Sample reads the host sampler (when configured) and updates the EWMA.
func (g *Governor) Sample() {
	if g.sampler != nil {
		sample := g.sampler.CPUPercent()
		g.Observe(sample)
	}
}
