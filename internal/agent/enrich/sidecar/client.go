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
