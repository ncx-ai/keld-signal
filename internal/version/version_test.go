package version

import "testing"

func TestSkewIdentityAfterOneLeadingV(t *testing.T) {
	for _, c := range []struct {
		daemon, sidecar string
		skewed, known   bool
	}{
		{"2.3.0", "2.3.0", false, true},
		{"v2.3.0", "2.3.0", false, true},   // one leading v is not a difference
		{"2.3.0", "v2.3.0", false, true},   // …in either direction
		{" v2.3.0 ", "2.3.0", false, true}, // nor is surrounding space
		{"2.3.0", "2.2.1", true, true},
		{"2.3.0", "0.21.0", true, true}, // a DOWNGRADE is skew too, not just a lag
	} {
		skewed, known := Skew(c.daemon, c.sidecar)
		if skewed != c.skewed || known != c.known {
			t.Errorf("Skew(%q, %q) = (%v, %v), want (%v, %v)",
				c.daemon, c.sidecar, skewed, known, c.skewed, c.known)
		}
	}
}

// AC-9. A dev half means the comparison cannot conclude anything, and reporting
// it as skew nags every developer about a problem they do not have.
func TestSkewIsUnknownWhenEitherHalfCannotNameItsBuild(t *testing.T) {
	for _, c := range [][2]string{
		{"dev", "2.3.0"},
		{"2.3.0", "dev"},
		{"dev", "dev"},
		{"", "2.3.0"},
		{"2.3.0", ""},
		{"  ", "2.3.0"},
	} {
		skewed, known := Skew(c[0], c[1])
		if known {
			t.Errorf("Skew(%q, %q) claimed to know; an unknown half must report nothing", c[0], c[1])
		}
		if skewed {
			t.Errorf("Skew(%q, %q) reported skew off an unknown half", c[0], c[1])
		}
	}
}

// The sidecar spells its fallback in Python (sidecar/app/buildversion.py's
// UNKNOWN); if the two ever drift, every release machine reports skew against
// a sidecar that is merely unstamped.
func TestUnknownMatchesTheSidecarsFallbackSpelling(t *testing.T) {
	if Unknown != "dev" {
		t.Fatalf("Unknown = %q; sidecar/app/buildversion.py answers \"dev\"", Unknown)
	}
}
