package cli

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
)

// TelemetryTarget is where AI tools are told to send their OTLP, and the
// credential they authenticate with.
type TelemetryTarget struct {
	Endpoint string
	Secret   string
}

// telemetryTarget resolves what `keld signal setup` writes into every tool's
// configuration.
//
// ⚠️ IT IS THE DAEMON'S LOOPBACK ADDRESS AND A LOCAL SECRET — never Atlas's URL
// and never the org ingest token. That substitution is the whole point of the
// telemetry proxy: tools read their configuration once, at startup, and keep it
// in memory, so a tool holding an Atlas credential goes stale the moment that
// credential rotates and only restarting the tool recovers it. Measured on a
// real machine, telemetry died for 40 minutes and `keld signal doctor` reported
// no problems throughout — correctly, because the stale copy lived inside a
// process it cannot inspect.
//
// The daemon holds the Atlas credential instead, reads it per request, and
// already self-heals it on a 401.
func telemetryTarget() (TelemetryTarget, error) {
	secret, err := agentcfg.EnsureTelemetrySecret()
	if err != nil {
		return TelemetryTarget{}, fmt.Errorf("telemetry secret: %w", err)
	}
	return TelemetryTarget{
		Endpoint: "http://" + teleproxy.Addr(),
		Secret:   secret,
	}, nil
}
