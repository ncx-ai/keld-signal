package sidecar

import "github.com/ncx-ai/keld-signal/internal/agent/enrich"

// AnalyzeLabeled is Analyze in the shape the enrichment pipeline consumes: the
// window's deterministic dimensions as enrich.Labeled, plus the same window's
// dynamics as enrich.Dynamic, both keyed by dimension. It satisfies
// enrich.WorkstreamAnalyzer, and is what the daemon wires into
// enrich.WithWorkstreams.
//
// It is the ONE chokepoint where /analyze becomes publishable, which is why both
// halves convert here rather than one here and one somewhere later.
//
// The conversion is deliberately lossy, and this is the whole of it:
//
//   - Share -> Confidence. Share is already a 0..1 dominance fraction (the
//     proportion of the window's evidence the winning value holds; a dimension
//     below the 0.50 floor is reported unattributed rather than won), so it
//     reads exactly as a confidence and needs no rescaling.
//
//   - Value -> Value, unchanged.
//
//   - Provenance is DROPPED. It is a constant ("known:tool_inputs") for every
//     dimension the current analysis produces, so it distinguishes nothing;
//     Labeled.Producer already attributes the value to the pass. If provenance
//     ever varies per dimension (e.g. a vocabulary-matched value alongside a
//     counted one), it needs a real field — folding it into Producer would
//     make the producer string a second, unparsed data channel.
//
//   - Evidence is DROPPED, and this one costs information: share=1.0 over 1
//     observation and share=1.0 over 500 look identical downstream. Labeled has
//     no field for it and widening the published enrichment contract is not
//     this change's job. If Atlas needs to weight a dimension by how much of
//     the window backs it, the right move is a dedicated wire type for
//     workstreams rather than overloading Labeled.
//
//   - Session / WindowStart / WindowEnd stay local: window metadata, useful for
//     debugging on-device, with no business on the published payload.
//
//   - The response's inventory block contributes exactly ONE of its six keys:
//     `physical_acts`, converted by convertActs below. The other five are not
//     modelled by AnalyzeResult at all (see InventoryBlock), so there is
//     structurally nothing here to forward — named_terms above all is drawn from
//     message TEXT and has been observed to contain real person names; keep it
//     unrepresentable.
//
//   - The DYNAMICS block converts field-for-field (see convertDynamics) for the
//     six derived fields and drops everything else structurally — AnalyzeResult
//     models no per-side value, no timestamp, no sizer detail. What this function
//     adds on top is a VOCABULARY GATE: `status` and `reading` are closed
//     published sets gated by enrich.SchemaVersion, and the sidecar that computes
//     them is frozen and shipped separately from keld-agent (an older or newer one
//     can sit in ~/.local/bin indefinitely). A value this binary does not
//     recognise is version skew, and forwarding it would publish a label no Atlas
//     consumer's vocabulary contains — the same rule that keeps masked spans to
//     matched ids only. The whole dimension is dropped, not half-published: a
//     reading without a readable status is not interpretable.
//
// Producer is left unset: the pass stamps its own version onto every dimension
// it emits (see enrich.WorkstreamsExtractor), the same way every other pass
// does, so attribution does not depend on which analyzer supplied the map.
//
// A dimension with no dominant value (JSON null, decoding to a nil
// *Workstream) is OMITTED, never emitted as a Labeled with an empty Value: a
// published dimension whose value is "" reads downstream as a real answer.
// ok=false is propagated unchanged — a failed analysis is not an empty one.
func (c *Client) AnalyzeLabeled(path, promptID string, spanMinutes int) (enrich.WindowAnalysis, bool) {
	res, ok := c.Analyze(path, promptID, spanMinutes)
	if !ok {
		return enrich.WindowAnalysis{}, false
	}
	out := make(map[string]enrich.Labeled, len(res.Workstreams))
	for dim, w := range res.Workstreams {
		if w == nil || w.Value == "" {
			continue
		}
		out[dim] = enrich.Labeled{Value: w.Value, Confidence: w.Share}
	}
	return enrich.WindowAnalysis{
		Workstreams:  out,
		PhysicalActs: convertActs(res.Inventory.PhysicalActs),
		Dynamics:     convertDynamics(res.Dynamics),
		Effort:       convertEffort(res.Effort),
	}, true
}

