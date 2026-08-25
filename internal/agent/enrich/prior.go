package enrich

// Prior is the SESSION the window sat in, reported BESIDE the window's own
// answer — the session as it stood before this window opened
// (sidecar/app/analysis/prior.py). Profile.Workstreams says what this hour was
// about; this says what the day around it looked like, so a value sitting just
// over the attribution floor is distinguishable from one that is the whole
// story.
//
// CONTRAST, NEVER FALLBACK. It never supplies an answer the window did not have.
// An unattributed window stays unattributed: the pass publishes no Workstreams
// entry for that dimension and the Prior beside it changes nothing about that.
// The rejected alternative — a thin window inheriting the session's value — buys
// coverage by laundering "we do not know" into something confident, which is
// precisely the defect the sidecar's MIN_EVIDENCE exists to prevent and which
// this project has already paid for twice (activity_type's `transform`:
// predicted 36 times, right zero; speech_act's `statement`: 22 times, right
// zero). Do not soften this under any argument about coverage.
//
// FOUR DIMENSIONS CARRY IT, and that is a measurement rather than a
// preference. Over 1,022 windows of the frozen corpus
// (docs/superpowers/specs/2026-08-24-session-prior-results.md):
//
//	skill      agreement 25.8%   novelty 44.0%   <- the signal: brainstorming ->
//	                                               writing-plans -> executing ->
//	                                               debugging, the phase
//	                                               transitions of the workflow
//	language   agreement 70.6%   novelty  2.3%
//	branch     agreement 76.1%   novelty  6.1%
//	output_type agreement 86.7%  novelty  1.1%  <- the one the aggregate hid
//
// `project` and `model` agree 100.0% with ZERO disagreements and a largest
// departure of +0.000 and -0.103; a contrast there publishes a constant, so the
// sidecar does not compute one.
//
// `output_type`'s 86.7% reads as "rarely fires" and is not what it looks like:
// agreement is defined ONLY where both sides are attributed, so it is silent
// about exactly the windows this block exists for. On a real Cowork session the
// prior carried `output_type` in 6 of 7 windows where the window could not
// attribute one — and that session is SKILL-FREE, which 61.6% of corpus
// transcripts are. `tooling` (98.5%, prior attributed on 24.3% of windows) is
// still held back. Which dimensions arrive is the sidecar's
// decision (prior.ENABLED), forwarded here rather than restated — a second list
// on this side is a second thing to drift, and the same reasoning already
// governs Workstreams.
//
// THE POINTERS ARE THE CONTRACT, for the reason they are on Dynamic. 45.1% of
// windows are a session's FIRST and have no prior at all: their Status is
// "absent" and all three contrast measures are nil, because a window with
// nothing behind it did not agree, did not depart, and cannot be novel. Plain
// bool/float64 fields would marshal that as false/0.0/false — "we compared this
// window to its session and it matched" — stated about a comparison nobody could
// make. That reading is the fallback in a different costume.
type Prior struct {
	// Value is the session's dominant value at this dimension, under the SAME
	// 0.50 share floor and evidence bar the window uses, or "" when the session
	// had none. It is a reference level (a branch, a language, a skill) and is
	// the same class of value Workstreams already publishes for this dimension:
	// the sidecar derives the prior's vocabulary from its ALLOCATION list, so
	// `named_terms` — the one level read from message text — can never occupy it.
	Value string `json:"value,omitempty"`
	// Share is the session's own dominance fraction, stated whatever the Status,
	// so an unattributed prior is visible as measured-and-mixed rather than
	// merely missing.
	Share float64 `json:"share"`
	// Evidence is how much session the prior rests on. It is the one thing this
	// block deliberately carries that sidecar/workstreams.go drops from a
	// Labeled, and the design asks for it by name: a window is a fixed 60
	// minutes, but a session's length is unbounded, so a prior over 6
	// observations and one over 600 are not the same frame of reference and no
	// other field says which this is.
	Evidence int `json:"evidence"`
	// Status is WHY the session had a value or did not, from PriorStatuses —
	// window.attribution's own four reasons plus "attributed". A prior that is
	// itself "no_majority" is INFORMATIVE: it says the window's ambiguity is the
	// session's ambiguity rather than a thin-slice artefact, and collapsing it
	// into "no prior" would discard that.
	Status string `json:"status"`
	// Agrees: is the window's dominant value the session's? Nil where the
	// question is undefined — the window has no value, or the PRIOR is not
	// itself attributed. A no_majority session has no value to agree with, and
	// scoring that as disagreement would count the session's own ambiguity as
	// the window departing from it.
	Agrees *bool `json:"agrees,omitempty"`
	// Departure is the window's share of ITS value minus the session's share of
	// THAT SAME value — not the difference of the two dominant shares, which
	// subtracts two different values and states nothing.
	//
	// IT IS THE MEASURE THAT WORKS. Nine windows on the corpus are Python under
	// 0.62 inside a TypeScript-led session — the design's motivating case — and
	// all nine are caught here, one at +0.516 where the session gives Python a
	// 5.5% share and the window gives it 57.1%. Novel was false in all nine.
	Departure *float64 `json:"departure,omitempty"`
	// Novel: the window's value has NO presence in the session at all. Narrow,
	// and it earns its place on `skill` alone (44.0%; every other dimension
	// is at or below 6.1%). Nil where the session has no evidence at this level:
	// against an empty prior everything is trivially novel, which is a session
	// with no history rather than yield.
	Novel *bool `json:"novel,omitempty"`
}

// PriorStatuses is the closed published vocabulary of Prior.Status, mirroring
// the sidecar's window.REASONS and pinned against it by
// TestPriorStatusVocabularyMatchesTheSidecar. Order is the sidecar's own.
//
// They are FIVE DIFFERENT FACTS and only one of them is about the share floor:
// "absent" (no evidence at this level in the session at all — a session-first
// window, or a level that never fired), "thin" (some evidence, under the
// evidence bar), "tie" (two values claim the session), "no_majority" (enough
// evidence, top share under 0.50 — the work was genuinely mixed).
var PriorStatuses = []string{"attributed", "absent", "thin", "tie", "no_majority"}

// KnownPriorStatus gates a status against the published set. A value this binary
// does not recognise is sidecar version skew — the sidecar is frozen and shipped
// separately from keld-agent, so an older or newer one can sit in ~/.local/bin
// indefinitely — and forwarding it would publish a label no Atlas consumer's
// vocabulary contains.
func KnownPriorStatus(s string) bool {
	for _, v := range PriorStatuses {
		if v == s {
			return true
		}
	}
	return false
}
