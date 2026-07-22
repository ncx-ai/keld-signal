package enrich

import (
	"os"
	"strings"
)

// A4 (KELD_ENRICH_COMPOSITIONAL_FUNCTION): stop topically classifying
// function_guess for interactive coding tools. Those tools are ~always
// engineering (the user is doing eng work regardless of the software's subject
// domain), so we set function_guess = eng structurally and let the (already
// function-conditioned) subcategory pass classify over the eng subcategories.
// Generic/unknown tools keep the topical classifier. This sidesteps the
// bi-encoder's inability to separate "doing X" from "coding software about X" —
// the ceiling that label/prior tweaks (A1/A2) could not break. Validated as a
// strict win (leakage 0.375→0.000, confound accuracy 0.773→0.909, gold-only
// flat), so it is on by default; set KELD_ENRICH_COMPOSITIONAL_FUNCTION to
// "off", "0", or "false" (case-insensitive) to disable it and restore topical
// classification for coding tools.
func compositionalFunctionEnabled() bool {
	switch strings.ToLower(os.Getenv("KELD_ENRICH_COMPOSITIONAL_FUNCTION")) {
	case "off", "0", "false":
		return false
	default:
		return true
	}
}

// codingTools are the interactive coding tools A4 treats as ~always eng.
var codingTools = map[string]bool{"claude_code": true, "codex": true, "gemini": true}

// funcGuessExtractor replaces the plain passExtractor for function_guess. For a
// coding tool under A4 it emits eng directly; otherwise it runs the normal
// topical classification over Functions.
type funcGuessExtractor struct{}

func (funcGuessExtractor) Name() string    { return "function_guess" }
func (funcGuessExtractor) Version() string { return versioned("function_guess") }

func (e funcGuessExtractor) Run(ctx *JobContext) (map[string]any, error) {
	if compositionalFunctionEnabled() && codingTools[ctx.Meta.Tool] {
		return map[string]any{
			"function_guess":     Labeled{Value: "eng", Confidence: 1.0, Producer: e.Version()},
			"function_guess_alt": []Labeled(nil),
		}, nil
	}
	top, alts := classifyPass(ctx, "function_guess", Functions)
	return map[string]any{"function_guess": top, "function_guess_alt": alts}, nil
}
