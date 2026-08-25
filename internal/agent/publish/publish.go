// Package publish sends enrichment results to Atlas. It never transmits raw
// prompt text or raw sensitive values.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

type Source struct {
	ID      string `json:"id"`
	Origin  string `json:"origin,omitempty"`
	Version string `json:"version,omitempty"`
}

type Correlation struct {
	Scheme    string `json:"scheme"`
	ID        string `json:"id"`
	SessionID string `json:"session_id,omitempty"`
}

// Enrichment is the POST /v1/enrichments wire shape (spec §11).
type Enrichment struct {
	Source           Source           `json:"source"`
	Correlation      Correlation      `json:"correlation"`
	Actor            string           `json:"actor,omitempty"`
	TaskType         enrich.Labeled   `json:"task_type"`
	TaskTypeAlt      []enrich.Labeled `json:"task_type_alt,omitempty"`
	Domain           enrich.Labeled   `json:"domain"`
	Entities         []enrich.Entity  `json:"entities,omitempty"`
	Sensitivity      enrich.Labeled   `json:"sensitivity"`
	SensitivitySpans []enrich.Entity  `json:"sensitivity_spans,omitempty"`
	Activity         enrich.Labeled   `json:"activity_type"`
	Personal         enrich.Labeled   `json:"personal"`
	FunctionGuess    enrich.Labeled   `json:"function_guess"`
	Subcategory      enrich.Labeled   `json:"subcategory"`
	SubcategoryAlt   []enrich.Labeled `json:"subcategory_alt,omitempty"`
	// Workstreams are the deterministic window dimensions (project, branch,
	// model, ...), counted from tool-call metadata by the sidecar's /analyze —
	// no inference, and no text: the analysis is asked for a window by
	// COORDINATES and only its matched dimension values reach here. Absent when
	// the window attributed none.
	Workstreams map[string]enrich.Labeled `json:"workstreams,omitempty"`
	// Dynamics is how those dimensions are CHANGING: the recent slice of the
	// window read against the longer baseline before it, keyed by dimension.
	// Same /analyze call, same no-inference path, no text either — and, unlike
	// the workstreams beside it, no reference-level VALUE at all: only the
	// closed status/reading vocabularies and the shares they were computed from
	// (see enrich.Dynamic). Absent when the analysis compared nothing.
	Dynamics map[string]enrich.Dynamic `json:"dynamics,omitempty"`
	// Effort is what the window COST in work: the bytes its edits authored and
	// how fast its turns came (see enrich.Effort for the six-candidate
	// measurement that left exactly these two, and for the four refuted signals
	// that deliberately do not appear). Same /analyze call as the two blocks
	// above, same no-inference path, and no text: the diff magnitude arrives as
	// a byte LENGTH — the one function permitted to read `old_string`/
	// `new_string`/`content` returns an int — and the tempo is derived from
	// timestamps alone. Absent when the analysis produced no block.
	Effort *enrich.Effort `json:"effort,omitempty"`
	// PhysicalActs is what the window physically DID: an inventory of acts with
	// counts (see enrich.Act, and enrich.Acts for the closed vocabulary and the
	// measurement that made this an inventory rather than an eighth workstream).
	// Same /analyze call as the three blocks above, same no-inference path.
	//
	// It is EIGHT of that call's `inventory` block's nine keys that now publish
	// (this one plus the seven below), and the reason each earns its place is
	// provenance, not preference: the `action` level is written from a tool NAME
	// and from a shell command's argv, both through a closed lookup table, so no
	// fragment of a transcript can occupy it. Its one remaining sibling,
	// `named_terms`, stays on-device — it is read from message text and has held
	// real person names — and is not modelled in sidecar.InventoryBlock at all,
	// so there is nowhere here to forward it even by mistake
	// (TestEnrichmentWireShapeCannotCarryAnalysisInternals fails if that changes).
	//
	// Absent when the window recorded no act; never an empty list.
	PhysicalActs []enrich.Act `json:"physical_acts,omitempty"`
	// Files, Directories and Components are what the window physically TOUCHED:
	// inventories of the `file`/`dir`/`component` levels with counts (see
	// enrich.PathCount). Same /analyze call as PhysicalActs, same no-inference
	// path. OPEN vocabulary, unlike PhysicalActs — a file path is not a member of
	// a closed table — so what makes publishing them acceptable is measurement,
	// not a lookup: `reconcile()` normalizes every value against the workspace
	// root before the sidecar ever sees it, verified over 500 real corpus
	// transcripts plus John's (zero absolute paths, zero `~`/`/Users`/`/home`
	// paths, zero `../` escapes, zero URLs, zero Windows drive paths), and
	// checked again structurally at the Go decode boundary
	// (sidecar.convertPathInventory). Absent when the window touched no path in
	// that dimension; never an empty list.
	Files       []enrich.PathCount `json:"files,omitempty"`
	Directories []enrich.PathCount `json:"directories,omitempty"`
	Components  []enrich.PathCount `json:"components,omitempty"`
	// HarnessTools, Programs, ExternalSystems and Integrations are what the
	// window USED: inventories of the `tool`/`exe`/`service`/`mcp_tool` levels
	// with counts (see enrich.NameCount). Same /analyze call as the three blocks
	// above, same no-inference path. OPEN vocabulary, like the path inventories
	// and unlike PhysicalActs, so each is gated per entry by a structural rule
	// rather than a lookup table (see sidecar.convertIdentifierInventory /
	// convertProgramInventory / convertExternalSystemInventory):
	// HarnessTools/Integrations by bare identifier shape, Programs by identifier
	// shape plus a rejection of path separators and a leading dot,
	// ExternalSystems by rejecting bare IP literals while deliberately KEEPING
	// internal/corporate hostnames (see convertExternalSystemInventory for the
	// argument). Absent when the window used nothing in that dimension; never an
	// empty list.
	HarnessTools    []enrich.NameCount `json:"harness_tools,omitempty"`
	Programs        []enrich.NameCount `json:"programs,omitempty"`
	ExternalSystems []enrich.NameCount `json:"external_systems,omitempty"`
	Integrations    []enrich.NameCount `json:"integrations,omitempty"`
	// InventoryOmitted names, per inventory dimension, how many values the
	// sidecar's own top-N cut dropped. It is the visibility the truncation
	// lacked before this: the pre-existing inventory dimensions truncated
	// silently, and this makes that cut readable for all nine — even
	// named_terms, the one whose values never reach this struct at all, since
	// what publishes here is only a COUNT, never a value. Absent when nothing was
	// cut.
	InventoryOmitted map[string]int `json:"inventory_omitted,omitempty"`
	// Prior is the SESSION this window sat in, keyed by dimension (see
	// enrich.Prior): the same three measures per dimension the daemon reads
	// on-device — the session's own value/share/evidence/status, and the
	// contrast (agrees, departure, novel). Same /analyze call as the four blocks
	// above, same no-inference path, no text.
	//
	// A CONTRAST, NEVER A FALLBACK, and that rule survives to the wire: a
	// dimension missing from `workstreams` above stays missing here too. A
	// consumer must not read `prior.language.value` as the window's language —
	// it is what the SESSION was, offered so that `workstreams.language` can be
	// read as an excursion or as business as usual.
	//
	// Its values are reference levels of the same class `workstreams` already
	// publishes (a branch, a language, a skill), because the sidecar derives the
	// prior's vocabulary from its own ALLOCATION list — `named_terms`, the one
	// level read from message text, is structurally not addable to it.
	//
	// Absent when the analysis produced no block. `status: "absent"` on every
	// dimension is the EXPECTED answer for a session's first window and is 45.1%
	// of all windows measured; it means the session had nothing to say, not that
	// the field failed.
	Prior          map[string]enrich.Prior `json:"prior,omitempty"`
	PipelineStatus string                  `json:"pipeline_status"`
	// FacetsSkipped names the passes this run structurally does not have (a
	// model-dependent pass under ml_backend "deterministic"). It rides with
	// pipeline_status because it is what makes that field readable: without it,
	// a deterministic profile is indistinguishable from an auto-mode one that
	// happened to produce nothing for those facets. Absent when nothing was
	// skipped, so the default mode's payload is byte-identical to before.
	FacetsSkipped []string `json:"facets_skipped,omitempty"`
	// FacetsDegraded names the passes that ran with part of their evidence
	// unavailable (sensitivity under ml_backend "deterministic": credential
	// layer yes, model NER no). It rides with the values it qualifies for the
	// same reason facets_skipped rides with pipeline_status: without it,
	// sensitivity:"none" from a pass that only looked for credentials is
	// indistinguishable from "we looked for PII and found none". Absent when
	// nothing was degraded.
	FacetsDegraded    []string          `json:"facets_degraded,omitempty"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	ModelVersion      string            `json:"model_version"`
	// Custom carries org-defined (custom) pass results, keyed by pass key. Atlas
	// stores it verbatim (enrichment.custom / raw); built-ins stay in the typed
	// fields above.
	Custom map[string]enrich.CustomResult `json:"custom,omitempty"`
	// PromptChars is the resolved (typed) prompt length in Unicode code points —
	// a derived integer, never the prompt text. Omitted when 0 (unknown).
	PromptChars int    `json:"prompt_chars,omitempty"`
	TS          string `json:"ts"`
}

// Build maps a job + profile into the wire shape.
func Build(j queue.Job, p enrich.Profile, actor string, includeEntityText bool, promptChars int, now time.Time) Enrichment {
	entities := p.Entities
	if !includeEntityText && len(entities) > 0 {
		entities = make([]enrich.Entity, len(p.Entities))
		for i, e := range p.Entities {
			e.Text = "" // domain-entity surface text gated off by default (privacy)
			entities[i] = e
		}
	}
	return Enrichment{
		Source:            Source{ID: j.Source, Origin: j.Origin, Version: j.Version},
		Correlation:       Correlation{Scheme: j.Scheme, ID: j.ID, SessionID: j.SessionID},
		Actor:             actor,
		TaskType:          p.TaskType,
		TaskTypeAlt:       p.TaskTypeAlt,
		Domain:            p.Domain,
		Entities:          entities,
		Sensitivity:       p.Sensitivity,
		SensitivitySpans:  p.SensitivitySpans,
		Activity:          p.Activity,
		Personal:          p.Personal,
		FunctionGuess:     p.FunctionGuess,
		Subcategory:       p.Subcategory,
		SubcategoryAlt:    p.SubcategoryAlt,
		Workstreams:       p.Workstreams,
		Dynamics:          p.Dynamics,
		Effort:            p.Effort,
		PhysicalActs:      p.PhysicalActs,
		Files:             p.Files,
		Directories:       p.Directories,
		Components:        p.Components,
		HarnessTools:      p.HarnessTools,
		Programs:          p.Programs,
		ExternalSystems:   p.ExternalSystems,
		Integrations:      p.Integrations,
		InventoryOmitted:  p.InventoryOmitted,
		Prior:             p.Prior,
		PipelineStatus:    p.PipelineStatus,
		FacetsSkipped:     p.FacetsSkipped,
		FacetsDegraded:    p.FacetsDegraded,
		ExtractorVersions: p.ExtractorVersions,
		SchemaVersion:     p.SchemaVersion,
		Custom:            p.Custom,
		ModelVersion:      "gliner2-large-v1",
		PromptChars:       promptChars,
		TS:                now.UTC().Format(time.RFC3339),
	}
}

// Publisher POSTs enrichments to Atlas.
type Publisher struct {
	Endpoint string
	Token    func() string
	Actor    string
	HTTP     *http.Client
}

// New returns a Publisher targeting the enrichments endpoint. token is called
// on every Send so a later credential rotation (e.g. creds.Token.Set) is
// observed without reconstructing the Publisher.
func New(endpoint string, token func() string, actor string) *Publisher {
	return &Publisher{Endpoint: endpoint, Token: token, Actor: actor, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Send POSTs one enrichment; returns an error on transport failure or status >= 400.
func (p *Publisher) Send(e Enrichment) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	// Auth is the ingest token only; x-keld-actor is deprecated (never sent).
	req.Header.Set("x-keld-ingest-token", p.Token())

	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return &retry.StatusError{Code: resp.StatusCode}
	}
	return nil
}
