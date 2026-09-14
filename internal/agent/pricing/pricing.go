// Package pricing turns token counts into a dollar ESTIMATE on the device.
//
// ⚠️ **It is an estimate and the word is not decoration.** The client does not
// know the org's negotiated rates, its committed-use discounts, or whether a
// request was billed at all; every figure derived from this table is published
// and rendered as "est." and Atlas's own model_prices overrides it wherever the
// two disagree (docs/v3/contracts.md, D4). This table exists so that
// `send_to_atlas: false` — a Signal running with no cloud at all — can still
// show a person what their day cost, which is most of why anyone opens the app.
//
// ⚠️ **An unknown model has NO estimate, never a guessed one.** Estimate
// returns ok=false and the page says "no rate for <model>". A fabricated rate
// is worse than a blank: it is a confident number over evidence nobody has,
// which is the failure this codebase names at every other layer (see
// window.MIN_EVIDENCE, prior.py's CONTRAST-NEVER-FALLBACK).
//
// The table is a snapshot, refreshed at release time by
// scripts/refresh-prices.py. It is DERIVED from LiteLLM's public
// model_prices_and_context_window.json by way of codeburn's bundled copy
// (github.com/getagentseal/codeburn, MIT, (c) 2026 AgentSeal) and carries only
// the ~326 ids a Keld-supported tool can actually emit; the upstream table is
// 4,739 rows of models nobody here runs.
package pricing

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed prices.json
var pricesJSON []byte

// Rate is USD per token, by token class. CacheWrite/CacheRead are zero when the
// source carried no explicit rate — see Estimate for what that means.
type Rate struct {
	In         float64 `json:"in"`
	Out        float64 `json:"out"`
	CacheWrite float64 `json:"cache_write,omitempty"`
	CacheRead  float64 `json:"cache_read,omitempty"`
}

type table struct {
	Generated string          `json:"generated"`
	Source    string          `json:"source"`
	Note      string          `json:"note"`
	Models    map[string]Rate `json:"models"`
}

var (
	once   sync.Once
	loaded table
)

func load() table {
	once.Do(func() {
		_ = json.Unmarshal(pricesJSON, &loaded)
		if loaded.Models == nil {
			loaded.Models = map[string]Rate{}
		}
	})
	return loaded
}

// Generated is the snapshot's date, so a caller can say how old the estimate's
// source is rather than implying it is live.
func Generated() string { return load().Generated }

// Tokens is one block's spend, in the four classes a Claude-family response
// reports. Field names match message.usage.
type Tokens struct {
	Input         int64
	Output        int64
	CacheRead     int64
	CacheCreation int64
}

// Lookup resolves a model id to a rate. Resolution is EXACT first, then a small
// set of normalisations that cannot change which family is matched: the
// vendor prefix an OTLP exporter may prepend (anthropic/, openai/), a trailing
// @date or -YYYYMMDD build stamp, and case. It never falls back to "the nearest
// model", because the nearest model to opus is sonnet and that is a 3x error.
func Lookup(model string) (Rate, bool) {
	t := load()
	m := strings.TrimSpace(strings.ToLower(model))
	if m == "" {
		return Rate{}, false
	}
	if r, ok := t.Models[m]; ok {
		return r, true
	}
	if i := strings.LastIndex(m, "/"); i >= 0 { // "anthropic/claude-opus-4-8"
		if r, ok := t.Models[m[i+1:]]; ok {
			return r, true
		}
		m = m[i+1:]
	}
	if i := strings.LastIndex(m, "@"); i > 0 { // "claude-opus-4-6@default"
		if r, ok := t.Models[m[:i]]; ok {
			return r, true
		}
	}
	// "claude-opus-4-6-20260205" -> "claude-opus-4-6"
	if i := strings.LastIndex(m, "-"); i > 0 && isDigits(m[i+1:]) && len(m[i+1:]) == 8 {
		if r, ok := t.Models[m[:i]]; ok {
			return r, true
		}
	}
	return Rate{}, false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Estimate is the USD estimate for these tokens on this model, and whether a
// rate was found at all. ok=false means NO ESTIMATE — render a blank, never 0.
//
// A missing cache rate is handled by CHARGING THE INPUT RATE for those tokens
// rather than by fabricating the usual 1.25x/0.1x multipliers: over-charging a
// cache read by 10x would make the estimate wrong in the direction a person
// notices and cannot explain, while the input rate is at least a real number
// this model is billed at. The alternative — dropping the class — silently
// under-reports every cached hour, which for agentic work is most of the spend.
func Estimate(model string, tk Tokens) (float64, bool) {
	r, ok := Lookup(model)
	if !ok {
		return 0, false
	}
	cw, cr := r.CacheWrite, r.CacheRead
	if cw == 0 {
		cw = r.In
	}
	if cr == 0 {
		cr = r.In
	}
	usd := float64(tk.Input)*r.In +
		float64(tk.Output)*r.Out +
		float64(tk.CacheCreation)*cw +
		float64(tk.CacheRead)*cr
	return usd, true
}
