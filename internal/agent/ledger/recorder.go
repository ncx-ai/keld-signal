// Package ledger is the daemon's delivery ledger: one row per focus block the
// cutter closes, one cell per stage (cut, measured, attributed, sent,
// received), and a machine-level health table. It exists because until it did
// the daemon observed every one of those outcomes and then forgot it — the
// 24-day zero-blocks outage (see docs/v3/contracts.md) was invisible to every
// surface precisely because nothing recorded "was a block published, and did
// Atlas take it" at the level where the answer lives.
//
// This file is the CONTRACT other packages compile against: the Recorder
// interface, the stage and reason vocabularies, and a Nop. The SQLite store
// behind it lives in store.go (deliverable D2). Nothing here may hold text, a
// span, an offset or a path: a reason is a code from the closed set below and
// the API refuses free-text messages by construction (there is no string
// parameter for one).
package ledger

import "time"

// Stage names a cell of a block row. The order is the order a block moves
// through; a stage never marked reads as ABSENT (unknown), never as false.
type Stage string

const (
	StageCut        Stage = "cut"        // the sidecar closed the block (/blocks answered it)
	StageMeasured   Stage = "measured"   // tokens, model and estimate joined from the store
	StageAttributed Stage = "attributed" // a project named it (by repo, ticket, embedding) or not
	StageSent       Stage = "sent"       // the publisher POSTed it
	StageReceived   Stage = "received"   // Atlas acknowledged it (status AND body check)
)

// Status is the state of one cell.
type Status string

const (
	StatusOK      Status = "ok"
	StatusFailed  Status = "failed"
	StatusPending Status = "pending" // known to be in progress or held (e.g. sidecar outdated)
	StatusNA      Status = "n/a"     // structurally not applicable (e.g. sent/received with Atlas off)
)

// Reason is a closed vocabulary. Adding one is a contract change: the page
// renders each with its own sentence and a wire test enumerates them.
type Reason string

const (
	ReasonNone               Reason = ""
	ReasonAtlasRejected      Reason = "atlas_rejected"      // 401/403 — the credential
	ReasonAtlasRefused       Reason = "atlas_refused"       // other 4xx — this payload
	ReasonAtlasUnavailable   Reason = "atlas_unavailable"   // net error / 5xx
	ReasonCaptivePortal      Reason = "captive_portal"      // 200 with a non-JSON body
	ReasonAtlasOff           Reason = "atlas_off"           // Send to Atlas is off
	ReasonSidecarOutdated    Reason = "sidecar_outdated"    // /blocks 404 — route unsupported
	ReasonSidecarDown        Reason = "sidecar_down"        // no health answer
	ReasonSidecarBehind      Reason = "sidecar_behind"      // store watermark behind the ask
	ReasonAttributeFailed    Reason = "attribute_failed"    // attribution job quarantined
	ReasonNoRuleMatched      Reason = "no_rule_matched"     // deterministic pass found nothing
	ReasonConflict           Reason = "conflict"            // two projects claim the same rule
	ReasonNoTokens           Reason = "no_tokens"           // store had no requests in the span
	ReasonSpooled            Reason = "spooled"             // sent could not happen now; durable copy kept
	ReasonWeightsUnavailable Reason = "weights_unavailable" // vector pass wanted, encoder absent
)

// Method is how a project was named.
type Method string

const (
	MethodRepo      Method = "repo"
	MethodTicket    Method = "ticket"
	MethodEmbedding Method = "embedding"
	MethodNone      Method = ""
)

// BlockKey identifies a block the way Atlas does: session id plus the block's
// start instant (unix seconds). Never a path.
type BlockKey struct {
	Session string
	Start   int64
}

// Measured is what the store knows about a block's spend.
type Measured struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	RequestTokens       int64 // price-weighted, as turn_magnitude records it
	Requests            int
	Model               string  // dominant model in the span
	EstimateUSD         float64 // 0 when no price table applies; the page shows "est." regardless
}

// Dims are the few workstream dimensions the PROJECTS pane needs to group
// unattributed blocks into suggestions: which repository, branch and workspace
// a block belonged to.
//
// ⚠️ **The ledger stores these because nothing else persists them, and a
// suggestion has to survive a restart.** A block's dims are computed once, at
// publish time, from the sidecar's payload; if only the daemon's memory held
// them, every restart would empty the Projects pane until new work arrived —
// which is exactly the "the app is broken" reading the ledger exists to
// prevent. They are identifiers of the class already published to Atlas as
// workstream values (`repo`, `branch`), never text, a span or an offset, and
// they pass the same shape validation every other identifier here does.
//
// Only these three. The block payload carries eight allocation dimensions and
// nine inventory ones; the rest are Atlas's business and storing them here
// would make this file a second copy of the enrichment, which it must not be.
type Dims struct {
	Repo      string
	Branch    string
	Workspace string
}

// Attributed is the outcome of the DETERMINISTIC attribution pass for one
// block — the rules a person declared, matched against the block's own
// workstream dimensions. It is written by exactly one caller
// (daemon.attributeAndRecord) and read as the machine's answer.
type Attributed struct {
	ProjectID string
	Method    Method
	Conflict  []string // project ids when Reason == ReasonConflict
}

