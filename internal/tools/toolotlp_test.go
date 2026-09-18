package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// otlpMarker is the one string in each tool's config that only keld's OTLP
// wiring puts there. Asserted on the TEXT rather than on a parse, because what
// matters is whether the tool will read an exporter out of that file.
var otlpMarker = map[string]string{
	"claude_code": "OTEL_EXPORTER_OTLP_ENDPOINT",
	"codex":       "[otel]",
	"gemini":      "otlpEndpoint",
}

func adaptersUnderTest(t *testing.T) []Adapter {
	t.Helper()
	// Every adapter resolves its config path through HOME at call time, so one
	// temp dir isolates all three.
	t.Setenv("HOME", t.TempDir())
	return All()
}

func params(on bool) SetupParams {
	return SetupParams{
		Endpoint:    "http://127.0.0.1:14318",
		IngestToken: "local-secret",
		BinPath:     "/home/u/.local/bin/keld",
		ToolOTLP:    on,
	}
}

// ⚠️ OFF MEANS NOTHING IS WRITTEN — and the hook is untouched. The hook lane
// carries no credential and is how a prompt pointer reaches the daemon; only
// the tool's own OTLP export is behind the switch.
func TestApplyWritesNoOTLPBlockWhileTheSwitchIsOff(t *testing.T) {
	for _, a := range adaptersUnderTest(t) {
		plan := a.Apply(nil, params(false), false)
		if plan.Conflict != "" {
			t.Fatalf("%s: unexpected conflict %q", a.Name(), plan.Conflict)
		}
		if strings.Contains(plan.AfterText, otlpMarker[a.Name()]) {
			t.Errorf("%s: wrote %q with the switch off:\n%s", a.Name(), otlpMarker[a.Name()], plan.AfterText)
		}
		if !strings.Contains(plan.AfterText, telemetry.HookCommandSubstr) {
			t.Errorf("%s: the hook is gone too; only the OTLP lane is behind the switch:\n%s", a.Name(), plan.AfterText)
		}
		// And the tool still counts as configured, or the detector rewrites it
		// forever and doctor reports drift on a healthy machine.
		st := a.Status(&plan.AfterText, plan.Managed)
		if !st.Configured {
			t.Errorf("%s: a config with the hook and no OTLP block reads as NOT configured", a.Name())
		}
		if st.OTLP {
			t.Errorf("%s: Status reports the OTLP lane present when nothing wrote it", a.Name())
		}
	}
}

func TestApplyWritesTheOTLPBlockWhileTheSwitchIsOn(t *testing.T) {
	for _, a := range adaptersUnderTest(t) {
		plan := a.Apply(nil, params(true), false)
		if !strings.Contains(plan.AfterText, otlpMarker[a.Name()]) {
			t.Errorf("%s: the switch is on and %q was not written:\n%s", a.Name(), otlpMarker[a.Name()], plan.AfterText)
		}
		st := a.Status(&plan.AfterText, plan.Managed)
		if !st.Configured || !st.OTLP {
			t.Errorf("%s: Status = {configured:%v otlp:%v}, want both true", a.Name(), st.Configured, st.OTLP)
		}
	}
}

// The upgrade case: a machine configured by an earlier keld carries the block,
// and the next apply must TAKE IT OUT rather than leave a lane nobody asked for
// pointing at a credential.
func TestApplyRemovesAnExistingOTLPBlockWhenTheSwitchGoesOff(t *testing.T) {
	for _, a := range adaptersUnderTest(t) {
		with := a.Apply(nil, params(true), false).AfterText
		if !strings.Contains(with, otlpMarker[a.Name()]) {
			t.Fatalf("%s: fixture did not carry the block to begin with", a.Name())
		}
		off := a.Apply(&with, params(false), false)
		if !off.Changed {
			t.Errorf("%s: apply reported no change over a config that still carries the block", a.Name())
		}
		if strings.Contains(off.AfterText, otlpMarker[a.Name()]) {
			t.Errorf("%s: the block survived:\n%s", a.Name(), off.AfterText)
		}
		if !strings.Contains(off.AfterText, telemetry.HookCommandSubstr) {
			t.Errorf("%s: removing the block took the hook with it:\n%s", a.Name(), off.AfterText)
		}
	}
}

// ⚠️ **THE ANTI-REWRITE PIN.** The detector re-applies a configured tool when
// the config disagrees with the switch, so a second apply that still reported a
// change would rewrite every tool's config every minute forever — with a backup
// each time. Both positions, because the loop is available in both directions.
func TestApplyIsIdempotentInEitherPosition(t *testing.T) {
	for _, a := range adaptersUnderTest(t) {
		for _, on := range []bool{false, true} {
			first := a.Apply(nil, params(on), false).AfterText
			second := a.Apply(&first, params(on), false)
			if second.Changed {
				t.Errorf("%s (tool_otlp=%v): a second apply still reports a change:\n--- first\n%s\n--- second\n%s",
					a.Name(), on, first, second.AfterText)
			}
			if second.AfterText != first {
				t.Errorf("%s (tool_otlp=%v): a second apply produced different text", a.Name(), on)
			}
		}
	}
}

// OTLPOnDisk is the question the detector asks before deciding to re-apply, and
// it must answer about the FILE rather than about the manifest: the manifest
// records that keld configured the tool, not which lanes it wrote.
func TestOTLPOnDiskReadsTheFile(t *testing.T) {
	for _, a := range adaptersUnderTest(t) {
		path := a.ConfigPath()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, on := range []bool{true, false} {
			plan := a.Apply(ReadConfig(a), params(on), false)
			if err := os.WriteFile(path, []byte(plan.AfterText), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := OTLPOnDisk(a, plan.Managed); got != on {
				t.Errorf("%s: OTLPOnDisk = %v after applying with tool_otlp=%v", a.Name(), got, on)
			}
		}
	}
}
