package sidecar

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// THE SIGNAL-EMBEDDINGS PATH's client half: POST /features. Coordinates and
// instants — never text, never a span into a message, never an offset.
//
// THE CONTRACT, and it is the sidecar's own (app/analysis/features.py's
// `feature_rows`, routed in app/main.py; app/analysis/featuretext.py for the
// text half):
//
//	POST /features
//	  { "path": str, "since_ts": float|null, "now": float,
//	    "max_rows": int, "resolved": {...}|null }
//	->
//	  { "schema": int, "feature_spec": int, "spec_sha": str, "dims": int,
//	    "session": str, "watermark": float|null,
//	    "text_status": {stream: str},          # present only with the text half on
//	    "rows": [ { "anchor": "message"|"bin"|"block",
//	                "anchor_id": str,          # REQUIRED for message
//	                "ts": float,               # epoch seconds
//	                "role": "user"|"assistant",# message rows only
//	                "start_reason": str, "end_reason": str,   # block rows only
//	                "feature_spec": int, "spec_sha": str,
//	                "encoder": {"model": str, "width": int, "projection": str},
//	                "structured": {"dims": int, "scale": float, "q": base64},
//	                "capture_recorded": bool, "text_recorded": bool,
//	                "text": {"user"|"asst"|"think":
//	                         {"dims": int, "scale": float, "q": base64}} } ] }
//
// ⚠️ THE SIDECAR ENUMERATES THE ANCHORS; THIS SIDE SENDS ONLY A CURSOR. That is
// not an accident of who wrote which half: the sidecar owns the reference-series
// store, so it is the only side that can see where the non-empty 5-minute bins
// and the CLOSED blocks are. A daemon supplying a grid would have to guess it,
// and a guessed grid is silently wrong rather than visibly wrong. It is also
// exactly the shape POST /blocks has (`{path, since_ts, resolved}` ->
// `{rows, watermark}`), and consistency with the sibling route outweighs either
// half's local preference. The anchor-instant form lives on as
// POST /features/probe, for studies; nothing here calls it.
//
// `since_ts` is compared against a row's OWN instant with `>`, so the caller
// resumes by passing the last emitted row's instant: rows are chronological
// ACROSS ALL THREE ANCHOR KINDS within a transcript, so that admits the next row
// and excludes the one already sent — and the sidecar never cuts a batch inside
// an instant, because two rows can share one and emitting half of them would
// advance this cursor past the other half forever. Nil means "from the beginning
// of the session" — BACKFILL — and the caller owns that choice; the emitter
// seeds its own cursor forward-only instead (see internal/agent/features).
//
// `q` is base64 of TWO'S-COMPLEMENT int8 bytes, and `dims` is the declared
// length. They are compared here, not trusted: see FeatureRowsFor.
//
// FOUR KEYS ARE DELIBERATELY UNMODELLED, and encoding/json drops them:
//
//   - `spec_sha` — a digest of the sidecar's ordered slot manifest, so a slot
//     inserted without a `feature_spec` bump is still detectable. It is a
//     PRODUCER-side change detector and a corpus-builder's partition key, not
//     something this binary can act on: the row's published identity is
//     `feature_spec`, and a client that noticed a sha it did not recognise could
//     only do what it already does — forward the row and let Keld partition.
//   - `capture_recorded` / `text_recorded` — whether the capture slots and the
//     per-shell text slots of `structured` may be read at all. They are not lost
//     by being dropped here: both are also DIMENSIONS of the vector itself
//     (`row.meta.capture_recorded`, `row.meta.text_recorded`), which is where a
//     consumer that is reading the slots is already looking.
//   - `text_status` — the per-stream reason there is no vector (off, no weights,
//     encoder unavailable, or a stream that genuinely held nothing). An operator
//     and study fact; it names a local machine state, so there is nothing for it
//     to mean at Atlas.
//
// ⚠️ A `message` ROW EXISTS ONLY WHERE THE TEXT HALF RAN. It carries no
// `structured` vector — a single message has no lookback to compute one from —
// so with KELD_TEXTEMBED off the kind is simply ABSENT from the stream rather
// than emitted empty. `bin` and `block` rows are unaffected and carry the text
// half, when there is one, as per-shell scalars inside `structured`.

