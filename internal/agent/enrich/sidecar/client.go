// Package sidecar is the HTTP client for the bundled GLiNER2 sidecar; it
// implements enrich.Model. It returns RAW entities — masking is enforced by the
// enrichment pipeline (SensitivityExtractor), not here.
//
// Availability policy: enrichment must never silently degrade to a
// lower-fidelity backend — there is none; the sidecar is the sole Model. When
// the sidecar is temporarily unavailable — idle-evicted (503, reloads on
// demand) or briefly down/restarting (transport error) — the client waits
// (with backoff) and retries until the sidecar answers, so every enrichment
// runs on GLiNER2. Retries stop only on context cancellation (daemon
// shutdown) or a genuine non-retryable error (a real inference failure).
package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

type Client struct {
	base string
	hc   *http.Client
	ctx  context.Context
	// maxLen truncates each inference's input to this many word tokens,
	// bounding its transient activation memory. 0 means "no cap", which is
	// gliner2's own default — see lenstat for why a cap is required and how
	// this value is derived from the machine's prompt-length distribution.
	maxLen int
}

func New(baseURL string, timeout time.Duration) *Client {
	return NewCtx(context.Background(), baseURL, timeout)
}

// NewCtx binds the client's retry loop to ctx so it stops on daemon shutdown.
func NewCtx(ctx context.Context, baseURL string, timeout time.Duration) *Client {
	return &Client{base: baseURL, hc: &http.Client{Timeout: timeout}, ctx: ctx}
}

// WithContext returns a shallow copy bound to ctx (sharing the underlying
// http.Client, which is concurrency-safe). The daemon uses this to give each
// job its own deadline: cancelling ctx aborts any in-flight request AND stops
// the retry loop, so a timed-out job's sidecar work is reclaimed instead of
// leaking and retrying forever (the death-spiral root cause). Mirrors
// http.Request.WithContext.
func (c *Client) WithContext(ctx context.Context) *Client {
	cp := *c
	cp.ctx = ctx
	return &cp
}

// WithMaxLen returns a shallow copy that truncates each inference's input to n
// word tokens (<= 0 clears the cap). The daemon binds this per job from the
// adaptive cap; it composes with WithContext/WithModelContext, so binding a
// per-pass deadline afterwards preserves the cap.
func (c *Client) WithMaxLen(n int) *Client {
	cp := *c
	if n < 0 {
		n = 0
	}
	cp.maxLen = n
	return &cp
}

// WithModelContext satisfies enrich.ContextModel so the pipeline can bind a
// per-pass deadline to the backend. Returns the same shallow copy WithContext
// does, typed as enrich.Model (Go interfaces are invariant in the return type,
// so WithContext cannot satisfy the interface directly).
func (c *Client) WithModelContext(ctx context.Context) enrich.Model {
	return c.WithContext(ctx)
}

// postOnce performs one POST. ok=true means a 200 was decoded into out.
// retryable=true means the sidecar is temporarily unavailable (transport error
// or 503) and the caller should wait and try again rather than degrade.
func (c *Client) postOnce(path string, body any, out any) (ok bool, retryable bool) {
	b, err := json.Marshal(body)
	if err != nil {
		return false, false
	}
	// Bind the request to c.ctx so cancelling the per-job context aborts the
	// call in flight — not just the retry backoff. Without this the request ran
	// to the http.Client timeout and a timed-out job's work could not be
	// reclaimed. A cancelled request surfaces as a transport error below; the
	// retry loop's own ctx.Done() check turns that into a clean stop.
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return false, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, true // transport error: sidecar down/restarting — wait+retry
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out) == nil, false
	case resp.StatusCode == http.StatusServiceUnavailable:
		return false, true // evicted / overloaded — the request woke it; wait+retry
	default:
		return false, false // genuine error — do not spin forever
	}
}

