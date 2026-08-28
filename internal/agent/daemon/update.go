package daemon

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/service"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/update"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// updateMinInterval bounds how often an update may be ATTEMPTED, independent
// of how often settings are polled. The settings poll is the trigger, and a
// fast KELD_SETTINGS_POLL must not mean a fast update loop.
func updateMinInterval() time.Duration {
	return envDuration("KELD_UPDATE_MIN_INTERVAL", time.Hour)
}

// updateConfirmDeadline bounds how long an update may stay unproven before it
// is undone. Past it, a new binary that never cleared its own marker is read
// as having crashed on startup.
func updateConfirmDeadline() time.Duration {
	return envDuration("KELD_UPDATE_CONFIRM_DEADLINE", update.DefaultConfirmDeadline)
}

// envDuration parses a KELD_* duration, falling back to def on anything
// unparseable or non-positive — the same shape as the KELD_SETTINGS_POLL parse
// in daemon.go.
func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// serviceRestarter adapts the OS service package to update.Restarter.
type serviceRestarter struct{}

func (serviceRestarter) Restart() error { return service.Restart() }

// bufferedEvents collects client-events raised BEFORE the emitter exists.
//
// The confirm pass has to run at the very top of Run — before awaitConfig,
// which blocks indefinitely on a machine that has not been onboarded. A daemon
// idling there still needs a bad update undone. But the emitter is built from
// the config that awaitConfig is waiting for, so the events have nowhere to go
// yet; they are buffered and replayed once it exists.
type bufferedEvents struct {
	ev []bufferedEvent
}

type bufferedEvent struct {
	code   string
	sev    string
	fields map[string]any
}

func (b *bufferedEvents) emit(code, sev string, fields map[string]any) {
	b.ev = append(b.ev, bufferedEvent{code: code, sev: sev, fields: fields})
}

// replay forwards the buffered events to the real emitter.
func (b *bufferedEvents) replay(e *clientevents.Emitter) {
	if e == nil {
		return
	}
	for _, ev := range b.ev {
		e.EmitExempt(ev.code, clientevents.Severity(ev.sev), ev.fields)
	}
	b.ev = nil
}

// confirmPendingUpdate resolves any update left in flight by a previous
// process. It runs before anything else in Run, including awaitConfig.
func confirmPendingUpdate(emit func(string, string, map[string]any)) {
	err := update.Confirm(
		paths.UpdateStatePath(),
		version.CLI,
		time.Now(),
		updateConfirmDeadline(),
		serviceRestarter{},
		emit,
	)
	if err != nil {
		log.Printf("keld-agent: %v", err)
	}
}

// resolveDest works out where this machine's artifacts live and whether we can
// replace them.
//
// The binaries are located from the RUNNING process, never guessed. When that
// directory is not writable — the macOS pkg stages to a root-owned
// /usr/local/keld — the update migrates to ~/.local/bin, which is
// user-writable and is already on sidecarBinPath()'s well-known list.
func resolveDest() (update.Dest, bool) {
	exe, err := os.Executable()
	if err != nil {
		return update.Dest{}, false
	}
	binDir := filepath.Dir(exe)
	d := update.Dest{BinDir: binDir, Writable: update.Writable(binDir)}

	if scBin, ok := sidecarBinPath(); ok {
		scDir := filepath.Dir(scBin)
		nested := filepath.Base(scDir) == "keld-agent-sidecar"
		if nested {
			scDir = filepath.Dir(scDir)
		}
		d.SidecarDir, d.SidecarNested, d.HasSidecar = scDir, nested, true
	}

	if !d.Writable {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return d, false
		}
		target := update.MigrationTarget(home)
		if err := os.MkdirAll(target, 0o755); err != nil || !update.Writable(target) {
			return d, false
		}
		d.OrigBinDir, d.Migrated = binDir, true
		d.BinDir = target
		d.Writable = true
		// The sidecar follows the binaries only when its own home is
		// unwritable too; a pkg machine already keeps it in ~/.local/bin
		// (onboard.command puts it there), so it usually does not move.
		if d.HasSidecar && !update.Writable(d.SidecarDir) {
			d.SidecarDir = target
		}
	}
	return d, true
}

// newUpdater builds the machine's updater, or reports that this machine cannot
// auto-update (no resolvable, writable destination).
func newUpdater(emit func(string, string, map[string]any)) (*update.Updater, bool) {
	dest, ok := resolveDest()
	if !ok {
		return nil, false
	}
	goos, goarch := update.HostOSArch()
	return &update.Updater{
		Current:     version.CLI,
		StatePath:   paths.UpdateStatePath(),
		Dest:        dest,
		Fetch:       &update.Fetcher{},
		Restarter:   serviceRestarter{},
		Now:         time.Now,
		Emit:        emit,
		MinInterval: updateMinInterval(),
		GOOS:        goos,
		GOARCH:      goarch,
		OnMigrate:   service.InstallAt,
	}, true
}

// updateTargetFrom reads the org's target release off a settings poll. A nil
// block is not an update: see settings/release.go.
func updateTargetFrom(r *settings.Remote) update.Target {
	if r == nil {
		return update.Target{}
	}
	v, base, enabled := r.Release.Target()
	return update.Target{Version: v, BaseURL: base, Enabled: enabled}
}

// quiesceFn returns a bounded wait for the enrichment queue to drain, so a
// restart does not land in the middle of a job. The spool makes a mid-flight
// restart survivable, not free — so this is a courtesy with a deadline, never
// a condition for updating.
func quiesceFn(depth func() int) func(context.Context) {
	return func(ctx context.Context) {
		if depth == nil {
			return
		}
		deadline := time.After(30 * time.Second)
		t := time.NewTicker(200 * time.Millisecond)
		defer t.Stop()
		for {
			if depth() == 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-deadline:
				log.Printf("keld-agent: update restarting with %d job(s) still queued; they are spooled and will be retried", depth())
				return
			case <-t.C:
			}
		}
	}
}

// updateStatePathForTest exposes the resolved marker path so a test can assert
// it stays inside KELD_HOME. No test in this repo may write the developer's
// real ~/.keld.
func updateStatePathForTest() string { return paths.UpdateStatePath() }
