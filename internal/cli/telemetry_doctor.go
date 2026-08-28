package cli

import (
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// telemetryState assembles what doctor knows about telemetry actually reaching
// Atlas.
//
// The credential's write time is hook.json's mtime: that file is rewritten
// whenever the daemon re-onboards, which is the exact instant every running
// tool's in-memory copy went stale.
func telemetryState(manifest *config.Manifest) localagent.TelemetryState {
	s := localagent.TelemetryState{Now: time.Now()}
	if manifest != nil && len(manifest.Tools) > 0 {
		s.Configured = true
	}
	if fi, err := os.Stat(paths.HookConfigPath()); err == nil {
		s.HookWritten = fi.ModTime()
	}
	s.LastForward, s.Known = teleproxy.LastForwardOnDisk()
	return s
}
