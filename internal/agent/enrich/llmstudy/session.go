package llmstudy

import (
	"strings"
	"time"
)

// sessionFacets are classified at SESSION scope. There is no control for these —
// GLiNER2 classifies a text and has no notion of a session — so they are reported,
// never adjudicated as win/loss.
var sessionFacets = []Facet{FacetDomain, FacetFunction, FacetActivity}

// maxTopics bounds the open-vocabulary topic list.
const maxTopics = 6

// SessionAnswer is the session-tier result for one window.
//
// Summary is a LOCAL-ONLY diagnostic. It exists to judge whether the model
// genuinely comprehends the session, and is never published, transmitted, or
// proposed as an output — free prose has no deterministic redaction gate, unlike
// the enum facets and the verified topic terms.
type SessionAnswer struct {
	Session   map[Facet]string `json:"session"`
	Topics    []string         `json:"topics"`     // survived substring verification
	RawTopics []string         `json:"raw_topics"` // as emitted, for pass-rate measurement
	Dropped   []string         `json:"dropped"`    // failed verification
	Summary   string           `json:"summary"`    // LOCAL-ONLY diagnostic
	LatencyMS int64            `json:"latency_ms"`
	Valid     bool             `json:"valid"`
	Err       string           `json:"err,omitempty"`
}

// SessionRun is one arm's session-tier answers, index-aligned with the windows.
type SessionRun struct {
	Arm     string          `json:"arm"`
	Answers []SessionAnswer `json:"answers"`
}

// SessionSchema is the schema for the session tier: enum facets, bounded topic
// terms, and the diagnostic summary.
func SessionSchema() map[string]any {
	sess := map[string]any{}
	req := make([]string, 0, len(sessionFacets))
	for _, f := range sessionFacets {
		sess[string(f)] = map[string]any{"type": "string", "enum": idsOf(defsFor(f))}
		req = append(req, string(f))
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session": map[string]any{
				"type": "object", "properties": sess,
				"required": req, "additionalProperties": false,
			},
			"topics": map[string]any{
				"type": "array", "maxItems": maxTopics,
				"items": map[string]any{"type": "string"},
			},
			"summary": map[string]any{"type": "string"},
		},
		"required":             []string{"session", "topics", "summary"},
		"additionalProperties": false,
	}
}

// SessionPrompt asks what the session is about, what the user is trying to do, and
// what is being discussed. It shows the coarse digest AND the recent window,
// because the session tier needs breadth while the topic terms need the detail of
// the recent exchange.
func SessionPrompt(w Window) string {
	var b strings.Builder
	b.WriteString(`You are analysing a conversation between a software engineer and an AI coding assistant.

Two views are given. SESSION SO FAR is a compressed sample spanning the whole
session. RECENT EXCHANGE is the latest turns in detail. Lines are prefixed with the
speaker; generated code is replaced with a placeholder.

SESSION SO FAR (compressed):
`)
	b.WriteString(renderDigest(w))
	b.WriteString("\nRECENT EXCHANGE:\n")
	b.WriteString(Render(w))
	b.WriteString(`
Report three things about the SESSION AS A WHOLE:

1. "session" — choose exactly one option per field below.
2. "topics" — up to 6 short phrases (1-4 words) naming what the conversation is
   actually about. Copy each phrase VERBATIM from the conversation above. Do NOT
   paraphrase, translate, or invent wording: any phrase that does not appear
   verbatim will be discarded.
3. "summary" — one sentence, under 30 words, on what the user is trying to do.

`)
	for _, f := range sessionFacets {
		b.WriteString(labelMenu(f))
		b.WriteString("\n")
	}
	b.WriteString("Respond with JSON only.\n")
	return b.String()
}

// sessionPayload mirrors SessionSchema for decoding.
type sessionPayload struct {
	Session map[string]string `json:"session"`
	Topics  []string          `json:"topics"`
	Summary string            `json:"summary"`
}

// ClassifySession runs the session tier.
//
// It is a SEPARATE inference from Classify rather than extra fields on the Wave-1
// call. Folding the session tier into Wave 1 would change the prompt-tier arm's
// input and so invalidate its head-to-head comparison against the control; keeping
// them apart leaves that measurement untouched and makes this an independent
// capability probe, which is how the two tiers are evaluated differently.
func (l *Llama) ClassifySession(w Window) (a SessionAnswer) {
	a = SessionAnswer{Session: map[Facet]string{}}
	start := time.Now()
	defer func() { a.LatencyMS = time.Since(start).Milliseconds() }()

	var p sessionPayload
	if err := l.call(SessionPrompt(w), SessionSchema(), &p); err != nil {
		a.Err = err.Error()
		return a
	}
	for _, f := range sessionFacets {
		v := p.Session[string(f)]
		if err := validate(f, v); err != nil {
			a.Err = "session: " + err.Error()
			return a
		}
		a.Session[f] = v
	}
	a.RawTopics = p.Topics
	// Verify against BOTH views: a topic may only appear in the older digest.
	a.Topics, a.Dropped = VerifyTopics(p.Topics, renderDigest(w)+"\n"+Render(w))
	a.Summary = p.Summary
	a.Valid = true
	return a
}

// TopicPassRate is the share of emitted topic terms that survived verification. A
// low rate means the model is paraphrasing rather than naming what is actually in
// the conversation — which is exactly the failure the gate exists to catch.
func TopicPassRate(r SessionRun) float64 {
	var raw, kept int
	for _, a := range r.Answers {
		if !a.Valid {
			continue
		}
		raw += len(a.RawTopics)
		kept += len(a.Topics)
	}
	if raw == 0 {
		return 0
	}
	return float64(kept) / float64(raw)
}

// SessionLatency returns p50, p95 and max over an arm's valid session answers.
func SessionLatency(r SessionRun) (p50, p95, max int64) {
	proxy := Run{Arm: r.Arm}
	for _, a := range r.Answers {
		proxy.Answers = append(proxy.Answers, Answer{LatencyMS: a.LatencyMS, Valid: a.Valid})
	}
	return Latency(proxy)
}
