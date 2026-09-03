package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// BlockEnrichment is the wire shape of THE V2 PATH's row: one BLOCK of work,
// characterised. POST /v1/signal/blocks.
//
//	a transcript -> blocks of work -> one characterisation per block
//
// No prompt anchor, no look-back window, no gap-filling. Blocks TILE ACTIVE
// TIME, so coverage is 100% of activity by construction — see
// docs/superpowers/specs/2026-08-25-v2-block-path-design.md.
//
// IT IS ITS OWN STRUCT, NOT AN Enrichment WITH MOST FIELDS ZERO, for exactly
// the reason WindowEnrichment is: Enrichment declares task_type/domain/
// sensitivity/activity_type/personal/function_guess/subcategory WITHOUT
// omitempty, and they are structs, so a zero value serialises as
// `{"value":"","confidence":0}` — an ASSERTION, not an absence. A block reads
// no prompt text and computes none of those facets. "Nobody looked" and "we
// looked and found nothing" are different facts.
//
// AND IT IS NOT WindowEnrichment EITHER, which is the v2-specific half of that
// decision. A tick window is v1: an hour anchored to a prompt, published under
// corr_scheme "window" to the enrichments route, and slated for retirement with
// the rest of the prompt-anchored path. A block is a span the cutter chose,
// with two boundary REASONS a window has no equivalent of, on its own Atlas
// route with its own identity. Bending one type to serve both
// would make the stepping stone look like the destination — precisely the
// failure the design spec was written to prevent. What the two DO share is
// AnalysisFacets, embedded by both, because that part genuinely is the same
// analysis over different bounds.
//
// THE ATLAS CONTRACT this matches (extra="ignore" on its side, so every field
// here is either read or harmlessly carried, and the inventories are read off
// the raw body):
//
//	source, correlation{session_id} | session_id, actor,
//	window{start,end} | start/end, start_reason, end_reason,
//	workstreams{}, dynamics{}, prior{}, effort{},
//	pipeline_status, extractor_versions, schema_version, ts
//
// ⚠️ NO `covers`. A block row used to carry the prompt episodes overlapping it;
// the field is DELETED, and Atlas's own ingest ignores one an older client still
// sends. A block is TIME end to end, and Atlas must join
// `event_ts ∈ [start, end)` within the session for cost attribution regardless —
// a turn spanning several blocks would double-count its spend through a prompt
// mapping — so that mandatory join is also what answers "which turns ran in this
// block": Atlas holds ToolEvent.session_id and event_ts. `covers` was a second,
// weaker copy of it, and it published empty on every real run because the daemon
// named prompts by `promptId` while the sidecar's store indexed the per-message
// `uuid`. A block row is (principal, session, span, reasons, facets).
//
// A block with no span or no session is a 422 there; both are refused on this
// side first (see sidecar.BlocksCharacterised and BuildBlock).
type BlockEnrichment struct {
	Source      Source      `json:"source"`
	Correlation Correlation `json:"correlation"`
	Actor       string      `json:"actor,omitempty"`
	// SessionID is the block's identity half, and it is stated TWICE on purpose
	// — here and inside Correlation. Atlas accepts either spelling, and the
	// duplication costs one short string against the alternative of a client
	// and a server disagreeing about which one is canonical.
	SessionID string `json:"session_id"`
	// Window is the block's span, half-open [start, end), plus its two boundary
	// reasons. Mandatory: a block row has no prompt to be located by, so
	// without the bounds it says nothing about anything — which is why Atlas
	// 422s a block with no span rather than storing it.
	Window enrich.BlockRef `json:"window"`
	// StartReason and EndReason are ALSO stated at the top level, the second
	// spelling Atlas accepts. They are the fields that separate an arithmetic
	// cut from a real pause, so they are the last thing that should depend on a
	// reader finding the right nesting.
	StartReason string `json:"start_reason"`
	EndReason   string `json:"end_reason"`
	// Projects, ProjectsStatus and Attribution are the on-device project
	// attribution for this block (see enrich.ProjectAttribution). All three are
	// omitempty so a block published by a machine with attribution switched off
	// is byte-identical to the payload before this field existed — Atlas parses
	// with extra="ignore" and stores the raw body, so a silent machine and an
	// old client are indistinguishable on the wire, which is exactly what an
	// opt-in facet must be. They are set together, after the fact, via
	// WithProjects rather than as BuildBlock parameters: a block is characterised
	// long before attribution can run (it needs the block's own analysis as
	// input), so BuildBlock's callers must not have to thread three fields they
	// don't yet have through every existing call site.
	Projects       []enrich.ProjectAttribution `json:"projects,omitempty"`
	ProjectsStatus string                      `json:"projects_status,omitempty"`
	Attribution    *enrich.AttributionMeta     `json:"attribution,omitempty"`
	// Concepts is what this block was ABOUT — see enrich.Concept, which carries
	// the privacy argument, since this is the one field on a block row derived
	// from message text that is not already covered by the named_terms decision.
	//
	// It rides the attribution republish (set by WithProjects) because the pass
	// that produces it IS the attribution pass: the encoder and the block's
	// message vectors are both already resident there. A block published before
	// attribution runs carries no concepts and is byte-identical to the payload
	// before this field existed, exactly as Projects is.
	Concepts []enrich.Concept `json:"concepts,omitempty"`
	// AnalysisFacets is the deterministic analysis of this block: workstreams,
	// dynamics, effort, the thirteen inventories, the cut-visibility map and
	// the session prior. Embedded and SHARED with WindowEnrichment — see
	// AnalysisFacets.
	AnalysisFacets
	// PipelineStatus is always enrich.PipelineStatusBlock. It rides here so a
	// reader can tell WHY there is no task_type on this row (there was never a
	// prompt) rather than inferring it from an absence.
	PipelineStatus    string            `json:"pipeline_status"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	TS                string            `json:"ts"`
}

// BuildBlock maps one closed, characterised block into the wire shape.
//
// IDENTITY IS `(session, block.start)`, deterministic and immutable, and that
// is what makes emission idempotent: Atlas upserts on it, so a crash mid-batch,
// a re-delivery after a failed publish, or a cursor that never advanced costs
// nothing but bandwidth. The emitter depends on this — it prefers re-fetching
// and re-publishing a block to tracking which individual blocks landed — so the
// id must be a pure function of those two facts and nothing else. It is: see
// BlockCorrID.
//
// The START, not the end, because that is what the identity is defined on and
// what the cursor's `>=` comparison is made against; blocks within a session
// are disjoint and chronological, so a start is unique per session by
// construction.
func BuildBlock(b enrich.BlockCharacterisation, actor string, now time.Time) BlockEnrichment {
	return BlockEnrichment{
		Source: Source{ID: b.Source},
		Correlation: Correlation{
			Scheme:    enrich.BlockCorrScheme,
			ID:        BlockCorrID(b.SessionID, b.Ref.Start),
			SessionID: b.SessionID,
		},
		Actor:             actor,
		SessionID:         b.SessionID,
		Window:            b.Ref,
		StartReason:       b.Ref.StartReason,
		EndReason:         b.Ref.EndReason,
		AnalysisFacets:    facetsOf(b.Analysis),
		PipelineStatus:    enrich.PipelineStatusBlock,
		ExtractorVersions: blockExtractorVersions(),
		SchemaVersion:     enrich.SchemaVersion,
		TS:                now.UTC().Format(time.RFC3339),
	}
}

// WithProjects returns a copy of b with its project attribution set —
// Projects, ProjectsStatus, the pass's own AttributionMeta, and the Concepts
// the same pass extracted.
//
// A SEPARATE function from BuildBlock, deliberately, rather than three more
// BuildBlock parameters: attribution runs AFTER a block is built (it needs the
// block's own analysis as input, and it may run again later on the same
// block — an embedding match superseded by a verifier's pass, or a machine
// that turns attribution on after the block already published once), so every
// existing BuildBlock caller must stay untouched. b is passed by value and
// returned, not mutated, so a caller holding the original (e.g. to retry a
// failed publish) is unaffected by a later WithProjects call on the copy.
func WithProjects(b BlockEnrichment, ps []enrich.ProjectAttribution, status string,
	meta *enrich.AttributionMeta, concepts []enrich.Concept) BlockEnrichment {
	b.Projects = ps
	b.ProjectsStatus = status
	b.Attribution = meta
	b.Concepts = concepts
	return b
}

// BlockCorrID is a block row's correlation id: the session and the block's own
// START instant, normalised to UTC so two spellings of one moment cannot become
// two ids.
//
// DETERMINISTIC, because that is the whole of the idempotency this path relies
// on instead of client-side delivery tracking. An unparseable start falls back
// to the raw string, which keeps distinct blocks distinct — collapsing them
// onto a shared placeholder would make them overwrite each other, the exact
// failure the scheme exists to avoid.
func BlockCorrID(sessionID, start string) string {
	if t, err := time.Parse(time.RFC3339Nano, start); err == nil {
		start = t.UTC().Format(time.RFC3339Nano)
	}
	return sessionID + "@" + start
}

// blockExtractorVersions attributes a block row to the pass that produced it.
// A block runs exactly one thing — the deterministic analysis — so the map has
// one entry, and it is the SAME key and version a prompt row's workstreams
// carry, because it IS the same analysis over different bounds. A reader
// comparing a block row against a prompt row must not have to learn a second
// name for one producer; the row's KIND is already stated, once, in
// pipeline_status.
func blockExtractorVersions() map[string]string {
	var e enrich.WorkstreamsExtractor
	return map[string]string{e.Name(): e.Version()}
}

// blocksEnvelope is what POST /v1/signal/blocks takes: a BATCH, deliberately.
// Blocks arrive several at a time — a sweep drains a backlog, and a session
// that has been quiet closes its trailing block alongside the ones behind it —
// so a row-per-request would turn one settled session into a burst of POSTs
// against an endpoint whose whole job is to accept them together.
type blocksEnvelope struct {
	Blocks []BlockEnrichment `json:"blocks"`
}

// SendBlocks POSTs one batch of block rows.
//
// ⚠️ IT IS ALL-OR-NOTHING FROM THE CALLER'S POINT OF VIEW, AND THE CALLER MUST
// TREAT IT THAT WAY. An error here says nothing about which rows Atlas stored:
// the request may have been rejected whole, or accepted after a retry the
// client did not see. That is safe only because a block's identity is
// deterministic and Atlas upserts, so the correct recovery is to re-send —
// which is precisely why the emitter advances its cursor only past a batch that
// SUCCEEDED, and re-fetches the rest next interval rather than tracking rows.
//
// A batch this call cannot even marshal is an error too, not a silent drop: the
// cursor must not move past rows that were never offered to the network.
//
// 201 is the documented success; anything below 400 is accepted, so a later
// 200/202 on the same route is not read as a failure and re-sent forever.
func (p *Publisher) SendBlocks(blocks []BlockEnrichment) error {
	if len(blocks) == 0 {
		return nil
	}
	body, err := json.Marshal(blocksEnvelope{Blocks: blocks})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-ingest-token", p.Token())

	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return &retry.StatusError{Code: resp.StatusCode}
	}
	return nil
}
