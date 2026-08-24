package daemon

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/hook"
)

// defaultConfigPoll is how often an unconfigured daemon re-reads hook.json.
// 5s is imperceptible to a human finishing `keld login` in another window and
// costs one tiny file read per interval.
const defaultConfigPoll = 5 * time.Second

// configPollInterval resolves the unconfigured-idle poll cadence from
// KELD_CONFIG_POLL. A missing, unparseable or non-positive value falls back to
// defaultConfigPoll — a zero interval would spin the loop at full tilt.
func configPollInterval() time.Duration {
	if v := os.Getenv("KELD_CONFIG_POLL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultConfigPoll
}

// awaitConfig blocks until load reports a hook config carrying BOTH an endpoint
// and an ingest token, then returns it. It returns ctx.Err() if the context is
// cancelled first (a clean shutdown while idling).
//
// Why idle rather than exit: "not configured" is not a crash. On the documented
// macOS flow the pkg registers the service BEFORE onboarding runs, so the daemon
// is legitimately configuration-less on first boot. Exiting non-zero there made
// launchd's KeepAlive respawn it every ~10s — a tester logged 69 spawns in the
// 12 minutes before they signed in — and on Linux systemd's start-limit burst
// instead left the unit permanently `failed`, so the daemon stayed dead even
// after onboarding finished. Idling fixes both ends: no spawn churn, and the
// daemon starts working the moment `keld signal setup` writes hook.json, with no
// restart needed.
//
// onWait is invoked exactly once, on the first poll that finds no config, so the
// wait is announced without one log line per poll forever. Both an empty config
// and a read error count as "not configured yet": hook.json can be missing,
// half-written, or unreadable, and all three resolve themselves once setup runs.
func awaitConfig(ctx context.Context, load func() (*hook.Config, error), poll time.Duration, onWait func()) (*hook.Config, error) {
	announced := false
	for {
		cfg, err := load()
		if err != nil {
			log.Printf("keld-agent: hook config read error: %v", err)
		}
		if cfg != nil && cfg.Endpoint != "" && cfg.IngestToken != "" {
			return cfg, nil
		}
		if !announced {
			announced = true
			onWait()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}
