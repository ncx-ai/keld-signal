package daemon

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
)

// ⚠️ A SECOND DAEMON MUST FAIL LOUDLY, not silently steal the port or silently
// lose telemetry. `keld signal setup` writes this address into every AI tool's
// config, so a daemon that shrugged at a bind failure would leave every tool
// posting into a socket nobody is listening on. A developer running
// `keld-agent run` beside the installed service is ordinary on an engineer's
// laptop, and this is the failure mode a FIXED port introduces that the
// ephemeral ingress never had.
func TestASecondDaemonReportsThePortRatherThanStealingIt(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv(teleproxy.EnvPort, port)

	p, err := startTelemetryProxy(context.Background(), nil, "https://atlas.example/v1/traces",
		func() string { return "tok" }, nil)
	if err == nil {
		t.Fatal("a taken port was accepted; the daemon would point tools at a listener it does not own")
	}
	if p != nil {
		t.Fatal("a failed bind returned a proxy")
	}
	// The message must name the port and the way out, or an operator cannot act.
	if !strings.Contains(err.Error(), port) || !strings.Contains(err.Error(), teleproxy.EnvPort) {
		t.Fatalf("error does not name the port and the override: %v", err)
	}
}

// The happy path binds, serves, and stops with the context.
func TestTelemetryProxyServesAndStops(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(free.Addr().String())
	free.Close()
	t.Setenv(teleproxy.EnvPort, port)

	ctx, cancel := context.WithCancel(context.Background())
	p, err := startTelemetryProxy(ctx, nil, "https://atlas.example/v1/traces",
		func() string { return "tok" }, nil)
	if err != nil {
		t.Fatalf("startTelemetryProxy: %v", err)
	}
	if p == nil {
		t.Fatal("nil proxy on success")
	}
	conn, err := net.Dial("tcp", teleproxy.Addr())
	if err != nil {
		t.Fatalf("listener not accepting: %v", err)
	}
	conn.Close()

	cancel()
	p.WaitIdle()
}
