package cli

import (
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
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
	s.Configured = configuredForToolTelemetry(manifest)
	if fi, err := os.Stat(paths.HookConfigPath()); err == nil {
		s.HookWritten = fi.ModTime()
	}
	s.LastForward, s.Known = teleproxy.LastForwardOnDisk()
	return s
}

// configuredForToolTelemetry is the `Configured` both telemetry findings turn
// on, and it asks what that field's own contract says: are the tools set up to
// send telemetry AT ALL.
//
// ⚠️ **THE MANIFEST ALONE IS NOT THAT QUESTION ANY MORE.** It records that keld
// configured a tool, and since the tool's OTLP export went behind a Developer
// switch (settings.Settings.ToolOTLP, off by default) keld configures tools
// without writing any OTEL block. A machine that HAD the lane on is the one
// that breaks: its proxy record is non-empty and frozen, so the machine-wide
// check sees nothing arriving since the credential was written and the
// per-session check sees every running session missing from the record. Both
// would then tell a person to restart their editor to repair a lane that was
// switched off on purpose — advice that cannot work, on every poll, forever.
//
// Asked per call rather than latched, like every other reader of this switch.
func configuredForToolTelemetry(manifest *config.Manifest) bool {
	if manifest == nil || len(manifest.Tools) == 0 {
		return false
	}
	return settings.Load().ToolOTLPEnabled()
}
