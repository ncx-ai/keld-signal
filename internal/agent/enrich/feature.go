package enrich

import "strings"

// THE SIGNAL-EMBEDDINGS PATH — the training corpus for future-work prediction.
// See docs/superpowers/specs/2026-08-26-signal-embeddings-design.md.
//
// A feature row is the `X` a centrally-trained model is fitted on: a vector
// computed ON THE DEVICE from the reference series (and, later, from a
// device-resident text encoder), published to Atlas, and paired there with a
// `y` derived from what the same machine records afterwards. Training never
// runs on a client; the client computes and publishes.
//
// ⚠️ NOTHING HERE CARRIES TEXT. The whole subsystem exists because the prose
// probe measured a digest-as-text encoder answering `record` on 36 of 36 inputs
// — the deterministic half enters as NUMBERS and the text half enters as an
// EMBEDDING, and neither is ever serialised into the other's modality. What
// crosses the wire is a quantised array, a scale, and the identities needed to
// pool two rows only when they mean the same thing.
//
// It is registered under ml_backend "deterministic" ONLY. Under "auto" it is
// ABSENT — never wired, so it appears in neither facets_skipped nor
// extractor_versions, which is this codebase's existing distinction between a
// pass that was skipped and one that never existed (see WithWorkstreams).

// FeatureCorrScheme is the correlation scheme a feature row carries.
//
// ⚠️ ITS OWN SCHEME IS THE WHOLE OF THE DEDUP STORY, and this is the trap
// window.go documents at length. Atlas keys enrichments
// UNIQUE(org_id, source_id, corr_scheme, corr_id) and inserts with
// ON CONFLICT DO UPDATE over EVERY column, so a row sharing a scheme with a
// prompt's enrichment or with a block row OVERWRITES it rather than deduping
// against it — replacing that row's facets with the nothing a feature vector
// computes. Under a scheme of its own the key cannot collide with either, and
// FeatureCorrID is a pure function of (session, anchor kind, anchor key), so a
// re-emitted row upserts ITSELF. That is what makes re-delivery free, which is
// what makes a bounded, drop-oldest spool acceptable.
const FeatureCorrScheme = "feature"

// FeatureAnchors is the closed vocabulary of where a row is snapshotted,
// mirroring the sidecar's own anchor kinds.
//
// IT IS A GATE, NOT DOCUMENTATION, for the reason every other mirrored
// vocabulary in this package is one: the sidecar ships frozen and separately
// from keld-agent, so an anchor kind this binary cannot read is version skew.
// The three carry DIFFERENT THINGS — a `message` row is not a small `bin` row —
// so a row whose kind is unreadable cannot be interpreted at all, and an
// unreadable kind published anyway would be pooled with rows that mean
// something else.
var FeatureAnchors = map[string]bool{
	// message: one per user/assistant message. A text vector only, plus its
	// timestamp and role. No shell ladder — a single message has no lookback.
	// The ONLY kind that preserves order, which pooling destroys.
	"message": true,
	// bin: one per non-empty 5-minute bin. The full structured vector plus the
	// per-shell pooled text scalars.
	"bin": true,
	// block: the same as bin, at a closed block's own end instant, plus the two
	// boundary reasons. Published because Atlas already renders blocks, NOT
	// because an arithmetic cut is the right sampling grid.
	"block": true,
}

// KnownFeatureAnchor reports whether a is in the published anchor vocabulary.
func KnownFeatureAnchor(a string) bool { return FeatureAnchors[a] }

// FeatureStreams is the closed vocabulary of text streams, kept SEPARATE and
// never concatenated: they are different registers and mixing them averages
// away the distinction that makes them worth encoding.
var FeatureStreams = map[string]bool{
	"user":  true, // the human's own words: smallest volume, highest density
	"asst":  true, // assistant prose, excluding thinking and tool results
	"think": true, // thinking-block content: largest volume, where the reasoning is
}

// KnownFeatureStream reports whether s is in the published stream vocabulary.
func KnownFeatureStream(s string) bool { return FeatureStreams[s] }

