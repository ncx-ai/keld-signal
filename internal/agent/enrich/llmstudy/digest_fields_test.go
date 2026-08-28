package llmstudy

import (
	"reflect"
	"strings"
	"testing"
)

// The guard: every prose section must reach every gate. Adding `synopsis` slipped past all
// three hand-written enumerations, leaving the most-read section unverified.
func TestEveryProseSectionReachesEveryGate(t *testing.T) {
	tp := reflect.TypeOf(Digest{})
	var names []string
	for i := 0; i < tp.NumField(); i++ {
		if tp.Field(i).Type.Kind() == reflect.String {
			names = append(names, tp.Field(i).Name)
		}
	}
	if len(names) < 7 {
		t.Fatalf("expected at least 7 prose sections, found %v", names)
	}
	if got := len(ProseFields(Digest{})); got != len(names) {
		t.Fatalf("ProseFields returns %d of %d string sections", got, len(names))
	}

	// Each gate must observe a marker placed in each section individually.
	for i := range names {
		d := Digest{Unresolved: []string{"x"}}
		v := reflect.ValueOf(&d).Elem()
		fi := 0
		for j := 0; j < tp.NumField(); j++ {
			if tp.Field(j).Type.Kind() != reflect.String {
				continue
			}
			if fi == i {
				v.Field(j).SetString("the Zyxwvu-Marker path/to/thing.go was reversed")
			}
			fi++
		}
		if ids := Identifiers(d); len(ids) == 0 {
			t.Errorf("%s: identifiers gate does not see this section", names[i])
		}
		if !LooksRubberstamped(d, DigestFacts{Corrections: 1}) == false {
			// A section naming a reversal must clear the rubberstamp gate.
			t.Errorf("%s: rubberstamp gate does not see this section", names[i])
		}
	}
	_ = strings.TrimSpace
}
