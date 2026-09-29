package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
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
	// ProjectMatches is every project this block matched, each WITH THE RULES
	// that matched it.
	//
	// ⚠️ **IT WAS `Entered` / `entered` UNTIL 2026-09-09.** The old name came
	// from the set model underneath — a project is a set of rules and a block
	// enters it by matching any member — which describes the mechanism and
	// tells a reader of the payload nothing. Renamed while it was free: the key
	// had shipped only in a pre-release and no Atlas consumer read it yet. The
	// pair now reads as what it is: `project_matches` is matched by rule,
	// `projects` is scored by model.
	//
	// ⚠️ **IT IS NOT `Projects`, AND THE DIFFERENCE IS THE WHOLE FEATURE.**
	// `Projects` is the semantic matcher's answer: Atlas value ids with
	// confidences, averaging 4.9 ids per block on a real machine.
	// `ProjectMatches` is the deterministic one — which projects hold a rule
	// this block actually matches — and it carries the LOCAL projects too,
	// which nothing else on the wire has ever done.
	//
	// The rules travel with it because Atlas cannot see the local side any
	// other way. With both sides on one row, "a machine groups C with your A
	// and B" is a set difference over rows Atlas already stores — no route, no
	// suggestion object with a lifecycle, nothing to schedule or retract.
	//
	// A local project's TITLE and ID are never here: the id is derived from the
	// title, so sending it would send the title in a thin disguise. Rules are a
	// repository remote or a ticket key, both of which already cross as block
	// dimensions.
	ProjectMatches []ProjectMatch              `json:"project_matches"`
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
	// IsSubagentRun says this block's transcript IS a subagent's own conversation,
	// not the main one a person was driving.
	//
	// ⚠️ IT IS NOT THE `subagents` INVENTORY, AND THE TWO ARE OPPOSITE DIRECTIONS.
	// `subagents` (an AnalysisFacets inventory level) names the subagents a block
	// LAUNCHED. This says the block IS one. A row can carry both: a delegated run
	// that itself delegates further.
	//
	// Why it is needed: Signal already publishes blocks cut from subagent
	// transcripts -- measured on one real machine, 294 of 355 block cursors are
	// `agent-*.jsonl` -- and until now nothing on the wire distinguished them. On
	// two real corpora that is 41.5% and 70.6% of all inference requests silently
	// blended into the same block counts, with no way for a reader to separate
	// work a person did from work a person delegated. `labels.go` already names
	// the gap from the other side: "`subagents` is the ONLY dimension that says
	// work was DELEGATED, invisible in every other level because a subagent's own
	// turns are a different transcript."
	//
	// ⚠️ NOT `delegated`: Atlas already uses `delegates` for a collector
	// credential that asserts no identity of its own, so `delegated: true` on a
	// block reads there as a claim about identity, which is a different axis
	// entirely. Named after a check against the Atlas ingest path rather than
	// after this side's own vocabulary.
	//
	// omitempty, so a main-line block is byte-identical to the payload before
	// this field existed -- the same contract `projects` and `concepts` keep.
	IsSubagentRun bool `json:"is_subagent_run,omitempty"`
	// SubagentID is the run's identity, taken from the transcript filename
	// (`agent-<id>.jsonl`). It is what makes a run's blocks groupable into one
	// delegated task; a run spans several blocks whenever it outlives the cutter's
	// 20-minute budget, measured at 20% of runs on one corpus.
	//
	// ⚠️ IT IS NOT GLOBALLY UNIQUE, and this comment used to imply it was by
	// calling it "the run's identity" with no qualification. A consumer read that
	// as licence to fetch a whole run by this id alone, which merges two unrelated
	// runs when the id repeats.
	//
	// Measured across 632 subagent transcripts from two machines: 631 distinct
	// ids, with one id appearing under TWO different parent sessions — and both of
	// those on the SAME machine and org, so an org scope does not separate them.
	// The id is 17 hex characters, so this is not random collision; whatever
	// Claude Code derives it from can repeat. (One of the two colliding files is
	// unreadable on disk, so the collision is established at the filename level
	// and 1-in-632 is an upper bound on how often it bites, not a rate.)
	//
	// So the key for a run is (ParentSessionID, SubagentID). A run belongs to
	// exactly one parent session, which is what makes that pair sound. ⚠️ And the
	// pair is unavailable precisely when ParentSessionID is empty, where this id
	// alone is the only key there is and merging is the accepted risk — a strict
	// two-part key would instead drop those runs silently, which is worse.
	SubagentID string `json:"subagent_id,omitempty"`
	// ParentSessionID is the session that LAUNCHED this run, recovered from the
	// transcript's path. Empty and omitted on a main-line block, and also on a
	// delegated run whose layout does not name a parent — never guessed.
	//
	// ⚠️ WITHOUT IT A SUBAGENT BLOCK NAMES NOBODY. Its own session_id is
	// `agent-<hash>`, which no tool_event carries and no other row shares, so a
	// consumer can see that delegated work happened and cannot say whose. That
	// makes the only honest total of a session that delegated uncomputable: a
	// parent block's mix EXCLUDES its subagents' calls while its cost INCLUDES
	// them, so neither side alone is the whole, and the composite is the parent's
	// blocks plus the blocks of the runs it launched.
	ParentSessionID string `json:"parent_session_id,omitempty"`
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

