package daemon

import (
	"errors"
	"log"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/pricing"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// recordCut is the emitter's OnCut hook: every block this sweep BUILT, before
// any publish is attempted.
//
// ⚠️ **This is where the ledger learns a block exists, and it deliberately does
// not wait for a successful publish.** The first version hung everything off
// OnPublished, which fires only on success — so with Send to Atlas off, or with
// Atlas simply unreachable, no block ever reached the ledger and the page
// showed an empty day on a machine that had worked all afternoon. A recorder
// that only hears about successes cannot report a failure, which is the one
// thing this page exists to do. Found by running it end to end, not by a test.
//
// ⚠️ **It must never be slow and must never panic into the emitter.** This runs
// on the sweep goroutine that gets work to Atlas; a ledger write that blocked
// it would trade the product for the window onto it. Every ledger write is
// fire-and-forget and the attribution pass is a pure function over a document
// the store holds in memory.
func (v *v3) recordCut(rows []publish.BlockEnrichment, _ string) {
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
		dims := dimsOf(r.Workstreams)
		v.ledger.Observe(k, dims, now)
		// ⚠️ **And teach this session's EARLIER blocks the checkout.** Resolving
		// a workspace is a whole-file pre-pass in the sidecar, so the first
		// blocks of a session close before it knows the repository and would
		// otherwise keep no `repo` dim forever — grouping that work under a bare
		// directory name while the same session's later blocks group under the
		// remote. A transcript is scoped to one checkout, so this is filling in
		// something that was always true, never inventing it. See
		// ledger.BackfillSessionDims for why `branch` is excluded.
		v.ledger.BackfillSessionDims(k.Session, dims, now)
		// ⚠️ **NO FIGURE IS NOT A FIGURE OF ZERO.** A sidecar older than SCHEMA
		// 18 sends no token counts, and writing zeros for those blocks would put
		// "0 tokens · $0.00 est." on a page describing an afternoon of work —
		// a confident number over evidence nobody has, which is the exact
		// failure this ledger exists to prevent. Measured therefore stays
		// UNKNOWN, stated as such, and the page renders a dash.
		if m, known := measuredOf(r); known {
			v.ledger.Measure(k, m, now)
		} else {
			v.ledger.NotApplicable(k, ledger.StageMeasured, ledger.ReasonNoTokens, now)
		}
		v.attributeAndRecord(k, r, now)
	}
}

// recordCutPending is the emitter's OnCutPending hook: this sweep could not
// ask the analysis service for the transcript's blocks at all — the sidecar
// has no /blocks route, or could not answer for any other reason (not ready,
// restarting, its own store behind the ask). No block exists yet, so this is
// keyed by SESSION rather than by a BlockKey; it clears itself the moment a
// later sweep succeeds — ledger.Store.Cut deletes the pending row for a
// session the instant it cuts a real block for it (TestCutPendingClearedByLaterCut).
func (v *v3) recordCutPending(session, reason string) {
	if v == nil || v.ledger == nil {
		return
	}
	v.ledger.CutPending(session, ledger.Reason(reason), time.Now().UTC())
}

// recordDelivered is the OnPublished hook: the batch reached Atlas.
//
// `received` is written from the same fact `sent` is, because the emitter only
// calls this after SendBlocks returned no error — and SendBlocks now confirms
// delivery from the RESPONSE, rejecting a captive portal's 2xx (see
// publish.SendBlocksResult). With Send to Atlas off both cells are recorded as
// NOT APPLICABLE rather than as success: nothing was sent, and a green tick
// against a machine that published nothing is the confident lie this whole
// build exists to remove.
func (v *v3) recordDelivered(rows []publish.BlockEnrichment, _ string) {
	if v == nil || v.ledger == nil {
		return
	}
	now := time.Now().UTC()
	for _, r := range rows {
		start, ok := epochOf(r.Window.Start)
		if !ok || r.SessionID == "" {
			continue
		}
		k := ledger.BlockKey{Session: r.SessionID, Start: start}
		if !v.atlasOn {
			v.ledger.NotApplicable(k, ledger.StageSent, ledger.ReasonAtlasOff, now)
			v.ledger.NotApplicable(k, ledger.StageReceived, ledger.ReasonAtlasOff, now)
			continue
		}
		v.ledger.Sent(k, now)
		v.ledger.Received(k, 200, now)
	}
}

