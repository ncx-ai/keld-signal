package enrich

import (
	"os"
	"strings"
	"testing"
)

// Acts is a CLOSED published set computed in Python, and a value this binary does
// not recognise is dropped at the conversion boundary rather than failing loudly
// — so, exactly as the dynamics and effort vocabularies are, it is read from the
// sidecar's own source instead of being mirrored by hand. The sidecar is frozen
// and shipped separately from keld-agent, so an older or newer one can sit in
// ~/.local/bin indefinitely.
//
// This is the one vocabulary in the set whose values are multi-word phrases
// ("run a service", "version control"), which is why pyTuple's value pattern
// admits a space.
func TestActVocabularyMatchesTheSidecar(t *testing.T) {
	src, err := os.ReadFile("../../../sidecar/app/analysis/vocab.py")
	if err != nil {
		t.Fatalf("cannot read vocab.py, so enrich.Acts is unpinned: %v", err)
	}
	want := pyTuple(t, string(src), "ACTIONS")
	if len(want) < 20 {
		t.Fatalf("parsed only %d values out of ACTIONS; the comparison would be "+
			"vacuous: %v", len(want), want)
	}
	if strings.Join(want, ",") != strings.Join(Acts, ",") {
		t.Errorf("ACTIONS drifted:\n python %v\n go     %v", want, Acts)
	}
}

func TestKnownActVocabulary(t *testing.T) {
	for _, a := range Acts {
		if !KnownAct(a) {
			t.Errorf("published act %q not recognised", a)
		}
	}
	// Unlike a tempo reading or a dynamics reading, there is NO honest empty act.
	// An inventory entry states what was done; an entry whose value is "" reads
	// downstream as a real answer nobody can name.
	if KnownAct("") {
		t.Error(`"" must not be a publishable act: an unnamed act is not an act`)
	}
	// Values from the neighbouring vocabularies, and from the `tool`/`exe` levels
	// the act is DERIVED from rather than equal to. A gate that let these through
	// would be publishing the implementation detail vocab.py exists to replace.
	for _, bad := range []string{"Bash", "Read", "git", "pnpm", "attributed", "thin",
		"steered", "review", "converse", "code_generation"} {
		if KnownAct(bad) {
			t.Errorf("%q is not in the published act vocabulary", bad)
		}
	}
}
