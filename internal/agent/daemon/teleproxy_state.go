package daemon

import (
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
)

// The running proxy, held so `keld-agent status` can report when telemetry last
// reached Atlas — the fact `keld signal doctor` needs to tell "configured and
// flowing" from "configured and silent", and which does not exist anywhere else:
// pre-proxy, tool telemetry went straight to Atlas and the client kept no record
// of it at all.
var (
	teleMu sync.Mutex
	tele   *teleproxy.Proxy
)

func setTelemetryProxy(p *teleproxy.Proxy) {
	teleMu.Lock()
	tele = p
	teleMu.Unlock()
}

// TelemetryLastForward reports the instant telemetry last reached Atlas, and
// whether that is KNOWN at all. Not known means the proxy is not running on this
// machine — a direct-push install, or a failed bind — and an unknown answer must
// never be reported as a problem.
func TelemetryLastForward() (time.Time, bool) {
	teleMu.Lock()
	p := tele
	teleMu.Unlock()
	if p == nil {
		return time.Time{}, false
	}
	return p.LastForward(), true
}
