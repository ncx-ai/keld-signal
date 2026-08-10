package llmstudy

import "reflect"

// ProseFields returns every prose section of a digest, in declaration order.
//
// It exists because adding a section used to leave it outside the quality gates silently.
// Identifier verification, leak detection and rubberstamp detection each enumerated the six
// prose fields by hand, so when `synopsis` was introduced it was checked by none of them —
// the section a reader is most likely to read was the one section nothing verified. Worse,
// the omission looked like a result: unverified identifiers appeared to jump when the
// synopsis landed, and the synopsis was not being measured at all.
//
// Reflection rather than a hand-maintained list, so the next section added cannot be
// forgotten. Lists stay hand-written where ORDER carries meaning — the schema's required
// list, the prompt's section descriptions — but a "check everything" set must be derived.
func ProseFields(d Digest) []string {
	v := reflect.ValueOf(d)
	t := v.Type()
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type.Kind() == reflect.String {
			out = append(out, v.Field(i).String())
		}
	}
	return out
}
