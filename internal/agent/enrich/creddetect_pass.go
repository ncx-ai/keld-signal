package enrich

import "github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"

// CredentialSpans runs the deterministic credential layer (creddetect) over
// text: pure Go, no model, no network. It returns masked spans plus the
// "found" set that sensitivityFromEntities consumes to elevate severity (e.g.
// to "secrets") without overriding a higher-severity class already present
// (e.g. phi).
func CredentialSpans(text string) ([]Entity, map[string]bool) {
	found := map[string]bool{}
	var spans []Entity
	for _, c := range creddetect.Detect(text) {
		if creddetect.IsPlaceholder(text[c.Start:c.End]) {
			continue // defense-in-depth: a placeholder that matched a regex is still a placeholder
		}
		found["api_key"] = true
		spans = append(spans, Entity{
			Label:      "api_key",
			Start:      c.Start,
			End:        c.End,
			Confidence: 1.0,
			Masked:     Mask("api_key", text[c.Start:c.End]),
		})
	}
	return spans, found
}
