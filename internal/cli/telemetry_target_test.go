package cli

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// THE ACTUAL REGRESSION TEST FOR THE ORIGINAL BUG, and it belongs HERE rather
// than in internal/telemetry: the per-tool writers were never at fault — they
// faithfully wrote whatever endpoint and token they were handed. The defect was
// in what setup HANDED them (ob.Endpoint / ob.IngestToken), so a test of the
// writers would have passed before the fix and proved nothing.
func TestSetupTargetsTheDaemonAndNeverAtlas(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	got, err := telemetryTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Endpoint, "http://127.0.0.1:") {
		t.Fatalf("endpoint = %q, want the daemon's loopback address", got.Endpoint)
	}
	if got.Secret == "" {
		t.Fatal("no telemetry secret")
	}

	// It must be the STABLE local secret, not anything Atlas issued.
	want, err := agentcfg.EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != want {
		t.Fatalf("secret = %q, want the stable local secret %q", got.Secret, want)
	}
}

// A second call must return the same credential, or every re-run of setup would
// invalidate the configs written by the previous one.
func TestSetupTargetIsStableAcrossRuns(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	first, err := telemetryTarget()
	if err != nil {
		t.Fatal(err)
	}
	second, err := telemetryTarget()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("target changed between runs: %+v -> %+v", first, second)
	}
}

// The port override moves the endpoint, so an operator who had to relocate the
// listener gets tools pointed at the same place the daemon bound.
func TestSetupTargetHonoursThePortOverride(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_TELEMETRY_PORT", "15999")
	got, err := telemetryTarget()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Endpoint, "15999") {
		t.Fatalf("endpoint = %q, want the overridden port", got.Endpoint)
	}
}

// The runtime invariant: the one place both credentials are in scope refuses to
// let the Atlas token through to a tool. This is what a future refactor would
// trip over, and it is the guard the writer-level tests cannot provide — those
// faithfully write whatever they are handed and would have passed before the fix.
func TestRunSetupRefusesToWriteTheAtlasTokenIntoAToolConfig(t *testing.T) {
	ob := &api.Onboarding{Endpoint: "https://atlas.keld.co/v1/traces", IngestToken: "ATLAS-ORG-TOKEN"}
	p := tools.SetupParams{Endpoint: "https://atlas.keld.co/v1/traces", IngestToken: "ATLAS-ORG-TOKEN"}

	_, err := runSetup(nil, p, nil, ob, SetupOpts{Yes: true})
	if err == nil {
		t.Fatal("runSetup accepted the Atlas ingest token as a tool credential")
	}
	if !strings.Contains(err.Error(), "Atlas ingest token") {
		t.Fatalf("error does not name the problem: %v", err)
	}
}

// And the legitimate combination is accepted: the same Onboarding, but the tool
// params carrying the LOCAL secret.
func TestRunSetupAcceptsTheLocalSecret(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	ob := &api.Onboarding{Endpoint: "https://atlas.keld.co/v1/traces", IngestToken: "ATLAS-ORG-TOKEN"}
	tp, err := telemetryTarget()
	if err != nil {
		t.Fatal(err)
	}
	p := tools.SetupParams{Endpoint: tp.Endpoint, IngestToken: tp.Secret}

	if _, err := runSetup(nil, p, nil, ob, SetupOpts{Yes: true}); err != nil &&
		strings.Contains(err.Error(), "Atlas ingest token") {
		t.Fatalf("the local secret was rejected as if it were Atlas's: %v", err)
	}
}
