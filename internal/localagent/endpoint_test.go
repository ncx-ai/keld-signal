package localagent

import (
	"strings"
	"testing"
)

// ⚠️ A MACHINE CAN BE SIGNED IN TO ONE ATLAS AND REPORTING TO ANOTHER, AND
// NOTHING NOTICED. `auth.json` records the Atlas a person authenticated against;
// `hook.json` records where the daemon actually publishes. They are written by
// different steps, and an upgrade whose tools were already configured used to
// skip the hook write entirely — so the two could disagree indefinitely.
//
// Measured on a real v3.0.0 install (2026-09-16): identity `https://atlas.keld.co`,
// hook `http://localhost:8000` from a dev session the day before, 882 daemon
// calls to localhost against 9 to Atlas. `keld signal status` printed the
// production login, `doctor` reported no problems, and both were telling the
// truth about the half they could see. The defect was that nobody compared them.
func TestEndpointAgreement(t *testing.T) {
	cases := []struct {
		name          string
		identity      string
		hook          string
		wantKnown     bool
		wantAgreement bool
	}{
		{
			name:          "agree",
			identity:      "https://atlas.keld.co",
			hook:          "https://atlas.keld.co",
			wantKnown:     true,
			wantAgreement: true,
		},
		{
			// The measured failure: signed in to production, publishing to a
			// dev stack left behind by an earlier session.
			name:          "disagree",
			identity:      "https://atlas.keld.co",
			hook:          "http://localhost:8000",
			wantKnown:     true,
			wantAgreement: false,
		},
		{
			// ⚠️ NOT LOGGED IN IS NOT A DISAGREEMENT. A daemon configured by a
			// setup code has a hook and no CLI credential; calling that a
			// mismatch would accuse every such machine of a fault it does not
			// have. Silence, not a finding — the same refusal SidecarVersion
			// and ModelState make.
			name:      "no identity",
			identity:  "",
			hook:      "https://atlas.keld.co",
			wantKnown: false,
		},
		{
			// An unconfigured daemon is a normal startup state (the service is
			// registered before onboarding runs), not a mismatch.
			name:      "no hook",
			identity:  "https://atlas.keld.co",
			hook:      "",
			wantKnown: false,
		},
		{
			// Cosmetic difference only. A trailing slash and a case-different
			// host are the SAME destination, and reporting them would train
			// people to ignore this check — which is how a real mismatch then
			// gets ignored too.
			name:          "trailing slash and host case are not a mismatch",
			identity:      "https://Atlas.Keld.co/",
			hook:          "https://atlas.keld.co",
			wantKnown:     true,
			wantAgreement: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EndpointAgreement(tc.identity, tc.hook)
			if got.Known != tc.wantKnown {
				t.Fatalf("Known = %v, want %v", got.Known, tc.wantKnown)
			}
			if !tc.wantKnown {
				return
			}
			if got.Agrees != tc.wantAgreement {
				t.Errorf("Agrees = %v, want %v (identity=%q hook=%q)",
					got.Agrees, tc.wantAgreement, tc.identity, tc.hook)
			}
			if !got.Agrees {
				// The finding has to name BOTH sides. "Your endpoints disagree"
				// sends someone reading config files; naming them is the
				// difference between a one-line fix and an investigation.
				if got.Identity == "" || got.Hook == "" {
					t.Error("a disagreement must report both endpoints")
				}
			}
		})
	}
}

// A finding nobody can act on is noise. This one has to name both destinations
// and the single command that reconciles them — the machine that produced it
// took an archaeology session (agent logs, file mtimes, an install log) to
// diagnose, which is exactly what one line here replaces.
func TestEndpointProblemLine(t *testing.T) {
	line := EndpointAgreement("https://atlas.keld.co", "http://localhost:8000").ProblemLine()
	if line == "" {
		t.Fatal("a disagreement must produce a problem line")
	}
	for _, want := range []string{"https://atlas.keld.co", "http://localhost:8000", "keld signal setup"} {
		if !strings.Contains(line, want) {
			t.Errorf("problem line does not mention %q: %s", want, line)
		}
	}

	// Silence where there is nothing conclusive, and where they agree.
	if l := EndpointAgreement("https://atlas.keld.co", "https://atlas.keld.co").ProblemLine(); l != "" {
		t.Errorf("agreement reported a problem: %s", l)
	}
	if l := EndpointAgreement("", "https://atlas.keld.co").ProblemLine(); l != "" {
		t.Errorf("a machine with no CLI credential was reported as mismatched: %s", l)
	}
	if l := EndpointAgreement("https://atlas.keld.co", "").ProblemLine(); l != "" {
		t.Errorf("an unconfigured daemon was reported as mismatched: %s", l)
	}
}
