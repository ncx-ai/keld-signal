// Package sidecar is the HTTP client for the bundled GLiNER2 sidecar; it
// implements enrich.Model. It returns RAW entities — masking is enforced by the
// enrichment pipeline (SensitivityExtractor), not here.
package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

type Client struct {
	base string
	hc   *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{base: baseURL, hc: &http.Client{Timeout: timeout}}
}

func (c *Client) post(path string, body any, out any) bool {
	b, err := json.Marshal(body)
	if err != nil {
		return false
	}
	resp, err := c.hc.Post(c.base+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return json.NewDecoder(resp.Body).Decode(out) == nil
}

type extractReq struct {
	Text   string              `json:"text"`
	Labels map[string]string   `json:"labels"`
	Tasks  map[string][]string `json:"tasks"`
}
type extractResp struct {
	Entities []enrich.Entity            `json:"entities"`
	Results  map[string][]enrich.Ranked `json:"results"`
}

func (c *Client) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	var r extractResp
	if !c.post("/extract", extractReq{text, labels, tasks}, &r) {
		return enrich.ExtractResult{}
	}
	return enrich.ExtractResult{Entities: r.Entities, Results: r.Results}
}

func (c *Client) Entities(text string, labels map[string]string) []enrich.Entity {
	var r extractResp
	if !c.post("/entities", struct {
		Text   string            `json:"text"`
		Labels map[string]string `json:"labels"`
	}{text, labels}, &r) {
		return nil
	}
	return r.Entities
}

func (c *Client) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	var r extractResp
	if !c.post("/classify", struct {
		Text  string              `json:"text"`
		Tasks map[string][]string `json:"tasks"`
	}{text, tasks}, &r) {
		return nil
	}
	return r.Results
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
