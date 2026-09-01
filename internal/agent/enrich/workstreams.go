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

// WorkstreamStatuses is the closed published vocabulary of Labeled.Status on a
// workstream dimension: the ATTRIBUTION OUTCOME, mirroring the sidecar's
// window.REASONS.
//
// It is the SAME LIST as PriorStatuses and is defined as that list rather than
// retyped, because they are the same fact asked of two intervals — the window
// and the session before it — and two copies of one vocabulary is one more thing
// to drift. TestPriorStatusVocabularyMatchesTheSidecar pins it against
// window.REASONS for both.
//
// Only "attributed" may be read as the window's answer, and the floor behind it
// is UNCHANGED by this vocabulary reaching the wire: window.MIN_EVIDENCE
// observations (5) plus the per-dimension 0.50 share floor, exactly as before.
// What changed is that "thin" (evidence, under the floor), "tie", "no_majority"
// and "absent" now publish AS THEMSELVES instead of the dimension being deleted
// — 924 of 12,016 measured dimension-slots held real evidence and published
// nothing, 198 of them one observation short of the floor.
var WorkstreamStatuses = PriorStatuses

// WorkstreamAttributed is the ONE member of WorkstreamStatuses a consumer may
// read as the window's answer. Named rather than spelled as a literal at each
// site because a consumer that forgets to check it does not fail — it silently
// reports a `thin`/`tie`/`no_majority` leader as the answer, which is exactly
// what internal/agent/attrib's dims builder did (I6).
const WorkstreamAttributed = "attributed"

// KnownWorkstreamStatus gates a status against the published set. A value this
// binary does not recognise is sidecar version skew — the sidecar is frozen and
// shipped separately from keld-agent — and forwarding it would publish a label
// no Atlas consumer's vocabulary contains.
func KnownWorkstreamStatus(s string) bool { return KnownPriorStatus(s) }

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
//
// It returns a WindowAnalysis rather than the dimension map alone because ONE
// /analyze call answers seven questions — what the window contains, how that is
// changing, what it cost in work, what it physically did, what PATHS it
// touched, what TOOLS/PROGRAMS/SYSTEMS it used, and what the SESSION around it
// looked like — and asking again would multiply the cost of the facet for
// blocks the first call already computed.
//
// `resolved` are the facts about the job's checkout that only the daemon can
// resolve (see ResolvedFacts). They are a PARAMETER rather than something the
// analyzer works out, because the analyzer is an HTTP client and must do no
// filesystem IO — and because the analysis is the thing that should be given
// them, not a prompt preamble. The zero value is normal.
type WorkstreamAnalyzer func(path, promptID string, spanMinutes int,
	resolved ResolvedFacts) (WindowAnalysis, bool)

