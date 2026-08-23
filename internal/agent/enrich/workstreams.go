package enrich

import "errors"

// WorkstreamSpanMinutes is the window the deterministic analysis characterises:
// the hour of work ending at this prompt. It matches the span the analysis was
// developed and measured against (see sidecar/app/analysis/workstreams.py).
const WorkstreamSpanMinutes = 60

// errAnalysisUnavailable marks the pass failed because the window analysis
// could not be obtained at all — a different fact from "the analysis ran and
// found no dominant value", which is a real answer and succeeds with no
// dimensions. Only the former downgrades the profile to "partial".
var errAnalysisUnavailable = errors.New("workstreams: window analysis unavailable")

// WorkstreamAnalyzer resolves the deterministic workstream dimensions for the
// window of spanMinutes ending at promptID in the transcript at path.
//
// It takes COORDINATES, never text — the same rule the enrichment hook and
// spool.Pointer follow. ok=false means the analysis could not be produced
// (backend absent, transcript/prompt not found, transport failure); a true
// return with no dimensions means the window genuinely had no dominant value
// for any dimension.
type WorkstreamAnalyzer func(path, promptID string, spanMinutes int) (map[string]Labeled, bool)

// WorkstreamsExtractor publishes the deterministic dimensions a cost report
// buckets by (project, branch, model, output_type, language, workflow,
// tooling). It runs no inference: the values are counted from tool-call
// metadata in the transcript window, so the pass declares itself ModelFree and
// must still run when ctx.Model is nil (ml_backend "deterministic", or a
// sidecar that has no model resident). Gating it on inference readiness would
// defeat the point of having a model-free facet.
//
// It is also AlwaysRun: the dimensions describe the WINDOW, not this turn, so
// they stay valid for a turn the content gate filters out.
//
// Analyze is injected so the pass is testable without a sidecar; the daemon
// wires it to sidecar.Client.AnalyzeLabeled (see enrich.WithWorkstreams).
type WorkstreamsExtractor struct {
	Analyze WorkstreamAnalyzer
	// SpanMinutes overrides the window; <= 0 uses WorkstreamSpanMinutes.
	SpanMinutes int
}

func (WorkstreamsExtractor) Name() string    { return "workstreams" }
func (WorkstreamsExtractor) Version() string { return versioned("workstreams") }
func (WorkstreamsExtractor) ModelFree() bool { return true }
func (WorkstreamsExtractor) AlwaysRun() bool { return true }

func (e WorkstreamsExtractor) Run(ctx *JobContext) (map[string]any, error) {
	// No analyzer wired, or no coordinates to analyze (inline text, the eval
	// harness): there is nothing to ask, and asking anyway can only 404.
	if e.Analyze == nil || ctx.TranscriptPath == "" || ctx.PromptID == "" {
		return nil, errAnalysisUnavailable
	}
	span := e.SpanMinutes
	if span <= 0 {
		span = WorkstreamSpanMinutes
	}
	ws, ok := e.Analyze(ctx.TranscriptPath, ctx.PromptID, span)
	if !ok {
		// Absent and empty are different facts: a failed analysis must not
		// publish "no dimensions applied", which a report would read as a real
		// answer. Fail the pass instead — the profile publishes as "partial".
		return nil, errAnalysisUnavailable
	}
	out := make(map[string]Labeled, len(ws))
	for dim, l := range ws {
		// A dimension the window could not attribute is reported as absent, not
		// as a dimension whose value is the empty string.
		if l.Value == "" {
			continue
		}
		l.Producer = e.Version() // stamped here, as every other pass stamps its own
		out[dim] = l
	}
	return map[string]any{"workstreams": out}, nil
}
