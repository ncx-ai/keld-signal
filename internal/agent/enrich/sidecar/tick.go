package sidecar

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// tickReq is one tick over one transcript: COORDINATES and instants, never text.
//
// PromptIDs are the human prompts enrichment has already characterised, and the
// DAEMON names them rather than the sidecar reading them off the store. The
// store's `prompt` index holds every user- AND assistant-shaped turn (it indexes
// everything the transcript reader yields, so an assistant uuid still resolves),
// which on a real transcript is ~260 rows against 14 human prompts. Planning
// against all of them computes a covered set that swallows the whole session and
// the tick emits nothing, ever. Only this side knows which prompts enrichment
// fired on, because only this side applies the human-prompt filter
// (internal/agent/watch/filter.go). The sidecar times the ids it is given.
type tickReq struct {
	Path       string   `json:"path"`
	PromptIDs  []string `json:"prompt_ids"`
	CursorTS   *float64 `json:"cursor_ts"`
	Now        float64  `json:"now"`
	SpanMin    float64  `json:"span_minutes"`
	MaxWindows int      `json:"max_windows"`
	// Resolved rides the tick for the same reason it rides /analyze: a
	// tick-emitted window is not a lesser window, and one that answered with one
	// fewer dimension than a prompt's window over the same hour would be exactly
	// that. Per TRANSCRIPT rather than per window, which is the granularity the
	// facts have — a transcript is scoped to one project directory, so every
	// window in the batch sits in the same checkout.
	Resolved *enrich.ResolvedFacts `json:"resolved,omitempty"`
}

// TickResult is the Go-side view of POST /tick.
//
// Windows reuses AnalyzeResult unchanged, which is the point: a tick-emitted
// window is not a lesser window. It is computed by the same analyze_window a
// prompt's window goes through, so it carries the same blocks under the same
// vocabulary gates and there is no second decode path to drift.
type TickResult struct {
	// Cursor is where the next tick for this transcript resumes, as epoch
	// seconds. It is monotonic and the caller must persist it: losing it does
	// not lose correctness (the planner re-derives gaps from the prompts) but it
	// does replan settled ground, and a re-published window upserts itself
	// rather than duplicating (see enrich.WindowCorrScheme).
	Cursor  float64         `json:"cursor"`
	Windows []AnalyzeResult `json:"windows"`
	// Planned/Empty/Expired/Behind are the tick's own accounting. Empty is the
	// "idle ticks emit nothing" rule firing; Expired is characterisation
	// permanently lost to retention; Behind means the store had not caught up
	// and the cursor stopped short, so the next tick retries.
	Planned int  `json:"planned"`
	Empty   int  `json:"empty"`
	Expired int  `json:"expired"`
	Behind  bool `json:"behind"`
}

// tickTimeout bounds one tick. A tick is a series QUERY — a window is a ~2 ms
// rollup and a batch is bounded by MaxWindows — so this is generous by an order
// of magnitude and exists only so an unresponsive sidecar cannot park the ticker
// goroutine indefinitely. It is deliberately NOT the ingest signal's 30s: a tick
// never parses a transcript (the sidecar runs it with refresh off), so a call
// taking that long means something is wrong rather than something is big.
const tickTimeout = 20 * time.Second

// Tick asks the sidecar to characterise the slices of one transcript that no
// prompt's look-back will ever reach.
//
// cursor is nil for a transcript never ticked before, which starts the cursor at
// the frontier so nothing historical is back-filled — the same forward-only
// default KELD_WATCH_BACKFILL sets for capture. now is passed explicitly rather
// than read sidecar-side because it is what the settle rule is computed from,
// and a caller that cannot move it cannot test the rule.
//
// ok=false on any transport or status failure. Unlike Analyze there is no
// partial success to report: the cursor and the windows are one answer, and
// advancing a cursor past windows that were never received would silently lose
// exactly the characterisation this whole path exists to add.
func (c *Client) Tick(path string, promptIDs []string, cursor *float64, now time.Time,
	spanMinutes float64, maxWindows int, resolved enrich.ResolvedFacts) (TickResult, bool) {
	var r TickResult
	req := tickReq{
		Path: path, PromptIDs: promptIDs, CursorTS: cursor,
		Now:     float64(now.UnixNano()) / 1e9,
		SpanMin: spanMinutes, MaxWindows: maxWindows,
		Resolved: resolvedOrNil(resolved),
	}
	if req.PromptIDs == nil {
		req.PromptIDs = []string{} // an omitted list and an empty one mean the same thing; say so
	}
	if !c.post("/tick", req, &r) {
		return TickResult{}, false
	}
	return r, true
}

