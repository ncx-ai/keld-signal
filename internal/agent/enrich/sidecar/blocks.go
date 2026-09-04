package sidecar

import (
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// THE V2 PATH's client half: POST /blocks. Coordinates and instants — never
// text, never an id, never a span into a message, never an offset.
//
// v2 IS A PATH, NOT A PARAMETER (design spec
// docs/superpowers/specs/2026-08-25-v2-block-path-design.md). This file is its
// own request, its own response and its own entry point; Analyze and Tick are
// untouched. What it DOES share is analysisFrom — the one conversion from a
// sidecar analysis payload into enrich.WindowAnalysis — because a block and a
// window are the same deterministic analysis over different bounds, and a
// second copy of that assignment list is how a dimension comes to be readable
// on one row and deleted on another.

// blocksReq is one /blocks call over one transcript.
//
// ⚠️ IT CARRIES NO PROMPT IDS, and that is the deletion rather than an omission.
// It used to carry a `prompts` list feeding the sidecar's `covers` mapping; both
// are gone. A block is TIME end to end and Atlas has to join
// `event_ts ∈ [start, end)` within the session for cost attribution regardless —
// a turn spanning several blocks would double-count its spend through a prompt
// mapping — and that join answers the display question too. The list also never
// resolved: the daemon's filter yields `promptId` and the store indexes the
// per-message `uuid`. The sidecar's request model ignores an unknown key, so an
// older daemon still sending one is tolerated rather than 422'd.
//
// SinceTS is compared against a block's START, `>=`, so the caller resumes by
// passing the LAST EMITTED BLOCK'S END: blocks abut inside an active segment,
// which admits the next block and excludes the one already emitted. Nil means
// "from the beginning of the session" — BACKFILL — and the caller owns that
// choice; the emitter seeds its own cursor forward-only instead (see
// internal/agent/blocks).
//
// Now is the daemon's clock, sent rather than read sidecar-side, because it is
// what the trailing block's idle settle is measured against and a caller that
// cannot move it cannot test the rule.
type blocksReq struct {
	Path      string   `json:"path"`
	SinceTS   *float64 `json:"since_ts"`
	Now       float64  `json:"now"`
	MaxBlocks int      `json:"max_blocks"`
	// Resolved rides a /blocks call for the reason it rides /analyze and
	// /ingest: the sidecar is confined out of reading a repo's .git/config, and
	// a block that could not name its repository is a lesser block for no
	// reason. Per TRANSCRIPT, the granularity the facts have.
	Resolved *enrich.ResolvedFacts `json:"resolved,omitempty"`
}

// BlockResult is one closed block as it arrives on the wire.
//
// It models the SAME analysis fields AnalyzeResult does, plus the two a block
// has and a window does not: its two boundary reasons.
// It deliberately does NOT embed AnalyzeResult — a window's `window_start`/
// `window_end` are ISO instants anchored to a prompt and a block's bounds are
// epoch seconds chosen by the cutter, so sharing the struct would mean carrying
// two spellings of "where does this sit" and letting a caller read the wrong
// one. Everything that IS the same is converted by the same functions.
//
// What is not modelled here is structurally unforwardable, exactly as on
// AnalyzeResult: no per-side dynamics value, no sizer detail, no timestamps
// beyond the block's own bounds, and no `inventory` key this binary does not
// name.
type BlockResult struct {
	Schema  int    `json:"schema"`
	Session string `json:"session"`
	// Start and End are EPOCH SECONDS — the unit the cutter keys everything on
	// and the unit `since_ts` is in. The Go side converts once, at the publish
	// boundary.
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	StartReason  string  `json:"start_reason"`
	EndReason    string  `json:"end_reason"`
	BlockMinutes float64 `json:"block_minutes"`
	Evidence     int     `json:"evidence"`
	// ⚠️ No `covers`, deliberately. A sidecar old enough to still answer with one
	// has it DROPPED here — an unmodelled key is ignored by encoding/json — for
	// the reason no other unforwardable key is modelled: a field this binary
	// names is a field a publish path can forward.
	Workstreams      map[string]*Workstream `json:"workstreams"`
	Inventory        InventoryBlock         `json:"inventory"`
	InventoryOmitted map[string]int         `json:"inventory_omitted"`
	Dynamics         DynamicsBlock          `json:"dynamics"`
	Effort           *EffortBlock           `json:"effort"`
	Prior            PriorBlock             `json:"prior"`
	// Tokens and Requests are the block's own spend, added by sidecar SCHEMA
	// 18. A POINTER because absent and zero are different facts: a sidecar
	// older than 18 sends no `tokens` key at all, and reporting that machine's
	// every block as costing nothing would be a confident number over evidence
	// nobody has — the page says "no figure" instead. Counts only; no rate, no
	// money. Pricing is the client's own, from a local table.
	Tokens   *BlockTokens `json:"tokens"`
	Requests *int         `json:"requests"`
}

// BlockTokens is one block's consumption, by the four classes a response
// reports plus the price-weighted rollup weight.
//
// ⚠️ `Request` is NOT a token count a person should ever be shown. It is the
// weight `turn_magnitude` uses to make a rollup comparable across models, and
// rendering it as "tokens" would read as a wrong number. What a card shows is
// input + output + cache_read + cache_creation (docs/v3/contracts.md).
type BlockTokens struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
	Request       int64 `json:"request"`
}

