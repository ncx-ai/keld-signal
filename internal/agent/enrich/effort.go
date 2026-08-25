package enrich

// Act is one entry of a window's PHYSICAL ACTS inventory: an act from the closed
// Acts vocabulary and how many times the window did it.
//
// It is a COUNT, not a share, and that follows from being an inventory rather
// than an allocation: there is no denominator to divide by, because the acts do
// not partition the hour — an hour that reads and edits and tests is all three at
// once, which is precisely why this is not an eighth workstream (see Acts).
//
// PRIVACY, structurally, and the reason this ships while `named_terms` does not.
// Value is gated against Acts at the decode boundary (sidecar.convertActs), and
// on the producing side the `action` level is written in exactly two places
// (sidecar/app/analysis/levels.py, both inside the `tool_use` branch) — from a
// tool NAME and from a shell command's argv — each through `vocab.action_for`,
// whose every return path is a literal or a closed-table lookup. So there is no
// field here a transcript string could occupy and no path by which one could
// arrive, which is the mechanism rather than a comment asking for one.
// publish.TestEnrichmentWireShapeCannotCarryAnalysisInternals fails if a later
// change adds a field that could.
type Act struct {
	// Value is a member of Acts. Never empty: an entry that names no act is a
	// count attached to nothing (see KnownAct).
	Value string `json:"value"`
	// N is how many times the window did it. Always stated.
	N int `json:"n"`
}

