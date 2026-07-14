// Package localagent is the client-side access layer for the local keld-agent
// daemon + GLiNER2 sidecar. Both keld (internal/cli) and keld-agent
// (internal/agentcli) call it in-process to run test enrichments, read the
// sidecar's /metrics, and summarize local-service health.
package localagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// ReadPrompt returns the prompt from args (joined) or, if none, from stdin.
func ReadPrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return "", errors.New("no prompt: pass text as an argument or on stdin")
	}
	return text, nil
}

// ResolveModel picks the enrichment backend and a human note naming it. It
// uses the running sidecar (via agent.json) when available; there is no
// deterministic fallback (the deterministic backend has been removed — ML is
// mandatory), so when the sidecar is not running (or forceDeterministic is
// requested, no longer supported) it returns a clear error instead of a
// degraded Model. Removing forceDeterministic entirely + the --deterministic
// flag on its callers is tracked separately; this keeps the seam compiling
// with its previous behavior gone.
func ResolveModel(info *agentcfg.Info, forceDeterministic bool) (enrich.Model, string, error) {
	if forceDeterministic {
		return nil, "", errors.New("--deterministic is no longer supported: the deterministic backend has been removed (ML is mandatory)")
	}
	if info != nil && info.SidecarPort != 0 {
		url := fmt.Sprintf("http://127.0.0.1:%d", info.SidecarPort)
		// Generous per-call timeout: the pipeline issues up to 7 sidecar calls
		// and CPU inference can be slow on a busy host.
		return sidecar.New(url, 30*time.Second), "using live GLiNER2 sidecar at " + url, nil
	}
	return nil, "", errors.New("sidecar not running — start keld-agent / wait for provisioning")
}

// MetricsURL resolves the running sidecar's /metrics URL from agent.json.
func MetricsURL(info *agentcfg.Info) (string, error) {
	if info == nil || info.Port == 0 {
		return "", errors.New("keld-agent is not running")
	}
	if info.SidecarPort == 0 {
		return "", errors.New("sidecar is not running (ML disabled, or not yet provisioned/ready)")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/metrics", info.SidecarPort), nil
}

// FetchText GETs url and returns the body, erroring on a non-200 response.
func FetchText(url string) (string, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sidecar returned HTTP %d", resp.StatusCode)
	}
	return string(b), nil
}

// Metrics reads agent.json and returns the sidecar's /metrics body.
func Metrics(info *agentcfg.Info) (string, error) {
	url, err := MetricsURL(info)
	if err != nil {
		return "", err
	}
	return FetchText(url)
}

// EnrichJSON runs the enrichment pipeline on text with the given model and
// returns the profile as indented JSON. Local only; never publishes.
func EnrichJSON(text, source string, model enrich.Model) (string, error) {
	cwd, _ := os.Getwd()
	meta := enrich.Meta{Repo: cwd, Tool: source}
	profile := enrich.Run(text, source, meta, model)
	b, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
