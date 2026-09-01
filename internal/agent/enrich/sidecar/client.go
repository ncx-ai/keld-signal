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
	"fmt"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
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
	ok, retryable, _ = c.postOnceStatus(path, body, out)
	return ok, retryable
}

// postOnceStatus is postOnce plus the HTTP status the attempt saw (0 for a
// transport error). Only /attribute reads it, and only to tell a 404 — an
// older frozen sidecar with no such route — apart from every other
// non-retryable answer; see AttributeResult.RouteUnsupported.
func (c *Client) postOnceStatus(path string, body any, out any) (ok bool, retryable bool, status int) {
	b, err := json.Marshal(body)
	if err != nil {
		return false, false, 0
	}
	// Bind the request to c.ctx so cancelling the per-job context aborts the
	// call in flight — not just the retry backoff. Without this the request ran
	// to the http.Client timeout and a timed-out job's work could not be
	// reclaimed. A cancelled request surfaces as a transport error below; the
	// retry loop's own ctx.Done() check turns that into a clean stop.
	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		return false, false, 0
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, true, 0 // transport error: sidecar down/restarting — wait+retry
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return json.NewDecoder(resp.Body).Decode(out) == nil, false, resp.StatusCode
	case resp.StatusCode == http.StatusServiceUnavailable:
		return false, true, resp.StatusCode // evicted / overloaded — the request woke it; wait+retry
	default:
		return false, false, resp.StatusCode // genuine error — do not spin forever
	}
}

// post waits + retries through temporary unavailability (never degrades). It
// returns false only on a non-retryable error or ctx cancellation.
func (c *Client) post(path string, body any, out any) bool {
	ok, _ := c.postStatus(path, body, out)
	return ok
}

