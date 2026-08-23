package enrich

import (
	"context"
	"sync"
	"time"
)

// Labeled is a single classification result with provenance.
type Labeled struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Producer   string  `json:"producer,omitempty"`
}

// Ranked is one scored candidate label.
type Ranked struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
}

// Entity is a detected span. For sensitive spans, Text is cleared and Masked is
// set so the raw value never crosses the wire.
type Entity struct {
	Text       string  `json:"text,omitempty"`
	Label      string  `json:"label"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Confidence float64 `json:"confidence"`
	Masked     string  `json:"masked,omitempty"`
}

// ExtractResult is the composed output of a GLiNER2-style extract call.
type ExtractResult struct {
	Entities []Entity
	Results  map[string][]Ranked
}

// Model is the swappable inference backend. The GLiNER2 sidecar is the only
// production implementation — enrichment is ML-only, with no deterministic
// fallback (see the daemon's mlBackend/Worker readiness gate).
type Model interface {
	Classify(text string, tasks map[string][]string) map[string][]Ranked
	Entities(text string, labels map[string]string) []Entity
	Extract(text string, labels map[string]string, tasks map[string][]string) ExtractResult
}

// ContextModel is an optional Model capability: return a copy of the backend
// bound to ctx, so a per-pass deadline can abort that pass's in-flight
// inference instead of letting it run on unattended. The sidecar client
// implements it (its HTTP request carries the context); test fakes and other
// backends have no call to cancel and may omit it, in which case a timed-out
// pass is abandoned rather than actively aborted.
type ContextModel interface {
	Model
	WithModelContext(ctx context.Context) Model
}

// MultiTask is a multi-label classification request: score each label
// independently (sigmoid) and keep those at/above Threshold. Descriptions
// optionally maps a label to a GLiNER2 hint that steers the match by meaning; a
// label absent from the map carries no hint. Empty ⇒ the wire stays the bare
// label-list form, so passes without authored descriptions are unaffected.
type MultiTask struct {
	Labels       []string
	Threshold    float64
	Descriptions map[string]string
}

// DescribedTask is a single-label (softmax) classification carrying optional
// per-label GLiNER2 hints — the single-label counterpart of MultiTask's
// Descriptions. Labels is the full readable label set; Descriptions maps a
// label to its hint (subset; omitted labels carry no hint).
type DescribedTask struct {
	Labels       []string
	Descriptions map[string]string
}

// DescribedLabelModel is an OPTIONAL Model capability (like MultiLabelModel):
// single-label classification with per-label descriptions. Custom single_label
// passes use it when the backend implements it AND the pass has any authored
// value descriptions; otherwise they fall back to Classify (no hints), so the
// behavior of description-less passes and built-ins is unchanged.
type DescribedLabelModel interface {
	Model
	ClassifyDescribed(text string, tasks map[string]DescribedTask) map[string][]Ranked
}

// MultiLabelModel is an OPTIONAL Model capability (like ContextModel): true
// multi-label classification. The GLiNER2 sidecar implements it; fakes/other
// backends may too. Custom multi_label passes require it and degrade (skip with
// an empty result) when the backend lacks it.
type MultiLabelModel interface {
	Model
	ClassifyMulti(text string, tasks map[string]MultiTask) map[string][]Ranked
}

// HealthFunc reports whether the sidecar backend is currently usable. Used by
// the daemon's Supervisor to poll sidecar health.
type HealthFunc func() bool

// Profile is the full enrichment result for one prompt.
type Profile struct {
	TaskType          Labeled           `json:"task_type"`
	TaskTypeAlt       []Labeled         `json:"task_type_alt,omitempty"`
	Domain            Labeled           `json:"domain"`
	Entities          []Entity          `json:"entities,omitempty"`
	Sensitivity       Labeled           `json:"sensitivity"`
	SensitivitySpans  []Entity          `json:"sensitivity_spans,omitempty"`
	Activity          Labeled           `json:"activity_type"`
	Personal          Labeled           `json:"personal"`
	FunctionGuess     Labeled           `json:"function_guess"`
	SpeechAct         Labeled           `json:"speech_act"`
	SpeechActAlt      []Labeled         `json:"speech_act_alt,omitempty"`
	Subcategory       Labeled           `json:"subcategory"`
	SubcategoryAlt    []Labeled         `json:"subcategory_alt,omitempty"`
	// Workstreams are the deterministic window dimensions (project, branch,
	// model, ...) counted from tool-call metadata rather than classified — see
	// WorkstreamsExtractor. Keyed by dimension; a dimension the window could not
	// attribute is ABSENT rather than present-and-empty.
	Workstreams       map[string]Labeled `json:"workstreams,omitempty"`
	PipelineStatus    string            `json:"pipeline_status"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	Custom            map[string]CustomResult `json:"custom,omitempty"`
	EnrichedAt        time.Time         `json:"-"`
}

