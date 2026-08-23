package enrich

import (
	"sort"
	"strings"
	"unicode"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"
)

// PIIResult is one personal-data scan of one prompt.
//
// Spans carry the entity vocabulary name, the offsets into the SCANNED text and
// a score — never the matched value. The backend deliberately does not return
// it (the caller already holds the text), so putting a leaked value in an HTTP
// body, and in every log line that body later touches, is structurally
// impossible.
//
// Truncated says the backend could not read the whole input and scanned a head
// window. Offsets into that head stay valid, but the tail is UNSCANNED, so a
// clean result from a truncated scan is not a clean result — it is possible
// under-detection, and the pass reports it as such (see
// SensitivityExtractor.Degraded).
type PIIResult struct {
	Spans     []Entity
	Truncated bool
}

// PIIScanner detects concrete leaked personal data in text: the presidio layer
// behind the sidecar's /pii, which needs no GLiNER2 and never touches the
// inference single-flight. It is injected (like WorkstreamAnalyzer) rather than
// derived from Model, because ml_backend "deterministic" has this service and
// no Model at all.
//
// ok=false means the scan could not be performed — the service is unreachable,
// or it failed. That is a DIFFERENT fact from "scanned, found nothing", which
// is a real answer with an empty span list, and the two must never be
// conflated: a confident "no PII here" sourced from a check that never ran is
// the worst output this facet can produce.
type PIIScanner func(text string) (PIIResult, bool)

// scanSpans turns a scan result into masked, publishable spans plus the "found"
// set sensitivityFromEntities rolls up. The values are resolved HERE, from the
// caller's own copy of the text, and immediately reduced to a mask — the raw
// value never leaves this function.
//
// Published test/example values are already suppressed by the backend's own
// gate (sidecar/app/wellknown.py), which is the single home for that list.
// What is applied here is the one gate the backend cannot know about: this
// repo's redaction placeholders (creddetect.IsPlaceholder), the same
// defense-in-depth CredentialSpans applies.
func scanSpans(text string, res PIIResult) ([]Entity, map[string]bool) {
	found := map[string]bool{}
	var spans []Entity
	// One value routinely matches two recognizers — a dashed nine-digit run is
	// both a US_SSN and, to libphonenumber, a phone. Both LABELS are recorded
	// (the rollup takes the highest severity present, which is the safe
	// direction) but only one SPAN is published, or one leaked value would be
	// reported as two. Ordering the candidates by severity first makes that
	// choice deterministic and picks the label the class was derived from.
	for _, s := range bySeverityWithinPosition(res.Spans) {
		v := spanText(text, s)
		if v == "" || creddetect.IsPlaceholder(v) {
			continue
		}
		found[s.Label] = true
		spans = appendDistinctSpan(spans, Entity{
			Label:      s.Label,
			Start:      s.Start,
			End:        s.End,
			Confidence: s.Confidence,
			Masked:     Mask(s.Label, v), // Text intentionally never set
		})
	}
	return spans, found
}

// bySeverityWithinPosition returns a copy of spans ordered by start offset and,
// for spans starting together, by the severity their label rolls up to.
func bySeverityWithinPosition(spans []Entity) []Entity {
	out := append([]Entity(nil), spans...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return severityRank(out[i].Label) < severityRank(out[j].Label)
	})
	return out
}

// severityRank is a label's position in the severity order (0 = most severe).
// An unmapped label sorts last.
func severityRank(label string) int {
	for i, rule := range SensitivityFromEntity {
		for _, trig := range rule.Triggers {
			if trig == label {
				return i
			}
		}
	}
	return len(SensitivityFromEntity)
}

// spanText resolves a span's surface text from the caller's copy. A span whose
// offsets do not index the text is dropped rather than published blind: an
// entity with no resolvable value silently passes every precision gate below,
// so a backend that returned garbage offsets would DISABLE the gates instead of
// failing visibly.
func spanText(text string, e Entity) string {
	if e.Start < 0 || e.End > len(text) || e.Start >= e.End {
		return ""
	}
	return text[e.Start:e.End]
}

// patternTypes are the entity types produced by a PATTERN — a regular format a
// deterministic recognizer can validate. They are exactly the types the
// published-value gate has ever been able to judge, and exactly the types
// GLiNER2 reports most confidently and most wrongly: 4111 1111 1111 1111 and
// 123-45-6789 come back as flawless entities because they are flawless in
// shape. See nerContributes.
var patternTypes = map[string]bool{
	"ssn": true, "credit_card": true, "email": true, "phone": true,
}

// nerContributes decides whether one GLiNER2 entity may be published, given the
// spans the PII scan returned for the SAME text.
//
// This is where the well-known gate now lives for the NER path, and it is a
// corroboration rule rather than a second copy of the published-value list.
// Rationale, because the alternative looks simpler than it is: the list is a
// precision gate that only ever fires on the pattern types, and the sidecar
// already holds it (app/wellknown.py, extended there against measured presidio
// behaviour). Keeping a Go copy for the NER path is the "duplicated but
// divergent" failure — two lists, two languages, one of them silently stale.
// So the NER's pattern-type findings are admitted only where the scan, whose
// output IS gated, found something at the same offsets. A documentation
// constant is absent from the scan by construction, so it cannot be
// corroborated, and the gate holds without a second list.
//
// Two consequences worth stating:
//   - A corroborated pattern span necessarily overlaps the scan's own span, so
//     appendDistinctSpan deduplicates it and the scan's version is what
//     publishes. The NER's real contribution is therefore person/address —
//     the types with no pattern to validate, which the list never gated either.
//   - With no scan available there is nothing to corroborate against, so
//     pattern types are DROPPED, not published ungated. Ungated is the state
//     that makes the facet noise (every engineer's transcript reporting
//     pci/phi), and the loss is reported: see Degraded.
//
// person/address are admitted uncorroborated, subject to the one shape rule the
// list encoded for them: it re-checked any nine-plus-digit value against the
// SSN and card sets whatever label it arrived under, because an NER reads
// 123-45-6789 as a name about as often as an SSN. A name has letters in it; a
// mislabelled order id does not.
func nerContributes(e Entity, value string, scan []Entity, scanned bool) bool {
	if patternTypes[e.Label] {
		return scanned && overlapsAny(e, scan)
	}
	return hasLetter(value)
}

func overlapsAny(e Entity, spans []Entity) bool {
	for _, s := range spans {
		if e.Start < s.End && s.Start < e.End {
			return true
		}
	}
	return false
}

func hasLetter(s string) bool {
	return strings.IndexFunc(s, unicode.IsLetter) >= 0
}

// appendDistinctSpan drops a span that overlaps one already collected. The NER
// and the scan cover the same entity types, so the same SSN is routinely found
// twice; publishing it twice would misrepresent one leaked value as two.
func appendDistinctSpan(spans []Entity, s Entity) []Entity {
	for _, x := range spans {
		if s.Start < x.End && x.Start < s.End {
			return spans
		}
	}
	return append(spans, s)
}

// sortSpans orders the published spans by position, so the wire is a stable
// function of the prompt rather than of which evidence source happened to be
// appended first.
func sortSpans(spans []Entity) []Entity {
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].Start < spans[j].Start })
	return spans
}
