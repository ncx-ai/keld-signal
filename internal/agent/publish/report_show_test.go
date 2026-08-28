package publish

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func lb(v string, c float64) enrich.Labeled { return enrich.Labeled{Value: v, Confidence: c} }
func n2(v string, n int) enrich.NameCount   { return enrich.NameCount{Value: v, N: n} }
func p2(v string, n int) enrich.PathCount   { return enrich.PathCount{Value: v, N: n} }
func bp(b bool) *bool                       { return &b }
func fp(v float64) *float64                 { return &v }
func ip(v int64) *int64                     { return &v }

// Three real-shaped windows, printed so the rendering can be read rather than
// asserted into. Values are taken from real measured windows.
func TestShowReportInstances(t *testing.T) {
	cases := map[string]Enrichment{
		"a busy backend hour, everything attributed": {
			TaskType: lb("code_generation", 0.81), Domain: lb("software_engineering", 0.74),
			Sensitivity: lb("none", 1.0),
			Workstreams: map[string]enrich.Labeled{
				"repo": lb("github.com/ncx-ai/keld-atlas", 1.0), "project": lb("keld-atlas", 1.0),
				"branch": lb("main", 1.0), "model": lb("claude-opus-4-8", 1.0),
				"output_type": lb("code", 0.964), "language": lb("Python", 0.929),
				"tooling": lb("infrastructure", 0.917),
			},
			Dynamics: map[string]enrich.Dynamic{
				"branch":      {Status: "compared", Reading: "steady", Changed: bp(false), Turnover: fp(0), Decay: fp(0)},
				"language":    {Status: "baseline_thin"},
				"output_type": {Status: "baseline_thin"},
				"skill":       {Status: "both_absent", Changed: bp(false)},
			},
			Effort: &enrich.Effort{AuthoredBytes: ip(20633), AuthoringTurns: 26,
				AuthoredStatus: "attributed", FastShare: fp(0.615), Gaps: 122, Tempo: "steered"},
			PhysicalActs:     []enrich.Act{{Value: "edit", N: 41}, {Value: "read", N: 33}, {Value: "test", N: 12}},
			Components:       []enrich.PathCount{p2("services/api/app", 34), p2("services/api/tests", 11)},
			Files:            []enrich.PathCount{p2("services/api/app/services/telemetry.py", 14)},
			HarnessTools:     []enrich.NameCount{n2("Bash", 30), n2("Edit", 22), n2("Read", 18)},
			Programs:         []enrich.NameCount{n2("git", 9), n2("pytest", 6), n2("docker", 3)},
			ShellVerbs:       []enrich.NameCount{n2("git commit", 6), n2("pytest -q", 4)},
			FileTypes:        []enrich.NameCount{n2(".py", 182), n2(".tsx", 91)},
			ExternalSystems:  []enrich.NameCount{n2("api.anthropic.com", 4), n2("github.com", 2)},
			Integrations:     []enrich.NameCount{n2("notion-fetch", 1)},
			NamedTerms:       []enrich.NameCount{n2("ACME", 12), n2("Together.ai", 5)},
			Subagents:        []enrich.NameCount{n2("general-purpose", 3)},
			InventoryOmitted: map[string]int{"programs": 10, "named_terms": 1},
			Prior: map[string]enrich.Prior{
				"branch":   {Value: "main", Share: 1, Evidence: 184, Status: "attributed", Agrees: bp(true), Departure: fp(0), Novel: bp(false)},
				"language": {Value: "Python", Share: 0.716, Evidence: 74, Status: "attributed", Agrees: bp(true), Departure: fp(0.213), Novel: bp(false)},
			},
			PipelineStatus: "enriched",
		},
		"a thin window in a directory that is not a repo": {
			Sensitivity: lb("none", 1.0),
			Workstreams: map[string]enrich.Labeled{
				"project": lb("notes", 1.0), "model": lb("claude-opus-4-8", 1.0),
			},
			Dynamics:       map[string]enrich.Dynamic{"branch": {Status: "both_absent", Changed: bp(false)}},
			Effort:         &enrich.Effort{AuthoringTurns: 2, AuthoredStatus: "absent", Tempo: "autonomous"},
			PhysicalActs:   []enrich.Act{{Value: "read", N: 4}},
			FileTypes:      []enrich.NameCount{n2(".pdf", 3)},
			PipelineStatus: "enriched",
			FacetsSkipped:  []string{"task_type", "domain", "subcategory"},
		},
		"a degraded sensitivity scan, and a branch that switched": {
			Sensitivity: lb("none", 1.0),
			Workstreams: map[string]enrich.Labeled{
				"repo": lb("github.com/ncx-ai/keld-signal", 1.0), "project": lb("keld-signal", 1.0),
				"branch": lb("design-sync", 0.83), "language": lb("TypeScript", 0.85),
				"output_type": lb("code", 0.83), "model": lb("claude-opus-4-8", 1.0),
			},
			Dynamics: map[string]enrich.Dynamic{
				"branch":   {Status: "compared", Reading: "switched", Changed: bp(true), Turnover: fp(0.62), Decay: fp(0.41)},
				"language": {Status: "compared", Reading: "narrowing", Changed: bp(false), Turnover: fp(0.05)},
			},
			Effort: &enrich.Effort{AuthoredBytes: ip(16389), AuthoringTurns: 31,
				AuthoredStatus: "attributed", FastShare: fp(0.42), Tempo: "steered"},
			PhysicalActs:   []enrich.Act{{Value: "edit", N: 52}, {Value: "read", N: 19}},
			Components:     []enrich.PathCount{p2("services/web/app", 61)},
			Prior:          map[string]enrich.Prior{"branch": {Value: "main", Share: 0.9, Evidence: 210, Status: "attributed", Agrees: bp(false), Departure: fp(-0.4), Novel: bp(true)}},
			PipelineStatus: "partial",
			FacetsDegraded: []string{"sensitivity"},
		},
	}
	for name, e := range cases {
		summary, report := renderReport(e.reportSource())
		t.Logf("\n════════ %s\n[summary] %s\n%s\n", name, summary, report)
	}
}