// featuresReq is one /features call over one transcript.
type featuresReq struct {
	Path    string   `json:"path"`
	SinceTS *float64 `json:"since_ts"`
	Now     float64  `json:"now"`
	MaxRows int      `json:"max_rows"`
	// Resolved rides a /features call for the reason it rides /analyze,
	// /ingest and /blocks: the sidecar is confined out of reading a repo's
	// .git/config, and a vector whose `repo` level could not be named is a
	// lesser vector for no reason. Per TRANSCRIPT, the granularity the facts
	// have.
	Resolved *enrich.ResolvedFacts `json:"resolved,omitempty"`
}

// FeatureRowResult is one feature row as it arrives on the wire.
//
// It deliberately models NOTHING beyond the contract above. An unmodelled key
// is dropped by encoding/json, which is the same defence every other decode
// boundary in this package relies on: a field this binary names is a field a
// publish path can forward, so a text-bearing key a later sidecar adds has
// structurally nowhere to go.
type FeatureRowResult struct {
	Anchor   string `json:"anchor"`
	AnchorID string `json:"anchor_id"`
	// TS is EPOCH SECONDS — the unit the cursor is in and the unit the series
	// keys everything on. The Go side converts once, at this boundary.
	TS          float64                 `json:"ts"`
	Role        string                  `json:"role"`
	StartReason string                  `json:"start_reason"`
	EndReason   string                  `json:"end_reason"`
	FeatureSpec int                     `json:"feature_spec"`
	Encoder     *enrich.EncoderRef      `json:"encoder"`
	Structured  *enrich.QuantisedVector `json:"structured"`
	// TextRaw is the per-stream text vectors AS SENT. Named apart from the
	// converted form so validTextVectors is the only way one reaches a row —
	// a field called `Text` here would be the obvious thing to assign straight
	// across, and the stream vocabulary would stop being a gate.
	TextRaw map[string]enrich.QuantisedVector `json:"text"`
}

// FeaturesResult is POST /features' whole answer.
//
// Watermark is returned even when no row is emittable, and that is the one fact
// separating "nothing new yet" from "this transcript has never been ingested"
// (nil). The emitter uses it to seed a first-sight cursor FORWARD-ONLY, which
// is why it must be a pointer rather than a zero float: 0 is a real instant and
// would backfill the whole of 1970 onwards.
type FeaturesResult struct {
	Schema    int                `json:"schema"`
	Rows      []FeatureRowResult `json:"rows"`
	Watermark *float64           `json:"watermark"`
}

// Features asks the sidecar for the feature rows this transcript has produced
// since the cursor.
//
// ok=false on any transport or status failure. There is no partial success to
// report: the cursor and the rows are one answer, and advancing past rows that
// never arrived would permanently lose exactly the vectors this path exists to
// collect. A sidecar too old to know the route answers 404 and lands here, which
// is the same "hold the cursor and ask again" behaviour every other route in
// this package gets from a not-yet-ready sidecar.
func (c *Client) Features(path string, since *float64, now time.Time,
	maxRows int, resolved enrich.ResolvedFacts) (FeaturesResult, bool) {
	var r FeaturesResult
	req := featuresReq{
		Path: path, SinceTS: since,
		Now:      float64(now.UnixNano()) / 1e9,
		MaxRows:  maxRows,
		Resolved: resolvedOrNil(resolved),
	}
	if !c.post("/features", req, &r) {
		return FeaturesResult{}, false
	}
	return r, true
}

