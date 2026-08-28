package sidecar

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func okVector(n int) *enrich.QuantisedVector {
	return &enrich.QuantisedVector{Dims: n, Scale: 0.0078125, Q: make([]byte, n)}
}

// The six refusals on FeatureRowsFor, each one exercised. Every one of these
// failures is SILENT downstream — a bad row does not look bad, it looks like a
// vector — and the sidecar ships frozen and separately from keld-agent, so
// version skew is the realistic cause rather than a bug in one binary.
func TestFeatureRowGates(t *testing.T) {
	base := func() FeatureRowResult {
		return FeatureRowResult{
			Anchor: "bin", TS: 1755610800, FeatureSpec: 1,
			Structured: okVector(8),
		}
	}
	cases := []struct {
		name string
		mut  func(*FeatureRowResult)
		want bool
	}{
		{"a well-formed bin row survives", func(*FeatureRowResult) {}, true},
		{
			// The three kinds carry DIFFERENT things, so a row whose kind
			// cannot be read cannot be interpreted at all.
			"unknown anchor kind drops the row",
			func(r *FeatureRowResult) { r.Anchor = "hour" }, false,
		},
		{
			// The normalisation transform is not recoverable after the fact, so
			// an unversioned vector can never be safely pooled with anything.
			"missing feature_spec drops the row",
			func(r *FeatureRowResult) { r.FeatureSpec = 0 }, false,
		},
		{
			// Series instants are quantized to 0.1s and two turns can collide
			// on one tick, so a message with no key would upsert its neighbour.
			"message with no anchor id drops",
			func(r *FeatureRowResult) {
				r.Anchor, r.Role, r.AnchorID = "message", "user", ""
				r.Structured, r.TextRaw = nil, map[string]enrich.QuantisedVector{"user": *okVector(4)}
				r.Encoder = &enrich.EncoderRef{Model: "m", Width: 4}
			}, false,
		},
		{
			"message with a valid anchor id survives",
			func(r *FeatureRowResult) {
				r.Anchor, r.Role, r.AnchorID = "message", "user", "m-1"
				r.Structured, r.TextRaw = nil, map[string]enrich.QuantisedVector{"user": *okVector(4)}
				r.Encoder = &enrich.EncoderRef{Model: "m", Width: 4}
			}, true,
		},
		{
			// A vector pooled under the wrong register is worse than one absent.
			"message with an unknown role drops",
			func(r *FeatureRowResult) {
				r.Anchor, r.Role, r.AnchorID = "message", "tool", "m-1"
				r.Structured, r.TextRaw = nil, map[string]enrich.QuantisedVector{"user": *okVector(4)}
				r.Encoder = &enrich.EncoderRef{Model: "m", Width: 4}
			}, false,
		},
		{
			// The boundary reasons are also a training TARGET, so a row
			// carrying an unmeasured one trains against a label nobody produced.
			"block with an unreadable boundary reason drops whole",
			func(r *FeatureRowResult) {
				r.Anchor, r.StartReason, r.EndReason = "block", "detected", "budget"
			}, false,
		},
		{
			"block with readable reasons survives",
			func(r *FeatureRowResult) {
				r.Anchor, r.StartReason, r.EndReason = "block", "idle", "budget"
			}, true,
		},
		{
			// A vector cut short is not a smaller vector, it is a FALSE one.
			"declared dims disagreeing with the bytes drops the row",
			func(r *FeatureRowResult) { r.Structured.Dims = 9 }, false,
		},
		{
			// Every component would dequantise to 0.0 — a vector that says
			// nothing while looking like one that says everything is average.
			"zero scale drops the row",
			func(r *FeatureRowResult) { r.Structured.Scale = 0 }, false,
		},
		{
			"a row with no vectors at all drops",
			func(r *FeatureRowResult) { r.Structured = nil }, false,
		},
		{
			// Two corpora encoded by different models must never be pooled, and
			// nothing downstream can tell from the numbers.
			"text with no encoder identity is dropped, and with it the row",
			func(r *FeatureRowResult) {
				r.Structured = nil
				r.TextRaw = map[string]enrich.QuantisedVector{"user": *okVector(4)}
				r.Encoder = nil
			}, false,
		},
		{
			// An unreadable stream should not cost the two beside it: entry by
			// entry, the same call convertPathInventory makes.
			"an unknown stream is dropped entry-wise, not row-wise",
			func(r *FeatureRowResult) {
				r.TextRaw = map[string]enrich.QuantisedVector{
					"user": *okVector(4), "tool_result": *okVector(4),
				}
				r.Encoder = &enrich.EncoderRef{Model: "m", Width: 4}
			}, true,
		},
		{
			// The field is the row's one OPEN string; carrying something that
			// failed the shape gate is the whole exposure the gate closes.
			"a bin's malformed anchor id is cleared, not fatal",
			func(r *FeatureRowResult) { r.AnchorID = "a b\nc" }, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mut(&r)
			v, ok := featureVectorFrom(r, "claude_code", "S1")
			if ok != tc.want {
				t.Fatalf("accepted = %v, want %v (row %+v)", ok, tc.want, r)
			}
			if !ok {
				return
			}
			if v.SessionID != "S1" || v.Source != "claude_code" {
				t.Fatalf("identity not taken from the caller: %+v", v)
			}
			if v.AnchorID != "" && !enrich.ValidAnchorID(v.AnchorID) {
				t.Fatalf("a malformed anchor id survived: %q", v.AnchorID)
			}
			for s := range v.Text {
				if !enrich.KnownFeatureStream(s) {
					t.Fatalf("unknown stream %q survived", s)
				}
			}
		})
	}
}

// A bin/block row may fall back to its instant for identity — a bin sits on a
// 300-second grid and a block is bin-aligned and disjoint — but it must be
// rendered in ONE canonical spelling, or two renderings of a moment become two
// rows at Atlas.
func TestFeatureRowInstantIsCanonicalUTC(t *testing.T) {
	v, ok := featureVectorFrom(FeatureRowResult{
		Anchor: "bin", TS: 1755610800, FeatureSpec: 1, Structured: okVector(2),
	}, "claude_code", "S1")
	if !ok {
		t.Fatal("row rejected")
	}
	if !strings.HasSuffix(v.TS, "Z") {
		t.Fatalf("instant is not UTC: %q", v.TS)
	}
	if v.TSUnix != 1755610800 {
		t.Fatalf("cursor unit lost: %v", v.TSUnix)
	}
}

// ⚠️ The anchor id is the ONE open string on the row, and the bound is what
// keeps it an identifier rather than somewhere a message fragment could sit.
func TestValidAnchorIDRefusesTextShapes(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"a8f58d56-f6e0-4f32-a78c-9d85e1d8df37", true},
		{"1755600000", true},
		{"", false},
		{"please refactor the billing service", false}, // whitespace
		{"line\nbreak", false},
		{"tab\there", false},
		{strings.Repeat("x", enrich.MaxAnchorIDLen), true},
		{strings.Repeat("x", enrich.MaxAnchorIDLen+1), false},
	}
	for _, tc := range cases {
		if got := enrich.ValidAnchorID(tc.id); got != tc.want {
			t.Fatalf("ValidAnchorID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}
