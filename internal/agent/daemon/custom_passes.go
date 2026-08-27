package daemon

import (
	"sort"
	"strings"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// rejectReporter reports a pass we could not build ONCE per distinct reject set, not once per
// settings poll.
//
// A reject is a static fact about the org's configuration: the same pass fails identically on
// every poll, on every machine, until an admin edits it. onRemote runs on every successful poll
// (5m default), so emitting from there directly turned one bad pass into ~288 client events per
// machine per day, indefinitely — enough to clear Atlas's attention threshold and pin every
// device in the fleet to "Needs attention" over what is really one org-level config problem.
//
// Change-driven instead: emit the whole set when it first appears and whenever it differs from
// last time — including when a reject RETURNS after being fixed, which is a regression an admin
// needs to hear about. A daemon restart re-emits once, which is the floor we want: the platform
// still learns the current state from a cold start.
type rejectReporter struct{ last string }

// report calls emit for each reject, but only when the set differs from the last call.
func (r *rejectReporter) report(rejects []enrich.CustomReject, emit func(enrich.CustomReject)) {
	d := rejectDigest(rejects)
	if d == r.last {
		return
	}
	r.last = d
	for _, rj := range rejects {
		emit(rj)
	}
}

// syncCustomPasses rebuilds the org's custom extractors from a polled schema, hot-swaps them in,
// and reports any pass that could not be built. Named rather than inlined in Run's onRemote
// closure so the "report a static reject once, not once per poll" contract is testable without
// standing up a daemon. emit matches clientevents.Emitter.Emit.
func syncCustomPasses(s *settings.EnrichmentSchema, custom *customHolder, rejects *rejectReporter,
	emit func(string, clientevents.Severity, map[string]any)) {
	w1, w2, rejected := enrich.BuildCustomExtractors(passesFromSchema(s))
	custom.store(w1, w2)
	rejects.report(rejected, func(rj enrich.CustomReject) {
		emit("enrich.custom.rejected", clientevents.SevWarn,
			map[string]any{"key": rj.Key, "reason": rj.Reason})
	})
}

// rejectDigest is an ORDER-INDEPENDENT fingerprint of a reject set. passesFromSchema ranges over
// settings.EnrichmentSchema.Passes, a map, so Go's randomized iteration order means an unchanged
// org config yields the same rejects in a different order on most polls. Sorting is what makes
// the comparison above mean "the set changed" rather than "the map iterated differently".
func rejectDigest(rejects []enrich.CustomReject) string {
	if len(rejects) == 0 {
		return ""
	}
	parts := make([]string, 0, len(rejects))
	for _, rj := range rejects {
		parts = append(parts, rj.Key+"="+rj.Reason)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

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