// recordPublishFailed is the OnPublishFailed hook: the batch did not land, and
// the reason a person reads on the page is the reason the transport gave.
//
// ⚠️ **WHICH CELL FAILS DEPENDS ON WHETHER ATLAS EVER ANSWERED.** `sent` and
// `received` are different facts (recorder.go: "the publisher POSTed it" vs
// "Atlas acknowledged it"), and a batch can fail at either one. A captive
// portal or a 4xx/5xx status means the request reached a server and got an
// HTTP response back — the bytes were sent — so what failed is the ANSWER,
// and the page must say `received`, not `sent` (see docs/v3/contracts.md's
// own wire example: `sent: ok`, `received: failed, atlas_rejected, 401` — a
// rejected credential does not mean the POST never left the machine). Only a
// raw transport error, where no response was reached at all (DNS, connection
// refused, TLS, timeout), is a `sent` failure: there is no answer to blame,
// so there is nothing for `received` to say.
func (v *v3) recordPublishFailed(rows []publish.BlockEnrichment, err error) {
	if v == nil || v.ledger == nil {
		return
	}
	now := time.Now().UTC()
	stage, reason, status := classifyPublishFailure(err)
	for _, r := range rows {
		start, ok := epochOf(r.Window.Start)
		if !ok || r.SessionID == "" {
			continue
		}
		k := ledger.BlockKey{Session: r.SessionID, Start: start}
		if stage == ledger.StageReceived {
			// An HTTP response came back, so the POST itself succeeded — it is
			// Atlas's answer that failed, not the sending of it.
			v.ledger.Sent(k, now)
		}
		v.ledger.Failed(k, stage, reason, status, now)
	}
}

// classifyPublishFailure turns a transport error into the closed (stage,
// reason, http_status) triple the page renders as a sentence against the
// right cell. The reasons are the ones a person can act on differently:
// re-pair, wait, report, or check the network they are on.
func classifyPublishFailure(err error) (ledger.Stage, ledger.Reason, int) {
	if err == nil {
		return ledger.StageSent, ledger.ReasonNone, 0
	}
	if errors.Is(err, publish.ErrIntercepted) {
		// A 2xx came back — something answered — so transmission succeeded;
		// what failed is that the answer was not from Atlas.
		return ledger.StageReceived, ledger.ReasonCaptivePortal, 0
	}
	var se *retry.StatusError
	if errors.As(err, &se) {
		// Any HTTP status, good or bad, means the POST reached a server and
		// it answered. Transmission succeeded; this is reporting the answer.
		return ledger.StageReceived, classifyAtlasStatus(se.Code), se.Code
	}
	// No response was reached at all, so there is no answer to attribute the
	// failure to — the transmission itself is what failed.
	return ledger.StageSent, ledger.ReasonAtlasUnavailable, 0
}

// classifyPublishError is classifyPublishFailure without the stage — kept for
// callers that record a publish failure against one fixed cell regardless of
// which fact actually failed (internal/agent/daemon/republish.go, B2's
// recovery sweep for blocks captured while Send to Atlas was off). The reason
// vocabulary and its (reason, http_status) shape are UNCHANGED from before
// the stage split above; only recordPublishFailed's own caller needed to know
// which cell to write.
func classifyPublishError(err error) (ledger.Reason, int) {
	_, reason, status := classifyPublishFailure(err)
	return reason, status
}

// classifyAtlasStatus turns a raw HTTP status Atlas answered with into the
// same closed reason vocabulary a block's `received` cell uses, so the health
// strip's `atlas` row (see startHealth) and a block's own delivery cell never
// disagree about what a given status means. status 0 here means "we asked
// and got no usable response" (a network fault, or an intercepted 2xx) — a
// FAILURE, not "never tried"; "never tried" is a fact about the instant, not
// the status, and is handled by the caller before this is reached.
func classifyAtlasStatus(status int) ledger.Reason {
	switch {
	case status == 401 || status == 403:
		return ledger.ReasonAtlasRejected
	case status >= 500 || status == 0:
		return ledger.ReasonAtlasUnavailable
	default:
		return ledger.ReasonAtlasRefused
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
func measuredOf(r publish.BlockEnrichment) (ledger.Measured, bool) {
	if r.Tokens == nil {
		// The second return is what keeps "this sidecar does not report spend"
		// distinguishable from "this block cost nothing".
		return ledger.Measured{}, false
	}
	m := ledger.Measured{
		Model:               dominantModel(r.Workstreams),
		InputTokens:         r.Tokens.Input,
		OutputTokens:        r.Tokens.Output,
		CacheReadTokens:     r.Tokens.CacheRead,
		CacheCreationTokens: r.Tokens.CacheCreation,
		RequestTokens:       r.Tokens.Request,
	}
	if r.Requests != nil {
		m.Requests = *r.Requests
	}
	if usd, ok := pricing.Estimate(m.Model, pricing.Tokens{
		Input:         m.InputTokens,
		Output:        m.OutputTokens,
		CacheRead:     m.CacheReadTokens,
		CacheCreation: m.CacheCreationTokens,
	}); ok {
		m.EstimateUSD = usd
	}
	return m, true
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
