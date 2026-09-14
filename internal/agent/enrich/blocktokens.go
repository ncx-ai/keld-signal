package enrich

// BlockTokens is one block's consumption, by the four classes a response
// reports, plus the price-weighted rollup weight.
//
// ⚠️ **`Request` is not a token count anyone should be shown.** It is the
// weight `turn_magnitude` uses to make a rollup comparable across models —
// a cache read billed at a tenth of an input token contributes a tenth — so
// rendering it beside the others, or summing it with them, produces a number
// that is wrong in a way nobody can check. What a person sees is
// `Input + Output + CacheRead + CacheCreation`; the weight is here because a
// consumer that wants to weight a rollup should not have to recompute it.
//
// ⚠️ **This carries COUNTS, never money.** The dollar figure is the client's
// own estimate over a local price table (internal/agent/pricing) and is never
// published: the client does not know the org's negotiated rates, so a price
// crossing the wire would be a guess Atlas could not distinguish from a fact.
type BlockTokens struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
	Request       int64 `json:"request"`
}

// Consumed is what a card shows: the four classes actually spent. Named rather
// than inlined at each call site so no reader has to check whether a particular
// sum quietly included the rollup weight.
func (t BlockTokens) Consumed() int64 {
	return t.Input + t.Output + t.CacheRead + t.CacheCreation
}
