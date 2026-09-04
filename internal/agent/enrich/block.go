package enrich

// THE V2 BLOCK PATH — a transcript cut into BLOCKS OF WORK, one
// characterisation each. See docs/superpowers/specs/2026-08-25-v2-block-path-design.md.
//
// v2 is a PATH, not a parameter. Nothing here decorates the v1 window types
// beside it: a WindowCharacterisation is a 60-minute look-back ANCHORED TO A
// PROMPT, and a BlockCharacterisation is a span the cutter chose with no prompt
// anchor and no look-back at all. The two carry the same WindowAnalysis because
// it is literally the same deterministic analysis over different bounds — that
// is composition, not v2 reaching sideways into v1.

// BlockReasons is the closed vocabulary a block's two boundaries are named
// from, mirroring the sidecar's app/analysis/blocks.py REASONS.
//
// IT IS A GATE, NOT DOCUMENTATION, for the reason every other mirrored
// vocabulary in this package is one: the sidecar is frozen and shipped
// separately from keld-agent, so an older or newer one can sit in
// ~/.local/bin indefinitely, and a reason this binary cannot read is version
// skew. A boundary reason is the one field that distinguishes an ARITHMETIC cut
// ("budget" — we had to stop somewhere, the plurality case at 48.5%) from a
// claim there was no work at all ("idle"); publishing an unreadable one would
// let a reader draw a pause that never happened.
var BlockReasons = map[string]bool{
	"session_start": true,
	"idle":          true,
	"budget":        true,
	"session_end":   true,
}

// KnownBlockReason reports whether r is in the published boundary vocabulary.
func KnownBlockReason(r string) bool { return BlockReasons[r] }

// BlockCorrScheme is the correlation scheme a block row carries.
//
// A block is not a prompt and not a tick window: it has its own Atlas route
// (POST /v1/signal/blocks) and its own identity, `(session, block.start)`. The
// scheme is stated anyway, and stated distinctly, because the row also carries
// a correlation object for readers that key on one — and sharing "window" with
// a tick row would make two different units indistinguishable on a key.
const BlockCorrScheme = "block"

// PipelineStatusBlock is what a block row reports for pipeline_status.
//
// A SIXTH value beside enriched/partial/gated/window, for the same reason
// PipelineStatusWindow is a fifth: the others describe how a PROMPT's pipeline
// went, and a block row ran one deterministic pass with no text facet to have
// succeeded, failed or been gated. Naming the row's own kind is the only honest
// option and doubles as the flag saying why no text facet is present.
//
// It is deliberately NOT gated by SchemaVersion and did not bump it: the
// enrichment schema version governs the LABEL VOCABULARIES in labels.go that
// the eval scores, and this is a row-kind marker on a different endpoint's
// contract. Bumping it would force an eval re-run over facets this path does
// not compute.
const PipelineStatusBlock = "block"

// ⚠️ THERE IS NO `Cover` TYPE, AND THIS PATH HAS NO PROMPT-ID SPACE.
//
// There used to be one: a block <-> prompt-episode mapping (`covers`), an id
// plus two clipped instants plus a `complete` flag. It was DELETED, not
// repaired, and the argument is worth keeping here because the type is the
// obvious thing to re-add.
//
// A block is TIME end to end — a cap, a silence threshold, a rollup over a
// span — and the principal comes from the device's own auth. Nothing in the
// cutter, the digest, the dimensions, the dynamics, the prior or the effort
// touches a prompt id. Atlas must join on time REGARDLESS: cost attribution is
// `event_ts ∈ [block.start, block.end)` within the session, never through a
// prompt mapping, because a turn spanning several blocks would double-count its
// spend through one. That mandatory join answers the DISPLAY question too —
// Atlas holds `ToolEvent.session_id` and `event_ts`, so given a span it can
// find the events inside, which IS which turns overlap, and "this turn
// continues past the block" falls out of the same rows. So `covers` was a
// second, weaker copy of a join Atlas owes anyway.
//
// It also never worked: the daemon's own human-prompt filter
// (watch/filter.go) yields `promptId` while the sidecar's store indexes the
// per-message `uuid`, so the store's lookup resolved NONE of the ids it was
// sent and every real run published an empty list.
//
// A block is exactly (principal, session, span, boundary reasons, facets).

