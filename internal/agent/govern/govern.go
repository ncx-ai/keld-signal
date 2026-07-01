// Package govern is the adaptive host-load governor for the ML path.
package govern

const (
	alpha    = 0.3 // EWMA smoothing
	highMark = 85.0
	lowMark  = 60.0
)

type Sampler interface{ CPUPercent() float64 }

type Governor struct {
	sampler Sampler
	maxConc int
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
	if !g.seen {
		g.ewma, g.seen = sample, true
		return
	}
	g.ewma = alpha*sample + (1-alpha)*g.ewma
}

// Concurrency scales from maxConc (<= lowMark) down to 1 (>= highMark).
func (g *Governor) Concurrency() int {
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
func (g *Governor) Admit() bool {
	if g.ewma < highMark {
		return true
	}
	// keep 1 of every N; N grows with overload (85->keep most, 100->shed ~half)
	keep := uint64(2)
	if g.ewma >= 95 {
		keep = 2
	} else {
		keep = 4
	}
	g.tick++
	return g.tick%keep == 0
}

// Sample reads the host sampler (when configured) and updates the EWMA.
func (g *Governor) Sample() {
	if g.sampler != nil {
		g.Observe(g.sampler.CPUPercent())
	}
}
