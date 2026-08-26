package publish

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func sampleFeature() enrich.FeatureVector {
	return enrich.FeatureVector{
		SessionID:   "a8f58d56-f6e0-4f32-a78c-9d85e1d8df37",
		Source:      "claude_code",
		Anchor:      "block",
		AnchorID:    "1755600000",
		TS:          "2026-08-19T13:40:00Z",
		TSUnix:      1755610800,
		StartReason: "idle",
		EndReason:   "budget",
		FeatureSpec: 1,
		Structured:  &enrich.QuantisedVector{Dims: 4, Scale: 0.0078125, Q: []byte{1, 255, 40, 200}},
		Text: map[string]enrich.QuantisedVector{
			"user": {Dims: 3, Scale: 0.0078125, Q: []byte{9, 8, 7}},
		},
		Encoder: &enrich.EncoderRef{Model: "qwen3-embedding-0.6b", Width: 256, Projection: "p1"},
	}
}

// ⚠️ THE COLLISION TEST. Atlas keys enrichments
// UNIQUE(org_id, source_id, corr_scheme, corr_id) and inserts with
// ON CONFLICT DO UPDATE across every column, so a feature row sharing a scheme
// with a prompt's enrichment or with a block row does not dedup — it OVERWRITES
// it. This is the assertion that keeps that from being reintroduced by someone
// tidying three schemes into one.
func TestAFeatureRowCannotCollideWithAPromptOrBlockRow(t *testing.T) {
	f := sampleFeature()
	row := BuildFeature(f, "dg@keld.co", time.Now())

	if row.Correlation.Scheme != enrich.FeatureCorrScheme {
		t.Fatalf("scheme = %q, want %q", row.Correlation.Scheme, enrich.FeatureCorrScheme)
	}
	for _, other := range []string{enrich.BlockCorrScheme, enrich.WindowCorrScheme, "prompt", ""} {
		if row.Correlation.Scheme == other {
			t.Fatalf("feature row shares scheme %q", other)
		}
	}

	// A block row at the same session and the same instant must not produce the
	// same id even if a reader keys on the id alone. Block ids are two
	// segments; a feature id is always four.
	blockID := BlockCorrID(f.SessionID, f.TS)
	if row.Correlation.ID == blockID {
		t.Fatalf("feature id collides with block id: %q", blockID)
	}
	if got := strings.Count(row.Correlation.ID, "@"); got != 3 {
		t.Fatalf("feature id has %d separators, want 3 (session@scheme@anchor@key): %q",
			got, row.Correlation.ID)
	}

	// A prompt row's correlation id is the bare prompt id; no feature id can be
	// one, because a prompt id contains no "@" and every feature id contains
	// three.
	if !strings.Contains(row.Correlation.ID, "@") {
		t.Fatalf("feature id is prompt-id shaped: %q", row.Correlation.ID)
	}
}

