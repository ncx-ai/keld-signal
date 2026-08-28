package enrich

import (
	"sort"

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

// appendDistinctSpan drops a span that overlaps one already collected. One
// value routinely matches two recognizers — a dashed nine-digit run is both a
// US_SSN and, to libphonenumber, a phone — and the credential layer scans the
// same text independently of the PII scan, so the same value is routinely found
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