// Effort is a window's EFFORT signals: how much was authored in it, and how fast
// its turns came. The digest beside it (Profile.Workstreams) says what the window
// was about and Profile.Dynamics says how that is changing; this says what the
// hour COST in work, in the two units that turned out to measure it.
//
// WHY ONLY THESE TWO. Six transcript signals the pipeline discarded were
// measured against one pre-registered rule — is the candidate a size bucket
// wearing a label — over 1,022 windows of the frozen corpus. Two passed and four
// were refuted, and the refuted four are named here so that re-adding one is a
// decision rather than an oversight
// (.superpowers/sdd/2026-08-24-transcript-signal/):
//
//	diff magnitude   PASSED. Held at a FIXED edit count, per-window byte totals
//	                 span 22x-87x p10->p90: windows indistinguishable under
//	                 `edit >= 5` differ by two orders of magnitude in bytes
//	                 authored. AuthoredBytes/AuthoringTurns.
//	turn tempo       PASSED. fast_share r = +0.012 against log window volume —
//	                 the cleanest independence of any candidate axis across five
//	                 studies — and -0.001 against the published evidence count,
//	                 -0.015 against n_prompts, so it is NOT `interactivity`
//	                 (+0.497) relabelled. Prevalence 0.979; session structure
//	                 eta2 0.401 against 0.082 chance. FastShare/Gaps/Tempo.
//	token weight     REFUTED: 0.89% of dominant values flip. Still computed and
//	                 stored on-device (the weighted rollup uses it); never
//	                 published, which is the only thing keeping it off the wire.
//	output volume    REFUTED: +0.552 against log volume (+0.654 logged) — a
//	                 restated count of tool calls.
//	thrashing        REFUTED on prevalence: 4.8% of windows, below the 0.20 floor.
//	                 Real and concentrated (49 windows hold 29.1% of all errors) —
//	                 an alert, not an axis.
//	error rate       REFUTED as a facet: size-free but at chance on session
//	                 structure. A window statistic.
//
// THE POINTERS ARE THE CONTRACT, and for a measured reason in each case.
//
//   - FastShare is nil when there was no gap to measure. A one-turn window has
//     ZERO gaps, and the study's first pass reported `fast_share 0.0` for it —
//     the identical value reported by a window whose gaps genuinely run to 257s.
//     0/0 is not 0.0. That defect was found by naming extremes, not by any
//     aggregate, which is why the distinction is in the type rather than in a
//     convention.
//   - AuthoredBytes is nil when NO magnitude was recorded for the window at all
//     (no costed turn in it, or a reference series that predates magnitudes and
//     has not been re-ingested). A recorded window that edited nothing publishes
//     0, because a sum of no terms is unambiguous once we know we looked — the
//     asymmetry with FastShare is deliberate and AuthoredStatus names which case
//     it is.
//
// WHAT GATES EACH, since a floor that does not apply is worse than none. The
// tempo needs `>= latency.MIN_GAPS` (== window.MIN_EVIDENCE == 5) GAPS before a
// reading is stated: a ratio over fewer observations is not a ratio. The
// magnitude has no significance floor at all and AuthoringTurns is published in
// its place — a sum is a total, not an estimate from a sample, so one 22 KB Write
// really did author 22 KB. Both gates are COUNT-derived. A count threshold
// compared against a byte sum is the specific artefact the token-weight study
// measured: a sum in the thousands clears a floor of 5 unconditionally, which
// deletes the floor while leaving it visible, and produced apparent +187/+123
// attributions that collapsed to ~0 once the gate was a count.
//
// WHY Tempo IS A WORD AND AuthoredBytes IS NOT. Measured: a document of raw
// window numbers scored -3.3/-20.0 on synthesis accuracy — worse than emitting
// nothing — against +36.7 for one that labelled each number and STATED the
// conclusion, and every one of the 14 full-document failures was the tempo
// question (~/keld/refseries-context/experiment/RESULTS.md). A bare 0.83 invites
// the reading a model already demonstrated when it answered `2659` to "which
// ticket?" because that was the window's event count. So the tempo states its
// conclusion and ships WITH the share it was computed from. The byte sum states
// none, because no measurement supplies a cut point on it — the study reports the
// spread and no boundary inside it — and inventing `small`/`large` would be the
// fabricated vocabulary this project keeps paying for.
//
// PRIVACY, structurally. `old_string`/`new_string`/`content`/`new_source` are
// file contents, and this struct is the far end of the only path that reads them:
// magnitude.edit_bytes returns an `int`, so what arrives here is a length. Every
// field below is a number or a value from one of the three closed vocabularies,
// gated at the decode boundary (sidecar.convertEffort) — there is no field a
// transcript string could occupy, which is the mechanism rather than a comment
// asking for one. publish.TestEnrichmentWireShapeCannotCarryAnalysisInternals
// fails if a later change adds one.
type Effort struct {
	// AuthoredBytes is the window's total diff magnitude — the summed byte
	// extent of the file regions its edits handled, `max(len(old), len(new))`
	// per edit event. Nil means no magnitude was recorded (see above), never
	// "nothing was authored".
	AuthoredBytes *int64 `json:"authored_bytes,omitempty"`
	// AuthoringTurns is how many turns those bytes were spread over. It is not
	// decoration: the sum alone cannot separate one 22 KB authoring from fifty
	// 400 B fixes, and it is what a reader gates on in place of a floor this
	// number has no business carrying. Always stated.
	AuthoringTurns int `json:"authoring_turns"`
	// AuthoredStatus is from AuthoredStatuses. Always stated, so a nil
	// AuthoredBytes is readable rather than merely missing.
	AuthoredStatus string `json:"authored_status"`
	// FastShare is the share of the window's inter-turn gaps shorter than 5s
	// (measured threshold — see sidecar/app/analysis/latency.py for the table
	// that picked it). Nil when there was no gap at all.
	FastShare *float64 `json:"fast_share,omitempty"`
	// Gaps is how many inter-turn gaps the share was computed from — the count
	// the eligibility floor is applied to, published so the floor is visible.
	// Always stated.
	Gaps int `json:"gaps"`
	// Tempo is the stated conclusion, from Tempos. Empty outside
	// TempoStatus "attributed" — unstated, never defaulted to "steered".
	Tempo string `json:"tempo,omitempty"`
	// TempoStatus is from TempoStatuses: `attributed`, or which of the two
	// abstentions it was. Always stated.
	TempoStatus string `json:"tempo_status"`
	// RequestTokens is the window's spend, priced into input-token equivalents:
	// magnitude.REQUEST_TOKENS summed once per requestId (never
	// magnitude.TOKENS, which repeats a request's cost per transcript line and
	// would over-count).
	//
	// ⚠️ THIS IS NOT THE TOKEN COUNTS ATLAS ALREADY HAS. Atlas already receives
	// raw `input_tokens`/`output_tokens`/`cache_read_tokens` per ToolEvent from
	// telemetry — this figure is a DIFFERENT thing: window-scoped (summed over
	// the hour ending at this prompt, not one event) AND price-weighted (a
	// cache-read token and a fresh input token do not cost the same, so this is
	// input-token EQUIVALENTS, not a token tally). A consumer that adds this to
	// the telemetry totals double-counts, and nothing else on the wire warns it.
	// Nil when the window priced no request at all — never 0, which would claim
	// a window that spent nothing rather than one nobody measured.
	RequestTokens *int64 `json:"request_tokens,omitempty"`
	// GapP50S is the median of the window's inter-turn gaps, in seconds — the
	// same gap population FastShare's fast/slow split is computed over, read as
	// a distribution instead of a threshold share. Nil under the same floor
	// FastShare uses (see WHAT GATES EACH above): fewer than latency.MIN_GAPS
	// gaps is not a population a percentile can describe.
	GapP50S *float64 `json:"gap_p50_s,omitempty"`
	// GapP90S is the 90th percentile of the same gap population — the tail
	// FastShare's single split point cannot show: two windows with an identical
	// fast_share can have very different p90s (a few long waits against many
	// evenly slow ones). Nil under the same floor as GapP50S.
	GapP90S *float64 `json:"gap_p90_s,omitempty"`
}
