package daemon

import (
	"log"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/pricing"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// recordPublished is the block emitter's OnPublished hook, teaching the ledger
// what the daemon has always known and never written down: which blocks were
// cut, what they cost, which project they belong to, and that they were sent.
//
// ⚠️ **It runs AFTER a successful publish, so `sent` is a fact and `received`
// is the status Atlas answered with.** The emitter only calls this hook when
// SendBlocks returned no error, which is why there is no failure path here: a
// batch that failed never reaches this function, and the ledger learns about it
// from the publisher's own recording instead. Recording a success here and a
// failure elsewhere would be two writers for one cell.
//
// ⚠️ **It must never be slow and must never panic into the emitter.** This runs
// on the emitter's sweep goroutine, which is the one that gets blocks to Atlas;
// a ledger write that blocked it would trade the product for the window onto
// it. Every ledger write is already fire-and-forget, and the attribution pass
// below is a pure function over a document the store keeps in memory.
func (v *v3) recordPublished(rows []publish.BlockEnrichment, _ string) {
	if v == nil || v.ledger == nil {
		return
	}
	now := time.Now().UTC()
	for _, r := range rows {
		start, okS := epochOf(r.Window.Start)
		end, _ := epochOf(r.Window.End)
		k := ledger.BlockKey{Session: r.SessionID, Start: start}
		if k.Session == "" || !okS {
			continue
		}
		v.ledger.Cut(k, end, r.StartReason, r.EndReason, r.Source.ID, now)
		v.ledger.Observe(k, dimsOf(r.Workstreams), now)
		v.ledger.Measure(k, measuredOf(r), now)
		v.attributeAndRecord(k, r, now)
		v.ledger.Sent(k, now)
	}
}

// epochOf parses the RFC3339 instant a block's span is spelled in on the wire.
// The sidecar answers in epoch seconds and the decode boundary converts once,
// so the wire carries one spelling of an instant (enrich.BlockRef); the ledger
// keys on seconds, so this converts back. A block whose start will not parse is
// SKIPPED rather than keyed at zero, which would collide every such block onto
// one row and make the pane silently wrong instead of visibly short.
func epochOf(s string) (int64, bool) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, false
	}
	return t.Unix(), true
}

// dimsOf lifts the three dimensions the Projects pane groups on out of a
// block's published workstreams. A dimension that did not reach `attributed` is
// left empty: "only 'attributed' may be read as the window's answer" is the
// workstreams contract's own rule, and a thin repo is not evidence to group a
// person's work by.
func dimsOf(ws map[string]enrich.Labeled) ledger.Dims {
	get := func(key string) string {
		l, ok := ws[key]
		if !ok || l.Value == "" {
			return ""
		}
		if l.Status != "" && l.Status != enrich.WorkstreamAttributed {
			return ""
		}
		return l.Value
	}
	return ledger.Dims{
		Repo:      get(projects.DimRepo),
		Branch:    get(projects.DimBranch),
		Workspace: get(projects.DimWorkspace),
	}
}

// measuredOf turns a block's published token counts into the ledger's measured
// cell, and prices it locally.
//
// ⚠️ **The estimate is computed HERE rather than by the page** so that the
// number the ledger holds and the number a person reads are the same number. It
// is an estimate in the strict sense — the client does not know the org's
// negotiated rates — and every surface says "est." for that reason. An unknown
// model produces no estimate at all rather than zero; see internal/agent/pricing.
func measuredOf(r publish.BlockEnrichment) ledger.Measured {
	m := ledger.Measured{Model: dominantModel(r.Workstreams)}
	if r.Requests != nil {
		m.Requests = *r.Requests
	}
	if r.Tokens != nil {
		m.InputTokens = r.Tokens.Input
		m.OutputTokens = r.Tokens.Output
		m.CacheReadTokens = r.Tokens.CacheRead
		m.CacheCreationTokens = r.Tokens.CacheCreation
		m.RequestTokens = r.Tokens.Request
	}
	if usd, ok := pricing.Estimate(m.Model, pricing.Tokens{
		Input:         m.InputTokens,
		Output:        m.OutputTokens,
		CacheRead:     m.CacheReadTokens,
		CacheCreation: m.CacheCreationTokens,
	}); ok {
		m.EstimateUSD = usd
	}
	return m
}

func dominantModel(ws map[string]enrich.Labeled) string {
	if l, ok := ws["model"]; ok && l.Value != "" {
		if l.Status == "" || l.Status == enrich.WorkstreamAttributed {
			return l.Value
		}
	}
	return ""
}

// attributeAndRecord runs the DETERMINISTIC attribution pass for one block and
// records its outcome — including, deliberately, the outcomes that are not a
// project: no rule matched, or two projects claim the same one.
//
// ⚠️ **A conflict is recorded as a conflict, never resolved by picking first.**
// Two projects tagged with the same repository is a configuration error only a
// person can settle, and choosing one silently would put a confident number
// against work that belongs to neither.
func (v *v3) attributeAndRecord(k ledger.BlockKey, r publish.BlockEnrichment, now time.Time) {
	if v.projects == nil {
		return
	}
	doc, err := v.projects.Load()
	if err != nil {
		// The projects document could not be read. That is not "no project" —
		// it is "we could not tell" — so nothing is recorded and the cell stays
		// ABSENT, which the page renders as unknown rather than as unattributed.
		return
	}
	// The candidate set is the local document's projects PLUS the org's pooled
	// workstream values, which are the vocabulary and arrive on the settings
	// poll. The vector pass is nil here: this is the deterministic path, and a
	// nil Vector is what makes "unattributed" mean "no rule matched" rather
	// than "the encoder was not asked".
	candidates := append([]projects.Project(nil), doc.Projects...)
	if v.projects.RemoteProjects != nil {
		candidates = append(candidates, projects.FromRemoteProjects(v.projects.RemoteProjects())...)
	}
	res := projects.Attribute(r.Workstreams, candidates,
		projects.WorkstreamOffFunc(settings.Load()), nil)
	v.ledger.Attribute(k, ledger.Attributed{
		ProjectID: res.ProjectID,
		Method:    ledger.Method(res.Method),
		Conflict:  res.Conflict,
	}, ledger.Reason(res.Reason), now)
}

// chainOnPublished runs two OnPublished hooks in order, tolerating a nil first
// (attribution is off on most machines, which leaves it nil) and isolating each
// from the other's panic.
//
// ⚠️ **The isolation is not decoration.** This hook runs on the block emitter's
// sweep goroutine — the one that gets work to Atlas. A panic there would take
// down publishing in order to record that publishing happened, which is the
// exact inversion the ledger exists to prevent, and the emitter has no recover
// of its own on this path.
func chainOnPublished(first, second func([]publish.BlockEnrichment, string)) func([]publish.BlockEnrichment, string) {
	return func(rows []publish.BlockEnrichment, path string) {
		guard := func(name string, fn func([]publish.BlockEnrichment, string)) {
			if fn == nil {
				return
			}
			defer func() {
				if r := recover(); r != nil {
					log.Printf("keld-agent: %s hook panicked, ignoring: %v", name, r)
				}
			}()
			fn(rows, path)
		}
		guard("block attribution", first)
		guard("ledger", second)
	}
}
