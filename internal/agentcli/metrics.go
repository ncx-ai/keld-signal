package agentcli

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/spf13/cobra"
)

// sidecarMetricsURL resolves the running sidecar's /metrics URL from agent.json,
// or explains why it is unreachable.
func sidecarMetricsURL(info *agentcfg.Info) (string, error) {
	if info == nil || info.Port == 0 {
		return "", errors.New("keld-agent is not running")
	}
	if info.SidecarPort == 0 {
		return "", errors.New("sidecar is not running (ML disabled or deterministic backend in use)")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/metrics", info.SidecarPort), nil
}

// fetchText GETs url and returns the body, erroring on a non-200 response. The
// body is capped since /metrics is a small JSON object.
func fetchText(url string) (string, error) {
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

// newMetricsCmd builds `keld-agent metrics`: print the running sidecar's
// /metrics JSON (state, CPU/threads, live RSS, queue, counts) to stdout — the
// same payload as curling the sidecar, but with no port to look up by hand.
func newMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Print the running GLiNER2 sidecar's /metrics JSON to stdout.",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := agentcfg.Read()
			if err != nil {
				return err
			}
			url, err := sidecarMetricsURL(info)
			if err != nil {
				return err
			}
			body, err := fetchText(url)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), body)
			return nil
		},
	}
}
