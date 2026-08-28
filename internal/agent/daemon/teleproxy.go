package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// telemetryDrainInterval is how often spooled OTLP batches are retried. The same
// order as the client-events sweep: this is latency on already-durable data, and
// a tighter loop buys nothing while Atlas is unreachable.
const telemetryDrainInterval = time.Minute

// startTelemetryProxy binds the fixed loopback telemetry port and serves the
// OTLP receiver until ctx ends.
//
// ⚠️ A PORT IT CANNOT BIND IS AN ERROR, NEVER A WARNING. `keld signal setup`
// writes this address into every AI tool's config, so a daemon that shrugged and
// carried on would leave every tool posting into a socket nobody is listening
// on — telemetry silently going nowhere, which is the exact failure this whole
// path exists to remove. The realistic cause is a second daemon: a developer
// running `keld-agent run` beside the installed service is ordinary, and this is
// the failure mode a FIXED port introduces that the ephemeral ingress never had.
func startTelemetryProxy(ctx context.Context, emitter *clientevents.Emitter,
	ingestEndpoint string, token func() string, onAuthRejection func()) (*teleproxy.Proxy, error) {

	addr := teleproxy.Addr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("telemetry listener on %s: %w "+
			"(another daemon, or another process, already holds it; "+
			"set %s to move it)", addr, err, teleproxy.EnvPort)
	}

	secret, err := agentcfg.EnsureTelemetrySecret()
	if err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("telemetry secret: %w", err)
	}
	p := teleproxy.New(logsEndpoint(ingestEndpoint), metricsEndpoint(ingestEndpoint),
		token, secret, paths.TelemetrySpoolDir())
	if onAuthRejection != nil {
		p.OnAuthRejection(onAuthRejection)
	}

	// Arm the doctor check NOW, not on the first successful forward: the machine
	// this check exists for is one whose tools were never restarted, which will
	// never forward at all. See teleproxy.MarkRunning.
	if err := teleproxy.MarkRunning(); err != nil {
		log.Printf("keld-agent: telemetry state not recorded (doctor cannot judge telemetry): %v", err)
	}

	srv := &http.Server{Handler: p.Handler()}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
			log.Printf("keld-agent: telemetry listener stopped: %v", err)
		}
	}()
	// Drain spooled batches on a timer. A laptop that was offline, or whose token
	// was rotated while it slept, catches up here — and the drain's own rules
	// (stop on a REJECTION, end the sweep on UNAVAILABLE, continue past a
	// REFUSED payload) are what keep that from becoming a burst.
	go func() {
		t := time.NewTicker(telemetryDrainInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.DrainSpools(ctx)
			}
		}
	}()
	log.Printf("keld-agent: telemetry proxy on %s — AI tools post here and hold no Atlas credential", addr)
	if emitter != nil {
		emitter.EmitExempt("telemetry.proxy_start", clientevents.SevInfo,
			map[string]any{"addr": addr})
	}
	return p, nil
}
