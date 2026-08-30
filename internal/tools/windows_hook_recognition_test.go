package tools

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// winBin is the shape the Windows installer pins: an absolute path ending in
// keld.exe. Everything below turns on that ".exe", which is why no test carrying
// a Unix path could ever have caught these.
const winBin = `C:\Users\KingR\AppData\Local\Programs\keld\keld.exe`

func winParams() SetupParams {
	return SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "t0ken", BinPath: winBin}
}

// THE DRIFT BUG. `keld signal doctor` reported "manifest records setup but config
// is not configured" for a Windows install that was configured and actively
// sending telemetry — because Status asked whether a keld hook was present using
// a recognizer that could not match a keld.exe command. Setup then said "already
// configured" (AddClaudeHook is idempotent, so nothing changed), leaving the user
// with a finding that re-running the suggested command could never clear.
func TestWindowsInstallReportsItselfConfigured(t *testing.T) {
	for _, a := range []Adapter{&ClaudeAdapter{}, &GeminiAdapter{}} {
		t.Run(a.Name(), func(t *testing.T) {
			plan := a.Apply(nil, winParams(), false)
			if plan.Conflict != "" {
				t.Fatalf("unexpected conflict: %s", plan.Conflict)
			}
			if !strings.Contains(plan.AfterText, winBin[len(winBin)-len(`keld.exe`):]) {
				t.Fatalf("the pinned windows binary is not in the written config:\n%s", plan.AfterText)
			}
			st := a.Status(&plan.AfterText, plan.Managed)
			if !st.Configured {
				t.Fatalf("a config this adapter JUST WROTE reports as not configured — "+
					"doctor would call a healthy install drifted:\n%s", plan.AfterText)
			}
		})
	}
}

// THE PARTIAL-UNINSTALL BUG. Teardown prefers the hook_substr recorded in the
// manifest, and every manifest written before this fix records the Unix-only
// "keld __hook". Correcting the constant does not reach those machines, so
// teardown must also strip by the current recognizer or their hooks survive
// every future uninstall.
func TestUninstallCleansAMachineWhoseManifestHoldsTheOldRecognizer(t *testing.T) {
	for _, a := range []Adapter{&ClaudeAdapter{}, &GeminiAdapter{}} {
		t.Run(a.Name(), func(t *testing.T) {
			plan := a.Apply(nil, winParams(), false)
			stale := map[string]any{}
			for k, v := range plan.Managed {
				stale[k] = v
			}
			stale["hook_substr"] = "keld __hook" // what an existing manifest records

			out := a.Remove(&plan.AfterText, stale)
			if strings.Contains(out.AfterText, "__hook --source ") {
				t.Fatalf("keld hooks survived uninstall on a machine with an old manifest:\n%s", out.AfterText)
			}
		})
	}
}

// And a current manifest must still tear down cleanly — the two-recognizer pass
// must not depend on the recorded value differing.
func TestUninstallCleansAMachineWithACurrentManifest(t *testing.T) {
	for _, a := range []Adapter{&ClaudeAdapter{}, &GeminiAdapter{}} {
		t.Run(a.Name(), func(t *testing.T) {
			plan := a.Apply(nil, winParams(), false)
			if got := plan.Managed["hook_substr"]; got != telemetry.HookCommandSubstr {
				t.Fatalf("manifest records %q, want the current recognizer %q", got, telemetry.HookCommandSubstr)
			}
			out := a.Remove(&plan.AfterText, plan.Managed)
			if strings.Contains(out.AfterText, "__hook --source ") {
				t.Fatalf("keld hooks survived uninstall:\n%s", out.AfterText)
			}
		})
	}
}

// A moved binary must not leave its old hook behind beside the new one. Apply
// strips keld's existing hooks before re-adding precisely for this, and on
// Windows the strip matched nothing, so the old command stayed.
func TestReinstallToADifferentPathDoesNotDuplicateHooks(t *testing.T) {
	for _, a := range []Adapter{&ClaudeAdapter{}, &GeminiAdapter{}} {
		t.Run(a.Name(), func(t *testing.T) {
			first := a.Apply(nil, winParams(), false)
			moved := winParams()
			moved.BinPath = `D:\keld\keld.exe`
			second := a.Apply(&first.AfterText, moved, false)
			if n := strings.Count(second.AfterText, "__hook --source "); n != strings.Count(first.AfterText, "__hook --source ") {
				t.Fatalf("hook count changed from %d to %d after a path move — the old command was not stripped:\n%s",
					strings.Count(first.AfterText, "__hook --source "), n, second.AfterText)
			}
			if strings.Contains(second.AfterText, winBin) {
				t.Fatalf("the OLD pinned path survived alongside the new one:\n%s", second.AfterText)
			}
		})
	}
}
