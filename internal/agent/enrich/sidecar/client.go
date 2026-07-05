// Package sidecar is the HTTP client for the bundled GLiNER2 sidecar; it
// implements enrich.Model. It returns RAW entities — masking is enforced by the
// enrichment pipeline (SensitivityExtractor), not here.
//
// Availability policy: enrichment must NEVER silently degrade to the
// deterministic backend. When the sidecar is temporarily unavailable — idle-
// evicted (503, reloads on demand) or briefly down/restarting (transport error)
// — the client waits (with backoff) and retries until the sidecar answers, so
// every enrichment runs on GLiNER2. Retries stop only on context cancellation
// (daemon shutdown) or a genuine non-retryable error (a real inference failure).
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
	ctx  context.Context
}

func New(baseURL string, timeout time.Duration) *Client {
	return NewCtx(context.Background(), baseURL, timeout)
}

// NewCtx binds the client's retry loop to ctx so it stops on daemon shutdown.
func NewCtx(ctx context.Context, baseURL string, timeout time.Duration) *Client {
	return &Client{base: baseURL, hc: &http.Client{Timeout: timeout}, ctx: ctx}
}

// postOnce performs one POST. ok=true means a 200 was decoded into out.
// retryable=true means the sidecar is temporarily unavailable (transport error
// or 503) and the caller should wait and try again rather than degrade.
func (c *Client) postOnce(path string, body any, out any) (ok bool, retryable bool) {
	b, err := json.Marshal(body)
	if err != nil {
		return false, false
	}
	resp, err := c.hc.Post(c.base+path, "application/json", bytes.NewReader(b))
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
