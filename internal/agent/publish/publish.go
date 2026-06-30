// Package publish sends enrichment results to Atlas. It never transmits raw
// prompt text or raw sensitive values.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
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
	Source            Source            `json:"source"`
	Correlation       Correlation       `json:"correlation"`
	Actor             string            `json:"actor,omitempty"`
	TaskType          enrich.Labeled    `json:"task_type"`
	TaskTypeAlt       []enrich.Labeled  `json:"task_type_alt,omitempty"`
	Domain            enrich.Labeled    `json:"domain"`
	Entities          []enrich.Entity   `json:"entities,omitempty"`
	Sensitivity       enrich.Labeled    `json:"sensitivity"`
	SensitivitySpans  []enrich.Entity   `json:"sensitivity_spans,omitempty"`
	PipelineStatus    string            `json:"pipeline_status"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	ModelVersion      string            `json:"model_version"`
	TS                string            `json:"ts"`
}

// Build maps a job + profile into the wire shape.
func Build(j queue.Job, p enrich.Profile, actor string, includeEntityText bool, now time.Time) Enrichment {
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
		PipelineStatus:    p.PipelineStatus,
		ExtractorVersions: p.ExtractorVersions,
		SchemaVersion:     p.SchemaVersion,
		ModelVersion:      "deterministic-v1",
		TS:                now.UTC().Format(time.RFC3339),
	}
}

// Publisher POSTs enrichments to Atlas.
type Publisher struct {
	Endpoint string
	Token    string
	Actor    string
	HTTP     *http.Client
}

// New returns a Publisher targeting the enrichments endpoint.
func New(endpoint, token, actor string) *Publisher {
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
	req.Header.Set("x-keld-ingest-token", p.Token)
	req.Header.Set("x-keld-actor", p.Actor)

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
		return fmt.Errorf("atlas returned %d", resp.StatusCode)
	}
	return nil
}
