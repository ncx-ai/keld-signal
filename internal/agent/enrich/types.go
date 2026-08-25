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
	TaskType         Labeled   `json:"task_type"`
	TaskTypeAlt      []Labeled `json:"task_type_alt,omitempty"`
	Domain           Labeled   `json:"domain"`
	Entities         []Entity  `json:"entities,omitempty"`
	Sensitivity      Labeled   `json:"sensitivity"`
	SensitivitySpans []Entity  `json:"sensitivity_spans,omitempty"`
	Activity         Labeled   `json:"activity_type"`
	Personal         Labeled   `json:"personal"`
	FunctionGuess    Labeled   `json:"function_guess"`
	Subcategory      Labeled   `json:"subcategory"`
	SubcategoryAlt   []Labeled `json:"subcategory_alt,omitempty"`
	// Workstreams are the deterministic window dimensions (project, branch,
	// model, ...) counted from tool-call metadata rather than classified — see
	// WorkstreamsExtractor. Keyed by dimension; a dimension the window could not
	// attribute is ABSENT rather than present-and-empty.
	Workstreams map[string]Labeled `json:"workstreams,omitempty"`
	// Dynamics is the same window's DERIVATIVE, keyed by dimension: how the
	// recent slice differs from the baseline before it (see Dynamic). It comes
	// from the same /analyze call Workstreams does and is likewise model-free, so
	// it is published in ml_backend "deterministic" too. Absent when the analysis
	// computed no comparison.
	Dynamics map[string]Dynamic `json:"dynamics,omitempty"`
	// Effort is what the same window COST in work: the bytes its edits authored
	// and how fast its turns came (see Effort). Third half of the same /analyze
	// call Workstreams and Dynamics come from, model-free like both, so it is
	// published in ml_backend "deterministic" too. Absent when the analysis
	// produced no block.
	Effort *Effort `json:"effort,omitempty"`
	// PhysicalActs is what the same window physically DID: an inventory of acts
	// against the closed Acts vocabulary, with counts (see Act). Fourth half of
	// the same /analyze call, model-free like the other three, so it is published
	// in ml_backend "deterministic" too.
	//
	// A LIST, not a single value, and that is the measured finding rather than a
	// convenience: assessed as an eighth entry in Workstreams the level reaches
	// coverage 0.185 against a 0.70 bar, not for lack of evidence (97.8% of
	// windows, median 34 observations) but because an hour reads AND edits AND
	// tests — top-act share p50 0.403 over p50 7 distinct acts. See Acts.
	// Absent, never an empty list, when the analysis produced none.
	PhysicalActs []Act `json:"physical_acts,omitempty"`
	// Files, Directories and Components are what the same window physically
	// TOUCHED: inventories of the `file`/`dir`/`component` levels, with counts
	// (see PathCount). Same /analyze call as PhysicalActs, model-free like it,
	// so all three publish in ml_backend "deterministic" too.
	//
	// OPEN vocabulary, unlike PhysicalActs' closed one — a file path is not a
	// member of a table — so the caps that bound them are measured rather than
	// structural: 40/24/16 respectively, each set just above that level's own
	// p90 over 70 corpus transcripts / 165 one-hour windows (see
	// sidecar/app/analysis/workstreams.py's INVENTORY). Absent, never an empty
	// list, when the analysis produced none.
	Files       []PathCount `json:"files,omitempty"`
	Directories []PathCount `json:"directories,omitempty"`
	Components  []PathCount `json:"components,omitempty"`
	// HarnessTools, Programs, ExternalSystems and Integrations are what the same
	// window's hour USED: inventories of the `tool` (the calling harness's own
	// tool set — Bash, Edit, Read, ToolSearch, ...), `exe` (shell programs it
	// invoked — git, pnpm, docker, ...), `service` (external hosts it reached —
	// api.anthropic.com, github.com, ...) and `mcp_tool` (MCP tool ids it called
	// — notion-fetch, ...) levels, with counts (see NameCount). Same /analyze
	// call as PhysicalActs/Files/Directories/Components, model-free like them,
	// so all four publish in ml_backend "deterministic" too.
	//
	// OPEN vocabulary, like the path inventories and unlike PhysicalActs' closed
	// one, so each is gated per entry by a STRUCTURAL rule rather than a lookup
	// table (see sidecar.convertIdentifierInventory / convertProgramInventory /
	// convertExternalSystemInventory): HarnessTools/Integrations by bare
	// identifier shape, Programs by identifier shape plus a rejection of path
	// separators and a leading dot (closes the measured `.env.example` defect),
	// ExternalSystems by rejecting bare IP literals while deliberately KEEPING
	// internal/corporate hostnames — see convertExternalSystemInventory for the
	// argument, which is structural and does not rest on what this project's own
	// corpus happens to contain. Absent, never an empty list, when the analysis
	// produced none.
	HarnessTools    []NameCount `json:"harness_tools,omitempty"`
	Programs        []NameCount `json:"programs,omitempty"`
	ExternalSystems []NameCount `json:"external_systems,omitempty"`
	Integrations    []NameCount `json:"integrations,omitempty"`
	// NamedTerms is the ninth inventory and the ONLY one drawn from message
	// TEXT rather than tool-call inputs: proper nouns lifted from the prompt,
	// matched against no declared vocabulary, observed to contain real person
	// names. It was withheld from the wire until that was reversed as an
	// explicit decision; it is bounded by shape only (see
	// sidecar.convertNamedTerms) and carries no person-name filter, because at
	// spaCy's measured ~1% precision a filter would create false assurance
	// rather than remove names.
	NamedTerms []NameCount `json:"named_terms,omitempty"`
	// FileTypes, ShellVerbs, Subagents and McpServers are the last four
	// inventories the analysis computed and nothing published: the `ext`
	// (`.tsx`/`.py`/`.css`), `verb` (the whole command — `git rebase`, `pnpm
	// test`), `agent` (`general-purpose`, `Explore`) and `mcp_server`
	// (`notion`) levels, with counts (see NameCount). Same /analyze call as
	// every inventory above, model-free like them.
	//
	// Each COMPLEMENTS a sibling rather than restating it, which is the bar an
	// inventory has to clear: FileTypes says what KIND of work the Files beside
	// it were (40 files is scattered, 40 `.tsx` files is front-end work);
	// ShellVerbs is the command where Programs is only the binary (`git` says
	// nothing that `git rebase` does not say better); Subagents is the ONLY
	// dimension that says work was DELEGATED, invisible in every other level
	// because a subagent's own turns are a different transcript; McpServers is
	// the server where Integrations is the tool, which is the grain an org
	// governs. Same OPEN vocabulary and same per-entry identifier-shape gate as
	// HarnessTools/Integrations. Absent, never an empty list.
	FileTypes  []NameCount `json:"file_types,omitempty"`
	ShellVerbs []NameCount `json:"shell_verbs,omitempty"`
	Subagents  []NameCount `json:"subagents,omitempty"`
	McpServers []NameCount `json:"mcp_servers,omitempty"`
	// InventoryOmitted names, per inventory dimension, how many values the
	// sidecar's own top-N cut dropped — visibility for a cut that used to be
	// silent for every one of the six pre-existing inventory dimensions (the
	// AGENTS.md "dropping must be visible" rule, applied one level below
	// FacetsSkipped: that field names a dropped FACET, this names a dropped
	// VALUE within one that still published). Absent when nothing was cut,
	// including for a sidecar too old to report it at all.
	InventoryOmitted map[string]int `json:"inventory_omitted,omitempty"`
	// Prior is the SESSION the same window sat in, keyed by dimension (see
	// Prior). Fifth answer from the same /analyze call, model-free like the other
	// four, so it is published in ml_backend "deterministic" too — and the only
	// one of the five that is about something OUTSIDE the window, which is why a
	// per-window view structurally cannot produce it.
	//
	// A CONTRAST, NEVER A FALLBACK. It is reported ALONGSIDE Workstreams and
	// never supplies an answer Workstreams lacked: a dimension absent from
	// Workstreams stays absent no matter what this map says about the session.
	// Four dimensions carry it (skill, language, branch, output_type), decided
	// by measurement over 1,022 windows; `project` and `model` agree with their
	// session 100.0% of the time and would publish a constant, and `tooling`
	// agrees 98.5% over a prior that is itself attributed on 24.3% of windows.
	//
	// Nearly half of all windows (45.1% measured) report `status: "absent"` on
	// every dimension because they are a session's FIRST window. That is
	// arithmetic, not a defect, and a reader has to be told so up front or they
	// will read the blank as a bug — and a blank read as a bug is what someone
	// eventually fills in.
	Prior          map[string]Prior `json:"prior,omitempty"`
	PipelineStatus string           `json:"pipeline_status"`
	// FacetsSkipped names the passes that did not run because THIS RUN has no
	// such pass — currently: a model-dependent pass under ml_backend
	// "deterministic", where ctx.Model is nil by design. It is the companion
	// that keeps PipelineStatus honest: a skip is not a failure, so it does not
	// downgrade the status to "partial", but a thinner profile must not read as
	// a complete one either, so the loss is NAMED rather than inferred from
	// missing fields (the omittedNotice rule, one level up). Absent when
	// nothing was skipped, so the default mode's wire shape is unchanged.
	FacetsSkipped []string `json:"facets_skipped,omitempty"`
	// FacetsDegraded names the passes that DID run and produced a value, but
	// with part of their evidence unavailable — currently: sensitivity under
	// ml_backend "deterministic", where the deterministic credential layer runs
	// and the model's NER half does not. It is a sibling of FacetsSkipped, not a
	// member of it, because the two ask a reader to do different things: a
	// skipped facet has NO value (read nothing), a degraded one has a real value
	// that must be read as "from the checks that ran". Folding them together
	// would either invite dropping a genuine finding or make "skipped" mean
	// "maybe there's a value" — and sensitivity:"none" from a half-blind pass is
	// exactly the case where that distinction is load-bearing. Like
	// FacetsSkipped, every name here is a key of ExtractorVersions, and the
	// field is absent when nothing was degraded.
	FacetsDegraded    []string                `json:"facets_degraded,omitempty"`
	ExtractorVersions map[string]string       `json:"extractor_versions"`
	SchemaVersion     int                     `json:"schema_version"`
	Custom            map[string]CustomResult `json:"custom,omitempty"`
	EnrichedAt        time.Time               `json:"-"`
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