// The id must be a pure function of (session, anchor kind, anchor key). That is
// the whole of the idempotency this path relies on instead of per-row delivery
// tracking: Atlas upserts, so re-delivery out of the spool costs bandwidth and
// nothing else.
func TestFeatureCorrIDIsDeterministicAndDiscriminating(t *testing.T) {
	const sess = "S1"
	cases := []struct {
		name                     string
		anchor, anchorID, ts     string
		otherAnchor, otherID, ot string
		wantSame                 bool
	}{
		{
			name:   "same inputs, same id",
			anchor: "bin", anchorID: "b1", ts: "2026-08-19T13:40:00Z",
			otherAnchor: "bin", otherID: "b1", ot: "2026-08-19T13:40:00Z",
			wantSame: true,
		},
		{
			// A block ends at a bin edge BY CONSTRUCTION (analyze._block_span
			// floors and ceils), so bin and block rows routinely share an
			// instant. Without the anchor kind in the id they would upsert each
			// other and the survivor would be whichever arrived last.
			name:   "bin and block at one instant differ",
			anchor: "bin", ts: "2026-08-19T13:40:00Z",
			otherAnchor: "block", ot: "2026-08-19T13:40:00Z",
		},
		{
			// Two messages CAN share a quantized instant — series timestamps are
			// 0.1s and store.py notes two turns colliding on one tick — which is
			// exactly why the anchor id carries the identity for that kind.
			name:   "messages at one instant differ by anchor id",
			anchor: "message", anchorID: "m1", ts: "2026-08-19T13:40:00.05Z",
			otherAnchor: "message", otherID: "m2", ot: "2026-08-19T13:40:00.05Z",
		},
		{
			name:   "different sessions cannot share an id",
			anchor: "bin", ts: "2026-08-19T13:40:00Z",
			otherAnchor: "bin", ot: "2026-08-19T13:45:00Z",
		},
		{
			// Two spellings of one moment must not become two rows.
			name:   "utc and offset spellings normalise to one id",
			anchor: "bin", ts: "2026-08-19T13:40:00Z",
			otherAnchor: "bin", ot: "2026-08-19T15:40:00+02:00",
			wantSame: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := FeatureCorrID(sess, tc.anchor, tc.anchorID, tc.ts)
			b := FeatureCorrID(sess, tc.otherAnchor, tc.otherID, tc.ot)
			if (a == b) != tc.wantSame {
				t.Fatalf("FeatureCorrID(%q,%q,%q)=%q vs (%q,%q,%q)=%q; wantSame=%v",
					tc.anchor, tc.anchorID, tc.ts, a, tc.otherAnchor, tc.otherID, tc.ot, b, tc.wantSame)
			}
		})
	}

	// Purity: the same inputs must give the same id across calls, or a
	// re-published row is a duplicate rather than an upsert.
	first := FeatureCorrID(sess, "block", "", "2026-08-19T13:40:00Z")
	for i := 0; i < 5; i++ {
		if got := FeatureCorrID(sess, "block", "", "2026-08-19T13:40:00Z"); got != first {
			t.Fatalf("id not stable: %q != %q", got, first)
		}
	}
}

// An unparseable instant must keep distinct anchors DISTINCT. Collapsing them
// onto a shared placeholder would make them overwrite each other, which is the
// exact failure the scheme exists to avoid.
func TestFeatureCorrIDKeepsUnparseableInstantsDistinct(t *testing.T) {
	a := FeatureCorrID("S1", "bin", "", "not-a-time")
	b := FeatureCorrID("S1", "bin", "", "also-not-a-time")
	if a == b {
		t.Fatalf("unparseable instants collapsed onto one id: %q", a)
	}
}

// A row must survive the wire unchanged: the vectors are the payload, and a
// quantised component that changed sign or width in transit is a silently wrong
// training example rather than a visible failure.
func TestFeatureRowRoundTrips(t *testing.T) {
	row := BuildFeature(sampleFeature(), "dg@keld.co", time.Unix(1755610900, 0))

	b, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back FeatureRow
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(row, back) {
		t.Fatalf("round trip changed the row:\n got %+v\nwant %+v", back, row)
	}

	// The vector rides as base64, not as an array of decimal numbers: 1,414
	// dimensions is ~1,886 characters that way against ~5,600, and at ~200 KB
	// per user per active day that difference is the difference between a
	// batched publish and a problem.
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	st, ok := raw["structured"].(map[string]any)
	if !ok {
		t.Fatalf("structured missing from %s", b)
	}
	if _, isString := st["q"].(string); !isString {
		t.Fatalf("structured.q is not base64-encoded: %T", st["q"])
	}
}

// The envelope is a BATCH because the volume demands it: ~190 rows per user per
// active day, an order more than any existing row type on this client.
func TestMarshalFeaturesIsABatch(t *testing.T) {
	rows := []FeatureRow{
		BuildFeature(sampleFeature(), "a", time.Unix(0, 0)),
		BuildFeature(sampleFeature(), "a", time.Unix(0, 0)),
	}
	b, err := MarshalFeatures("inst-1", rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var env FeaturesEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Features) != 2 {
		t.Fatalf("features = %d, want 2", len(env.Features))
	}
	if env.InstallID != "inst-1" || env.SchemaVersion != enrich.SchemaVersion {
		t.Fatalf("envelope = %+v", env)
	}
}

