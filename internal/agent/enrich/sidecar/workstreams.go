package sidecar

import "github.com/ncx-ai/keld-signal/internal/agent/enrich"

// AnalyzeLabeled is Analyze in the shape the enrichment pipeline consumes: the
// window's deterministic dimensions as enrich.Labeled, keyed by dimension. It
// satisfies enrich.WorkstreamAnalyzer, and is what the daemon wires into
// enrich.WithWorkstreams.
//
// The conversion is deliberately lossy, and this is the whole of it:
//
//   - Share -> Confidence. Share is already a 0..1 dominance fraction (the
//     proportion of the window's evidence the winning value holds; a dimension
//     below the 0.50 floor is reported unattributed rather than won), so it
//     reads exactly as a confidence and needs no rescaling.
//   - Value -> Value, unchanged.
//   - Provenance is DROPPED. It is a constant ("known:tool_inputs") for every
//     dimension the current analysis produces, so it distinguishes nothing;
//     Labeled.Producer already attributes the value to the pass. If provenance
//     ever varies per dimension (e.g. a vocabulary-matched value alongside a
//     counted one), it needs a real field — folding it into Producer would
//     make the producer string a second, unparsed data channel.
//   - Evidence is DROPPED, and this one costs information: share=1.0 over 1
//     observation and share=1.0 over 500 look identical downstream. Labeled has
//     no field for it and widening the published enrichment contract is not
//     this change's job. If Atlas needs to weight a dimension by how much of
//     the window backs it, the right move is a dedicated wire type for
//     workstreams rather than overloading Labeled.
//   - Session / WindowStart / WindowEnd stay local: window metadata, useful for
//     debugging on-device, with no business on the published payload.
//   - The response's inventory block (harness_tools, named_terms, ...) is not
//     modelled by AnalyzeResult at all, so there is structurally nothing here
//     to forward. named_terms is drawn from message TEXT and has been observed
//     to contain real person names; keep it unrepresentable.
//
// Producer is left unset: the pass stamps its own version onto every dimension
// it emits (see enrich.WorkstreamsExtractor), the same way every other pass
// does, so attribution does not depend on which analyzer supplied the map.
//
// A dimension with no dominant value (JSON null, decoding to a nil
// *Workstream) is OMITTED, never emitted as a Labeled with an empty Value: a
// published dimension whose value is "" reads downstream as a real answer.
// ok=false is propagated unchanged — a failed analysis is not an empty one.
func (c *Client) AnalyzeLabeled(path, promptID string, spanMinutes int) (map[string]enrich.Labeled, bool) {
	res, ok := c.Analyze(path, promptID, spanMinutes)
	if !ok {
		return nil, false
	}
	out := make(map[string]enrich.Labeled, len(res.Workstreams))
	for dim, w := range res.Workstreams {
		if w == nil || w.Value == "" {
			continue
		}
		out[dim] = enrich.Labeled{Value: w.Value, Confidence: w.Share}
	}
	return out, true
}