// ProjectMatch is one project a block landed in, on the wire.
//
// Defined HERE rather than reused from internal/agent/projects, so the publish
// layer does not depend on the decision layer: a wire shape and a matcher have
// different reasons to change, and one importing the other makes the payload
// hostage to a refactor of the rules.
type ProjectMatch struct {
	// ID is the Atlas value id, EMPTY for a local project — its id is derived
	// from its title, so sending it would send the title in a thin disguise.
	ID string `json:"id,omitempty"`
	// Origin is "atlas" or "user", so a reader need not infer whose project
	// this is from whether an id is present.
	Origin string `json:"origin"`
	// Repos and TicketKey are the rules, sorted. They are what makes the row
	// self-contained: Atlas can compute the difference against its own project
	// without joining to a definition that may have changed since.
	Repos     []string `json:"repos,omitempty"`
	TicketKey string   `json:"ticket_key,omitempty"`
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
// SubagentRunOf reports whether transcriptPath is a subagent's own conversation
// and, if so, that run's id.
//
// Claude Code writes each delegated task to its own `agent-<id>.jsonl`; every
// record in it carries isSidechain, and ZERO sidechain records appear in a
// session transcript (measured across 405 files). So the filename is the whole
// test -- there is no need to read the file, and no other source knows this.
func SubagentRunOf(transcriptPath string) (isRun bool, runID, parentSession string) {
	base := filepath.Base(transcriptPath)
	const pre, ext = "agent-", ".jsonl"
	if !strings.HasPrefix(base, pre) || !strings.HasSuffix(base, ext) {
		return false, "", ""
	}
	id := base[len(pre) : len(base)-len(ext)]
	if id == "" {
		return false, "", ""
	}
	return true, id, parentSessionOf(transcriptPath)
}

// parentSessionOf recovers the session that LAUNCHED a delegated run, from the
// path alone, or "" when the layout does not say.
//
// ⚠️ THIS EXISTS BECAUSE A SUBAGENT BLOCK NAMED NOBODY. Its `session_id` is
// `agent-<hash>` (blocks.sessionIDFor is the basename), a value no tool_event
// can carry and no other row shares, so a consumer could see that delegated work
// happened and could not say whose it was. That silently broke the one honest
// composite available: a session's own blocks plus its subagents' blocks cover
// the work exactly once, which is the only correct way to total a session that
// delegated — a parent block's mix excludes its subagents while its cost
// includes them (see internal/agent/blocks/emitter.go).
//
// Claude Code writes a delegated run to
// `<projects>/<parent-session-uuid>/subagents/agent-<hash>.jsonl`, so the parent
// is the grandparent directory. Measured on the frozen corpus: 445 of 445
// subagent transcripts have that shape with a UUID-shaped parent, and on all 444
// readable ones it equals the `sessionId` INSIDE the file — zero mismatches. So
// this is read from the path rather than by opening the transcript, which keeps
// the emitter's "path, cursor and clock, no text" contract intact.
//
// ⚠️ IT IS CHECKED, NOT ASSUMED. An unexpected layout returns "" and the field is
// omitted, because a wrong parent silently reassigns one person's work to another
// session. The two facts stay independent: a run whose parent cannot be named is
// still a run, and `is_subagent_run` remains true.
func parentSessionOf(transcriptPath string) string {
	dir := filepath.Dir(transcriptPath)
	if filepath.Base(dir) != "subagents" {
		return ""
	}
	parent := filepath.Base(filepath.Dir(dir))
	if !isSessionUUID(parent) {
		return ""
	}
	return parent
}

// isSessionUUID reports whether s has the 8-4-4-4-12 lowercase-hex shape Claude
// Code gives a session. Shape only — this never has to prove the session exists,
// only refuse a directory that plainly is not one.
func isSessionUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}

