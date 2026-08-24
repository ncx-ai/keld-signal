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
	Dynamics       map[string]enrich.Dynamic `json:"dynamics,omitempty"`
	PipelineStatus string                    `json:"pipeline_status"`
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
