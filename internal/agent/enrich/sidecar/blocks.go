package sidecar

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// THE V2 PATH's client half: POST /blocks. Coordinates, ids and instants —
// never text, never a span into a message, never an offset.
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
// Prompts are HUMAN prompt ids and the DAEMON names them, exactly as it does
// for /tick and for the same reason: the store's `prompt` index holds every
// user- AND assistant-shaped turn (~260 rows against 14 human prompts on a real
// session), so a sidecar that discovered its own prompts would make every agent
// turn an episode and swallow the session. They come from
// resolve.RecentPromptIDs, which is a deliberately separate method from the one
// that returns prompt TEXT — see resolve.RecentIDReader.
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
	Prompts   []string `json:"prompts"`
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
// It models the SAME analysis fields AnalyzeResult does, plus the three a block
// has and a window does not: its two boundary reasons and its covers mapping.
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
	// Start and End are EPOCH SECONDS — the unit the cutter keys everything on,
	// the unit `since_ts` is in, and the unit `covers` reports. The Go side
	// converts once, at the publish boundary.
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	StartReason  string  `json:"start_reason"`
	EndReason    string  `json:"end_reason"`
	BlockMinutes float64 `json:"block_minutes"`
	Evidence     int     `json:"evidence"`
	// Covers is the block <-> prompt-episode mapping, parallel to the ids this
	// call sent. Empty for a block no known prompt overlaps, which is an
	// ordinary fact about autonomous work, not a failure.
	Covers           []CoverItem            `json:"covers"`
	Workstreams      map[string]*Workstream `json:"workstreams"`
	Inventory        InventoryBlock         `json:"inventory"`
	InventoryOmitted map[string]int         `json:"inventory_omitted"`
	Dynamics         DynamicsBlock          `json:"dynamics"`
	Effort           *EffortBlock           `json:"effort"`
	Prior            PriorBlock             `json:"prior"`
}

// CoverItem is one episode's overlap with one block as it arrives on the wire:
// an id and two instants clipped to the block, plus whether the episode ended
// inside it. No text and no offset into a message — the sidecar has the id
// because the daemon sent it, and times it against the store.
type CoverItem struct {
	PromptID string  `json:"prompt_id"`
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Complete bool    `json:"complete"`
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
}

// Blocks asks the sidecar which CLOSED blocks of work this transcript has, and
// what each one was.
//
// ok=false on any transport or status failure. There is no partial success to
// report: the emitter's cursor and the blocks are one answer, and advancing a
// cursor past blocks that never arrived would permanently lose exactly the
// characterisation this path exists to publish.
func (c *Client) Blocks(path string, promptIDs []string, since *float64, now time.Time,
	maxBlocks int, resolved enrich.ResolvedFacts) (BlocksResult, bool) {
	var r BlocksResult
	req := blocksReq{
		Path: path, SinceTS: since, Prompts: promptIDs,
		Now:       float64(now.UnixNano()) / 1e9,
		MaxBlocks: maxBlocks,
		Resolved:  resolvedOrNil(resolved),
	}
	if req.Prompts == nil {
		req.Prompts = []string{} // an omitted list and an empty one mean the same thing; say so
	}
	if !c.post("/blocks", req, &r) {
		return BlocksResult{}, false
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
func (c *Client) BlocksCharacterised(path, source, sessionID string, promptIDs []string,
	since *float64, now time.Time, maxBlocks int,
	resolved enrich.ResolvedFacts) ([]enrich.BlockCharacterisation, *float64, bool) {
	res, ok := c.Blocks(path, promptIDs, since, now, maxBlocks, resolved)
	if !ok {
		return nil, nil, false
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
			Covers:  convertCovers(b.Covers),
			Analysis: analysisFrom(b.Workstreams, b.Inventory, b.InventoryOmitted,
				b.Dynamics, b.Effort, b.Prior),
		})
	}
	return out, res.Watermark, true
}

// convertCovers maps the episode overlaps onto the published shape, PER ENTRY
// like every inventory conversion: one unusable entry costs exactly that entry.
//
// The one gate is structural rather than a vocabulary: an entry with no prompt
// id names no episode, and an entry whose interval is empty or inverted covers
// no time — both would render as a zero-width bar attributed to a prompt.
// There is nothing else to check, because there is nothing else in the shape:
// an id the daemon itself supplied, and two instants.
//
// Nil rather than an empty slice when nothing survives, so the published row
// omits the key instead of stating "we mapped the episodes and found none" —
// the same rule every inventory here follows.
func convertCovers(items []CoverItem) []enrich.Cover {
	var out []enrich.Cover
	for _, it := range items {
		if it.PromptID == "" || it.To <= it.From {
			continue
		}
		out = append(out, enrich.Cover{
			PromptID: it.PromptID, From: it.From, To: it.To, Complete: it.Complete,
		})
	}
	return out
}

// epochRFC3339 renders an epoch-second instant as a UTC RFC3339 string, the one
// spelling of an instant the published row carries. UTC so two renderings of
// one moment cannot become two ids under Atlas's (session, start) identity.
func epochRFC3339(ts float64) string {
	sec := int64(ts)
	nsec := int64((ts - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC().Format(time.RFC3339Nano)
}
