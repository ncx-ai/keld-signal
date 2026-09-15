package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// eventEmitter adapts the client-events emitter to the narrow interface the
// integrations package takes. integrations must not depend on the package that
// batches and spools — its tests assert the event, not the transport.
type eventEmitter struct{ e *clientevents.Emitter }

func (a eventEmitter) Emit(code string, fields map[string]any) {
	if a.e == nil {
		return
	}
	a.e.Emit(code, clientevents.SevInfo, fields)
}

// startIntegrationsDetector runs the catalogue poll for the life of ctx.
//
// It is started unconditionally, because LISTING is the product even when
// auto-setup is off (AC-3's second half). The toggle is read live, per tick,
// inside the detector.
func startIntegrationsDetector(ctx context.Context, emitter *clientevents.Emitter) *integrations.Detector {
	d := &integrations.Detector{
		Entries:   integrations.Catalogue,
		AutoSetup: func() bool { return settings.Load().AutoSetupEnabled() },
		Params:    integrationsSetupParams,
		Emit:      eventEmitter{emitter},
		Log:       log.Printf,
	}
	go d.Run(ctx)
	return d
}

// integrationsSetupParams is what the detector writes into a tool's config.
//
// ⚠️ THE DAEMON'S LOOPBACK ADDRESS AND THE LOCAL SECRET — never Atlas's URL and
// never the org ingest token. It is deliberately the same pair
// `internal/cli`'s telemetryTarget resolves; a tool that holds an Atlas
// credential goes stale the moment that credential rotates, and only a human
// restarting the tool recovers it.
func integrationsSetupParams() (tools.SetupParams, error) {
	secret, err := agentcfg.EnsureTelemetrySecret()
	if err != nil {
		return tools.SetupParams{}, err
	}
	return tools.SetupParams{
		Endpoint:    "http://" + teleproxy.Addr(),
		IngestToken: secret,
		BinPath:     keldCLIPath(),
	}, nil
}

// keldCLIPath is the absolute path of the `keld` CLI to pin into a tool's hook
// command.
//
// ⚠️ IT IS THE SIBLING OF THE RUNNING keld-agent, NOT os.Executable() ITSELF.
// The daemon is keld-agent; pinning its own path would write a hook command
// that runs the daemon. Every installer puts the two binaries in one directory
// (`~/.local/bin`, `/usr/local/keld`, the Inno payload), so the sibling is the
// right answer wherever an installer put them.
//
// "" when it cannot be resolved, which the adapters render as a bare `keld` —
// the same fallback `keld signal setup` takes. A bare command is worse (a
// stale keld earlier on PATH can shadow it, which caused a real bug) but it is
// a working hook, and refusing to configure the tool at all is not better.
func keldCLIPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	name := "keld"
	if runtime.GOOS == "windows" {
		name = "keld.exe"
	}
	sibling := filepath.Join(filepath.Dir(exe), name)
	if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
		return sibling
	}
	return ""
}
