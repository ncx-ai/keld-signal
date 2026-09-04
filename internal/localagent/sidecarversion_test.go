package localagent

import (
	"errors"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/version"
)

func withCLIVersion(t *testing.T, v string) {
	t.Helper()
	old := version.CLI
	version.CLI = v
	t.Cleanup(func() { version.CLI = old })
}

func upInfo() *agentcfg.Info { return &agentcfg.Info{Port: 1234, SidecarPort: 5678} }

func fetching(body string) func(string) (string, error) {
	return func(string) (string, error) { return body, nil }
}

// AC-7. The two halves disagree: one line naming both versions and a remedy
// that can actually work.
func TestDoctorSidecarVersionSkew(t *testing.T) {
	withCLIVersion(t, "2.3.0")

	st := SidecarVersion(upInfo(), fetching(`{"ok":true,"state":"down","version":"2.2.1"}`))
	line := st.ProblemLine()
	if line == "" {
		t.Fatal("no problem line for a 2.3.0 keld against a 2.2.1 sidecar — this is exactly " +
			"the state doctor called clean for three weeks")
	}
	if !strings.Contains(line, "2.3.0") || !strings.Contains(line, "2.2.1") {
		t.Errorf("the line must name BOTH versions: %q", line)
	}
	if !strings.Contains(line, "onboard.command") {
		t.Errorf("the remedy must be the installer — `keld signal restart` cannot fix the "+
			"wrong artifact being on disk: %q", line)
	}
}

// The halves agree: silent. A per-run "all is well" line is the noise that
// trains people to stop reading this command.
func TestDoctorSaysNothingWhenTheHalvesAgree(t *testing.T) {
	withCLIVersion(t, "2.3.0")
	for _, body := range []string{
		`{"ok":true,"version":"2.3.0"}`,
		`{"ok":true,"version":"v2.3.0"}`, // one leading v is not a disagreement
	} {
		if l := SidecarVersion(upInfo(), fetching(body)).ProblemLine(); l != "" {
			t.Errorf("%s -> %q, want silence", body, l)
		}
	}
}

// AC-7's second half, and the one that keeps this command trustworthy: when the
// check CANNOT CONCLUDE it reports nothing rather than guessing. An unreachable
// sidecar is usually a stopped daemon; calling that version skew would send
// someone to reinstall a sidecar that is perfectly current.
func TestDoctorDegradesSilentlyWhenItCannotTell(t *testing.T) {
	withCLIVersion(t, "2.3.0")
	unreachable := func(string) (string, error) { return "", errors.New("connection refused") }

	cases := map[string]SidecarVersionState{
		"sidecar unreachable":     SidecarVersion(upInfo(), unreachable),
		"sidecar answers garbage": SidecarVersion(upInfo(), fetching("not json")),
		"sidecar predates the field": SidecarVersion(upInfo(),
			fetching(`{"ok":true,"state":"down"}`)),
		"sidecar is a dev build": SidecarVersion(upInfo(),
			fetching(`{"ok":true,"version":"dev"}`)),
		"daemon is not running": SidecarVersion(nil, fetching(`{"version":"2.2.1"}`)),
		"no sidecar this run":   SidecarVersion(&agentcfg.Info{Port: 1234}, fetching(`{"version":"2.2.1"}`)),
	}
	for name, st := range cases {
		if st.Known {
			t.Errorf("%s: claimed a conclusive comparison", name)
		}
		if l := st.ProblemLine(); l != "" {
			t.Errorf("%s: invented a problem: %q", name, l)
		}
	}
}

// A dev CLI must not report skew either — that is every developer's machine,
// and a check that fires there is one nobody reads where it matters.
func TestDoctorSaysNothingForADevCLI(t *testing.T) {
	withCLIVersion(t, "dev")
	st := SidecarVersion(upInfo(), fetching(`{"ok":true,"version":"2.2.1"}`))
	if st.Known {
		t.Error("a dev CLI cannot conclude anything about a release sidecar")
	}
	if l := st.ProblemLine(); l != "" {
		t.Errorf("reported %q on a dev build", l)
	}
}

func TestHealthURLIsTheSidecarsHealthRoute(t *testing.T) {
	got, err := HealthURL(upInfo())
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:5678/health" {
		t.Fatalf("HealthURL = %q", got)
	}
	if _, err := HealthURL(nil); err == nil {
		t.Error("HealthURL must refuse when there is no daemon, so the caller reports " +
			"nothing rather than probing a made-up port")
	}
}
