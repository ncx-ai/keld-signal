package llmstudy

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// EncoderArm runs the production enrichment pipeline over a Model backend — used
// for the GLiNER2 control and for the gliner-guard-omni arm.
//
// It deliberately feeds PRODUCTION INPUT (the raw target prompt plus the recent
// user prompts Meta carries), never the rendered multi-turn window. This is a
// methodological requirement, not a detail: gliner2 truncates head-keeping
// (text_tokens[:max_len]) while the window places the target prompt LAST, so a
// window over the token cap would silently discard the very prompt being
// classified and sandbag the control into an artificial loss. The study asks "is a
// prompted LLM on a conversation better than what we ship", so the control must
// run as shipped.
type EncoderArm struct {
	Model  enrich.Model
	Source string
}

// NewEncoderArm wraps a Model as a study arm reporting source "claude_code".
func NewEncoderArm(m enrich.Model) *EncoderArm {
	return &EncoderArm{Model: m, Source: "claude_code"}
}

// Classify runs the real pipeline and projects the Profile onto the study facets.
func (e *EncoderArm) Classify(w Window) Answer {
	a := Answer{Labels: map[Facet]string{}}
	start := time.Now()

	meta := enrich.Meta{Tool: e.Source, RecentPrompts: w.Recent}
	p := enrich.Run(w.Target, e.Source, meta, e.Model)

	a.LatencyMS = time.Since(start).Milliseconds()
	a.Labels[FacetTaskType] = p.TaskType.Value
	a.Labels[FacetDomain] = p.Domain.Value
	a.Labels[FacetActivity] = p.Activity.Value
	a.Labels[FacetPersonal] = p.Personal.Value
	a.Labels[FacetFunction] = p.FunctionGuess.Value
	a.Labels[FacetSubcategory] = p.Subcategory.Value
	a.Valid = true
	return a
}
