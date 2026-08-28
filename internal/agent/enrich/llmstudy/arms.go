package llmstudy

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
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
//
// Callers running against a real sidecar MUST also apply WithMaxLen: without it
// the control is not running as shipped, and gliner2 will not truncate at all.
func NewEncoderArm(m enrich.Model) *EncoderArm {
	return &EncoderArm{Model: m, Source: "claude_code"}
}

// WithMaxLen binds an input token cap, exactly as the daemon does at
// daemon.go:246 (bindMaxLen). This is REQUIRED for a valid control arm, for two
// separate reasons:
//
//   - Fidelity: production always caps, so an uncapped control is not the thing
//     being challenged.
//   - Survival: gliner2's own max_len defaults to None — NO truncation — so an
//     uncapped run lets one long input allocate a multi-GB activation spike.
//     Measured on this corpus, an uncapped control drove the sidecar worker to 45
//     hard kills (kills.hard), meaning jobs were being killed mid-inference and
//     the arm's answers were failures rather than classifications.
//
// n <= 0 leaves the model untouched, and a non-sidecar Model (test fake) has
// nothing to cap and passes through — matching bindMaxLen's contract.
func (e *EncoderArm) WithMaxLen(n int) *EncoderArm {
	if n <= 0 {
		return e
	}
	c, ok := e.Model.(*sidecar.Client)
	if !ok {
		return e
	}
	cp := *e
	cp.Model = c.WithMaxLen(n)
	return &cp
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
