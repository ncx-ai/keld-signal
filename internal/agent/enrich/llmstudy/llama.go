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
	// emptyUnresolved counts refinements where the model returned no open list at all and
	// ensureUnresolvedIsAddressed supplied the sentinel. Unexported with a reader, because it
	// is an observation the sweep prints rather than a knob: see EmptyUnresolvedSubstitutions.
	emptyUnresolved int
}

// EmptyUnresolvedSubstitutions reports how many refinements answered with an EMPTY open list,
// which code then rendered as the "nothing is open" sentinel.
//
// It exists so the substitution does not hide what it papers over. ValidateDigest rejects an
// empty list on purpose — its own comment says an empty list is what a rubberstamping model
// produces — and before the substitution existed those refinements were simply LOST (5 exhausted
// attempts on `unresolved is empty`, 3 of 56 digests). Substituting keeps the digest; counting
// keeps the difference between "the model said nothing is open" and "the model said nothing"
// visible in the sweep's own output, where a reader comparing runs will see it.
//
// Not concurrency-safe, and does not need to be: the sweep is sequential and the server is
// --parallel 1.
func (l *Llama) EmptyUnresolvedSubstitutions() int { return l.emptyUnresolved }

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
	return l.callValid(prompt, schema, out, nil)
}

// callValid is call with a validator that runs INSIDE the retry loop.
//
// Two generation-quality failures used to escape retrying entirely. A response cut off
// mid-object failed json.Unmarshal, and retry.IsTransient classifies unknown errors as
// permanent — correct for the dependency fetches that convention was written for, since
// hammering a broken endpoint helps nobody, but wrong for sampling: a truncated sample
// is self-correcting and a re-request nearly always succeeds. Separately, a response
// that parsed but was semantically empty ("next is empty") failed validation in the
// CALLER, after the loop had already returned success, so it could not be retried at
// all. Together those two were the whole of the T1 shortfall at n=56 (2 of 56).
//
// Both are now retryable and bounded by the same policy. This is a deliberate, narrow
// exception to "unknown errors are permanent": the condition is identified, not unknown,
// and it is a property of one sample rather than of the server.
func (l *Llama) callValid(prompt string, schema map[string]any, out any, validate func() error) error {
	return l.callValidSampled(prompt, schema, out, validate, nil)
}

// sampling says how attempt n of a retry loop should be sampled. Nil means every attempt is
// the greedy temperature-0 request callValid has always sent.
//
// This exists because ONE caller needs the re-request to differ, and it needs it for a
// measured reason rather than a plausible one. GenerateBeat rejects a generation holding no
// complete sentence; at temperature 0 the re-request comes back BYTE-IDENTICAL, so all five
// attempts fail on the same string and the beat is lost — 2 of 42 asked, identically in all
// six sweeps of the last round, and recorded in no artifact but a concerns list. sampleErr's
// own doc already carried the counterexample ("a caller must not assume the re-request
// differs"); this is the mechanism that makes it false.
//
// Seeded, not merely warmed. A bare temperature raise would make the recovered beat
// irreproducible, and "both arms run TWICE with identical figures" is what retired "is it
// variance?" for this whole study. An explicit per-attempt seed keeps a re-run byte-identical
// while still giving the sampler somewhere else to go.
type sampling struct {
	// Temp is the temperature for this attempt, Seed its sampling seed.
	Temp float64
	Seed int
}

// callValidSampled is callValid with per-attempt sampling.
//
// ⚠️ SCOPE. With sample == nil the request body is byte-identical to what callValid has always
// sent — temperature 0, no seed field — so every existing caller (Classify, the digest create
// and refine paths, the extraction study) is unchanged. Pinned by
// TestCallValidStillSendsTheGreedyRequest. The only caller passing a non-nil sample is
// GenerateBeat.
func (l *Llama) callValidSampled(prompt string, schema map[string]any, out any,
	validate func() error, sample func(attempt int) sampling) error {
	// Attempt counter for the sampling schedule. retry.DoClassify calls op sequentially, so a
	// plain counter is the attempt index — no shared state and no need for the retry package
	// to expose one.
	attempt := -1
	// Retry transient failures rather than recording them as model errors.
	// llama-server answers /health OK while its slots are still initialising, so
	// the first requests of a run can get 503 "no slot available" — a startup
	// race, not a classification failure. Uses the canonical policy/classifier
	// per the repo convention (don't hand-roll backoff loops).
	return retry.DoClassify(context.Background(), l.Policy, isTransientHTTP, func() error {
		attempt++
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
		if sample != nil {
			if s := sample(attempt); s.Temp > 0 {
				body["temperature"] = s.Temp
				body["seed"] = s.Seed
			}
		}
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
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
			return sampleErr{fmt.Errorf("content was not schema JSON: %w", err)}
		}
		if validate != nil {
			if err := validate(); err != nil {
				return sampleErr{err}
			}
		}
		return nil
	})
}

// statusErr carries an HTTP status so the retry classifier can see it.
type statusErr int

func (e statusErr) Error() string { return fmt.Sprintf("llama-server HTTP %d", int(e)) }

// sampleErr marks a failure caused by THIS sample rather than by the server, so it is
// worth re-requesting.
//
// ⚠️ A retry only helps if the re-request can DIFFER, and by default it cannot: temperature is
// 0, so a beat that came back as an unpunctuated headline clause repeated byte-identically
// across all five attempts and the beat was lost. That is why `sampling` exists — a caller
// whose rejection is a property of the sample now asks for a different sample explicitly
// instead of hoping slot state makes one. Callers that pass no schedule still get five
// identical requests, which is correct for the failures that ARE transient (a truncated
// response, a 503 from an initialising slot) and useless for the rest.
type sampleErr struct{ err error }

func (e sampleErr) Error() string { return e.err.Error() }
func (e sampleErr) Unwrap() error { return e.err }

// isTransientHTTP treats 408/429/5xx as retryable, matching retry.IsTransient's
// contract, and delegates everything else to it. Unknown errors stay permanent.
func isTransientHTTP(err error) bool {
	var sa sampleErr
	if errors.As(err, &sa) {
		return true
	}
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
