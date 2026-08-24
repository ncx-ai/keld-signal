package publish

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// The window analysis returns more than the seven dimensions that publish:
// inventory.named_terms (proper nouns lifted from message TEXT — real person
// names have been observed in it) and the window's own start/end timestamps.
// None of that may cross to Atlas.
//
// Today the guarantee is STRUCTURAL: sidecar.AnalyzeResult does not model
// inventory at all, and it drops session/window_start/window_end on the way to
// enrich.Labeled, so publish.Enrichment has nowhere to put them. Structural is
// the right mechanism — a comment saying "don't forward this" is not a
// mechanism. But nothing FAILS if a later change gives Enrichment (or anything
// it embeds) somewhere to put them, and a privacy regression that announces
// itself only in a code review is one that ships.
//
// So: fill every field of the wire struct with non-zero data — reflectively, so
// a field added tomorrow is populated too and `omitempty` cannot hide it —
// marshal it, and assert the forbidden keys are absent from the bytes.
//
// EXTENDED for the dynamics block. /analyze's dynamics carry, per dimension, a
// `slice` and a `baseline` object whose `value` is the reference level ITSELF —
// on `term` (the one level read from message text) that slot is a name someone
// spoke — plus the comparison's three timestamps and the sizer's detail. What
// publishes is the derived six: status, reading, and the three shares plus
// `changed`. Everything else in the block is on this list, so giving Enrichment
// somewhere to put one FAILS here rather than in a review.
//
// "slice" and "baseline" are listed as whole quoted keys, which is why
// `"slice_start"` does not satisfy `"slice"`.
var forbiddenWireKeys = []string{
	"inventory", "named_terms", "window_start", "window_end",
	"slice", "baseline", "slice_start", "slice_end", "baseline_start",
	"slice_minutes", "baseline_minutes", "sizer", "sizer_detail",
	"reconcile_scope", "emerged", "decayed", "provenance", "reason",
}

func TestEnrichmentWireShapeCannotCarryAnalysisInternals(t *testing.T) {
	var e Enrichment
	fillNonZero(reflect.ValueOf(&e).Elem())

	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)

	// Guard the guard: if the filler silently stopped populating things, every
	// assertion below would pass vacuously.
	for _, present := range []string{`"workstreams"`, `"sensitivity_spans"`, `"custom"`, `"task_type"`,
		`"dynamics"`, `"reading"`, `"turnover"`} {
		if !strings.Contains(got, present) {
			t.Fatalf("filler did not populate %s; the absence checks below would be vacuous:\n%s",
				present, got)
		}
	}

	for _, k := range forbiddenWireKeys {
		if strings.Contains(got, `"`+k+`"`) {
			t.Errorf("published enrichment carries %q — analysis internals must stay on-device "+
				"(named terms can be real person names; window bounds are local metadata):\n%s",
				k, got)
		}
	}
}

// fillNonZero recursively sets every field to a non-zero value, so `omitempty`
// cannot keep a newly-added field out of the marshalled bytes. Depth-bounded
// because a self-referential type would otherwise recurse forever; nothing in
// the wire shape is anywhere near it.
func fillNonZero(v reflect.Value) { fill(v, 0) }

func fill(v reflect.Value, depth int) {
	if depth > 6 || !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Ptr:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), depth+1)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			fill(v.Field(i), depth+1)
		}
	case reflect.Slice:
		el := reflect.New(v.Type().Elem()).Elem()
		fill(el, depth+1)
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), el))
	case reflect.Map:
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, depth+1)
		val := reflect.New(v.Type().Elem()).Elem()
		fill(val, depth+1)
		m := reflect.MakeMap(v.Type())
		m.SetMapIndex(k, val)
		v.Set(m)
	case reflect.Interface:
		v.Set(reflect.ValueOf("x"))
	}
}
