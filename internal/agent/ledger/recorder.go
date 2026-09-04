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

// Attributed is the outcome of the attribution pass for one block.
type Attributed struct {
	ProjectID string
	Method    Method
	Conflict  []string // project ids when Reason == ReasonConflict
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
func (Nop) Measure(BlockKey, Measured, time.Time)                  {}
func (Nop) Attribute(BlockKey, Attributed, Reason, time.Time)      {}
func (Nop) Sent(BlockKey, time.Time)                               {}
func (Nop) Received(BlockKey, int, time.Time)                      {}
func (Nop) Failed(BlockKey, Stage, Reason, int, time.Time)         {}
func (Nop) SetHealth(Health)                                       {}

var _ Recorder = Nop{}
