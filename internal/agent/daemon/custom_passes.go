package daemon

import (
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// customSet is a compiled snapshot of the org's custom passes, split into the
// two pipeline waves.
type customSet struct{ wave1, wave2 []enrich.Extractor }

// customHolder holds the live, hot-swappable custom extractor set. The settings
// poll rebuilds and stores a new set; the Worker loads a per-job snapshot. Safe
// for concurrent load/store and safe to call on a nil holder (tests).
type customHolder struct{ v atomic.Pointer[customSet] }

func newCustomHolder() *customHolder { return &customHolder{} }

func (h *customHolder) store(w1, w2 []enrich.Extractor) {
	h.v.Store(&customSet{w1, w2})
}

func (h *customHolder) load() (w1, w2 []enrich.Extractor) {
	if h == nil {
		return nil, nil
	}
	if s := h.v.Load(); s != nil {
		return s.wave1, s.wave2
	}
	return nil, nil
}

// passesFromSchema converts the distributed schema into enrich.CustomPass
// values, keeping ONLY the org's custom passes. Atlas serves built-ins in the
// same flat `passes` map (flagged is_system), and forwarding those made
// BuildCustomExtractors' defensive collision guard reject all 8 on every poll —
// a warn event per built-in per cycle, with the real custom passes unaffected
// (keld-atlas#62). Built-ins are compiled into this binary and never rebuilt as
// extractors, so dropping them here is the whole contract.
//
// Recognized by flag OR key: an Atlas older than is_system still lists built-ins,
// and keying off BuiltinPassKeys too means the client stops spamming without
// waiting for every deployment to upgrade. Kind (structure/relation) filtering
// stays downstream.
func passesFromSchema(s *settings.EnrichmentSchema) []enrich.CustomPass {
	if s == nil {
		return nil
	}
	out := make([]enrich.CustomPass, 0, len(s.Passes))
	for _, p := range s.Passes {
		if p.IsSystem || enrich.BuiltinPassKeys[p.Key] {
			continue
		}
		cp := enrich.CustomPass{
			Key: p.Key, Kind: p.Kind, Title: p.Title,
			ConditionOn: p.ConditionOn, MultiLabel: p.MultiLabel, Version: p.Version,
			ClsThreshold: p.ClsThreshold, // *float64 passthrough (nil vs explicit 0.0)
			Labels:       toCustomLabels(p.Labels),
			LabelsByCond: toCustomLabelsByCond(p.LabelsByCond),
		}
		out = append(out, cp)
	}
	return out
}

func toCustomLabels(in []settings.RemoteLabel) []enrich.CustomLabel {
	out := make([]enrich.CustomLabel, 0, len(in))
	for _, l := range in {
		out = append(out, enrich.CustomLabel{ID: l.ID, Text: l.Text, Description: l.Description, Label: l.Label, Regex: l.Regex})
	}
	return out
}

func toCustomLabelsByCond(in map[string][]settings.RemoteLabel) map[string][]enrich.CustomLabel {
	if in == nil {
		return nil
	}
	out := map[string][]enrich.CustomLabel{}
	for k, v := range in {
		out[k] = toCustomLabels(v)
	}
	return out
}