// BuildBlock builds the wire row.
//
// ⚠️ transcriptPath is REQUIRED rather than optional on purpose. It is the only
// input that says whether this block is a subagent's work, and there are two
// call sites -- the emitter and the attribution REPUBLISH. A republish rebuilds
// the row from scratch, so an optional path would let attribution silently strip
// the marker off a row that had carried it, which is invisible on both sides. A
// required parameter makes a future third call site a compile error instead.
func BuildBlock(b enrich.BlockCharacterisation, actor string, now time.Time, transcriptPath string) BlockEnrichment {
	isRun, runID, parentSession := SubagentRunOf(transcriptPath)
	return BlockEnrichment{
		Source: Source{ID: b.Source},
		Correlation: Correlation{
			Scheme:    enrich.BlockCorrScheme,
			ID:        BlockCorrID(b.SessionID, b.Ref.Start),
			SessionID: b.SessionID,
		},
		Actor:             actor,
		SessionID:         b.SessionID,
		IsSubagentRun:     isRun,
		SubagentID:        runID,
		ParentSessionID:   parentSession,
		Window:            b.Ref,
		StartReason:       b.Ref.StartReason,
		EndReason:         b.Ref.EndReason,
		AnalysisFacets:    withSpend(facetsOf(b.Analysis), b.Tokens, b.Requests),
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
	var e enrich.DimensionsExtractor
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
	_, err := p.SendBlocksResult(blocks)
	return err
}

// SendBlocksResult is SendBlocks plus the HTTP status Atlas actually answered
// with — 0 when no response was reached at all.
//
// ⚠️ **The status exists so the ledger can record `received` from the RESPONSE
// rather than from the absence of an error** (docs/v3/contracts.md). It also
// carries the captive-portal check this path did not have: the body used to be
// copied straight to io.Discard, so a hotel wifi answering **200 with an HTML
// login page** looked exactly like a successful publish — the emitter advanced
// its cursor and those blocks were never sent again. The telemetry drain has
// checked its response body for this reason since it was written; the block
// route inherited the status-only test and the bug with it. A response whose
// content-type is HTML, or whose body opens with `<`, is now a failure with
// status 0 so a caller cannot mistake it for a rejection by Atlas.
func (p *Publisher) SendBlocksResult(blocks []BlockEnrichment) (int, error) {
	if len(blocks) == 0 {
		return 0, nil
	}
	body, err := json.Marshal(blocksEnvelope{Blocks: blocks})
	if err != nil {
		return 0, err
	}
	// ErrNotPaired here is what HOLDS the emitter's cursor on an unpaired
	// machine: the blocks were cut and are still cuttable, so the sweep must ask
	// for the same ground again once a pairing arrives.
	url, err := p.url()
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-ingest-token", p.Token())

	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	head, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return resp.StatusCode, &retry.StatusError{Code: resp.StatusCode}
	}
	if looksIntercepted(resp.Header.Get("content-type"), head) {
		// Status 0, not the 200 we were handed: the 200 came from something
		// that is not Atlas, and reporting it would let a caller record the
		// batch as received.
		return 0, ErrIntercepted
	}
	return resp.StatusCode, nil
}

// ErrIntercepted is a 2xx that did not come from Atlas — a captive portal or a
// proxy's own page. Not a StatusError: nothing about the request was refused,
// so retrying it later is exactly right.
var ErrIntercepted = errors.New("publish: response body is not from Atlas (captive portal?)")

func looksIntercepted(contentType string, head []byte) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if strings.HasPrefix(ct, "text/html") {
		return true
	}
	trimmed := bytes.TrimLeft(head, " \t\r\n\xef\xbb\xbf") // leading space or a UTF-8 BOM
	return len(trimmed) > 0 && trimmed[0] == '<'
}
