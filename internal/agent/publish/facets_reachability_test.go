package publish

import (
	"reflect"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// TestFacetsOfCarriesEveryFieldItCanReach holds facetsOf to its own docstring
// ("Field-for-field and nothing else") by EXECUTING it rather than trusting it.
//
// ⚠️ THIS EXISTS BECAUSE THE DOCSTRING WAS TRUE WHEN WRITTEN AND SILENTLY STOPPED
// BEING TRUE. `activity_classes` and `activity_class_tokens` were added to the
// sidecar payload, to the decode struct, to enrich.WindowAnalysis, to
// AnalysisFacets and to Enrichment -- five of the six places -- and NOT to the
// one function both row builders call. The sidecar computed the level, the client
// decoded it, both wire structs declared the key, and it reached Atlas on NEITHER
// the block row NOR the prompt row. Every test passed: the sidecar-side guard
// checks the DECODE key set, and a struct field nothing assigns marshals away
// under `omitempty` without complaint, so the absence looked exactly like a window
// that happened to contain nothing.
//
// The general defect is a field declared on both sides of a copy and copied by
// neither, which is invisible precisely because `omitempty` is doing its job. So
// this asserts the property rather than the instance: for EVERY field of
// AnalysisFacets that WindowAnalysis can supply by the same name and type, a
// non-zero input must produce a non-zero output. A future inventory added to both
// structs and forgotten here fails this test on the commit that adds it.
func TestFacetsOfCarriesEveryFieldItCanReach(t *testing.T) {
	inT := reflect.TypeOf(enrich.WindowAnalysis{})
	outT := reflect.TypeOf(AnalysisFacets{})

	checked := 0
	for i := 0; i < outT.NumField(); i++ {
		of := outT.Field(i)
		inf, ok := inT.FieldByName(of.Name)
		if !ok || inf.Type != of.Type {
			// Not supplied by WindowAnalysis under this name, or a different
			// type: outside what "field-for-field" can mean. withSpend's
			// block-local token counts are the real instance of this.
			continue
		}
		filled, ok := nonZero(of.Type)
		if !ok {
			continue // no generic way to populate this shape; not a gap in facetsOf
		}
		in := reflect.New(inT).Elem()
		in.FieldByName(of.Name).Set(filled)

		got := reflect.ValueOf(facetsOf(in.Interface().(enrich.WindowAnalysis))).Field(i)
		if got.IsZero() {
			t.Errorf("facetsOf DROPS %s (%s): set on WindowAnalysis, empty on "+
				"AnalysisFacets. The block row would carry the key on no wire at "+
				"all, and `omitempty` makes that indistinguishable from an empty "+
				"window. Add it to facetsOf.", of.Name, of.Type)
		}
		checked++
	}
	if checked < 15 {
		t.Fatalf("only %d fields exercised; this test is meant to cover the whole "+
			"facet set and has stopped doing so", checked)
	}
	t.Logf("%d of %d AnalysisFacets fields reachable from WindowAnalysis, all carried",
		checked, outT.NumField())
}

// nonZero builds a non-zero value of t for the shapes AnalysisFacets uses,
// reporting false for anything it cannot populate rather than guessing.
func nonZero(t reflect.Type) (reflect.Value, bool) {
	switch t.Kind() {
	case reflect.Slice:
		el, ok := nonZeroElem(t.Elem())
		if !ok {
			return reflect.Value{}, false
		}
		s := reflect.MakeSlice(t, 1, 1)
		s.Index(0).Set(el)
		return s, true
	case reflect.Map:
		el, ok := nonZeroElem(t.Elem())
		if !ok {
			return reflect.Value{}, false
		}
		k, ok := nonZeroElem(t.Key())
		if !ok {
			return reflect.Value{}, false
		}
		m := reflect.MakeMap(t)
		m.SetMapIndex(k, el)
		return m, true
	case reflect.Pointer:
		return reflect.New(t.Elem()), true
	case reflect.String:
		return reflect.ValueOf("x").Convert(t), true
	case reflect.Int, reflect.Int64:
		return reflect.ValueOf(int64(1)).Convert(t), true
	}
	return reflect.Value{}, false
}

func nonZeroElem(t reflect.Type) (reflect.Value, bool) {
	if t.Kind() == reflect.Struct {
		// A zero struct in a length-1 slice is still a non-zero SLICE, which is
		// all this test needs; it never inspects the element.
		return reflect.New(t).Elem(), true
	}
	return nonZero(t)
}