// TickCharacterised is Tick in the shape the publisher consumes: each window
// converted through the SAME conversions AnalyzeLabeled applies to a prompt's
// window (share->confidence, the closed-vocabulary gates on dynamics, effort and
// acts, and the structural refusal to carry a reference-level value). Sharing
// them is the point — a tick row and a prompt row must not be able to differ in
// what they are allowed to say.
//
// sessionID is supplied by the caller rather than taken from the response: the
// response's `session` is the reference series' own key, a digest of the
// transcript's absolute path, which is machine-local and not the identifier
// anything downstream can join a session on.
func (c *Client) TickCharacterised(path, source, sessionID string, promptIDs []string,
	cursor *float64, now time.Time, spanMinutes float64, maxWindows int,
	resolved enrich.ResolvedFacts) ([]enrich.WindowCharacterisation, float64, bool) {
	res, ok := c.Tick(path, promptIDs, cursor, now, spanMinutes, maxWindows, resolved)
	if !ok {
		return nil, 0, false
	}
	out := make([]enrich.WindowCharacterisation, 0, len(res.Windows))
	for _, w := range res.Windows {
		// Defence in depth against a sidecar that ever stopped honouring the
		// rule: a window with no evidence is not a characterisation of nothing,
		// it is the absence of one, and publishing it is what turns a quiet
		// machine into a stream of empty rows.
		if w.Evidence <= 0 || w.WindowStart == "" || w.WindowEnd == "" {
			continue
		}
		// The SAME conversion AnalyzeLabeled uses, shared rather than repeated:
		// a tick window and a prompt window are the same analysis over different
		// bounds, so a dimension must not be readable in one and deleted in the
		// other. In particular a `thin` dimension carries its count and its
		// status here too — publishing the value with no status would render a
		// sub-floor reading as a confident one.
		dims := make(map[string]enrich.Labeled, len(w.Workstreams))
		for dim, ws := range w.Workstreams {
			l, keep := labeledWorkstream(ws)
			if !keep {
				continue
			}
			dims[dim] = l
		}
		out = append(out, enrich.WindowCharacterisation{
			SessionID: sessionID,
			Source:    source,
			Ref: enrich.WindowRef{
				Start: w.WindowStart, End: w.WindowEnd,
				SpanMinutes: spanBetween(w.WindowStart, w.WindowEnd),
				Evidence:    w.Evidence,
			},
			Analysis: enrich.WindowAnalysis{
				Workstreams:      dims,
				PhysicalActs:     convertActs(w.Inventory.PhysicalActs),
				Files:            convertPathInventory(w.Inventory.Files),
				Directories:      convertPathInventory(w.Inventory.Directories),
				Components:       convertPathInventory(w.Inventory.Components),
				HarnessTools:     convertIdentifierInventory(w.Inventory.HarnessTools),
				Programs:         convertProgramInventory(w.Inventory.Programs),
				ExternalSystems:  convertExternalSystemInventory(w.Inventory.ExternalSystems),
				Integrations:     convertIdentifierInventory(w.Inventory.Integrations),
				NamedTerms:       convertNamedTerms(w.Inventory.NamedTerms),
				FileTypes:        convertIdentifierInventory(w.Inventory.FileTypes),
				ShellVerbs:       convertShellVerbInventory(w.Inventory.ShellVerbs),
				Subagents:        convertIdentifierInventory(w.Inventory.Subagents),
				McpServers:       convertIdentifierInventory(w.Inventory.McpServers),
				InventoryOmitted: convertInventoryOmitted(w.InventoryOmitted),
				Dynamics:         convertDynamics(w.Dynamics),
				Effort:           convertEffort(w.Effort),
				Prior:            convertPrior(w.Prior),
			},
		})
	}
	return out, res.Cursor, true
}

// spanBetween is End-Start in minutes, or 0 when either bound will not parse.
// Fractional on purpose (see enrich.WindowRef.SpanMinutes).
func spanBetween(start, end string) float64 {
	a, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		return 0
	}
	b, err := time.Parse(time.RFC3339Nano, end)
	if err != nil {
		return 0
	}
	return b.Sub(a).Minutes()
}
