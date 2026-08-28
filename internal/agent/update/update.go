package update

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Restarter restarts the daemon's OS service. Injected so the whole apply path
// is testable without a service manager.
type Restarter interface{ Restart() error }

// Updater moves this machine to the release Atlas names.
//
// Every field that touches the world is injectable, which is what makes the
// unhappy paths — a failing probe, an unreachable release host, a restart that
// errors — reachable from a test rather than only from the platform that
// exhibits them.
type Updater struct {
	Current   string    // version.CLI
	StatePath string    // ~/.keld/update/state.json
	Dest      Dest      // where the artifacts live
	Fetch     *Fetcher  // download + verify
	Restarter Restarter // the OS service
	Probe     func(bin string) error
	Now       func() time.Time
	Emit      func(code, sev string, fields map[string]any)
	Quiesce   func(ctx context.Context) // wait for the enrichment queue to drain
	// OnMigrate repoints the OS service at execPath. It is called ONLY when
	// the update had to install somewhere other than where it was running —
	// the macOS pkg case. Without it, launchd/systemd would go on starting the
	// old path forever while the update reported success.
	OnMigrate   func(execPath string) error
	MinInterval time.Duration
	GOOS        string
	GOARCH      string

	mu       sync.Mutex
	inFlight bool
	wg       sync.WaitGroup
}

func (u *Updater) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Updater) emit(code, sev string, f map[string]any) {
	if u.Emit != nil {
		u.Emit(code, sev, f)
	}
}

// Wait blocks until any in-flight attempt has finished. Tests only; the daemon
// never waits on an update.
func (u *Updater) Wait() { u.wg.Wait() }

// Maybe is what the settings poll calls, on every tick, forever.
//
// It must be cheap and it must never block that loop: the settings poll is how
// per-org configuration reaches every other subsystem, and a 190 MB download
// on it would stall all of them. So the decision is taken inline (it is a pure
// function over already-loaded state) and only the work is handed to a
// goroutine — single-flighted, because a poll landing during an apply must not
// start a second swap of the same files.
func (u *Updater) Maybe(ctx context.Context, t Target) {
	s, err := LoadState(u.StatePath)
	if err != nil {
		u.emit("update.failed", "warn", map[string]any{"stage": "state", "error": err.Error()})
		return
	}
	d := Decide(u.Current, t, s, u.now(), localDisabled(), u.MinInterval)
	if !d.Act {
		// Always say WHY. A skipped update and a broken one are
		// indistinguishable from outside unless the skip states its reason.
		u.emit("update.skipped", "info", map[string]any{"reason": d.Reason, "target": t.Version, "current": u.Current})
		return
	}

	u.mu.Lock()
	if u.inFlight {
		u.mu.Unlock()
		return
	}
	u.inFlight = true
	u.mu.Unlock()

	u.emit("update.available", "info", map[string]any{"current": u.Current, "target": d.Version})
	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		defer func() {
			u.mu.Lock()
			u.inFlight = false
			u.mu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("keld-agent: update panicked: %v", r)
				u.emit("update.failed", "error", map[string]any{"stage": "panic"})
			}
		}()
		if err := u.apply(ctx, t, d.Version); err != nil {
			log.Printf("keld-agent: update to %s failed: %v", d.Version, err)
		}
	}()
}

