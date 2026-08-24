package enrich

// WindowSpanMinutes is the span a TICK-emitted characterisation covers, and it
// is the same hour WorkstreamSpanMinutes gives a prompt's window — deliberately,
// so a tick row and a prompt row describe comparable amounts of work and a
// report can pool them. A tick window is shorter only when the gap it fills is
// (see the sidecar's app/analysis/coverage.py).
const WindowSpanMinutes = WorkstreamSpanMinutes

// WindowCorrScheme is the correlation scheme a tick-emitted enrichment carries.
//
// IT IS NOT "prompt_id", AND THAT IS THE WHOLE POINT. Atlas's enrichment table
// is keyed `UNIQUE(org_id, source_id, corr_scheme, corr_id)` and the insert is
// `ON CONFLICT DO UPDATE` over every column (keld-atlas
// services/api/app/services/enrichments.py). So a tick row published under the
// anchor prompt's own corr_id would not be "deduped" — it would OVERWRITE that
// prompt's enrichment, nulling its task_type, sensitivity and domain, because a
// tick computes none of them. That refutes the design spec's recommended
// option (a) outright, and it is the reason a tick gets a scheme of its own:
// under a different scheme the unique key cannot collide with any prompt row,
// and the same window re-published (a retry, a restart) upserts itself, which
// is exactly the idempotency wanted.
//
// SHIPPING INERT, STATED PLAINLY. Every Atlas consumer joins
// `Enrichment.corr_id == ToolEvent.prompt_id` (14 sites) and
// `enrichment_for_event` additionally filters `corr_scheme == "prompt_id"`, so a
// window row joins to NOTHING until Atlas learns to join by time and identity.
// It is accepted and stored — Atlas's EnrichmentIn ignores unknown fields and
// persists the whole body in `enrichments.raw`, so the window block survives the
// round trip — but nothing reads it yet. That is why the daemon's ticker is OFF
// by default (KELD_TICK) and announces itself as unjoinable when switched on,
// rather than quietly filling a table with orphans.
const WindowCorrScheme = "window"

// PipelineStatusWindow is what a tick-emitted row reports for pipeline_status.
//
// It is a FIFTH value beside "enriched"/"partial"/"gated", not one of them, and
// the reason is that the existing four all describe how a PROMPT's pipeline
// went. A window row ran one pass and no text facet exists to have succeeded,
// failed or been gated, so "enriched" would overclaim and "partial" would report
// a failure that did not happen — and "partial" is an operational signal this
// repo already guards against corrupting. Naming the row's own kind is the only
// honest option, and it doubles as the flag that tells a reader why every text
// facet is missing.
const PipelineStatusWindow = "window"

// WindowRef is the bounds a tick-emitted characterisation applies to. It is the
// field that makes such a row interpretable at all: a prompt row is located by
// its prompt, and a window row has no prompt, so the window itself has to say
// where it sits.
//
// Timestamps and integers only. The blocks beside it (workstreams, dynamics,
// effort, physical acts) are the same ones a prompt row carries and are subject
// to the same vocabulary gates; this adds no new content channel, which is the
// property that lets the wire-shape test keep asserting nothing from a
// transcript can reach Atlas.
type WindowRef struct {
	// Start and End are RFC3339 instants, half-open [Start, End) — the same
	// convention the reference series stores rows on.
	Start string `json:"start"`
	End   string `json:"end"`
	// SpanMinutes is End-Start, and is fractional when the gap this window fills
	// is: a gap closed by the next prompt's look-back is not a whole number of
	// minutes, and rounding it up would reach back into the region that prompt
	// already characterises — the one arithmetic that could make a tick
	// double-count.
	SpanMinutes float64 `json:"span_minutes"`
	// Evidence is how many reference events the window held. Always > 0 on a
	// published row: a window with none is dropped rather than published, so a
	// quiet machine emits nothing (the design spec's second rule).
	Evidence int `json:"evidence"`
}

// WindowCharacterisation is one tick-emitted window: where it sits, and the same
// four blocks a prompt's window carries. It is deliberately NOT a Profile — a
// Profile has task_type, sensitivity, domain and the rest, all of which are read
// from prompt TEXT and none of which a tick computes. Modelling a tick row as a
// Profile would mean publishing those facets as empty values, which reads
// downstream as "we looked and found nothing" rather than "nobody looked", and
// would additionally invite the overwrite that WindowCorrScheme exists to
// prevent.
type WindowCharacterisation struct {
	// SessionID is the transcript's own session identifier (Claude Code's file
	// stem), not the reference series' internal key: it is what a reader can
	// join a session on, and the store key is a machine-local path digest by
	// construction (sidecar app/analysis/ingest.py's session_of).
	SessionID string
	Source    string
	Ref       WindowRef
	Analysis  WindowAnalysis
}
