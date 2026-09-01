package enrich

// PROJECT ATTRIBUTION — which declared project a closed block belongs to,
// decided on-device by comparing the block against `settings.RemoteProject`
// entries (Task 1) and never by reading message text into the wire.
//
// ProjectAttribution and AttributionMeta are the WIRE SHAPE these decisions
// travel in — they are what a later attribution loop hands to
// publish.WithProjects, and what a Python sidecar's matcher returns. Every
// field here is an id, a confidence, an integer timing or a closed enum: the
// same discipline Dynamic and Prior already enforce one level up, because a
// project name is exactly the kind of string `named_terms` has already shown
// can hold a real person's name. No message text, no character span, no byte
// offset may ever be added to either struct — see
// publish.TestAttributionShapeHoldsNoText, the reflection tripwire that fails
// the moment a new field appears here undeclared.
type ProjectAttribution struct {
	// ID is the matched project's declared identity (settings.RemoteProject.ID),
	// never its free-text description.
	ID string `json:"id"`
	// Confidence is the matcher's own score for this id, in [0,1].
	Confidence float64 `json:"confidence"`
	// Source names WHICH matcher produced this attribution — "embedding" (the
	// on-device semantic match against the project's declared text, which the
	// deterministic metadata boost contributes to) or "verifier" (a local LLM
	// call that adjudicated a borderline candidate).
	//
	// ⚠️ "metadata" WAS A THIRD VALUE AND IS NOT PRODUCIBLE ANY MORE. It meant
	// "assigned on the structural signal alone", and AC-4 as amended
	// (2026-09-01) removed that path: nothing is assigned without the encoder,
	// however strong the exact-match evidence, because BOOST_CAP (0.35) sits
	// below THRESHOLD-BAND (0.41) by construction and a boost-only assignment
	// could only exist under a second, unmeasured rule. Listing a value no
	// producer can emit invites a consumer to build a branch that can never
	// run and a reader to believe metadata-only attribution happens. The
	// boost is still in every confidence; it is not a source on its own.
	Source string `json:"source"`
}

// AttributionMeta is the attribution PASS's own report on itself — timings,
// counts and closed enums describing how the decision was reached, mirroring
// the discipline Effort and Dynamic already apply to their own passes: the
// facet's answer travels in ProjectAttribution, and everything about HOW that
// answer was produced travels here, kept structurally incapable of holding a
// project's name, a branch, or any text derived from the block itself.
type AttributionMeta struct {
	// EmbedMS is how long the on-device embedding match took, in whole
	// milliseconds.
	EmbedMS int `json:"embed_ms"`
	// VerifyMS is how long the verifier step took, in whole milliseconds. Zero
	// when Verifier is not "used".
	VerifyMS int `json:"verify_ms"`
	// PairsVerified is how many candidate (block, project) pairs the verifier
	// adjudicated. Zero when Verifier is not "used".
	PairsVerified int `json:"pairs_verified"`
	// EncoderState is the on-device embedding model's readiness at the time of
	// this pass: "warm" (resident, and the embedding ran) or "absent" (no
	// encoder — the weights are not provisioned, or the toggle is off — so
	// embedding was skipped).
	//
	// ⚠️ "cold" WAS A THIRD VALUE — AC-7 promised warm|cold — AND IT IS NOT
	// REACHABLE. The route never loads the encoder inside a request: a
	// not-yet-ready child answers `pending` with a NULL attribution meta and
	// warms on a background thread, precisely so no caller waits ~2.8 s warm /
	// ~20 s cold behind a model load. So there is no pass that "loaded it for
	// this pass" to report. Cold is reported as `pending`, in `status`, where a
	// consumer already has to handle it; a third enum here would be a state
	// nothing can produce, and this codebase's rule is that a stated
	// vocabulary is exhaustive or it is worse than none. If the route ever
	// loads synchronously, this comment and attribution.py must change together.
	EncoderState string `json:"encoder_state"`
	// Verifier is what happened to the verifier step for this block: "used" (it
	// ran and adjudicated), "opted_out" (the operator disabled it),
	// "unavailable" (it was needed but could not run) or "not_needed" (the
	// embedding match alone was decisive).
	Verifier string `json:"verifier"`
	// ModelVersions names the embedding/verifier model identifiers this pass
	// ran with, keyed by role. Omitted when empty rather than publishing an
	// empty object — the same "nobody looked" vs "looked and found nothing"
	// distinction BlockEnrichment's header comment already draws.
	ModelVersions map[string]string `json:"model_versions,omitempty"`
}

// Project attribution statuses — the closed vocabulary ProjectsStatus
// publishes on BlockEnrichment, mirroring how PipelineStatusBlock names a
// block row's own kind rather than leaving a reader to infer it from an
// absence.
const (
	// ProjectsAttributed means the pass ran and named a project for this
	// block — Projects holds at least one ProjectAttribution.
	ProjectsAttributed = "attributed"
	// ProjectsPending means the pass has not finished for this block yet (e.g.
	// awaiting a background embedding or verifier step); a later republish via
	// WithProjects is expected to supersede this row.
	ProjectsPending = "pending"
	// ProjectsSkippedDisabled means attribution is switched off on this
	// machine — the block was never a candidate, not a candidate that failed.
	ProjectsSkippedDisabled = "skipped:disabled"
	// ProjectsSkippedNoProjects means attribution ran with no declared
	// projects to match against (settings.RemoteProject is empty), so there
	// was structurally nothing to attribute to.
	ProjectsSkippedNoProjects = "skipped:no_projects"
	// ProjectsDegradedWeights means the pass ran with its embedding weights
	// unavailable — the same "degraded" idiom Effort/Sensitivity use for a
	// pass that produced an answer from less evidence than usual, rather than
	// no answer at all.
	ProjectsDegradedWeights = "degraded:weights_unavailable"
)