// FeatureRoles is the closed vocabulary a `message` row's author is named from.
// Two values, and a row is dropped rather than published with a third: a
// message vector pooled under the wrong register is worse than one absent.
var FeatureRoles = map[string]bool{"user": true, "assistant": true}

// KnownFeatureRole reports whether r is in the published role vocabulary.
func KnownFeatureRole(r string) bool { return FeatureRoles[r] }

// MaxAnchorIDLen bounds an anchor id at the decode boundary.
//
// ⚠️ THE BOUND IS WHAT KEEPS THE FIELD AN IDENTIFIER. Every other string on a
// feature row is drawn from a closed vocabulary or is an instant; `anchor_id`
// is the one open one, and an open string field on a row whose entire purpose
// is "no text crosses" is exactly the field a later change would smuggle a
// message fragment through. A message uuid is 36 characters; 128 is generous
// for a compound key and far below anything that could hold a sentence.
// Whitespace is refused outright for the same reason — an identifier has none.
const MaxAnchorIDLen = 128

// ValidAnchorID reports whether id is shaped like an identifier: non-empty,
// bounded, and free of whitespace. See MaxAnchorIDLen for why this is a gate
// rather than a convention.
func ValidAnchorID(id string) bool {
	if id == "" || len(id) > MaxAnchorIDLen {
		return false
	}
	return !strings.ContainsFunc(id, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
}

// QuantisedVector is one int8-quantised vector: value_i = int8(Q[i]) * Scale.
//
// ⚠️ THE GO SIDE IS DELIBERATELY AGNOSTIC ABOUT LENGTH, AND IT HAS ALREADY PAID
// OFF ONCE. The spec quoted a 1,414-dimension structured vector; the shipped
// sidecar emits 1,534 (the per-shell text scalars and `text_recorded` landed
// with feature_spec 2), and this file needed no edit for that. The published
// text width is 256, and it is expected to move too — the vocabulary manifest
// grows, and the MRL truncation width is a measurement not yet run. A constant
// here would be a second place to change and the one that fails silently: a
// length check against a stale constant drops every row a widened sidecar
// produces. The dimension count is the sidecar's, and it rides the row.
//
// Q is []byte rather than []int8 because encoding/json renders a byte slice as
// base64 — 1,534 dimensions in 2,048 characters against ~6,000 for an array of
// decimal numbers. At ~200 KB per user per active day, which is an order more
// than any existing row type, that difference is the difference between a
// batched publish and a problem. The bytes are TWO'S-COMPLEMENT int8, so a
// consumer reads each as a signed byte, never as 0-255.
type QuantisedVector struct {
	// Dims is the declared dimension count. Redundant with len(Q) BY DESIGN:
	// they are compared at the decode boundary, so a truncated payload is
	// REFUSED rather than silently read as a shorter vector. A vector cut short
	// is not a smaller vector, it is a false one — the "never truncate an
	// identifier" rule one level up.
	Dims int `json:"dims"`
	// Scale is the dequantisation factor. Zero is refused: it would render
	// every dimension as 0.0, which is a vector that says nothing while looking
	// like one that says everything is average.
	Scale float64 `json:"scale"`
	// Q holds the quantised components, base64 on the wire.
	Q []byte `json:"q"`
}

// Valid reports whether the vector is internally consistent: a positive scale,
// a positive declared width, and exactly that many components.
func (q QuantisedVector) Valid() bool {
	return q.Dims > 0 && len(q.Q) == q.Dims && q.Scale > 0
}

// EncoderRef is the identity of the text encoder that produced a row's text
// vectors, and it is mandatory wherever one is present.
//
// ⚠️ TWO CORPORA ENCODED BY DIFFERENT MODELS MUST NEVER BE POOLED, and nothing
// downstream can detect that from the numbers — two 256-dimension vectors from
// two encoders are the same shape and the same magnitude and mean entirely
// different things. Same argument as FeatureSpec one field down, and as
// ingest.terms_mode fingerprinting the terms pipeline's identity into
// parse_state.
type EncoderRef struct {
	// Model names the encoder, e.g. "qwen3-embedding-0.6b".
	Model string `json:"model"`
	// Width is the PUBLISHED width after MRL truncation, which is not the width
	// the encoder ran at: the spec encodes at 1,024 and publishes a 256-prefix.
	// Conflating the two is how the volume estimate goes wrong by 4x, so the
	// row states the one that is actually on the wire.
	Width int `json:"width"`
	// Projection identifies the fixed orthogonal projection applied on device
	// before publish. It preserves cosine similarity and inner products exactly
	// — nothing about training changes — and makes off-the-shelf embedding
	// inversion useless without the matrix, which Keld holds and the client
	// does not. Named so a rotation is detectable rather than silent. Empty
	// when no projection was applied.
	Projection string `json:"projection,omitempty"`
}

// FeatureVector is one anchor's feature row as the emitter publishes it: where
// it sits, what produced it, and the vectors themselves.
//
// TSUnix is the epoch-second form the sidecar answered with, kept alongside the
// RFC3339 TS because the EMITTER'S CURSOR is in those units — it resumes by
// passing the last emitted row's instant back as `since_ts`, and round-tripping
// that through a string would be a needless second spelling of the one number
// correctness depends on. It does not publish.
type FeatureVector struct {
	// SessionID is the transcript's own session identifier (Claude Code's file
	// stem), not the reference series' internal key — that is a machine-local
	// path digest and joins to nothing downstream.
	SessionID string
	Source    string
	// Anchor is from FeatureAnchors.
	Anchor string
	// AnchorID is the sidecar's own per-anchor key, and it is what the
	// correlation id is built on for a `message` row.
	//
	// ⚠️ A TIMESTAMP IS NOT A SUFFICIENT MESSAGE KEY, and the spec says so
	// explicitly: series timestamps are quantized to 0.1s (levels.quantize) and
	// store.py already notes two turns can collide on one tick. A `bin` or a
	// `block` instant IS an identity by construction — bins sit on a 300-second
	// grid and blocks are bin-aligned and disjoint — so those two may fall back
	// to it. A `message` may not, and one arriving without an anchor id is
	// DROPPED rather than published under an id that could overwrite its
	// neighbour.
	AnchorID string
	// TS is the anchor instant, RFC3339 in UTC.
	TS     string
	TSUnix float64
	// Role is from FeatureRoles, and is set on `message` rows only.
	Role string
	// StartReason and EndReason are from BlockReasons, and are set on `block`
	// rows only. They are the fields that separate an arithmetic cut from a
	// real pause; a block row carrying an unreadable one is dropped whole,
	// exactly as BlocksCharacterised drops such a block.
	StartReason string
	EndReason   string
	// FeatureSpec is the version of the vector's own definition — which
	// vocabularies, in which order, under which normalisation.
	//
	// ⚠️ IT IS PART OF THE ROW'S IDENTITY, not metadata. `log1p` versus raw is
	// not recoverable after the fact, and two spec versions pooled by accident
	// produce a corpus that is silently incoherent rather than visibly broken.
	// A row without one is dropped.
	FeatureSpec int
	// Structured is S(t): the disjoint shell ladder and the once-per-row block.
	// Present on `bin` and `block` rows; absent on `message`, which has no
	// lookback to compute one from.
	Structured *QuantisedVector
	// Text holds the per-stream text vectors, keyed by FeatureStreams. On a
	// `message` row these are the message's own; on `bin`/`block` rows the
	// per-shell pooled SCALARS ride inside Structured instead.
	//
	// ⚠️ CENTROIDS ARE DELIBERATELY NOT PUBLISHED ON bin/block ROWS. They are an
	// exact pooling of message vectors Atlas already holds, so they would
	// duplicate ~3x the message payload to say nothing new — and they would
	// freeze one pooling function into the wire when the pooling is precisely
	// what we expect to change.
	Text map[string]QuantisedVector
	// Encoder identifies what produced Text. Present iff Text is; a text vector
	// with no encoder identity is dropped, since it can never be safely pooled.
	Encoder *EncoderRef
}
