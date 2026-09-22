package version

import "testing"

func TestNewerOrdersReleasesAndPreReleases(t *testing.T) {
	for _, c := range []struct {
		a, b         string // is a newer than b?
		newer, known bool
	}{
		{"3.0.1", "3.0.0", true, true},
		{"3.0.0", "3.0.1", false, true},
		{"3.0.0", "3.0.0", false, true},
		{"v3.0.0", "3.0.0", false, true}, // one leading v is not a difference
		{"3.1.0", "3.0.9", true, true},
		{"10.0.0", "9.0.0", true, true}, // numeric, not lexical
		// ⚠️ THE INCIDENT'S OWN SHAPE: the binary shadowing the install was
		// 3.0.0-rc.3. A pre-release is OLDER than the release it leads to, and
		// rc.10 is newer than rc.3.
		{"3.0.0", "3.0.0-rc.3", true, true},
		{"3.0.0-rc.3", "3.0.0", false, true},
		{"3.0.0-rc.10", "3.0.0-rc.3", true, true},
		{"3.0.0-rc.3", "3.0.0-rc.3", false, true},
		{"3.1.0-rc.1", "3.0.0", true, true},
	} {
		newer, known := Newer(c.a, c.b)
		if newer != c.newer || known != c.known {
			t.Errorf("Newer(%q, %q) = (%v, %v), want (%v, %v)", c.a, c.b, newer, known, c.newer, c.known)
		}
	}
}

// ⚠️ "dev" MEANS CANNOT TELL, NEVER "OLDER". It is what a source checkout and
// every local build reports, and a guard that fired on it would fire on every
// developer machine — which is the same as not having a guard, because nobody
// reads a warning they see every time. Same refusal version.Skew makes.
func TestNewerIsUnknownWhenEitherHalfCannotNameItsBuild(t *testing.T) {
	for _, c := range [][2]string{
		{"dev", "3.0.0"}, {"3.0.0", "dev"}, {"", "3.0.0"}, {"3.0.0", ""},
		{"  ", "3.0.0"}, {"not-a-version", "3.0.0"}, {"3.0.0", "3.x"},
	} {
		newer, known := Newer(c[0], c[1])
		if known {
			t.Errorf("Newer(%q, %q) claimed to know", c[0], c[1])
		}
		if newer {
			t.Errorf("Newer(%q, %q) reported an ordering off an unknown half", c[0], c[1])
		}
	}
}
