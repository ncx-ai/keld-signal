package cli

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/localagent"
)

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestRenderLocalServiceDaemonDown(t *testing.T) {
	out := joined(renderLocalService(localagent.HealthInfo{Service: "not running", DaemonUp: false}))
	if !strings.Contains(out, "Local signal service:") || !strings.Contains(out, "service") ||
		!strings.Contains(out, "not running") || !strings.Contains(out, "daemon") {
		t.Fatalf("got:\n%s", out)
	}
	if strings.Contains(out, "backend") || strings.Contains(out, "memory") {
		t.Fatalf("should omit backend and memory when daemon down:\n%s", out)
	}
}

func TestRenderLocalServiceDeterministic(t *testing.T) {
	out := joined(renderLocalService(localagent.HealthInfo{
		Service: "active", DaemonUp: true, Backend: "deterministic (ML disabled)",
	}))
	if !strings.Contains(out, "deterministic (ML disabled)") {
		t.Fatalf("got:\n%s", out)
	}
	if strings.Contains(out, "memory") {
		t.Fatalf("should omit memory without metrics:\n%s", out)
	}
}

func TestRenderLocalServiceLoaded(t *testing.T) {
	out := joined(renderLocalService(localagent.HealthInfo{
		Service: "active", DaemonUp: true, Backend: "GLiNER2 sidecar",
		ModelState: "loaded", RSSMB: 2743.1, ModelCostMB: 2650.1, MetricsOK: true,
	}))
	for _, want := range []string{"GLiNER2 sidecar", "loaded", "rss 2743 MB", "model 2650"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
