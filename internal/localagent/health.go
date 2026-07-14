package localagent

import (
	"encoding/json"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

// HealthInfo is a snapshot of the local signal service for the status view.
type HealthInfo struct {
	Service     string // OS service state ("active", "not running", …)
	DaemonUp    bool   // agent.json present with a port
	Backend     string // "GLiNER2 sidecar" | "disabled (ML off)" | "sidecar unreachable"
	ModelState  string // "loaded" / "evicted" / … when the sidecar answered
	RSSMB       float64
	ModelCostMB float64
	MetricsOK   bool
}

type metricsPayload struct {
	ModelState string `json:"model_state"`
	Memory     struct {
		RSSMB       float64 `json:"rss_mb"`
		ModelCostMB float64 `json:"model_cost_mb"`
	} `json:"memory"`
}

// Health combines the OS service state (statusFn) with a parse of the sidecar
// /metrics (fetchFn). Dependencies are injected for testability. All fields are
// best-effort; a failing statusFn yields "unknown".
func Health(info *agentcfg.Info, statusFn func() (string, error), fetchFn func(string) (string, error)) HealthInfo {
	h := HealthInfo{Service: "unknown"}
	if s, err := statusFn(); err == nil && s != "" {
		h.Service = s
	}
	if info == nil || info.Port == 0 {
		return h
	}
	h.DaemonUp = true
	if info.SidecarPort == 0 {
		h.Backend = "disabled (ML off)"
		return h
	}
	body, err := fetchFn(MetricsURLNoErr(info))
	if err != nil {
		h.Backend = "sidecar unreachable"
		return h
	}
	var p metricsPayload
	if json.Unmarshal([]byte(body), &p) != nil {
		h.Backend = "sidecar unreachable"
		return h
	}
	h.Backend = "GLiNER2 sidecar"
	h.ModelState = p.ModelState
	h.RSSMB = p.Memory.RSSMB
	h.ModelCostMB = p.Memory.ModelCostMB
	h.MetricsOK = true
	return h
}

// MetricsURLNoErr returns the sidecar /metrics URL; caller guarantees a nonzero
// SidecarPort (Health checks it first).
func MetricsURLNoErr(info *agentcfg.Info) string {
	u, _ := MetricsURL(info)
	return u
}