// ⚠️ NO FIELD ON A FEATURE ROW MAY CARRY TEXT, and this is the mechanism rather
// than the intention. The row is the first thing this client publishes whose
// payload is derived from message TEXT (via the encoder), so the standing
// privacy invariant rests entirely on there being nowhere for a fragment to
// sit.
//
// The guarantee is that every string-typed field is drawn from a closed
// vocabulary, is an instant, or is an identifier gated by shape at the decode
// boundary — and that the vectors are []byte. A field added tomorrow that is
// none of those FAILS HERE rather than in a review. The allowlist is what makes
// that true: adding a string field means adding it here deliberately, with the
// argument written down.
func TestFeatureRowHasNoFieldThatCouldCarryText(t *testing.T) {
	allowed := map[string]string{
		"source.id":           "the tool that produced the transcript, from a closed source set",
		"source.origin":       "shared Source type; unset on this row",
		"source.version":      "shared Source type; unset on this row",
		"correlation.scheme":  "the constant enrich.FeatureCorrScheme",
		"correlation.id":      "session + anchor kind + anchor key, all identifiers",
		"correlation.session": "the transcript's file stem",
		"actor":               "the device's own authenticated principal",
		"session_id":          "the transcript's file stem",
		"anchor":              "closed: enrich.FeatureAnchors",
		"anchor_id":           "identifier, gated by enrich.ValidAnchorID (bounded, no whitespace)",
		"ts":                  "an instant",
		"role":                "closed: enrich.FeatureRoles",
		"start_reason":        "closed: enrich.BlockReasons",
		"end_reason":          "closed: enrich.BlockReasons",
		"encoder.model":       "the encoder's name, chosen by this client",
		"encoder.projection":  "the projection matrix's id, chosen by this client",
		"emitted_at":          "an instant",
		"text.<key>":          "closed: enrich.FeatureStreams",
		"structured.q/text.q": "[]byte, base64 — quantised components, never a string in Go",
	}
	var found []string
	walkStrings(t, reflect.TypeOf(FeatureRow{}), "", &found)
	for _, f := range found {
		if _, ok := allowed[f]; !ok {
			t.Fatalf("FeatureRow gained a string-typed field %q with no recorded argument for why "+
				"it cannot hold text. Add it to the allowlist WITH the argument, or use a type "+
				"that cannot hold a sentence.", f)
		}
	}
	// Sanity: the walk must actually be finding fields, or the test proves
	// nothing.
	if !slices.Contains(found, "anchor") {
		t.Fatalf("the string walk found nothing recognisable: %v", found)
	}

	// And the vectors must be []byte, not string. A string-typed vector field
	// is a text field wearing a hat.
	rt := reflect.TypeOf(FeatureRow{})
	for _, name := range []string{"Structured", "Text"} {
		f, ok := rt.FieldByName(name)
		if !ok {
			t.Fatalf("FeatureRow has no %s field", name)
		}
		q := vectorElem(f.Type)
		qf, ok := q.FieldByName("Q")
		if !ok || qf.Type.Kind() != reflect.Slice || qf.Type.Elem().Kind() != reflect.Uint8 {
			t.Fatalf("%s's quantised payload is not a byte slice: %v", name, qf.Type)
		}
	}
}

// vectorElem unwraps *QuantisedVector / map[string]QuantisedVector down to the
// struct type.
func vectorElem(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr || t.Kind() == reflect.Map || t.Kind() == reflect.Slice {
		t = t.Elem()
	}
	return t
}

// walkStrings collects the json names of every string-typed leaf reachable from
// t, including through pointers, maps and embedded structs.
func walkStrings(t *testing.T, rt reflect.Type, prefix string, out *[]string) {
	t.Helper()
	for rt.Kind() == reflect.Ptr {
		rt = rt.Elem()
	}
	if rt.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		// The wire spells Correlation's session key session_id; the allowlist
		// uses the shorter form so the two Correlation entries read distinctly.
		if prefix == "correlation." && name == "session_id" {
			name = "session"
		}
		full := prefix + name
		ft := f.Type
		for ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.String:
			*out = append(*out, full)
		case reflect.Struct:
			walkStrings(t, ft, full+".", out)
		case reflect.Map:
			// A map's KEYS are strings too; they are covered by the
			// "text.<key>" allowlist entry and gated by enrich.FeatureStreams.
			walkStrings(t, ft.Elem(), full+".", out)
		case reflect.Slice:
			walkStrings(t, ft.Elem(), full+".", out)
		}
	}
}
