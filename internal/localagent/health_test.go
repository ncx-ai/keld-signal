package localagent

import (
	"errors"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

func okStatus() (string, error) { return "active", nil }

func TestHealthDaemonDown(t *testing.T) {
	h := Health(nil, okStatus, func(string) (string, error) { return "", nil })
	if h.Service != "active" || h.DaemonUp {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthDeterministicWhenNoSidecarPort(t *testing.T) {
	h := Health(&agentcfg.Info{Port: 8765}, okStatus, func(string) (string, error) {
		t.Fatal("fetch should not be called without a sidecar port")
		return "", nil
	})
	if !h.DaemonUp || h.Backend != "deterministic (ML disabled)" || h.MetricsOK {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthSidecarUnreachable(t *testing.T) {
	h := Health(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, okStatus,
		func(string) (string, error) { return "", errors.New("connection refused") })
	if h.Backend != "sidecar unreachable" || h.MetricsOK {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthSidecarLoaded(t *testing.T) {
	body := `{"model_state":"loaded","memory":{"rss_mb":2743.1,"model_cost_mb":2650.1}}`
	h := Health(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, okStatus,
		func(string) (string, error) { return body, nil })
	if !h.MetricsOK || h.Backend != "GLiNER2 sidecar" || h.ModelState != "loaded" ||
		h.RSSMB != 2743.1 || h.ModelCostMB != 2650.1 {
		t.Fatalf("got %+v", h)
	}
}
