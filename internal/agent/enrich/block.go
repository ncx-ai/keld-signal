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

// Cover is one prompt EPISODE's overlap with one block: which human prompt, the
// portion of its episode that falls inside this block, and whether the episode
// ENDED here.
//
// An episode is a human prompt plus every agent turn that follows it. From and
// To are epoch seconds CLIPPED TO THE BLOCK — never the episode's own bounds —
// because a consumer renders one bar per entry and an unclipped To would draw a
// run overflowing its own block. Complete=false means the episode continues
// past the block's end, which is what lets a UI render CONTINUATION rather than
// implying the work stopped at an arithmetic boundary the cutter chose.
//
// A prompt ID and two instants: no text, no span into the message, no offset.
// The ids come from the daemon's own human-prompt filter
// (resolve.RecentPromptIDs -> watch.HumanPromptID) and the store only TIMES
// them; see resolve.RecentIDReader for why that lister is a separate method
// from the one that returns text.
type Cover struct {
	PromptID string  `json:"prompt_id"`
	From     float64 `json:"from"`
	To       float64 `json:"to"`
	Complete bool    `json:"complete"`
}

// BlockRef is where a block sits and how big it was. The block equivalent of
// WindowRef, and separate from it because the two are not interchangeable: a
// window's bounds are derived from a prompt's instant minus a fixed span, and a
// block's are the cutter's own decision, which is why this one additionally
// names WHY each edge is where it is.
type BlockRef struct {
	// Start and End are RFC3339 instants, half-open [Start, End). The sidecar
	// answers in epoch seconds — the unit its cursor and `covers` are in — and
	// the conversion happens once, at the decode boundary, so the wire carries
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
// edges are where they are, which prompt episodes it covers, and the same
// deterministic analysis a window row carries.
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
	Covers    []Cover
	Analysis  WindowAnalysis
}
