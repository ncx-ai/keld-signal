package llmstudy

import (
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// Facet names a scored classification facet. Values match both the JSON property
// names in the schema and the facet keys in the report.
type Facet string

const (
	FacetTaskType    Facet = "task_type"
	FacetDomain      Facet = "domain"
	FacetActivity    Facet = "activity_type"
	FacetPersonal    Facet = "personal"
	FacetFunction    Facet = "function_guess"
	FacetSubcategory Facet = "subcategory"
)

// waveOneFacets are classified in a single call, mirroring the pipeline's Wave 1.
var waveOneFacets = []Facet{FacetTaskType, FacetDomain, FacetActivity, FacetPersonal, FacetFunction}

// PrimaryFacets are the facets the study is designed to decide on — the ones
// measuring 0.42-0.53 on the gold set. The rest are secondary readouts.
var PrimaryFacets = []Facet{FacetDomain, FacetTaskType, FacetSubcategory}

// defsFor returns the live vocabulary for a facet, read from the enrich package so
// the study can never drift onto a stale taxonomy.
func defsFor(f Facet) []enrich.LabelDef {
	switch f {
	case FacetTaskType:
		return enrich.TaskTypeDefs
	case FacetDomain:
		return enrich.DomainDefs
	case FacetActivity:
		return enrich.Activities
	case FacetPersonal:
		return enrich.Personal
	case FacetFunction:
		return enrich.Functions
	}
	return nil
}

// subcatAll returns every function's subcategory definitions, for description lookup.
func subcatAll() map[string][]enrich.LabelDef { return enrich.Subcats }

func idsOf(defs []enrich.LabelDef) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}

// WaveOneSchema is the JSON schema for the five independent facets.
func WaveOneSchema() map[string]any {
	props := map[string]any{}
	req := make([]string, 0, len(waveOneFacets))
	for _, f := range waveOneFacets {
		props[string(f)] = map[string]any{"type": "string", "enum": idsOf(defsFor(f))}
		req = append(req, string(f))
	}
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             req,
		"additionalProperties": false,
	}
}

// SubcategorySchema is the Wave-2 schema for one function's subcategories, or nil
// when that function has none.
func SubcategorySchema(fn string) map[string]any {
	defs, ok := enrich.Subcats[fn]
	if !ok || len(defs) == 0 {
		return nil
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			string(FacetSubcategory): map[string]any{"type": "string", "enum": idsOf(defs)},
		},
		"required":             []string{string(FacetSubcategory)},
		"additionalProperties": false,
	}
}

// labelMenu renders a facet's options as "id — description" lines.
func labelMenu(f Facet) string {
	var b strings.Builder
	b.WriteString(string(f))
	b.WriteString(":\n")
	for _, d := range defsFor(f) {
		b.WriteString("  - ")
		b.WriteString(d.ID)
		b.WriteString(" — ")
		b.WriteString(d.Text)
		b.WriteString("\n")
	}
	return b.String()
}

const promptPreamble = `You are classifying one prompt from a conversation between a software engineer and an AI coding assistant.

Below is the recent conversation, oldest first. Lines are prefixed with the speaker: "user:", "assistant:", or "tool:" (a tool the assistant invoked; "(xN)" means it was called N times in a row). Generated code has been replaced with a placeholder.

CONVERSATION:
`

const promptRules = `
Classify ONLY the final user turn (repeated below as TARGET PROMPT). The earlier
conversation is context to help you interpret it — do not classify the session as a
whole. If the target prompt is terse ("do it", "commit"), use the conversation to
determine what it refers to.

TARGET PROMPT:
`

// WaveOnePrompt builds the Wave-1 classification prompt.
func WaveOnePrompt(w Window) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	b.WriteString(Render(w))
	b.WriteString(promptRules)
	b.WriteString(w.Target)
	b.WriteString("\n\nChoose exactly one option for each of the following:\n\n")
	for _, f := range waveOneFacets {
		b.WriteString(labelMenu(f))
		b.WriteString("\n")
	}
	b.WriteString("Respond with JSON only.\n")
	return b.String()
}

// SubcategoryPrompt builds the Wave-2 prompt, conditioned on the function id.
func SubcategoryPrompt(w Window, fn string) string {
	var b strings.Builder
	b.WriteString(promptPreamble)
	b.WriteString(Render(w))
	b.WriteString(promptRules)
	b.WriteString(w.Target)
	b.WriteString("\n\nThe business function is already determined to be \"")
	b.WriteString(fn)
	b.WriteString("\". Choose exactly one subcategory:\n\n")
	for _, d := range enrich.Subcats[fn] {
		b.WriteString("  - ")
		b.WriteString(d.ID)
		b.WriteString(" — ")
		b.WriteString(d.Text)
		b.WriteString("\n")
	}
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}