// postStatus is post plus the HTTP status of the LAST attempt (0 when none
// completed). See postOnceStatus for the one caller and why.
func (c *Client) postStatus(path string, body any, out any) (bool, int) {
	backoff := 200 * time.Millisecond
	last := 0
	for {
		if c.ctx.Err() != nil {
			return false, last // per-job deadline/shutdown — stop immediately, don't degrade
		}
		ok, retryable, status := c.postOnceStatus(path, body, out)
		last = status
		if ok {
			return true, status
		}
		if !retryable {
			return false, status
		}
		select {
		case <-c.ctx.Done():
			return false, last
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
	// Resolved are the facts the DAEMON resolved because this side structurally
	// cannot: /analyze is confined to KELD_ANALYZE_ROOTS precisely so it cannot
	// open a repo's .git/config as the daemon's user. Omitted entirely when
	// nothing was resolved, so a request from a caller with no cwd is
	// byte-identical to what it was before this field existed (and so the
	// sidecar's own `resolved is None` back-compat path is the one that runs).
	//
	// ⚠️ THE CLIENT DOES NO FILESYSTEM IO. These arrive as a parameter; nothing
	// here resolves them. That is not tidiness — an HTTP client that stat'd the
	// filesystem per call would put the resolution inside the per-pass deadline
	// and inside the single-flight, and it would be unable to answer for a job
	// whose cwd has since been removed.
	Resolved *enrich.ResolvedFacts `json:"resolved,omitempty"`
}

// resolvedOrNil is the ONE place the "empty means omit" rule is applied, shared
// by all three requests that carry the facts (/analyze, /tick, /ingest). Sending
// three empty strings and sending nothing are the same fact, and the sidecar
// distinguishes them (`resolved is None` is its back-compat path), so they must
// not be able to diverge per call site.
func resolvedOrNil(r enrich.ResolvedFacts) *enrich.ResolvedFacts {
	if r.Zero() {
		return nil
	}
	return &r
}

// Workstream is one deterministic dimension the sidecar's /analyze computed
// for the window (e.g. "project", "tooling").
//
// As of sidecar SCHEMA 16 EVERY dimension answers with an object and states its
// own outcome in Status (window.REASONS: attributed / thin / tie / no_majority /
// absent). A nil *Workstream is therefore no longer the normal way an
// unattributed dimension arrives — it is what a sidecar OLDER than 16 sends,
// which is a real case because the sidecar is frozen and shipped separately and
// can sit in ~/.local/bin indefinitely. The map still holds pointers for exactly
// that: an old sidecar's null and a new sidecar's `status:"absent"` object are
// different facts on the wire, and only the second one carries a count.
//
// Value is empty and Evidence 0 under `absent` — there was no value to name.
// Under every other status Value is the leading value and Evidence the count it
// was drawn from, and Status is what says whether it may be read as the window's
// answer. Do not read Value without reading Status.
type Workstream struct {
	Value      string  `json:"value"`
	Share      float64 `json:"share"`
	Evidence   int     `json:"evidence"`
	Status     string  `json:"status"`
	Provenance string  `json:"provenance"`
}

// AnalyzeResult is the Go-side view of the sidecar's /analyze response.
//
// It models ALL NINE keys of the response's "inventory" object. That is a
// reversal, and worth stating plainly because most of this file's surrounding
// prose was written under the opposite rule.
//
// The rule began as "one field for the one closed-vocabulary key", widened to
// "closed vocabulary OR a provably-constrained shape, gated per entry" when the
// path and identifier inventories were wired, and no longer withholds a key at
// all. named_terms was the last holdout and is now decoded like the rest — an
// explicit product decision by the repo owner, made against a stated
// alternative (publishing only org-declared vocabulary matches through
// /match + publish.Custom) and taken knowingly.
//
// ⚠️ named_terms is NOT like the other twelve, and a reader needs to know why
// even though it no longer changes what happens to it. The other twelve derive
// from tool-call INPUTS — a path opened, a command run, a host connected to.
// named_terms is proper nouns lifted from MESSAGE TEXT, matched against no
// declared vocabulary, and has been observed to contain real person names
// ("Federico", "Daniel" both appeared in real windows). It is the only
// inventory whose provenance is the prompt itself.
//
// There is deliberately NO person-name filter, and adding one would be worse
// than the absence. spaCy's person detection measured ~1% precision on
// developer prompts here — 998 of 1,090 spans with zero confirmed names — which
// is why presidio's SpacyRecognizer was removed from the sensitivity facet
// outright. A filter at that precision would not remove person names; it would
// only make callers believe they had been removed.
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
	// InventoryOmitted is the sibling of `inventory`: dimension name -> how many
	// of its values the sidecar's own top-N cut dropped, for every dimension it
	// actually truncated (a dimension it did not cut is absent, so an
	// untruncated payload decodes to an empty map, not nine zeros). It is just a
	// COUNT — never a value — so it carried no privacy weight back when this
	// struct could not decode some of the keys it counts, and carries none now
	// that it can: "programs cut 3 values" says nothing about which three.
	InventoryOmitted map[string]int `json:"inventory_omitted"`
	Dynamics         DynamicsBlock  `json:"dynamics"`
	Effort           *EffortBlock   `json:"effort"`
	Prior            PriorBlock     `json:"prior"`
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

// InventoryBlock is /analyze's inventory object, and it models ALL THIRTEEN of
// its keys: physical_acts, files, directories, components, harness_tools,
// programs, external_systems, integrations, named_terms, and — since the
// analysis began publishing the four levels it had always extracted and never
// emitted — file_types, shell_verbs, subagents, mcp_servers.
//
// The STRUCT is still the mechanism rather than a comment, and named_terms is
// why: it is the one level drawn from message TEXT, real person names have been
// observed in it ("Federico", "Daniel" in real windows), and it had no field at
// all until that was reversed as an explicit decision. A key with no field here
// is undecodable no matter what the sidecar sends. A FOURTEENTH key therefore
// still cannot ride along.
//
// THE RULE THIS STRUCT ENCODES HAS WIDENED. It used to be "only matched
// vocabulary IDs ever reach Atlas" (physical_acts' closed 22-value table). It
// now reads:
//
//	closed/matched vocabulary  OR  provably-constrained shape, gated per entry
//
// physical_acts is the CLOSED case (see convertActs). Every other field is an
// OPEN vocabulary — a file path, a tool name, a program name, a hostname, a
// shell command is not a member of a table — so each earns its field through a
// STRUCTURAL gate applied PER ENTRY instead of a vocabulary lookup:
//
//   - files / directories / components: workspace-relative shape
//     (convertPathInventory).
//   - harness_tools / integrations / file_types / subagents / mcp_servers: bare
//     identifier shape (convertIdentifierInventory). Deliberately NOT a
//     hardcoded allowlist — the harness's own tool set genuinely grows, and so
//     does the set of MCP servers an org installs.
//   - programs: identifier shape plus a rejection of path separators and a
//     leading dot (convertProgramInventory) — closes the measured
//     `.env.example` defect (a filename reaching the exe extraction).
//   - shell_verbs: the one that CANNOT use identifierShape, because a verb is a
//     COMMAND and legitimately multi-word (`git rebase`, `pnpm test`) — see
//     convertShellVerbInventory.
//   - external_systems: rejects bare IP literals, v4 and v6
//     (convertExternalSystemInventory) — see that function for why internal
//     and corporate HOSTNAMES are kept rather than filtered.
//
// Slices, not pointers: an inventory dimension's absence and its emptiness are
// the same fact ("nothing was recorded"), unlike an effort block, where a zeroed
// struct would state measurements nobody took. Every convert function still
// returns nil rather than an empty slice, so the published payload omits the
// key.
type InventoryBlock struct {
	PhysicalActs    []InventoryItem `json:"physical_acts"`
	Files           []InventoryItem `json:"files"`
	Directories     []InventoryItem `json:"directories"`
	Components      []InventoryItem `json:"components"`
	HarnessTools    []InventoryItem `json:"harness_tools"`
	Programs        []InventoryItem `json:"programs"`
	ExternalSystems []InventoryItem `json:"external_systems"`
	Integrations    []InventoryItem `json:"integrations"`
	// NamedTerms is the one inventory drawn from message TEXT rather than
	// tool-call inputs — see the AnalyzeResult comment above for why that
	// distinction survives even though the field now exists.
	NamedTerms []InventoryItem `json:"named_terms"`
	FileTypes  []InventoryItem `json:"file_types"`
	ShellVerbs []InventoryItem `json:"shell_verbs"`
	Subagents  []InventoryItem `json:"subagents"`
	McpServers []InventoryItem `json:"mcp_servers"`
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
//
// RequestTokens/GapP50S/GapP90S are the block's spend and gap distribution
// (sidecar SCHEMA 14): a price-weighted, window-scoped token figure and the
// median/p90 of the same inter-turn gap population FastShare summarises as a
// share. See enrich.Effort for what each means and why RequestTokens is not the
// raw token counts Atlas already receives from telemetry.
type EffortBlock struct {
	AuthoredBytes  *int64   `json:"authored_bytes"`
	AuthoringTurns int      `json:"authoring_turns"`
	AuthoredStatus string   `json:"authored_status"`
	FastShare      *float64 `json:"fast_share"`
	Gaps           int      `json:"gaps"`
	Tempo          string   `json:"tempo"`
	TempoStatus    string   `json:"tempo_status"`
	RequestTokens  *int64   `json:"request_tokens"`
	GapP50S        *float64 `json:"gap_p50_s"`
	GapP90S        *float64 `json:"gap_p90_s"`
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
func (c *Client) Analyze(path, promptID string, spanMinutes int,
	resolved enrich.ResolvedFacts) (AnalyzeResult, bool) {
	var r AnalyzeResult
	req := analyzeReq{path, promptID, spanMinutes, resolvedOrNil(resolved)}
	if !c.post("/analyze", req, &r) {
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
	// Resolved rides the ingest signal because INGEST IS WHERE THE ROWS ARE
	// WRITTEN. The sidecar's `repo` level is a series level written per turn by
	// its extractor, not a value overlaid on a digest, so the watcher-driven
	// ingest is the path that normally creates it — a transcript ingested without
	// the facts holds turns that no later recomputation can supply a row for
	// (the sidecar's own `repo_mode` parse-state fingerprint is what forces the
	// one reparse that repairs it). Still coordinates-plus-identifiers: no
	// offset, no line count, no bytes.
	Resolved *enrich.ResolvedFacts `json:"resolved,omitempty"`
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
func (c *Client) SignalIngest(path string, resolved enrich.ResolvedFacts) bool {
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
	ok, _ := cp.postOnce("/ingest", ingestReq{Path: path, Resolved: resolvedOrNil(resolved)}, &r)
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

// PROJECT ATTRIBUTION — which declared project a closed block belongs to,
// decided on-device by the sidecar's own embedding/verifier matcher against
// the org's declared settings.RemoteProject list (never by sending message
// text). See enrich/attribution.go for the wire shapes these two methods
// exchange.

// projectsReq is the whole of POST /projects: the org's declared project
// list, unchanged from settings.RemoteProject. Descriptions flow DOWN to the
// device for the sidecar to embed; nothing here is derived from a prompt.
type projectsReq struct {
	Projects []settings.RemoteProject `json:"projects"`
}

// projectsResp is decoded but its fields are not read further than error
// reporting: Count/Hash are the sidecar's own bookkeeping (how many projects
// it now holds, and a fingerprint of the set), useful for a log line, not for
// a caller decision.
type projectsResp struct {
	Count int    `json:"count"`
	Hash  string `json:"hash"`
}

// postProjectsCallTimeout bounds ONE /projects call the same way
// attributeCallTimeout bounds one /attribute call — see that var's comment.
// The daemon's caller (startup, and the settings poll loop) has no per-call
// deadline of its own, so without a bound here an unreachable sidecar would
// retry forever and could wedge the settings poll goroutine. A var, not a
// const, so a test can shrink it.
var postProjectsCallTimeout = 30 * time.Second

// PostProjects tells the sidecar which projects are currently declared, so
// /attribute has something to match a block against. The daemon calls this
// once at startup (after resolving KELD_PROJECTS_FILE / the remote settings
// key) and again whenever the resolved list changes on a later settings poll
// — never per block, since the declared set does not change per block.
func (c *Client) PostProjects(projects []settings.RemoteProject) error {
	cp := *c
	ctx, cancel := context.WithTimeout(c.ctx, postProjectsCallTimeout)
	defer cancel()
	cp.ctx = ctx
	var r projectsResp
	if !cp.post("/projects", projectsReq{Projects: projects}, &r) {
		return fmt.Errorf("sidecar: POST /projects failed")
	}
	return nil
}

// attributeReq is the whole of POST /attribute: coordinates, the block's own
// span (half-open [start, end)), and the block's ALREADY-COMPUTED dimensions
// (repo, branch, ...) as /blocks published them. Dims are the caller's own
// facts, passed through — never re-derived here, and never message text.
type attributeReq struct {
	Path      string            `json:"path"`
	SessionID string            `json:"session_id"`
	Start     float64           `json:"start"`
	End       float64           `json:"end"`
	Dims      map[string]string `json:"dims"`
}

// AttributeResult is the Go-side view of POST /attribute's response.
//
// Status is one of the closed vocabulary in enrich/attribution.go
// (ProjectsAttributed, ProjectsPending, ProjectsSkippedDisabled,
// ProjectsSkippedNoProjects, ProjectsDegradedWeights). Projects/Attribution
// are populated only when Status is a terminal answer that named something —
// an empty Projects with a terminal Status is a real "no project matched",
// not an absence.
type AttributeResult struct {
	Status      string                      `json:"status"`
	Projects    []enrich.ProjectAttribution `json:"projects"`
	Attribution *enrich.AttributionMeta     `json:"attribution"`
	// RouteUnsupported means the sidecar answered 404: it has no /attribute
	// route at all. `json:"-"` deliberately — it is this client's OWN reading
	// of the transport, never something a response body could set.
	//
	// ⚠️ IT IS SEPARATE FROM Status BECAUSE IT IS A DIFFERENT KIND OF FACT
	// (I5). Status is what the attribution pass concluded; this is that no
	// pass exists on the other side. The sidecar is frozen and shipped
	// separately, so an older one during a staged rollout 404s every call —
	// classed non-retryable, which quarantined the job after four sweeps, into
	// a subdirectory Store.List never re-reads. The work is not impossible,
	// only not yet possible, which is the pending/degraded shape rather than
	// the error one.
	RouteUnsupported bool `json:"-"`
}

// attributeCallTimeout bounds ONE /attribute call, including whatever
// retrying post() does through a transient 503/transport error. It is a var
// (not a const) so a test can shrink it rather than waiting out the real
// production bound.
//
// ⚠️ THIS IS THE PER-CALL DEADLINE THE PLAN CALLS FOR, and it is bound HERE
// rather than left to the caller's ctx for the same reason SignalIngest binds
// its own timeout below: the attributor's driving context (Run's ctx) is the
// DAEMON's lifetime context, which has no deadline at all, and post() retries
// a retryable failure until its context ends. Without a bound of its own, one
// stuck attribute call would retry forever against a wedged sidecar — the
// exact death-spiral shape client.go's package comment and WithContext already
// describe one level up (there, a per-JOB deadline; here, a per-CALL one,
// because the attributor's jobs are not the enrichment pipeline's jobs and
// have no per-job context of their own to bind).
var attributeCallTimeout = 2 * time.Minute

// Attribute asks the sidecar to match one closed block against the declared
// project list (POST /attribute). It sends COORDINATES and the block's own
// already-computed dimensions — never text.
//
// ok=false means this call did not land (transport failure, or the per-call
// deadline above expired mid-retry) and is retryable by the caller's sweep,
// exactly like every other post()-backed method here. ok=true with
// Status=="pending" is a NORMAL terminal HTTP answer, not a failure — the
// caller (attrib.Attributor) is what decides a pending answer must not
// consume a retry attempt; this method has no opinion on that.
//
// ⚠️ IT USES ITS OWN http.Client, NOT c.hc, AND THAT IS LOAD-BEARING. c.hc is
// shared with every other route on this Client and its Timeout is sized for
// an ordinary inference round-trip (the daemon constructs it with a 5s
// timeout — see daemon.go's scClient). /attribute is not that shape: a warm
// encoder with borderline pairs runs verifier verdicts SYNCHRONOUSLY inside
// one call, measured at several seconds per pair with no cap on the pair
// loop, so a real call routinely exceeds 5s. Sharing c.hc would mean every
// individual attempt gets cut at 5s, postOnce classifies that timeout as a
// retryable transport error, and post() would re-issue the SAME request —
// re-running the whole verification from scratch — a dozen or more times
// inside the 2-minute window: the self-amplifying-retry shape AGENTS.md
// documents for /features, reached here through the shared client's timeout
// rather than through this route's own design. Giving the call its own
// *http.Client, timed to attributeCallTimeout (the same bound as the
// context), lets ONE attempt run the full window: a genuinely slow verifier
// call gets the time it needs and is not retried into a storm, while a fast
// failure (connection refused, an immediate 503) still leaves room in the
// budget for post()'s backoff to retry it — exactly the SignalIngest pattern
// of giving a differently-shaped call its own transport rather than
// inheriting one sized for something else.
func (c *Client) Attribute(path, sessionID string, start, end float64,
	dims map[string]string) (AttributeResult, bool) {
	cp := *c
	cp.hc = &http.Client{Timeout: attributeCallTimeout}
	ctx, cancel := context.WithTimeout(c.ctx, attributeCallTimeout)
	defer cancel()
	cp.ctx = ctx
	var r AttributeResult
	ok, status := cp.postStatus("/attribute", attributeReq{
		Path: path, SessionID: sessionID, Start: start, End: end, Dims: dims,
	}, &r)
	if !ok {
		// A 404 is VERSION SKEW, not a failed call: this sidecar has no
		// /attribute route. Surfaced as its own fact so the attributor can hold
		// the job instead of spending an attempt on a component that updates
		// independently — see AttributeResult.RouteUnsupported (I5).
		if status == http.StatusNotFound {
			return AttributeResult{RouteUnsupported: true}, false
		}
		return AttributeResult{}, false
	}
	return r, true
}
