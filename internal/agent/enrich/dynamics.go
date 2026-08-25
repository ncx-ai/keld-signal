package enrich

// Dynamic is one window dimension's DERIVATIVE: how that dimension is changing,
// as computed by the sidecar's dynamics block (a recent slice read against a
// longer, disjoint baseline — sidecar/app/analysis/dynamics.py). The digest
// beside it (Profile.Workstreams) says what the window CONTAINS; this says
// whether the hour just turned over or has looked like this all day.
//
// WHAT IS HERE, AND WHY IT IS ONLY THIS. The sidecar's block carries per
// dimension a `slice` and a `baseline` object (each with the level's own value,
// share, evidence and attribution reason), the comparison's three timestamps,
// and the adaptive sizer's detail. None of that publishes. Two reasons, and both
// are measured rather than stylistic:
//
//   - PRIVACY, structurally. The per-side `value` is the reference level itself.
//     Every level the surviving dimensions read (branch, artifact, lang, skill)
//     comes from tool-call inputs, but `named_terms` reads message TEXT and has
//     held real person names — so the rule is that no field here can hold a level
//     value at all, rather than an audit of which levels are safe today. This
//     struct has no such field, and both the decode boundary
//     (sidecar.TestNothingInTheDynamicsSubtreeCanCarryALevelValue) and the wire
//     (publish.TestEnrichmentWireShapeCannotCarryAnalysisInternals) fail if one
//     appears.
//   - LEGIBILITY, measured. Three arms were scored on the same windows: a 16 KB
//     characterisation of raw window numbers came in at -3.3/-20.0 on synthesis
//     accuracy — WORSE than emitting nothing — against +36.7 for a digest of the
//     same facts (~/keld/refseries-context/experiment/RESULTS.md). The digest was
//     not number-free; it labelled each number and stated the conclusion. Every
//     one of the 14 full-document failures was the tempo question, where the
//     reader got `engineer_messages: 5` / `assistant_messages: 84` and had to
//     divide. So Reading (the conclusion) ships WITH the shares it was computed
//     from — in JSON the key is the label — and the unlabelled remainder, which is
//     what made the losing arm 16 KB, does not.
//
// THE POINTERS ARE THE CONTRACT. A metric is reported only under Status
// "compared"; outside it a share would be arithmetic rather than measurement (a
// ratio over one observation is 1.0 by construction, and there is no share of
// nothing). And Changed is a THREE-state answer: false for "both_absent",
// because a level that never fired did not change; nil wherever the comparison
// cannot support a yes or a no. A plain float64/bool would render every one of
// those as 0.0/false — "we checked, nothing moved" — which is the single
// misreading the sidecar's evidence-floor work exists to prevent.
type Dynamic struct {
	// Status is the comparison's own outcome, from DynamicStatuses. Always
	// stated, so a null metric is readable rather than merely missing.
	Status string `json:"status"`
	// Reading is the stated conclusion, from DynamicReadings. Empty outside
	// "compared" — unstated, never defaulted to "steady".
	Reading string `json:"reading,omitempty"`
	// Changed: did the dominant value change? Three-state (see above).
	Changed *bool `json:"changed,omitempty"`
	// Turnover is the share of the slice's evidence in values absent from the
	// baseline; Decay the mirror (baseline evidence in values absent from the
	// slice). Two different facts: a slice can take on a new value without
	// dropping an old one. Both are SHARES, so they are invariant to how busy
	// the window was.
	Turnover *float64 `json:"turnover,omitempty"`
	Decay    *float64 `json:"decay,omitempty"`
	// ConcentrationShift is the slice dominant's share of the slice minus that
	// same value's share of the baseline: is the thing that owns the window more
	// or less concentrated than it used to be. Withheld when the slice has no
	// dominant value, rather than computed against an arbitrary pick.
	ConcentrationShift *float64 `json:"concentration_shift,omitempty"`
}

// WindowAnalysis is everything one /analyze call yields for the pipeline: what
// the window CONTAINS (Workstreams) and how it is CHANGING (Dynamics). They
// travel together because they come from the same call — the dynamics block is
// computed in the same response the digest is, so publishing it costs no second
// round-trip and no second inference (it needs none at all).
//
// Either half may be nil: a window with no dominant value anywhere has no
// Workstreams, and a sidecar too old to compute dynamics — or a window with no
// series behind it — has no Dynamics. Neither is a failure, and neither may be
// published as an empty object: "we looked and found nothing" is a different
// fact from "nobody looked".
type WindowAnalysis struct {
	Workstreams map[string]Labeled
	Dynamics    map[string]Dynamic
	// PhysicalActs is what the window's hour physically DID — the `action` level,
	// published as an INVENTORY rather than a workstream (see Acts for the
	// measurement, and Act for the shape). Nil, never an empty slice, when the
	// analysis produced none: "the hour did nothing" is not a fact this can state.
	PhysicalActs []Act
	// Effort is the same window's two surviving transcript signals — how much was
	// authored and how fast the turns came (see Effort). Third half of the same
	// call, and nil for a sidecar too old to compute the block: a zeroed Effort
	// would state every count as 0 and every status as "", which reads as a real
	// answer nobody measured.
	Effort *Effort
	// Prior is the SESSION the window sat in, keyed by dimension (see Prior).
	// Fifth answer from the same call and the only one that is about something
	// OUTSIDE the window, which is exactly why a per-window view cannot produce
	// it. It is a CONTRAST and never a fallback: it sits beside Workstreams and
	// never fills a dimension Workstreams left blank. Nil for a sidecar too old
	// to compute the block — "we looked at the session and it said nothing" is a
	// different fact from "nobody looked".
	Prior map[string]Prior
}