// FeatureRowsFor is Features in the shape the feature emitter publishes, with
// every row put through this side's own gates.
//
// sessionID is supplied by the caller rather than taken from the response, for
// the reason BlocksCharacterised takes it: the series' own `session` is a
// digest of the transcript's absolute path, machine-local, and joins to nothing
// downstream.
//
// SIX REFUSALS, each defence in depth against a sidecar that stopped honouring
// a rule this side depends on. The sidecar ships frozen and separately, so
// version skew is real, and every one of these failures is SILENT downstream —
// a bad row does not look bad, it looks like a vector.
//
//   - An unreadable ANCHOR KIND drops the row. The three kinds carry different
//     things, so a row whose kind cannot be read cannot be interpreted, and one
//     published anyway would be pooled with rows that mean something else.
//   - A MESSAGE row with no valid anchor id drops. A timestamp is not a
//     sufficient message key: series instants are quantized to 0.1s and two
//     turns can collide on one tick, so such a row would upsert its neighbour
//     at Atlas. Bin and block instants ARE identities by construction (a
//     300-second grid; bin-aligned disjoint spans), so those may fall back.
//   - An unreadable ROLE on a message row drops it. A vector pooled under the
//     wrong register is worse than one absent.
//   - An unreadable BOUNDARY REASON on a block row drops it WHOLE, the same
//     call BlocksCharacterised makes: the reasons are also a training target,
//     so a row carrying an unmeasured one trains against a label nobody
//     produced.
//   - A row with NO VECTORS, or with a vector whose declared `dims` disagrees
//     with the bytes delivered, drops. A vector cut short is not a smaller
//     vector, it is a FALSE one — the identifier-truncation rule one level up —
//     and a row with nothing in it is the absence of a feature, not a feature.
//   - TEXT VECTORS WITHOUT AN ENCODER IDENTITY, or under an unreadable stream
//     name, are dropped from the row (the row survives if a structured vector
//     remains). Two corpora encoded by different models must never be pooled
//     and nothing downstream can tell from the numbers.
//
// Also refused: a row with no feature_spec. The normalisation transform is not
// recoverable after the fact, so an unversioned vector can never be safely
// pooled with anything, including itself a release later.
func (c *Client) FeatureRowsFor(path, source, sessionID string,
	since *float64, now time.Time, maxRows int,
	resolved enrich.ResolvedFacts) ([]enrich.FeatureVector, *float64, bool) {
	res, ok := c.Features(path, since, now, maxRows, resolved)
	if !ok {
		return nil, nil, false
	}
	out := make([]enrich.FeatureVector, 0, len(res.Rows))
	for _, r := range res.Rows {
		v, ok := featureVectorFrom(r, source, sessionID)
		if !ok {
			continue
		}
		out = append(out, v)
	}
	return out, res.Watermark, true
}

// featureVectorFrom converts one wire row, applying the refusals documented on
// FeatureRowsFor. Split out so each rule is testable on its own.
func featureVectorFrom(r FeatureRowResult, source, sessionID string) (enrich.FeatureVector, bool) {
	var zero enrich.FeatureVector
	if !enrich.KnownFeatureAnchor(r.Anchor) || r.FeatureSpec <= 0 {
		return zero, false
	}
	if r.Anchor == "message" {
		if !enrich.ValidAnchorID(r.AnchorID) || !enrich.KnownFeatureRole(r.Role) {
			return zero, false
		}
	} else if r.AnchorID != "" && !enrich.ValidAnchorID(r.AnchorID) {
		// A malformed id on a kind that does not need one is dropped rather
		// than carried: the field is the row's one open string, and carrying
		// something that failed the shape gate is the whole exposure the gate
		// exists to close. The instant still identifies the row.
		r.AnchorID = ""
	}
	if r.Anchor == "block" &&
		(!enrich.KnownBlockReason(r.StartReason) || !enrich.KnownBlockReason(r.EndReason)) {
		return zero, false
	}

	v := enrich.FeatureVector{
		SessionID:   sessionID,
		Source:      source,
		Anchor:      r.Anchor,
		AnchorID:    r.AnchorID,
		TS:          epochRFC3339(r.TS),
		TSUnix:      r.TS,
		FeatureSpec: r.FeatureSpec,
	}
	if r.Anchor == "message" {
		v.Role = r.Role
	}
	if r.Anchor == "block" {
		v.StartReason, v.EndReason = r.StartReason, r.EndReason
	}
	if r.Structured != nil && r.Structured.Valid() {
		s := *r.Structured
		v.Structured = &s
	}
	if text := validTextVectors(r); len(text) > 0 && r.Encoder != nil && r.Encoder.Model != "" {
		v.Text = text
		e := *r.Encoder
		v.Encoder = &e
	}
	if v.Structured == nil && len(v.Text) == 0 {
		return zero, false
	}
	return v, true
}

// validTextVectors keeps the streams that are both named in the published
// vocabulary and internally consistent, dropping the rest ENTRY BY ENTRY rather
// than dropping the row — the same call convertPathInventory makes, and for the
// same reason: one unreadable stream should not cost the two beside it.
func validTextVectors(r FeatureRowResult) map[string]enrich.QuantisedVector {
	if len(r.TextRaw) == 0 {
		return nil
	}
	out := make(map[string]enrich.QuantisedVector, len(r.TextRaw))
	for stream, q := range r.TextRaw {
		if !enrich.KnownFeatureStream(stream) || !q.Valid() {
			continue
		}
		out[stream] = q
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
