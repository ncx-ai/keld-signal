package promptlog

import "strings"

// modelPrice is USD per 1,000,000 tokens for a model family.
type modelPrice struct {
	in, out, cacheWrite, cacheRead float64
}

// prices is a small, best-effort per-family price table (USD / 1M tokens). These
// are approximate list prices and MAY DRIFT as Anthropic pricing changes — cost is
// a derived, flagged field (see the spec). Model names are matched by family
// substring so version suffixes (e.g. claude-opus-4-8) still resolve.
var prices = map[string]modelPrice{
	"opus":   {in: 15, out: 75, cacheWrite: 18.75, cacheRead: 1.5},
	"sonnet": {in: 3, out: 15, cacheWrite: 3.75, cacheRead: 0.3},
	"haiku":  {in: 0.8, out: 4, cacheWrite: 1, cacheRead: 0.08},
}

// costUSD returns the derived USD cost for a request's token counts, or
// (0, false) when the model family is unknown. cacheCreate/cacheRead are the
// cache-write / cache-read token counts.
func costUSD(model string, inTok, outTok, cacheCreateTok, cacheReadTok int) (float64, bool) {
	m := strings.ToLower(model)
	for family, p := range prices {
		if strings.Contains(m, family) {
			const perM = 1_000_000.0
			cost := float64(inTok)/perM*p.in +
				float64(outTok)/perM*p.out +
				float64(cacheCreateTok)/perM*p.cacheWrite +
				float64(cacheReadTok)/perM*p.cacheRead
			return cost, true
		}
	}
	return 0, false
}
