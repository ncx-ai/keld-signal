package enrich

import "time"

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

// Model is the swappable inference backend. P1 ships a deterministic
// implementation; P2 adds a GLiNER2 (ONNX or sidecar) implementation.
type Model interface {
	Classify(text string, tasks map[string][]string) map[string][]Ranked
	Entities(text string, labels map[string]string) []Entity
	Extract(text string, labels map[string]string, tasks map[string][]string) ExtractResult
}

// Profile is the full enrichment result for one prompt.
type Profile struct {
	TaskType          Labeled           `json:"task_type"`
	TaskTypeAlt       []Labeled         `json:"task_type_alt,omitempty"`
	Domain            Labeled           `json:"domain"`
	Entities          []Entity          `json:"entities,omitempty"`
	Sensitivity       Labeled           `json:"sensitivity"`
	SensitivitySpans  []Entity          `json:"sensitivity_spans,omitempty"`
	PipelineStatus    string            `json:"pipeline_status"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	EnrichedAt        time.Time         `json:"-"`
}

// JobContext threads input + per-stage outputs through the pipeline.
type JobContext struct {
	Text   string
	Source string
	Model  Model

	results map[string]map[string]any
}

// NewJobContext builds a context for one prompt.
func NewJobContext(text, source string, m Model) *JobContext {
	return &JobContext{Text: text, Source: source, Model: m, results: map[string]map[string]any{}}
}

// Set records a stage's output.
func (c *JobContext) Set(stage string, out map[string]any) { c.results[stage] = out }

// Get returns a stage's output or nil.
func (c *JobContext) Get(stage string) map[string]any { return c.results[stage] }