// CustomResult is one org-defined (custom) pass's output, shaped by kind. It
// rides alongside the built-in Profile fields and is emitted on the enrichment
// wire so Atlas can persist/surface arbitrary org-defined passes.
type CustomResult struct {
	Kind       string    `json:"kind"`                 // single_label | multi_label | entity
	Value      string    `json:"value,omitempty"`      // single_label
	Confidence float64   `json:"confidence,omitempty"` // single_label
	Alt        []Labeled `json:"alt,omitempty"`        // single_label alternates
	Values     []Labeled `json:"values,omitempty"`     // multi_label tags
	Entities   []Entity  `json:"entities,omitempty"`   // entity (masked)
	Producer   string    `json:"producer,omitempty"`
}

// JobContext threads input + per-stage outputs through the pipeline.
type JobContext struct {
	Text   string
	Source string
	Meta   Meta
	Model  Model

	// TranscriptPath and PromptID are the job's COORDINATES (never text): the
	// transcript file and the prompt within it. Model-free passes that
	// characterise the surrounding window rather than this prompt's text (see
	// WorkstreamsExtractor) need them; the daemon threads them from queue.Job
	// via WithCoordinates. They are empty for callers with no transcript
	// (inline text, the eval harness), which such a pass must tolerate.
	TranscriptPath string
	PromptID       string

	// res is shared by pointer with any per-stage context derived via
	// withModel, so a stage sees the same committed outputs.
	res *jobResults
}

// jobResults holds the stage outputs behind a lock. A pass that exceeds its
// deadline is abandoned, not killed — its goroutine may still call Get
// (conditioned passes do) while the pipeline commits a later stage's output, so
// the map needs guarding even though the pipeline itself never fans out.
type jobResults struct {
	mu sync.RWMutex
	m  map[string]map[string]any
}

// NewJobContext builds a context for one prompt.
func NewJobContext(text, source string, meta Meta, m Model) *JobContext {
	return &JobContext{Text: text, Source: source, Meta: meta, Model: m,
		res: &jobResults{m: map[string]map[string]any{}}}
}

// withModel returns a shallow copy bound to a different backend, sharing this
// context's committed results. Used to give one pass a deadline-bound model.
func (c *JobContext) withModel(m Model) *JobContext {
	return &JobContext{Text: c.Text, Source: c.Source, Meta: c.Meta, Model: m,
		TranscriptPath: c.TranscriptPath, PromptID: c.PromptID, res: c.res}
}

// Set commits a stage's output. Called by the pipeline between stages.
func (c *JobContext) Set(stage string, out map[string]any) {
	c.res.mu.Lock()
	defer c.res.mu.Unlock()
	c.res.m[stage] = out
}

// Get returns a stage's output or nil.
func (c *JobContext) Get(stage string) map[string]any {
	c.res.mu.RLock()
	defer c.res.mu.RUnlock()
	return c.res.m[stage]
}
