package publish

import (
	"encoding/json"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// FeatureRow is the wire shape of a SIGNAL-EMBEDDINGS row: one anchor's feature
// vector, the `X` a centrally-trained future-work model is fitted on.
// POST /v1/signal/features.
//
//	a transcript -> reference series -> feature vector -> Atlas
//	at Keld:      (X at time t, y at time t+h) -> a model
//
// Design: docs/superpowers/specs/2026-08-26-signal-embeddings-design.md.
//
// IT IS ITS OWN STRUCT, NOT AN Enrichment OR A BlockEnrichment WITH MOST FIELDS
// ZERO, and the reason is the one window.go spells out in full. Atlas keys
// enrichments UNIQUE(org_id, source_id, corr_scheme, corr_id) and inserts with
// ON CONFLICT DO UPDATE across every column, so a row riding either of those
// schemes does not dedup — it OVERWRITES, replacing that row's task_type,
// sensitivity, workstreams and boundary reasons with the nothing a feature
// vector computes. Under enrich.FeatureCorrScheme the key cannot collide with a
// prompt row or a block row, and FeatureCorrID is deterministic, so a
// re-published row upserts itself.
//
// It is ALSO not a BlockEnrichment with a vector bolted on, which is the v2
// version of the same decision. A block row is a characterisation a human
// reads: dominant values, stated statuses, honest blanks where MIN_EVIDENCE was
// not met. A feature row is the WHOLE DISTRIBUTION with no floor applied,
// because a sub-floor rollup is perfectly good input to a model even where it
// is not honest to publish as an attribution. Bending one type to serve both
// would make the presentation decision into a modelling one.
//
// ⚠️ THREE ANCHOR KINDS SHARE THIS TYPE AND THEY CARRY DIFFERENT THINGS. A
// `message` row is not a small `bin` row: it has a text vector, a role and no
// shell ladder, and it is the only kind that preserves ORDER, which any
// sequence model needs and which pooling destroys. What makes one type
// acceptable anyway is that `anchor` is STATED and gated against a closed
// vocabulary, so a consumer never has to infer the kind from which fields
// happen to be populated.
//
// ⚠️ NO FIELD HERE CAN HOLD TEXT, and that is asserted rather than intended
// (see feature_no_text_test.go). Every string is drawn from a closed
// vocabulary, is an instant, or is an identifier gated by shape at the decode
// boundary; the vectors are []byte, base64 on the wire.
type FeatureRow struct {
	Source      Source      `json:"source"`
	Correlation Correlation `json:"correlation"`
	Actor       string      `json:"actor,omitempty"`
	// SessionID is stated TWICE on purpose — here and inside Correlation — for
	// the reason BlockEnrichment states it twice: Atlas accepts either
	// spelling, and the duplication costs one short string against the
	// alternative of a client and a server disagreeing about which is
	// canonical.
	SessionID string `json:"session_id"`
	// Anchor is where this row was snapshotted: message | bin | block. From
	// enrich.FeatureAnchors, gated at the decode boundary.
	Anchor string `json:"anchor"`
	// AnchorID is the sidecar's own per-anchor key. It rides the row as well as
	// riding the correlation id, so a consumer can group a session's rows
	// without re-parsing an id it did not construct.
	AnchorID string `json:"anchor_id,omitempty"`
	// TS is the anchor's own instant, RFC3339 UTC — the time the vector
	// DESCRIBES, which is the join key `y` is derived against. Not the time the
	// row was built; that is EmittedAt.
	TS string `json:"ts"`
	// Role is the message's author, `message` rows only. From
	// enrich.FeatureRoles.
	Role string `json:"role,omitempty"`
	// StartReason and EndReason name why a `block` row's edges are where they
	// are. From enrich.BlockReasons, and set on block rows only — they are also
	// a training TARGET (`end_reason` measured at budget 48.5% / session_end
	// 33.0% / idle 18.5%), so a row publishing an unreadable one would be
	// training against a label nobody measured.
	StartReason string `json:"start_reason,omitempty"`
	EndReason   string `json:"end_reason,omitempty"`
	// FeatureSpec is the version of the vector's DEFINITION — which
	// vocabularies in which order under which normalisation. Part of the row's
	// identity, not metadata: see enrich.FeatureVector.FeatureSpec.
	FeatureSpec int `json:"feature_spec"`
	// Encoder is the text encoder's identity, present iff Text is. Two corpora
	// encoded by different models must never be pooled and nothing downstream
	// can tell from the numbers alone.
	Encoder *enrich.EncoderRef `json:"encoder,omitempty"`
	// Structured is S(t), on `bin` and `block` rows.
	Structured *enrich.QuantisedVector `json:"structured,omitempty"`
	// Text is the per-stream text vectors, keyed by enrich.FeatureStreams. The
	// three streams are kept SEPARATE and never concatenated: they are
	// different registers, and mixing them averages away the distinction.
	Text map[string]enrich.QuantisedVector `json:"text,omitempty"`
	// SchemaVersion is the enrichment schema version, carried for the same
	// reason every other row carries it — so a reader can tell which
	// vocabularies the row's non-vector fields were drawn from.
	SchemaVersion int `json:"schema_version"`
	// EmittedAt is when this row was BUILT, which is not when it applies. A
	// feature row can be emitted long after its anchor — a backlog drains, a
	// laptop comes back online — and a consumer that read TS as the emit time
	// would mis-order the corpus.
	EmittedAt string `json:"emitted_at"`
}

// BuildFeature maps one feature vector into the wire shape.
//
// IDENTITY IS `(session, anchor kind, anchor key)`, deterministic and
// immutable, and that is what makes emission idempotent: Atlas upserts on it,
// so a crash mid-batch, a re-delivery out of the spool, or a cursor that never
// advanced costs bandwidth and nothing else. The transport depends on this —
// it spools whole batches and re-posts them wholesale rather than tracking
// which individual rows landed.
func BuildFeature(f enrich.FeatureVector, actor string, now time.Time) FeatureRow {
	return FeatureRow{
		Source: Source{ID: f.Source},
		Correlation: Correlation{
			Scheme:    enrich.FeatureCorrScheme,
			ID:        FeatureCorrID(f.SessionID, f.Anchor, f.AnchorID, f.TS),
			SessionID: f.SessionID,
		},
		Actor:         actor,
		SessionID:     f.SessionID,
		Anchor:        f.Anchor,
		AnchorID:      f.AnchorID,
		TS:            f.TS,
		Role:          f.Role,
		StartReason:   f.StartReason,
		EndReason:     f.EndReason,
		FeatureSpec:   f.FeatureSpec,
		Encoder:       f.Encoder,
		Structured:    f.Structured,
		Text:          f.Text,
		SchemaVersion: enrich.SchemaVersion,
		EmittedAt:     now.UTC().Format(time.RFC3339),
	}
}

// FeatureCorrID is a feature row's correlation id: the session, the ANCHOR
// KIND, and the anchor's own key.
//
// THE ANCHOR KIND IS IN THE ID, NOT JUST THE SCHEME, and it has to be. All
// three kinds publish under one scheme, and a `bin` row and a `block` row
// routinely share an instant — a block ends at a bin edge by construction
// (analyze._block_span floors and ceils). Two rows carrying different vectors
// under one id would upsert each other, and the survivor would be whichever
// arrived last. The kind is the segment that separates them.
//
// IT ALSO CANNOT COLLIDE WITH A BLOCK ROW OR A PROMPT ROW. publish.BlockCorrID
// is `session@start` — two segments — and a prompt row's id is the prompt id
// itself. This one always has FOUR, so the id spaces are disjoint by shape as
// well as by scheme, which means a reader that keys on the id alone (and there
// are such readers) still cannot conflate them.
//
// The key is the anchor id when there is one, and the normalised instant
// otherwise. A bin sits on a 300-second grid and a block is bin-aligned and
// disjoint, so an instant IS an identity for those two; a MESSAGE's is not —
// series timestamps are quantized to 0.1s and two turns can collide on one tick
// — which is why a message with no anchor id is dropped upstream rather than
// given a colliding id here.
func FeatureCorrID(sessionID, anchor, anchorID, ts string) string {
	key := anchorID
	if key == "" {
		key = normaliseInstant(ts)
	}
	return sessionID + "@" + enrich.FeatureCorrScheme + "@" + anchor + "@" + key
}

// normaliseInstant renders an instant in one canonical spelling — UTC,
// RFC3339Nano — so two renderings of one moment cannot become two ids and
// therefore two rows. An unparseable value falls back to the raw string, which
// keeps distinct anchors DISTINCT; collapsing them onto a shared placeholder
// would make them overwrite each other, the exact failure this scheme exists to
// avoid.
func normaliseInstant(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return ts
}

// FeaturesEnvelope is what POST /v1/signal/features takes: a BATCH, and here
// that is not merely a convenience.
//
// ⚠️ VOLUME IS THE REASON. The spec's own estimate is ~200 KB per user per
// active day across ~190 rows (100 message, 72 bin, 18 block) — an order more
// than any existing row type on this client. A row-per-request would be ~190
// POSTs a day per user against an endpoint whose whole job is to accept them
// together, and it would make the bounded-buffer-plus-spool transport
// pointless, since there would be nothing to batch.
type FeaturesEnvelope struct {
	SchemaVersion int          `json:"schema_version"`
	InstallID     string       `json:"install_id,omitempty"`
	Features      []FeatureRow `json:"features"`
}

// MarshalFeatures renders one batch into the request body the transport
// delivers.
//
// It exists as a function rather than inside a Send method because the feature
// path does NOT own its own HTTP: it delivers through the clientevents
// transport (bounded buffer, periodic flush, internal/retry, drop-oldest
// spool), which takes bytes. Keeping the envelope here means the wire shape
// lives beside the row it wraps rather than in the package that happens to
// carry it.
func MarshalFeatures(installID string, rows []FeatureRow) ([]byte, error) {
	return json.Marshal(FeaturesEnvelope{
		SchemaVersion: enrich.SchemaVersion,
		InstallID:     installID,
		Features:      rows,
	})
}