// VectorAttributed is the VECTORISED pass's answer for the same block: a
// second opinion, decided on device by the encoder, and stored in its own cell
// beside the deterministic one rather than in place of it.
//
// ⚠️ **THE TWO ARE NEVER RECONCILED HERE, AND THAT IS DELIBERATE.** Both ids
// are stored; nothing in this package picks a winner when they differ, because
// choosing one needs data from a machine running both passes and no such data
// exists yet. Representing the disagreement IS the feature. A reader that
// renders one of them as "the" project is making a decision this package has
// refused to make for it.
type VectorAttributed struct {
	// ProjectID is the id the vector pass named. Set only alongside StatusOK.
	ProjectID string
	// Confidence is that pass's own score for the id, in [0,1]. It is stored
	// because a second opinion at 0.42 and one at 0.91 are different second
	// opinions, and nothing else on the row would say which this was.
	Confidence float64
}

// VectorRecorder is how — and the ONLY way — the vectorised attribution pass
// reaches the ledger.
//
// ⚠️ **IT IS DELIBERATELY NOT PART OF Recorder, AND Recorder IS DELIBERATELY
// NOT PART OF IT.** The two passes used to write the same cell: a vectorised
// job that exhausted its retries called Failed(k, StageAttributed,
// ReasonAttributeFailed) and overwrote whatever the deterministic pass had
// already decided. Measured on one machine, 44 rows lost a correct project id
// that way — 25 of them on a repository a declared rule matches exactly —
// because an encoder could not get memory and took the good answer down with
// it.
//
// Splitting the interfaces is what makes that unrepresentable rather than
// merely absent: the attribution path holds a VectorRecorder, which has one
// method that writes one cell, so there is no method on the value it holds
// that could name `attributed`. The other direction is closed in store.go —
// the vector columns are not registered in stageColumns, so the generic
// Failed/NotApplicable stage writers cannot reach them either. Do not embed
// one interface in the other, and do not add Vector to Recorder: either would
// compile, and either would put the 44 rows back within reach.
type VectorRecorder interface {
	// Vector writes the vector cell and nothing else. status is the cell's
	// own state (ok when the pass named a project, pending while it is still
	// warming or waiting on weights, n/a when there was structurally nothing
	// to match against, failed when the job was given up on); r names which,
	// from the same closed vocabulary every other cell uses.
	//
	// A block whose vector cell was never written reads as ABSENT — never
	// asked. That is a different fact from asked-and-failed, and a fleet that
	// cannot tell them apart cannot be debugged.
	Vector(k BlockKey, a VectorAttributed, status Status, r Reason, at time.Time)
}

// HealthKey names a machine-level fact. Health is not per block.
type HealthKey string

const (
	HealthDaemon    HealthKey = "daemon"    // version, up since
	HealthSidecar   HealthKey = "sidecar"   // version, matches daemon?
	HealthTelemetry HealthKey = "telemetry" // last successful forward
	HealthAtlas     HealthKey = "atlas"     // last response class, or off
	HealthStore     HealthKey = "store"     // ledger writable?
)

// Health is one machine-level fact with a status and an optional detail that is
// itself from a closed set (a version string or a Reason), never free text.
type Health struct {
	Key    HealthKey
	Status Status
	Detail string // version or Reason
	At     time.Time
}

// Recorder is what the daemon's hook points call. Every method must be safe to
// call from any goroutine and must never block the caller on I/O for long:
// implementations buffer and a write failure is logged once, never returned to
// the publish path (a ledger that cannot be written must not stop delivery).
type Recorder interface {
	// Cut records that the sidecar closed the block with the given span and
	// boundary reasons (REASONS in sidecar/app/analysis/blocks.py).
	Cut(k BlockKey, end int64, startReason, endReason string, source string, at time.Time)
	// CutPending records that blocks COULD NOT be asked for (sidecar outdated /
	// down / behind). Keyed by session because no block exists yet.
	CutPending(session string, r Reason, at time.Time)
	// Observe records the block's repo/branch/workspace dims — what the
	// Projects pane groups unattributed work by. Separate from Measure
	// because it answers a different question (where the work was, not what
	// it cost) and because a block can have dims with no spend and spend with
	// no dims, and conflating them would make either absence unreadable.
	Observe(k BlockKey, d Dims, at time.Time)
	Measure(k BlockKey, m Measured, at time.Time)
	Attribute(k BlockKey, a Attributed, r Reason, at time.Time)
	Sent(k BlockKey, at time.Time)
	Received(k BlockKey, httpStatus int, at time.Time)
	Failed(k BlockKey, s Stage, r Reason, httpStatus int, at time.Time)
	SetHealth(h Health)
}

// Nop is the Recorder used until D2 wires the store, and by every test that
// does not care. It records nothing and never fails.
type Nop struct{}

func (Nop) Cut(BlockKey, int64, string, string, string, time.Time) {}
func (Nop) CutPending(string, Reason, time.Time)                   {}
func (Nop) Observe(BlockKey, Dims, time.Time)                      {}
func (Nop) Measure(BlockKey, Measured, time.Time)                  {}
func (Nop) Attribute(BlockKey, Attributed, Reason, time.Time)      {}
func (Nop) Sent(BlockKey, time.Time)                               {}
func (Nop) Received(BlockKey, int, time.Time)                      {}
func (Nop) Failed(BlockKey, Stage, Reason, int, time.Time)         {}
func (Nop) SetHealth(Health)                                       {}

// Nop satisfies VectorRecorder too, so a test wiring the attribution path
// needs no second stub. Note it satisfies BOTH interfaces without either
// embedding the other — which is the point: an implementation may serve both
// roles, and no caller can hold one role and reach the other's cell.
func (Nop) Vector(BlockKey, VectorAttributed, Status, Reason, time.Time) {}

var (
	_ Recorder       = Nop{}
	_ VectorRecorder = Nop{}
)