// apply is the whole sequence: fetch, verify, extract, probe, swap, record,
// restart. Anything that fails before the swap leaves the machine untouched;
// anything that fails during it is rolled back by Swap.
func (u *Updater) apply(ctx context.Context, t Target, version string) error {
	u.recordAttempt(version)

	stage, err := StageDir(u.Dest.BinDir)
	if err != nil {
		return u.fail(version, "stage", err)
	}
	defer os.RemoveAll(stage)

	cliAsset, scAsset := AssetNames(u.GOOS, u.GOARCH)

	// ── fetch + verify ────────────────────────────────────────────────────
	cliArc := filepath.Join(stage, cliAsset)
	if err := u.Fetch.Fetch(ctx, version, cliAsset, cliArc); err != nil {
		return u.fail(version, "fetch_cli", err)
	}
	cliDir := filepath.Join(stage, "cli")
	if err := extract(cliArc, cliDir); err != nil {
		return u.fail(version, "extract_cli", err)
	}

	var scDir string
	if u.Dest.HasSidecar {
		scArc := filepath.Join(stage, scAsset)
		if err := u.Fetch.Fetch(ctx, version, scAsset, scArc); err != nil {
			return u.fail(version, "fetch_sidecar", err)
		}
		scDir = filepath.Join(stage, "sidecar")
		if err := ExtractTarGz(scArc, scDir); err != nil {
			return u.fail(version, "extract_sidecar", err)
		}
	}

	// ── pre-flight ────────────────────────────────────────────────────────
	// A wrong-architecture binary hashes correctly and cannot run. Finding
	// that out here costs milliseconds; finding it out after the swap costs a
	// restart, a rollback and a second restart.
	agentBin := filepath.Join(cliDir, exeNameFor(u.GOOS, "keld-agent"))
	if err := u.probe(agentBin); err != nil {
		return u.fail(version, "probe", err)
	}

	// ── swap ──────────────────────────────────────────────────────────────
	sw := NewSwap()
	fail := func(stage string, err error) error {
		if rbErr := sw.Rollback(); rbErr != nil {
			// The worst state this package can reach. Never silent.
			log.Printf("keld-agent: UPDATE ROLLBACK FAILED — the install may be inconsistent: %v", rbErr)
			u.emit("update.failed", "error", map[string]any{"stage": "rollback", "error": rbErr.Error(), "target": version})
		}
		return u.fail(version, stage, err)
	}

	if u.Dest.HasSidecar {
		src := filepath.Join(scDir, "keld-agent-sidecar")
		dst := filepath.Join(u.Dest.SidecarDir, "keld-agent-sidecar")
		if !u.Dest.SidecarNested {
			// A flat install (Windows Inno) stays flat: changing shape would
			// break resolution for anything still pointing at the old one.
			src = filepath.Join(src, exeNameFor(u.GOOS, "keld-agent-sidecar"))
		}
		if _, err := os.Stat(src); err != nil {
			return fail("sidecar_layout", fmt.Errorf("release sidecar archive did not contain %s: %w", filepath.Base(src), err))
		}
		if err := sw.Replace(dst, src); err != nil {
			return fail("swap_sidecar", err)
		}
	}
	for _, name := range binaryNamesFor(u.GOOS) {
		src := filepath.Join(cliDir, name)
		if _, err := os.Stat(src); err != nil {
			// keld-agent must be present; a release missing keld alone is not
			// worth abandoning the update over.
			if name == exeNameFor(u.GOOS, "keld-agent") {
				return fail("swap_missing_agent", err)
			}
			continue
		}
		if err := sw.Replace(filepath.Join(u.Dest.BinDir, name), src); err != nil {
			return fail("swap_"+name, err)
		}
	}

	// ── record BEFORE restarting ──────────────────────────────────────────
	// The marker must be on disk before the process can die, or a restart that
	// takes effect instantly leaves nothing for the new binary to confirm.
	s, _ := LoadState(u.StatePath)
	s.From, s.To = u.Current, version
	s.InstallDir = u.Dest.BinDir
	s.Prev = sw.Prev()
	s.PendingConfirm = true
	s.AttemptedAt = u.now()
	s.LastOutcome, s.LastError = "staged", ""
	if err := SaveState(u.StatePath, s); err != nil {
		return fail("state", err)
	}
	u.emit("update.staged", "info", map[string]any{"from": u.Current, "to": version, "install_dir": u.Dest.BinDir})

	// ── repoint the service, if we had to move ────────────────────────────
	// Only on a migration. The service definition names an absolute path, and
	// after a migration that path is the OLD, unwritable install — so a
	// restart without this brings the stale binary straight back up and the
	// confirm pass then rolls the whole update back, correctly and uselessly.
	if u.Dest.Migrated && u.OnMigrate != nil {
		newExe := filepath.Join(u.Dest.BinDir, exeNameFor(u.GOOS, "keld-agent"))
		if err := u.OnMigrate(newExe); err != nil {
			return fail("migrate_service", err)
		}
		u.emit("update.migrated", "warn", map[string]any{
			"from": u.Dest.OrigBinDir, "to": u.Dest.BinDir,
			"note": "the previous install directory was not writable; the service now runs from the new path",
		})
	}

	// ── restart ───────────────────────────────────────────────────────────
	if u.Quiesce != nil {
		u.Quiesce(ctx)
	}
	if u.Restarter == nil {
		return nil
	}
	if err := u.Restarter.Restart(); err != nil {
		// Deliberately NOT resolved: the swap already happened, so the marker
		// stays pending and the next start — by whichever binary the service
		// manager brings up — can still undo it.
		u.emit("update.restart_failed", "error", map[string]any{"to": version, "error": err.Error()})
		return fmt.Errorf("update: restart after staging %s: %w", version, err)
	}
	return nil
}

func (u *Updater) probe(bin string) error {
	if u.Probe != nil {
		return u.Probe(bin)
	}
	return DefaultProbe(bin)
}

// DefaultProbe runs the staged binary's --version. Bounded, because a binary
// that hangs on startup must not hang the update.
func DefaultProbe(bin string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("staged binary failed to run: %w (%s)", err, truncate(string(out), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// recordAttempt stamps the min-interval floor before the work starts, so a
// failure that takes a long time cannot be retried immediately afterwards.
func (u *Updater) recordAttempt(version string) {
	s, _ := LoadState(u.StatePath)
	s.LastAttempt = u.now()
	s.LastTarget = version
	s.LastOutcome = "started"
	s.LastError = ""
	_ = SaveState(u.StatePath, s)
}

func (u *Updater) fail(version, stage string, err error) error {
	s, _ := LoadState(u.StatePath)
	s.LastOutcome = "failed"
	s.LastError = fmt.Sprintf("%s: %v", stage, err)
	s.LastTarget = version
	_ = SaveState(u.StatePath, s)
	u.emit("update.failed", "warn", map[string]any{"stage": stage, "target": version, "error": err.Error()})
	return fmt.Errorf("update %s at %s: %w", version, stage, err)
}

func extract(archive, dest string) error {
	if filepath.Ext(archive) == ".zip" {
		return ExtractZip(archive, dest)
	}
	return ExtractTarGz(archive, dest)
}

// localDisabled reports the machine-local kill switch. Local refusal always
// wins; local permission never does.
func localDisabled() bool {
	switch os.Getenv("KELD_AUTOUPDATE") {
	case "0", "false", "off", "no":
		return true
	}
	return false
}