// post waits + retries through temporary unavailability (never degrades). It
// returns false only on a non-retryable error or ctx cancellation.
func (c *Client) post(path string, body any, out any) bool {
	backoff := 200 * time.Millisecond
	for {
		if c.ctx.Err() != nil {
			return false // per-job deadline/shutdown — stop immediately, don't degrade
		}
		ok, retryable := c.postOnce(path, body, out)
		if ok {
			return true
		}
		if !retryable {
			return false
		}
		select {
		case <-c.ctx.Done():
			return false
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// Request bodies carry MaxLen as omitempty so an unset cap is absent from the
// JSON rather than sent as 0 — the sidecar reads a present-but-zero value as
// "truncate to nothing", not "no cap".
type extractReq struct {
	Text   string              `json:"text"`
	Labels map[string]string   `json:"labels"`
	Tasks  map[string][]string `json:"tasks"`
	MaxLen int                 `json:"max_len,omitempty"`
}
type entitiesReq struct {
	Text   string            `json:"text"`
	Labels map[string]string `json:"labels"`
	MaxLen int               `json:"max_len,omitempty"`
}
type classifyReq struct {
	Text   string              `json:"text"`
	Tasks  map[string][]string `json:"tasks"`
	MaxLen int                 `json:"max_len,omitempty"`
}
type extractResp struct {
	Entities []enrich.Entity            `json:"entities"`
	Results  map[string][]enrich.Ranked `json:"results"`
}

func (c *Client) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	var r extractResp
	if !c.post("/extract", extractReq{text, labels, tasks, c.maxLen}, &r) {
		return enrich.ExtractResult{}
	}
	return enrich.ExtractResult{Entities: r.Entities, Results: r.Results}
}

func (c *Client) Entities(text string, labels map[string]string) []enrich.Entity {
	var r extractResp
	if !c.post("/entities", entitiesReq{text, labels, c.maxLen}, &r) {
		return nil
	}
	return r.Entities
}

// labelsPayload builds the /classify task "labels" value. With no authored
// descriptions it stays the bare []string (unchanged wire; a plain label set).
// When any label carries a description it becomes GLiNER2's dict form
// {label: hint} for EVERY label ("" where none was authored) — gliner2 reads a
// dict-valued "labels" as label→description and injects each hint into the model
// prompt (see gliner2 Schema.classification / processor DESC token).
func labelsPayload(labels []string, desc map[string]string) any {
	if len(desc) == 0 {
		return labels
	}
	m := make(map[string]string, len(labels))
	for _, l := range labels {
		m[l] = desc[l]
	}
	return m
}

// multiTaskWire is the per-task object form of the /classify contract: unlike
// the plain []string (single-label softmax), it asks the sidecar to score each
// label independently (sigmoid) at a threshold. Mirrors the Classifier Lab
// preview's multi_label request. Labels is []string or, with per-label
// descriptions, the {label: hint} dict form.
type multiTaskWire struct {
	Labels       any     `json:"labels"`
	MultiLabel   bool    `json:"multi_label"`
	ClsThreshold float64 `json:"cls_threshold"`
}
type classifyMultiReq struct {
	Text   string                   `json:"text"`
	Tasks  map[string]multiTaskWire `json:"tasks"`
	MaxLen int                      `json:"max_len,omitempty"`
}

// ClassifyMulti implements enrich.MultiLabelModel via the /classify route's
// object task form (labels + multi_label + cls_threshold).
func (c *Client) ClassifyMulti(text string, tasks map[string]enrich.MultiTask) map[string][]enrich.Ranked {
	wire := make(map[string]multiTaskWire, len(tasks))
	for name, t := range tasks {
		wire[name] = multiTaskWire{Labels: labelsPayload(t.Labels, t.Descriptions), MultiLabel: true, ClsThreshold: t.Threshold}
	}
	var r extractResp
	if !c.post("/classify", classifyMultiReq{text, wire, c.maxLen}, &r) {
		return nil
	}
	return r.Results
}

// describedTaskWire is the single-label (softmax) task object carrying per-label
// descriptions: the {label: hint} dict form of "labels", with multi_label left
// off so the sidecar scores it single-label. classify_text() accepts this task
// config dict directly (gliner2 Schema.classification).
type describedTaskWire struct {
	Labels map[string]string `json:"labels"`
}
type classifyDescribedReq struct {
	Text   string                       `json:"text"`
	Tasks  map[string]describedTaskWire `json:"tasks"`
	MaxLen int                          `json:"max_len,omitempty"`
}

// ClassifyDescribed implements enrich.DescribedLabelModel: single-label
// classification with per-label GLiNER2 hints, via the /classify route's object
// task form (dict labels, no multi_label).
func (c *Client) ClassifyDescribed(text string, tasks map[string]enrich.DescribedTask) map[string][]enrich.Ranked {
	wire := make(map[string]describedTaskWire, len(tasks))
	for name, t := range tasks {
		m := make(map[string]string, len(t.Labels))
		for _, l := range t.Labels {
			m[l] = t.Descriptions[l]
		}
		wire[name] = describedTaskWire{Labels: m}
	}
	var r extractResp
	if !c.post("/classify", classifyDescribedReq{text, wire, c.maxLen}, &r) {
		return nil
	}
	return r.Results
}

func (c *Client) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	var r extractResp
	if !c.post("/classify", classifyReq{text, tasks, c.maxLen}, &r) {
		return nil
	}
	return r.Results
}

type analyzeReq struct {
	Path        string `json:"path"`
	PromptID    string `json:"prompt_id"`
	SpanMinutes int    `json:"span_minutes"`
}

// Workstream is one deterministic dimension the sidecar's /analyze computed
// for the window (e.g. "project", "tooling"). A nil *Workstream (JSON null)
// means the window had no dominant value for that dimension — a different
// fact than an empty Workstream{}, which is why the map holds pointers.
type Workstream struct {
	Value      string  `json:"value"`
	Share      float64 `json:"share"`
	Evidence   int     `json:"evidence"`
	Provenance string  `json:"provenance"`
}

// AnalyzeResult is the Go-side view of the sidecar's /analyze response.
//
// It models the response's "inventory" the same SELECTIVE way it models
// "dynamics": one field for the one key that publishes. `physical_acts` (the
// `action` level) has one; harness_tools, programs, external_systems,
// integrations and above all named_terms do not, and so cannot be decoded at
// all. named_terms is drawn from the raw transcript and can carry person names
// (e.g. "Federico", "Daniel" have both appeared in real windows). The rule on
// this branch is that only matched vocabulary IDs ever reach Atlas — the
// Workstreams below, and now the closed 22-value act vocabulary, which is
// gated again in convertActs. Not giving InventoryBlock a field for the rest
// means a later publish path has structurally nowhere to forward it, rather
// than merely being told not to by a comment.
//
// Session/WindowStart/WindowEnd are kept: they're metadata about the window
// (a session hash, timestamps), not content, and are useful for local
// logging/debugging.
//
// Dynamics is modelled the SAME selective way, and for the same reason. The
// sidecar's block carries, per dimension, a `slice`/`baseline` pair naming the
// reference level's own value, plus the comparison's timestamps and the sizer's
// detail. Only the six derived fields below have a home here, so a level value
// inside the block cannot be decoded at all — the mechanism, not a comment.
type AnalyzeResult struct {
	Schema      int                    `json:"schema"`
	Evidence    int                    `json:"evidence"`
	Session     string                 `json:"session"`
	WindowStart string                 `json:"window_start"`
	WindowEnd   string                 `json:"window_end"`
	Workstreams map[string]*Workstream `json:"workstreams"`
	Inventory   InventoryBlock         `json:"inventory"`
	Dynamics    DynamicsBlock          `json:"dynamics"`
	Effort      *EffortBlock           `json:"effort"`
	Prior       PriorBlock             `json:"prior"`
}

// PriorBlock is /analyze's `prior` object, and it models ONE of its two keys.
// The other, `clamped`, says the prior's lower bound is the store's retention
// floor rather than the session's own start — local observability of exactly the
// class `sizer_detail` already sits in, useful on-device and with no business on
// a published enrichment. A struct with one field is deliberate, exactly as
// DynamicsBlock's and InventoryBlock's are: it keeps the rest structurally
// unreachable instead of decoded-then-dropped.
type PriorBlock struct {
	// Dimensions is keyed by dimension name (branch, language, output_type,
	// skill — the set the sidecar's own measurement left standing; see
	// enrich.Prior). A nil
	// value is a dimension the sidecar reported as null: no prior at all, which
	// is a different fact from a zero Prior, whose status would read "" and
	// whose evidence would read 0.
	Dimensions map[string]*Prior `json:"dimensions"`
}

// Prior is one dimension's session prior as it arrives on the wire. The three
// contrast measures are POINTERS because null is a FACT here and not an absence:
// 45.1% of real windows are a session's first and can report no contrast at all.
// See enrich.Prior, which this converts to unchanged.
type Prior struct {
	Value     string   `json:"value"`
	Share     float64  `json:"share"`
	Evidence  int      `json:"evidence"`
	Status    string   `json:"status"`
	Agrees    *bool    `json:"agrees"`
	Departure *float64 `json:"departure"`
	Novel     *bool    `json:"novel"`
}

// InventoryBlock is /analyze's inventory object, and it models ONE of its six
// keys. The other five — harness_tools, programs, external_systems, integrations
// and named_terms — are on-device only, and `named_terms` is the reason the
// distinction is enforced by the struct rather than by a comment: it is the one
// level drawn from message TEXT, and real person names have been observed in it.
// A struct with one field is deliberate, exactly as DynamicsBlock's is: it keeps
// the rest structurally unreachable instead of decoded-then-dropped.
//
// A slice, not a pointer: an inventory dimension's absence and its emptiness are
// the same fact ("nothing was recorded"), unlike an effort block, where a zeroed
// struct would state measurements nobody took. convertActs still returns nil
// rather than an empty slice, so the published payload omits the key.
type InventoryBlock struct {
	PhysicalActs []InventoryItem `json:"physical_acts"`
}

// InventoryItem is one entry of an inventory dimension as it arrives on the wire:
// a value and its count, and nothing else — no span, no offset, no surrounding
// message (the wire-shape discipline the sidecar's own payload test holds it to).
type InventoryItem struct {
	Value string `json:"value"`
	N     int    `json:"n"`
}

// DynamicsBlock is /analyze's dynamics object, and it models ONE of its keys.
// The others (sizer, sizer_detail, slice_start/slice_end/baseline_start,
// slice_minutes/baseline_minutes, source, reconcile_scope) are the block's own
// bookkeeping: local metadata of exactly the class window_start/window_end
// already sit in, plus — in `sizer_detail.level` — a reference level's name. A
// struct with one field is deliberate: it keeps the rest structurally
// unreachable instead of decoded-then-dropped.
type DynamicsBlock struct {
	// Dimensions is keyed by dimension name (branch, output_type, language,
	// skill — the set the sidecar's own measurement left standing). A nil
	// value is a dimension the sidecar reported as null: no comparison at all,
	// which is a different fact from a zero Dynamic.
	Dimensions map[string]*Dynamic `json:"dimensions"`
}

// Dynamic is one dimension's comparison as it arrives on the wire. Pointer
// metrics because null is a fact here and not an absence — see enrich.Dynamic,
// which this converts to unchanged.
// EffortBlock is /analyze's `effort` object: the two transcript signals that
// survived measurement (see enrich.Effort for the verdicts and the four refuted
// candidates). A POINTER on AnalyzeResult, because a sidecar too old to compute
// the block sends nothing and a zeroed struct would state every count as 0 and
// every status as "" — a real-looking answer nobody measured.
//
// AuthoredBytes and FastShare are pointers for the same reason they are on
// enrich.Effort: `null` means there was nothing to measure, and rendering that as
// 0 is precisely the defect the study found by naming extremes (a one-turn window
// has zero gaps, and `fast_share 0.0` is what a genuinely slow window reports).
//
// The fields below are the WHOLE block as far as this binary is concerned. The
// sidecar's own payload may grow — a byte length is safe, but the strings those
// lengths measure are file contents — so, exactly as with inventory and the
// dynamics per-side objects, what is not modelled here is structurally
// unforwardable rather than merely discouraged.
type EffortBlock struct {
	AuthoredBytes  *int64   `json:"authored_bytes"`
	AuthoringTurns int      `json:"authoring_turns"`
	AuthoredStatus string   `json:"authored_status"`
	FastShare      *float64 `json:"fast_share"`
	Gaps           int      `json:"gaps"`
	Tempo          string   `json:"tempo"`
	TempoStatus    string   `json:"tempo_status"`
}

type Dynamic struct {
	Status             string   `json:"status"`
	Reading            string   `json:"reading"`
	Changed            *bool    `json:"changed"`
	Turnover           *float64 `json:"turnover"`
	Decay              *float64 `json:"decay"`
	ConcentrationShift *float64 `json:"concentration_shift"`
}

// Analyze asks the sidecar to characterise the window ending at promptID
// (deterministic workstream dimensions — no ML model). It sends COORDINATES,
// never prompt text — the same rule spool.Pointer follows for the enrichment
// hook. ok=false on any failure, including a 404 (prompt id not found in the
// transcript): that is a different fact than "resolved, zero dimensions" and
// must not be reported as an empty success.
func (c *Client) Analyze(path, promptID string, spanMinutes int) (AnalyzeResult, bool) {
	var r AnalyzeResult
	if !c.post("/analyze", analyzeReq{path, promptID, spanMinutes}, &r) {
		return AnalyzeResult{}, false
	}
	return r, true
}

// ingestReq is the whole of the ingest signal: which transcript advanced. No
// offset (the sidecar checkpoints its own), no line count, and above all no
// bytes — the appended text stays on this side of the call, exactly as
// spool.Pointer and Analyze keep it.
type ingestReq struct {
	Path string `json:"path"`
}

// ingestResp is decoded but not used: the counts are the sidecar's own
// visibility (its /metrics), and the caller has no decision to make on them.
// It exists because postOnce decodes into something.
type ingestResp struct {
	NewLines int  `json:"new_lines"`
	Reparsed bool `json:"reparsed"`
}

// ingestSignalTimeout bounds one ingest signal. Sized above the measured worst
// case — a first whole-file ingest of a 90 MB transcript took 5.1s — so the
// common big-file case completes within one attempt and the outcome the client
// reports is the outcome that happened, rather than a timeout over work that
// actually succeeded. It stays bounded (rather than inheriting no deadline)
// because the caller dispatches signals serially: a sidecar that accepts
// connections and never answers must not hold the dispatcher forever.
const ingestSignalTimeout = 30 * time.Second

// SignalIngest tells the sidecar that the transcript at path has grown, so the
// reference series is brought up to date OFF the request path (POST /ingest).
// It sends COORDINATES — a path, nothing else.
//
// ONE ATTEMPT, NEVER A RETRY. Every other call here goes through post(), which
// waits out a reloading/evicted sidecar because its answer is needed and
// degrading is forbidden. This call is the opposite shape: nothing consumes its
// result. Ingest resumes from the byte offset the sidecar stored, so a signal
// lost to an unreachable sidecar costs latency and nothing else — the next
// signal for that file catches up on everything appended since, and /analyze's
// own on-demand ingest catches up even if no further signal ever arrives (see
// analyze.analyze_window's `refresh`, kept as exactly that backstop). Retrying
// here would trade that free recovery for a caller parked in a backoff sleep.
//
// ok=false means this attempt did not land. It is not an error the caller can
// act on and must not be treated as one; the sidecar counts the real outcomes
// (ingest_served/rejected/missing/failed in /metrics).
func (c *Client) SignalIngest(path string) bool {
	// A shallow copy with its own deadline and its own http.Client: c.hc's
	// timeout is sized for an inference round-trip and would cut a legitimate
	// first whole-file ingest short. Transport is left nil, so connections are
	// still pooled through http.DefaultTransport.
	cp := *c
	cp.hc = &http.Client{Timeout: ingestSignalTimeout}
	ctx, cancel := context.WithTimeout(c.ctx, ingestSignalTimeout)
	defer cancel()
	cp.ctx = ctx
	var r ingestResp
	ok, _ := cp.postOnce("/ingest", ingestReq{Path: path}, &r)
	return ok
}

// Warmup triggers and awaits the sidecar's on-demand model load by issuing a
// trivial /classify bound to ctx. The sidecar loads the model only when it
// receives an inference request, so this is the request that starts the load;
// post() waits+retries through the 503/reload window until the sidecar answers.
// Returns nil once the model is resident, ctx.Err() if ctx ends first, or a
// generic error on a non-retryable failure. The result is discarded.
func (c *Client) Warmup(ctx context.Context) error {
	var r extractResp
	if c.WithContext(ctx).post("/classify", struct {
		Text  string              `json:"text"`
		Tasks map[string][]string `json:"tasks"`
	}{"warmup", map[string][]string{"task_type": {"general"}}}, &r) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New("sidecar warmup failed")
}

func (c *Client) Healthy(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		Ok bool `json:"ok"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&h) == nil && h.Ok
}

// WorkerReady reports whether the sidecar's inference worker has the model
// resident RIGHT NOW (GET /metrics, worker.state == "ready"). Unlike Healthy
// (which only proves the HTTP server is up), this reflects post-idle-kill
// reloads: worker.state is "spawning" while the model reloads. Any state
// other than exactly "ready" — e.g. "spawning", "held", "down" — is treated
// as not warm, as is any transport or decode error, so a caller never starts
// a job's deadline against a cold model.
func (c *Client) WorkerReady(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/metrics", nil)
	if err != nil {
		return false
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var m struct {
		Worker struct {
			State string `json:"state"`
		} `json:"worker"`
	}
	return json.NewDecoder(resp.Body).Decode(&m) == nil && m.Worker.State == "ready"
}

// piiReq is the /pii request. It carries no max_len: the token cap bounds
// GLiNER2's activation memory, and this route never touches GLiNER2. The
// sidecar applies its own char clip + spaCy document guard and REPORTS the drop
// (see piiResp.Truncated), which is the bound that matters here.
type piiReq struct {
	Text string `json:"text"`
	// Regions is the country-tier selection (see settings.Settings.PIIRegions).
	// `omitempty` is DELIBERATELY ABSENT: a nil slice must marshal as `null`
	// (key present, no opinion → the sidecar applies its own default) and an
	// EMPTY slice as `[]` (key present, universal tier only). omitempty erases
	// both into "key absent" and collapses two different answers into one.
	Regions []string `json:"regions"`
}

// piiSpanWire is one span as /pii reports it: WHERE the leaked value is and
// what kind it is, never what it is. The caller holds the text and slices its
// own copy — returning the value would put raw PII in an HTTP body and in every
// log line that body later reaches.
type piiSpanWire struct {
	Type  string  `json:"type"`
	Start int     `json:"start"`
	End   int     `json:"end"`
	Score float64 `json:"score"`
}

type piiResp struct {
	Spans     []piiSpanWire `json:"spans"`
	Truncated bool          `json:"truncated"`
}

// DetectPII runs the sidecar's presidio layer over text (POST /pii) and returns
// the detected spans plus whether the scan read the whole input.
//
// It needs no GLiNER2 and does not go through the inference single-flight, so
// it answers with the model absent entirely — which is the point: the
// sensitivity facet must work under ml_backend "deterministic".
//
// ok=false means the scan could not be performed at all. It is deliberately
// distinct from a successful empty result: the caller publishes
// sensitivity:"none" off an empty scan, so reporting an unreachable service as
// "found nothing" would manufacture a confident negative out of a check that
// never ran. The caller marks the facet degraded on false (see
// enrich.SensitivityExtractor.Degraded).
func (c *Client) DetectPII(text string) (enrich.PIIResult, bool) {
	return c.DetectPIIIn(text, nil)
}

// DetectPIIIn is DetectPII with an explicit region tier — which country-specific
// checksum recognizers run on top of the universal ones (card, email, phone,
// IBAN, crypto wallet). See settings.Settings.PIIRegions for why this is scoped
// at all, and sidecar/app/pii.py REGION_RECOGNIZERS for the codes.
//
// It rides the REQUEST rather than the sidecar's environment because the value
// comes from org settings the daemon polls on a live interval: a startup flag
// would mean an org's change waits for a sidecar restart. The sidecar caches one
// analyzer per distinct region set, so varying it is cheap.
//
// nil regions means "no opinion" and lets the sidecar apply its own default
// (KELD_PII_REGIONS, else `us`). An EMPTY, non-nil slice means the universal
// tier only. Unknown codes are ignored by the sidecar, not rejected here — the
// list of servable regions lives in one place.
func (c *Client) DetectPIIIn(text string, regions []string) (enrich.PIIResult, bool) {
	var r piiResp
	if !c.post("/pii", piiReq{Text: text, Regions: regions}, &r) {
		return enrich.PIIResult{}, false
	}
	spans := make([]enrich.Entity, 0, len(r.Spans))
	for _, s := range r.Spans {
		// Label/Start/End/Confidence only: Text and Masked stay empty because
		// the wire carries no value, and the pipeline resolves + masks it from
		// its own copy of the text.
		spans = append(spans, enrich.Entity{Label: s.Type, Start: s.Start, End: s.End, Confidence: s.Score})
	}
	return enrich.PIIResult{Spans: spans, Truncated: r.Truncated}, true
}