// BlocksResult is POST /blocks' whole answer.
//
// Watermark is returned even when no block closed, and that is the one fact
// separating "nothing has settled yet" from "this transcript has never been
// ingested" (nil). The emitter uses it to seed a first-sight cursor
// FORWARD-ONLY, which is why it must be a pointer rather than a zero float: 0
// is a real instant and would backfill the whole of 1970 onwards.
type BlocksResult struct {
	Blocks    []BlockResult `json:"blocks"`
	Watermark *float64      `json:"watermark"`
	// RouteUnsupported is set by Blocks, never by the wire: the sidecar answered
	// 404, so it has no /blocks route to decode a body from.
	RouteUnsupported bool `json:"-"`
}

// Blocks asks the sidecar which CLOSED blocks of work this transcript has, and
// what each one was.
//
// ok=false on any transport or status failure. There is no partial success to
// report: the emitter's cursor and the blocks are one answer, and advancing a
// cursor past blocks that never arrived would permanently lose exactly the
// characterisation this path exists to publish.
func (c *Client) Blocks(path string, since *float64, now time.Time,
	maxBlocks int, resolved enrich.ResolvedFacts) (BlocksResult, bool) {
	var r BlocksResult
	req := blocksReq{
		Path: path, SinceTS: since,
		Now:       float64(now.UnixNano()) / 1e9,
		MaxBlocks: maxBlocks,
		Resolved:  resolvedOrNil(resolved),
	}
	// postStatus rather than post, for the STATUS: a 404 here means this sidecar
	// has no /blocks route at all — it predates the route — and that is a
	// different fact from "could not answer". The same reading /attribute
	// already makes. See enrich.BlocksAnswer.
	ok, status := c.postStatus("/blocks", req, &r)
	if !ok {
		return BlocksResult{RouteUnsupported: status == http.StatusNotFound}, false
	}
	return r, true
}

// BlocksCharacterised is Blocks in the shape the block emitter publishes: each
// block converted through the SAME gates a window row goes through
// (analysisFrom), plus this path's own two.
//
// sessionID is supplied by the caller rather than taken from the response: the
// response's `session` is the reference series' own key, a digest of the
// transcript's absolute path, which is machine-local and joins to nothing
// downstream.
//
// TWO REFUSALS OF ITS OWN, both defence in depth against a sidecar that stopped
// honouring a rule this side depends on:
//
//   - A block with no evidence, or with no span, is DROPPED. It is not a
//     characterisation of nothing, it is the absence of one, and publishing it
//     turns a quiet machine into a stream of empty rows.
//   - A block whose boundary reason is not in enrich.BlockReasons is DROPPED
//     WHOLE. The reasons are the only fields that say whether an edge is a real
//     pause or an arithmetic cut, and the sidecar ships frozen and separately —
//     so an unreadable reason is version skew, and a block published without one
//     invites a reader to draw a boundary nobody measured. Dropping the block
//     rather than the field is the same call convertEffort makes: a joined
//     statement is not interpretable in half.
//
// The watermark is returned so the caller can seed a first-sight cursor without
// a second call. nil means the transcript has never been ingested.
func (c *Client) BlocksCharacterised(path, source, sessionID string,
	since *float64, now time.Time, maxBlocks int,
	resolved enrich.ResolvedFacts) enrich.BlocksAnswer {
	res, ok := c.Blocks(path, since, now, maxBlocks, resolved)
	if !ok {
		return enrich.BlocksAnswer{RouteUnsupported: res.RouteUnsupported}
	}
	out := make([]enrich.BlockCharacterisation, 0, len(res.Blocks))
	for _, b := range res.Blocks {
		if b.Evidence <= 0 || b.End <= b.Start {
			continue
		}
		if !enrich.KnownBlockReason(b.StartReason) || !enrich.KnownBlockReason(b.EndReason) {
			continue
		}
		out = append(out, enrich.BlockCharacterisation{
			SessionID: sessionID,
			Source:    source,
			Ref: enrich.BlockRef{
				Start:       epochRFC3339(b.Start),
				End:         epochRFC3339(b.End),
				SpanMinutes: (b.End - b.Start) / 60.0,
				Evidence:    b.Evidence,
				StartReason: b.StartReason,
				EndReason:   b.EndReason,
			},
			StartTS: b.Start,
			EndTS:   b.End,
			Analysis: analysisFrom(b.Workstreams, b.Inventory, b.InventoryOmitted,
				b.Dynamics, b.Effort, b.Prior),
			Tokens:   tokensFrom(b.Tokens),
			Requests: b.Requests,
		})
	}
	return enrich.BlocksAnswer{Blocks: out, Watermark: res.Watermark, OK: true}
}

// tokensFrom converts the sidecar's per-block spend, preserving ABSENCE: a
// sidecar older than SCHEMA 18 sends no `tokens` key, and nil must survive all
// the way to the page rather than becoming a zero that reads as "this block
// cost nothing". The same distinction BlocksAnswer's fourth field exists for,
// one level down.
func tokensFrom(t *BlockTokens) *enrich.BlockTokens {
	if t == nil {
		return nil
	}
	return &enrich.BlockTokens{
		Input:         t.Input,
		Output:        t.Output,
		CacheRead:     t.CacheRead,
		CacheCreation: t.CacheCreation,
		Request:       t.Request,
	}
}

// epochRFC3339 renders an epoch-second instant as a UTC RFC3339 string, the one
// spelling of an instant the published row carries. UTC so two renderings of
// one moment cannot become two ids under Atlas's (session, start) identity.
func epochRFC3339(ts float64) string {
	sec := int64(ts)
	nsec := int64((ts - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC().Format(time.RFC3339Nano)
}