// BlockRef is where a block sits and how big it was. The block equivalent of
// WindowRef, and separate from it because the two are not interchangeable: a
// window's bounds are derived from a prompt's instant minus a fixed span, and a
// block's are the cutter's own decision, which is why this one additionally
// names WHY each edge is where it is.
type BlockRef struct {
	// Start and End are RFC3339 instants, half-open [Start, End). The sidecar
	// answers in epoch seconds — the unit its cursor is in — and the
	// conversion happens once, at the decode boundary, so the wire carries
	// one spelling of an instant rather than two.
	Start string `json:"start"`
	End   string `json:"end"`
	// SpanMinutes is End-Start. Fractional, like a window's: a block ends at a
	// bin edge but starts at one too, and rounding either would move a boundary
	// the cutter measured.
	SpanMinutes float64 `json:"span_minutes"`
	// Evidence is how many reference events the block held. Always > 0 on a
	// published row — a block with none is dropped rather than published, so a
	// quiet machine emits nothing.
	Evidence int `json:"evidence"`
	// StartReason and EndReason are from BlockReasons and are never
	// interchangeable. See BlockReasons.
	StartReason string `json:"start_reason"`
	EndReason   string `json:"end_reason"`
}

// BlockCharacterisation is one closed block of work: where it sits, why its
// edges are where they are, and the same deterministic analysis a window row
// carries. No prompt ids — see the note above the BlockRef type.
//
// StartTS/EndTS are the epoch-second forms the sidecar answered with, kept
// alongside the RFC3339 Ref because the EMITTER'S CURSOR is in those units: it
// resumes by passing the last emitted block's End back as `since_ts`, and
// round-tripping that through a string would be a needless second spelling of
// the one number correctness depends on. They do not publish.
type BlockCharacterisation struct {
	// SessionID is the transcript's own session identifier (Claude Code's file
	// stem), not the reference series' internal key — that is a machine-local
	// path digest and joins to nothing.
	SessionID string
	Source    string
	Ref       BlockRef
	StartTS   float64
	EndTS     float64
	Analysis  WindowAnalysis
}

// BlocksAnswer is everything the analysis service said about one transcript's
// closed blocks: the rows, the watermark, and whether it could answer at all.
//
// ⚠️ IT EXISTS FOR ITS FOURTH FIELD, and a bare `ok` bool is what let a real
// defect run for three weeks. An Aug 11 sidecar has no `/blocks` route at all —
// it predates it — so it answers 404. Under the old three-value return that
// arrived at the emitter as `ok == false`, indistinguishable from "the store is
// behind" or "nothing has closed yet", and the emitter's correct response to
// those (hold the cursor, say nothing, try next sweep) is exactly wrong here:
// the next sweep cannot succeed either, and the machine published zero blocks
// while looking healthy from every angle. See
// docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md.
//
// The HOLD is still right — a sidecar update makes the work doable, and the
// cursor is what asks for the same ground again — so RouteUnsupported changes
// what is SAID, not what is done. That is the same call `/attribute` already
// makes with AttributeResult.RouteUnsupported, and this is that rule
// generalized rather than a second one.
type BlocksAnswer struct {
	Blocks    []BlockCharacterisation
	Watermark *float64
	// OK is false for every failure, RouteUnsupported included: a caller that
	// only wants "did I get rows" reads this one field and is unaffected.
	OK bool
	// RouteUnsupported: the service answered 404 — it has no /blocks route,
	// which is version skew rather than an empty answer. Never true with OK.
	RouteUnsupported bool
}