// WorkstreamsExtractor publishes the deterministic dimensions a cost report
// buckets by (project, branch, model, output_type, language, skill,
// tooling), plus the same window's dynamics, effort, PHYSICAL-ACTS INVENTORY,
// FILE-PATH INVENTORIES (files/directories/components) and IDENTIFIER
// INVENTORIES (harness_tools/programs/external_systems/integrations) — seven
// answers from one /analyze call. It runs no inference: the values are counted
// from tool-call metadata in the transcript window, so the pass declares itself ModelFree and
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
	// The resolved facts ride the call. `ctx.Resolved` is the zero value for a
	// caller with no cwd, and an empty repo identity is a normal answer the
	// analysis handles by writing no rows -- not something to withhold the call
	// over.
	an, ok := e.Analyze(ctx.TranscriptPath, ctx.PromptID, span, ctx.Resolved)
	if !ok {
		// Absent and empty are different facts: a failed analysis must not
		// publish "no dimensions applied", which a report would read as a real
		// answer. Fail the pass instead — the profile publishes as "partial".
		return nil, errAnalysisUnavailable
	}
	out := make(map[string]Labeled, len(an.Workstreams))
	for dim, l := range an.Workstreams {
		// EVERY dimension the analyzer answered with is carried, including the
		// ones it could not attribute. The old `l.Value == ""` skip here was the
		// Go-side half of a suppression that discarded the evidence count along
		// with the value: measured over 1,502 blocks x 8 dimensions, 924 of
		// 12,016 slots (7.7%) held real evidence and published nothing, 198 of
		// them one observation short of the floor, and `toolchain` discarded
		// more slots than it published.
		//
		// Nothing is PROMOTED by this. The floor is untouched — Status still
		// reads "attributed" only at window.MIN_EVIDENCE observations and the
		// share floor — it is simply stated instead of enforced by deletion, so
		// the consumer decides. l.Evidence and l.Status arrive already set by the
		// analyzer (sidecar.AnalyzeLabeled) and are carried through unchanged;
		// Confidence keeps its existing mapping from the dimension's share.
		l.Producer = e.Version() // stamped here, as every other pass stamps its own
		out[dim] = l
	}
	res := map[string]any{"workstreams": out}
	// The derivative half of the same call. No Producer stamp: a Dynamic is not a
	// Labeled and has no field for one, and the pass is already attributed for
	// this job through extractor_versions — a second, unparsed attribution
	// channel is what workstreams' dropped `provenance` was.
	if len(an.Dynamics) > 0 {
		res["dynamics"] = an.Dynamics
	}
	// The effort half, same call, same no-Producer reasoning. Nil rather than a
	// zeroed struct when the sidecar sent no block: see effortFrom.
	if an.Effort != nil {
		res["effort"] = an.Effort
	}
	// The physical-acts inventory, same call, same no-Producer reasoning. An empty
	// list publishes NO key: an inventory of nothing is not an answer, it is the
	// absence of one, and the two must not look alike downstream.
	if len(an.PhysicalActs) > 0 {
		res["physical_acts"] = an.PhysicalActs
	}
	// The three path inventories, same call, same no-Producer reasoning, same
	// empty-means-no-key rule as physical_acts above.
	if len(an.Files) > 0 {
		res["files"] = an.Files
	}
	if len(an.Directories) > 0 {
		res["directories"] = an.Directories
	}
	if len(an.Components) > 0 {
		res["components"] = an.Components
	}
	// The four identifier-shaped inventories, same call, same no-Producer
	// reasoning, same empty-means-no-key rule as physical_acts/files above.
	if len(an.HarnessTools) > 0 {
		res["harness_tools"] = an.HarnessTools
	}
	if len(an.Programs) > 0 {
		res["programs"] = an.Programs
	}
	if len(an.ExternalSystems) > 0 {
		res["external_systems"] = an.ExternalSystems
	}
	if len(an.Integrations) > 0 {
		res["integrations"] = an.Integrations
	}
	if len(an.NamedTerms) > 0 {
		res["named_terms"] = an.NamedTerms
	}
	// The last four, same call, same no-Producer reasoning, same
	// empty-means-no-key rule as every inventory above.
	if len(an.FileTypes) > 0 {
		res["file_types"] = an.FileTypes
	}
	if len(an.ShellVerbs) > 0 {
		res["shell_verbs"] = an.ShellVerbs
	}
	if len(an.Subagents) > 0 {
		res["subagents"] = an.Subagents
	}
	if len(an.McpServers) > 0 {
		res["mcp_servers"] = an.McpServers
	}
	// The cut-visibility map beside the eight inventories above. Empty means no
	// key, same rule: an untruncated set of inventories has nothing to report.
	if len(an.InventoryOmitted) > 0 {
		res["inventory_omitted"] = an.InventoryOmitted
	}
	// The SESSION PRIOR, same call, same no-Producer reasoning — and published
	// as its OWN key rather than merged into `workstreams` above. That is the
	// design in one line: the prior is a contrast, never a fallback, so a
	// dimension the loop above skipped for having no value stays skipped no
	// matter what the session says about it.
	if len(an.Prior) > 0 {
		res["prior"] = an.Prior
	}
	return res, nil
}
