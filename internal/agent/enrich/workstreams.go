package enrich

import "errors"

// WorkstreamSpanMinutes is the window the deterministic analysis characterises:
// the hour of work ending at this prompt. It matches the span the analysis was
// developed and measured against (see sidecar/app/analysis/workstreams.py).
const WorkstreamSpanMinutes = 60

// workstreamAnalyzableSources are the sources whose transcripts the window
// analysis can actually read. It resolves a prompt by Claude-Code JSONL shape —
// a line with "type":"user" and a matching "uuid" (see
// sidecar/app/analysis/analyze.py:_prompt_time and transcript.py:iter_turns) —
// which Cowork writes too, since it is Claude Code in a sandbox.
//
// Codex ("<sessionID>#<ordinal>") and Gemini ("<sessionId>########<ordinal>")
// key their prompts differently over differently-shaped files, so the analysis
// cannot find the prompt and answers 404. Left ungated, that failure would run
// the pass, fail it, and downgrade EVERY Codex/Gemini job to
// pipeline_status:"partial" in the default ml_backend mode — corrupting an
// operational signal, one wasted sidecar round-trip at a time, for a facet that
// could never have been produced. Extend this set only alongside a reader in
// the analysis that resolves that source's prompt ids.
var workstreamAnalyzableSources = map[string]bool{"claude_code": true, "cowork": true}

// WorkstreamsEligible reports whether a source's transcripts can be read by the
// window analysis (mirrors ContextEligible's shape).
func WorkstreamsEligible(source string) bool { return workstreamAnalyzableSources[source] }

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
