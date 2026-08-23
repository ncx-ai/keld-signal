package enrich

import (
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/piidetect"
)

// PIISpans runs the deterministic personal-identifier layer (piidetect) over
// text: pure Go, no model, no network — the sibling of CredentialSpans. It
// returns masked spans plus the "found" set that sensitivityFromEntities rolls
// up, using the EXISTING entity vocabulary (ssn, credit_card, email), so the
// severity table in labels.go consumes them unchanged.
//
// Without this layer ssn/credit_card/email had exactly one source, the GLiNER2
// NER, which made the two highest sensitivity classes (phi, pci) structurally
// unreachable whenever no model was present.
func PIISpans(text string) ([]Entity, map[string]bool) {
	found := map[string]bool{}
	var spans []Entity
	for _, p := range piidetect.Detect(text) {
		v := text[p.Start:p.End]
		// Defense-in-depth, mirroring CredentialSpans: Detect already applies
		// the well-known gate, and a placeholder that matched a regex is still
		// a placeholder.
		if creddetect.IsPlaceholder(v) || piidetect.IsWellKnown(p.Label, v) {
			continue
		}
		found[p.Label] = true
		spans = append(spans, Entity{
			Label:      p.Label,
			Start:      p.Start,
			End:        p.End,
			Confidence: 1.0,
			Masked:     Mask(p.Label, v), // Text intentionally never set
		})
	}
	return spans, found
}

// DeterministicSensitiveEntities is the whole model-free half of the
// sensitivity facet: credentials (creddetect) unioned with personal identifiers
// (piidetect). Pure and cheap — regexes over one prompt — which is why
// SensitivityExtractor.Degraded may call it a second time to judge whether the
// answer it produced needed the NER at all.
func DeterministicSensitiveEntities(text string) ([]Entity, map[string]bool) {
	spans, found := CredentialSpans(text)
	pii, piiFound := PIISpans(text)
	for _, s := range pii {
		spans = appendDistinctSpan(spans, s)
	}
	for label := range piiFound {
		found[label] = true
	}
	return spans, found
}

// appendDistinctSpan drops a span that overlaps one already collected. The NER
// and the deterministic layers now cover the same entity types, so the same SSN
// is routinely found twice; publishing it twice would misrepresent one leaked
// value as two.
func appendDistinctSpan(spans []Entity, s Entity) []Entity {
	for _, x := range spans {
		if s.Start < x.End && x.Start < s.End {
			return spans
		}
	}
	return append(spans, s)
}