// convertActs is the same VOCABULARY GATE convertDynamics and convertEffort are,
// applied per ENTRY rather than to the block. `enrich.Acts` is a closed published
// set (22 values) gated by enrich.SchemaVersion, and the sidecar that computes it
// is frozen and shipped separately from keld-agent — an older or newer one can sit
// in ~/.local/bin indefinitely — so a value this binary does not recognise is
// version skew, and forwarding it would publish a label no Atlas consumer's
// vocabulary contains.
//
// PER-ENTRY, and that is the one place the rule differs from its two siblings. A
// dynamics reading or an effort block is a single joined statement, uninterpretable
// in half, so the whole of it drops. An inventory is a list of independent items —
// "what was done" — so one unreadable item costs exactly one item, and dropping the
// list instead would discard every act this binary does understand because of one
// it does not. Nothing publishes a total against which the surviving counts could
// look inconsistent, so there is no partial-sum to be wrong.
//
// NO TRUNCATION. The sidecar publishes this level whole on purpose: its vocabulary
// is closed, so the payload is bounded at 22 entries by construction, and a top-N
// cut would only reintroduce the arbitrary-tie-at-the-boundary effect the open
// levels have to live with (see workstreams.INVENTORY's cap column). Do not add
// one here either.
//
// Nil rather than an empty slice when nothing survives — including when the whole
// block is absent, which is what a sidecar too old to compute it sends: the pass
// then publishes no key at all instead of an empty list, which would read as "we
// looked and the hour did nothing".
func convertActs(items []InventoryItem) []enrich.Act {
	var out []enrich.Act
	for _, it := range items {
		if !enrich.KnownAct(it.Value) {
			continue
		}
		out = append(out, enrich.Act{Value: it.Value, N: it.N})
	}
	return out
}

// convertDynamics is the vocabulary gate described above. It returns nil rather
// than an empty map when nothing survives (or when the sidecar sent no block at
// all), so the pass publishes no dynamics key instead of an empty object that
// would read as "we compared and found nothing".
func convertDynamics(b DynamicsBlock) map[string]enrich.Dynamic {
	var out map[string]enrich.Dynamic
	for dim, d := range b.Dimensions {
		// A null dimension is no comparison at all; publishing a zero Dynamic
		// would state a status of "", i.e. a real-looking outcome nobody can read.
		if d == nil {
			continue
		}
		if !enrich.KnownDynamicStatus(d.Status) || !enrich.KnownDynamicReading(d.Reading) {
			continue
		}
		if out == nil {
			out = make(map[string]enrich.Dynamic, len(b.Dimensions))
		}
		out[dim] = enrich.Dynamic{
			Status: d.Status, Reading: d.Reading, Changed: d.Changed,
			Turnover: d.Turnover, Decay: d.Decay,
			ConcentrationShift: d.ConcentrationShift,
		}
	}
	return out
}

// convertEffort is the same vocabulary gate convertDynamics is, applied to the
// effort block. Three closed published sets (enrich.Tempos, enrich.TempoStatuses,
// enrich.AuthoredStatuses) are checked; a value this binary does not recognise is
// version skew from a separately-shipped sidecar, and forwarding it would publish
// a label no Atlas consumer's vocabulary contains.
//
// The WHOLE block is dropped rather than half of it, and rather than being
// repaired: a share with an unreadable status is not interpretable, and silently
// substituting a status we did recognise would invent the one thing this block
// exists to state. An unreadable block is not a failed analysis either — the
// digest and dynamics beside it still publish.
//
// Nil in, nil out: a sidecar too old to compute the block must not produce a
// zeroed Effort, whose every count reads 0 and whose every status reads "".
func convertEffort(b *EffortBlock) *enrich.Effort {
	if b == nil {
		return nil
	}
	if !enrich.KnownTempo(b.Tempo) || !enrich.KnownTempoStatus(b.TempoStatus) ||
		!enrich.KnownAuthoredStatus(b.AuthoredStatus) {
		return nil
	}
	return &enrich.Effort{
		AuthoredBytes:  b.AuthoredBytes,
		AuthoringTurns: b.AuthoringTurns,
		AuthoredStatus: b.AuthoredStatus,
		FastShare:      b.FastShare,
		Gaps:           b.Gaps,
		Tempo:          b.Tempo,
		TempoStatus:    b.TempoStatus,
	}
}
