package llmstudy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

// Answer is one arm's labels for one window, plus how the call went.
//
// Valid means Wave 1 produced a full set of in-vocabulary labels — it is NOT
// "everything succeeded". Partial means Wave 1 committed but the conditioned
// subcategory pass did not, so Subcategory is absent while the other five facets
// remain usable. This mirrors the pipeline's per-pass rule: a failing pass costs
// exactly one facet and the rest still commit (see AGENTS.md, "Deadlines are PER
// PASS"). Marking the whole answer invalid would discard five good facets to
// punish one failure.
type Answer struct {
	Labels    map[Facet]string `json:"labels"`
	LatencyMS int64            `json:"latency_ms"`
	Valid     bool             `json:"valid"`
	Partial   bool             `json:"partial,omitempty"`
	Err       string           `json:"err,omitempty"`
}

// Llama talks to a local llama-server over its OpenAI-compatible endpoint.
// Loopback only: window text never leaves the machine.
type Llama struct {
	BaseURL string
	Timeout time.Duration
	// Policy governs transient-failure retries. Exported so tests can disable
	// backoff; production callers should leave the default.
	Policy retry.Policy
	hc     *http.Client
}

// NewLlama returns a client for a llama-server base URL (e.g. http://127.0.0.1:8080).
// The timeout is generous because CPU prefill over a multi-turn window is slow —
// the study measures that cost rather than hiding it behind a short deadline.
func NewLlama(baseURL string) *Llama {
	const to = 180 * time.Second
	return &Llama{BaseURL: baseURL, Timeout: to, Policy: retry.DefaultPolicy(), hc: &http.Client{Timeout: to}}
}

type chatResp struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// call issues one constrained-decoding request and unmarshals the JSON content.
func (l *Llama) call(prompt string, schema map[string]any, out any) error {
	body := map[string]any{
		"messages":    []any{map[string]any{"role": "user", "content": prompt}},
		"temperature": 0,
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "facets",
				"strict": true,
				"schema": schema,
			},
		},
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	// Retry transient failures rather than recording them as model errors.
	// llama-server answers /health OK while its slots are still initialising, so
	// the first requests of a run can get 503 "no slot available" — a startup
	// race, not a classification failure. Uses the canonical policy/classifier
	// per the repo convention (don't hand-roll backoff loops).
	return retry.DoClassify(context.Background(), l.Policy, isTransientHTTP, func() error {
		req, err := http.NewRequest(http.MethodPost, l.BaseURL+"/v1/chat/completions", bytes.NewReader(buf))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := l.hc.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return statusErr(resp.StatusCode)
		}
		var cr chatResp
		if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
			return err
		}
		if len(cr.Choices) == 0 {
			return fmt.Errorf("llama-server returned no choices")
		}
		if err := json.Unmarshal([]byte(cr.Choices[0].Message.Content), out); err != nil {
			return fmt.Errorf("content was not schema JSON: %w", err)
		}
		return nil
	})
}

// statusErr carries an HTTP status so the retry classifier can see it.
type statusErr int

func (e statusErr) Error() string { return fmt.Sprintf("llama-server HTTP %d", int(e)) }

// isTransientHTTP treats 408/429/5xx as retryable, matching retry.IsTransient's
// contract, and delegates everything else to it. Unknown errors stay permanent.
func isTransientHTTP(err error) bool {
	var se statusErr
	if errors.As(err, &se) {
		c := int(se)
		return c == http.StatusRequestTimeout || c == http.StatusTooManyRequests || c >= 500
	}
	return retry.IsTransient(err)
}

// validate confirms a label is in the facet's live vocabulary.
func validate(f Facet, v string) error {
	for _, d := range defsFor(f) {
		if d.ID == v {
			return nil
		}
	}
	return fmt.Errorf("%s: off-vocabulary label %q", f, v)
}

// Classify runs Wave 1 then, when the chosen function has subcategories, Wave 2.
//
// The result is a NAMED return so the deferred latency assignment lands on the
// value actually returned. With an unnamed result the defer would mutate a local
// copy after the return value was set, and every latency would be reported as 0.
func (l *Llama) Classify(w Window) (a Answer) {
	a = Answer{Labels: map[Facet]string{}}
	start := time.Now()
	defer func() { a.LatencyMS = time.Since(start).Milliseconds() }()

	var one map[string]string
	if err := l.call(WaveOnePrompt(w), WaveOneSchema(), &one); err != nil {
		a.Err = err.Error()
		return a
	}
	for _, f := range waveOneFacets {
		v := one[string(f)]
		if err := validate(f, v); err != nil {
			a.Err = err.Error()
			return a
		}
		a.Labels[f] = v
	}

	fn := a.Labels[FacetFunction]
	schema := SubcategorySchema(fn)
	if schema == nil {
		a.Valid = true
		return a
	}
	var two map[string]string
	if err := l.call(SubcategoryPrompt(w, fn), schema, &two); err != nil {
		// Wave 1 already committed; keep it and report the missing facet.
		a.Valid, a.Partial = true, true
		a.Err = "subcategory: " + err.Error()
		return a
	}
	a.Labels[FacetSubcategory] = two[string(FacetSubcategory)]
	a.Valid = true
	return a
}
