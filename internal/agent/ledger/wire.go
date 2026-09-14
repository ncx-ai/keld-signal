package ledger

import "time"

// Snapshot is the exact shape GET /v1/ledger returns (docs/v3/contracts.md).
// Field order in the marshalled JSON follows encoding/json's own rules
// (struct fields in declaration order; map[string]any keys alphabetically)
// — the contract is the JSON *shape*, not its byte order.
type Snapshot struct {
	GeneratedAt string         `json:"generated_at"`
	Health      []HealthEntry  `json:"health"`
	Blocks      []BlockEntry   `json:"blocks"`
	Pending     []PendingEntry `json:"pending"`
}

// HealthEntry is one machine-level fact.
type HealthEntry struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	At     string `json:"at"`
}

// PendingEntry is a session for which blocks could not be asked for at all.
//
// ⚠️ **At AND Since ANSWER DIFFERENT QUESTIONS AND ARE NOT INTERCHANGEABLE.**
// At is refreshed every sweep this session still cannot be cut, so it says
// "this was still true a moment ago" and can never be more than one sweep
// interval old. Since is written once, when the streak began, so it is the
// only field that can say how long the wait has actually lasted. A reader
// that thresholds on At is measuring the sweep timer.
//
// Since is empty on a row written before it existed, and on any row whose
// origin is unknown. An empty Since must be read as "how long is unknown",
// never as "just now" — an unknown age must not render as a problem, and it
// must not render as reassurance either.
type PendingEntry struct {
	Session string `json:"session"`
	Reason  string `json:"reason"`
	At      string `json:"at"`
	Since   string `json:"since,omitempty"`
}

// BlockKeyEntry identifies a block the way Atlas does.
type BlockKeyEntry struct {
	Session string `json:"session"`
	Start   int64  `json:"start"`
}

// TokensEntry is the measured cell's token breakdown.
type TokensEntry struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
	Request       int64 `json:"request"`
}

// BlockEntry is one block row. Cells is keyed by Stage string and built as a
// map rather than a fixed struct with omitempty pointers so that "never
// marked" (key absent) and "marked but empty" cannot be confused by
// construction — see recorder.go and docs/v3/contracts.md: "a cell that
// never happened must be ABSENT from the marshalled cells object, never
// {"status":"failed"}".
type BlockEntry struct {
	Key         BlockKeyEntry             `json:"key"`
	End         int64                     `json:"end"`
	Source      string                    `json:"source"`
	StartReason string                    `json:"start_reason"`
	EndReason   string                    `json:"end_reason"`
	Cells       map[string]map[string]any `json:"cells"`
}

// Reader is what the ingress ledger route calls. Unlike Recorder's write
// methods (which must never block the caller or return an error — see
// recorder.go), Read is allowed to surface a genuine I/O failure: the route
// maps that to 500, which is a fine outcome for a GET.
type Reader interface {
	Read(since time.Time, limit int) (Snapshot, error)
}
