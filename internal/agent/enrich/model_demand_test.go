package enrich

import (
	"sync/atomic"
	"testing"
)

// demandCounter records every inference the pipeline asks a Model for.
type demandCounter struct {
	classify atomic.Int64
	extract  atomic.Int64
	entities atomic.Int64
}

func (m *demandCounter) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	m.classify.Add(1)
	out := make(map[string][]Ranked, len(tasks))
	for k, v := range tasks {
		if len(v) > 0 {
			out[k] = []Ranked{{Label: v[0], Confidence: 0.9}}
		}
	}
	return out
}

func (m *demandCounter) Extract(string, map[string]string, map[string][]string) ExtractResult {
	m.extract.Add(1)
	return ExtractResult{}
}

func (m *demandCounter) Entities(string, map[string]string) []Entity {
	m.entities.Add(1)
	return nil
}

// TestBuiltInPipelineStillDemandsAModel is a STANDING MEASUREMENT, not a
// behaviour assertion, and it exists because a claim to the contrary was acted
// on: that in v2 "the model is loaded by nothing", so the ~1.9 GB download is
// dead weight on every install.
//
// It is not true of this pipeline. Wave 1's task_type, domain, activity_type,
// personal and function_guess and Wave 2's subcategory all classify through
// ctx.Model, and sensitivity's NER half extracts through it. On a plain coding
// prompt with default settings that is SIX inferences per prompt (measured
// here, not estimated) — so in ml_backend "auto" the weights are genuinely
// required, and on-demand provisioning defers the download to the first prompt
// rather than eliminating it.
//
// If this count ever reaches zero — the model-backed facets removed in favour
// of deterministic ones — then "auto" no longer implies a download at all,
// because provisioning hangs off Worker's warmup and nothing would warm. That
// is a deliberate, documented consequence and this test is where it surfaces:
// it fails, and whoever made the passes model-free revisits what "auto" means.
func TestBuiltInPipelineStillDemandsAModel(t *testing.T) {
	m := &demandCounter{}
	p := Run(
		"Please refactor the auth handler in internal/api to return a typed error.",
		"claude_code",
		Meta{Repo: "/tmp/repo", Tool: "claude_code"},
		m,
	)

	total := m.classify.Load() + m.extract.Load() + m.entities.Load()
	t.Logf("default pipeline inferences: classify=%d extract=%d entities=%d total=%d (status=%s)",
		m.classify.Load(), m.extract.Load(), m.entities.Load(), total, p.PipelineStatus)
	if total == 0 {
		t.Fatal("no pass asked the Model for anything: the model-backed facets are gone, " +
			"so ml_backend \"auto\" no longer needs the GLiNER2 weights — revisit what \"auto\" means " +
			"(and see modelProvisioner: nothing would warm, so nothing would ever be fetched)")
	}
}
