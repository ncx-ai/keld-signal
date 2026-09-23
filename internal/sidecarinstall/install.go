// Package sidecarinstall downloads, verifies, unpacks and installs the analysis
// sidecar.
//
// ⚠️ IT EXISTS SO THERE IS ONE DEFINITION, NOT TWO. This logic was private to
// internal/cli, reachable only by running the `keld` binary — which is why the
// macOS installer pane spawned a CLI child to fetch 315 MB while a person
// watched a progress bar it could not cancel, and why the daemon (the process
// that actually knows whether the sidecar is missing) could do nothing about it
// at all. Both callers now enter here: the cobra command in internal/cli keeps
// its NDJSON surface, and the daemon's /v1/engine route drives the same
// function for the page.
package sidecarinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/service"
	"github.com/ncx-ai/keld-signal/internal/agent/update"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// The analysis sidecar is ~190MB and the macOS pkg cannot carry it (Apple's
// notary service scans every one of its ~15,000 files). This command is the Go
// owner of "fetch it, verify it, put it in place" — the wizard pane drives it and
// renders the NDJSON, so the installer holds no download logic of its own.
//
// ⚠️ THE MISSING-CHECKSUM POLICY HERE IS THE INSTALLER'S, NOT AUTO-UPDATE'S.
// update.Fetcher.Fetch refuses outright when a release publishes no SHA-256,
// because an unattended swap has no reader who can abort. Here a human is
// watching a progress bar, which is exactly the case scripts/install.sh
// degrades for. So a missing hash is reported and the fetch continues; a
// MISMATCH is always fatal.
type Opts struct {
	BaseURL   string // release download base; empty = the release mirror
	Tag       string // release tag; empty = resolve the latest release
	Dest      string // directory that holds keld-agent-sidecar/; empty = ~/.local/bin
	StageOnly bool
	Progress  func(received, total int64)
	// Warn reports a non-fatal problem during the fetch — currently only the
	// missing-published-hash case. Nil means: print it with console.Print (the
	// human CLI path). The --json path sets this to emit a `warning` NDJSON
	// event instead, because console.Print reaches nobody under --json — see
	// the policy note above.
	Warn func(string)
	// CleanupJob is a launchd plist to delete once the install SUCCEEDS.
	//
	// ⚠️ macOS NAMES THE BACKGROUND ITEM AFTER THE PROGRAM IT RUNS, AND SHOWS
	// THAT TO THE PERSON INSTALLING. The fallback fetch used to run from a
	// generated shell script, so macOS announced "'.sidecar-fetch.sh' can run
	// in the background" — a dot-prefixed script inside a log directory,
	// presented to someone who just wanted to install Keld (reported from a
	// real install, 2026-09-16). The job now runs this signed binary directly,
	// which is what macOS names instead, and launchd handles the logging.
	//
	// Deleting the plist is the one thing the shell wrapper did that the
	// binary must take over: without it the job re-runs a ~190MB download at
	// every login. Only on success — on failure the job is the ONLY thing that
	// will try again, and removing it would leave a stale sidecar with nothing
	// scheduled to fix it.
	CleanupJob string
	// Restart replaces RestartAfterSwap for this one install.
	//
	// ⚠️ THE DAEMON MUST NOT BOUNCE THE SERVICE IT IS RUNNING INSIDE. The CLI
	// is a short-lived process installing for somebody else, so restarting the
	// whole service is right there. The daemon is not: it IS the service, so
	// the default kills the very process doing the install — measured on a real
	// machine 2026-09-22, where the engine landed correctly and the daemon then
	// took itself down mid-commit and did not come back, leaving the page
	// polling a dead port and frozen on "Updating… 100%". It only ever needed
	// the SIDECAR restarted, which it supervises directly.
	//
	// Nil keeps RestartAfterSwap, so every existing caller is unchanged.
	Restart func() error
}

type Result struct {
	StagedPath string // set by StageOnly
	Path       string // set by a full install/commit
	Version    string
	// Restarted says the service was bounced so the RUNNING sidecar is the one
	// just installed; RestartErr says why it was not. A swap with neither is
	// not a thing the type can express, which is the point — see
	// RestartAfterSwap.
	Restarted  bool
	RestartErr string
}

// RestartAfterSwap bounces the local service so it respawns the sidecar
// from the tree that was just installed. A var so tests can observe it without
// touching the machine they run on.
//
// ⚠️ A NEW SIDECAR ON DISK IS NOT A NEW SIDECAR RUNNING, and on a real v3.0.1
// install the gap was five seconds in the wrong order:
//
//	08:41:26  daemon starts, spawns the sidecar   (postinstall: keld-agent install)
//	08:41:31  sidecar tree replaced on disk (v3.0.1)
//	08:41:32  sidecar binary written
//
// postinstall backgrounds the ~190MB fetch on purpose — it must not block the
// install — and restarts the daemon on its own schedule, so the daemon spawned
// the OLD image and held it. `doctor` then reported version skew on a machine
// whose disk was entirely correct, and a manual restart cleared it at once.
// An installer is supposed to replace AND restart; this is the second half.
var RestartAfterSwap = service.Restart

// DestDir is where the macOS pkg and scripts/install.sh both put the
// sidecar: a user-writable directory that sidecarBinPath() already searches, so
// no sudo prompt and no extra configuration.
func DestDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// LatestTag resolves the newest published release. Used only when no tag
// is supplied — a real pkg always supplies its own version, so this is the
// dry-run path (VERSION reads 0.0.0-dryrun, which is not a release).
func LatestTag(ctx context.Context, api string) (string, error) {
	if api == "" {
		api = update.DefaultMirrorURL + "/latest.json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", err
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	if body.TagName == "" {
		return "", fmt.Errorf("release lookup returned no tag_name")
	}
	return body.TagName, nil
}

// installSidecar downloads + verifies + extracts. With StageOnly it stops there
// and returns the staged path; otherwise it commits into Dest.
func Install(opts Opts) (Result, error) {
	var res Result
	dest := opts.Dest
	if dest == "" {
		d, err := DestDir()
		if err != nil {
			return res, err
		}
		dest = d
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return res, err
	}
	tag := opts.Tag
	if tag == "" {
		tag = DefaultTag()
	}
	if tag == "" {
		t, err := LatestTag(context.Background(), "")
		if err != nil {
			return res, err
		}
		tag = t
	}
	_, asset := update.AssetNames(runtime.GOOS, runtime.GOARCH)

	// Stage INSIDE dest so the commit is a same-filesystem rename rather than a
	// cross-device copy of ~15,000 files (update.StageDir documents this).
	stage, err := update.StageDir(dest)
	if err != nil {
		return res, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(stage)
		}
	}()

	f := &update.Fetcher{BaseURL: opts.BaseURL, Progress: opts.Progress}
	archive := filepath.Join(stage, asset)
	if err := f.Fetch(context.Background(), tag, asset, archive); err != nil {
		// A release with no published hash is a warning here, not a refusal —
		// see the policy note at the top of this file. Matched on the sentinel,
		// never a substring of Fetch's error text: a future reword of that
		// message must not silently flip this policy in either direction.
		if !errors.Is(err, update.ErrNoPublishedHash) {
			return res, err
		}
		msg := "no published SHA-256 for " + asset + "; skipping integrity check"
		if opts.Warn != nil {
			opts.Warn(msg)
		} else {
			console.Print("  ! " + msg)
		}
		if err := f.FetchUnverified(context.Background(), tag, asset, archive); err != nil {
			return res, err
		}
	}
	if err := update.ExtractTarGz(archive, stage); err != nil {
		return res, err
	}
	_ = os.Remove(archive)
	tree := filepath.Join(stage, "keld-agent-sidecar")
	if fi, err := os.Stat(tree); err != nil || !fi.IsDir() {
		return res, fmt.Errorf("sidecar archive had an unexpected layout (no keld-agent-sidecar/ at its root)")
	}
	res.Version = ReadVersion(tree)
	if opts.StageOnly {
		cleanup = false
		res.StagedPath = stage
		return res, nil
	}
	r, err := commit(stage, dest, opts)
	if err != nil {
		return res, err
	}
	cleanupJobPlist(opts.CleanupJob)
	return r, nil
}

// cleanupJobPlist removes a one-shot launchd plist. Best-effort: the install
// itself has already succeeded by this point, and a plist that cannot be
// deleted costs a redundant fetch at next login, not a broken machine.
//
// It deliberately does NOT `launchctl bootout` the label. That kills the very
// process doing the cleanup — measured on a real install, where the shell
// version logged exit=0 and left both its files on disk. A RunAtLoad job with no
// KeepAlive is finished when its program exits, and with the plist gone nothing
// loads it again.
func cleanupJobPlist(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// Commit moves a staged tree into place and removes the staging dir.
func Commit(staged, dest string) (Result, error) { return commit(staged, dest, Opts{}) }

func commit(staged, dest string, opts Opts) (Result, error) {
	var res Result
	tree := filepath.Join(staged, "keld-agent-sidecar")
	if fi, err := os.Stat(tree); err != nil || !fi.IsDir() {
		return res, fmt.Errorf("no staged sidecar at %s", tree)
	}
	// Cheap pre-flight before committing: a checksum-valid but wrong-arch or
	// badly-built tarball can still unpack into a tree with no runnable binary
	// at all. Catching that here — one os.Stat — is what keeps a bad release
	// from ever displacing a working install; Replace has no way to notice
	// this on its own, since a directory rename succeeds regardless of what's
	// inside it.
	bin := filepath.Join(tree, "keld-agent-sidecar")
	fi, err := os.Stat(bin)
	if err != nil {
		return res, fmt.Errorf("staged sidecar has no binary at %s: %w", bin, err)
	}
	if fi.IsDir() {
		return res, fmt.Errorf("staged sidecar binary at %s is a directory, not a file", bin)
	}
	if fi.Mode()&0o111 == 0 {
		return res, fmt.Errorf("staged sidecar binary at %s is not executable", bin)
	}
	res.Version = ReadVersion(tree)
	target := filepath.Join(dest, "keld-agent-sidecar")
	sw := update.NewSwap()
	if err := sw.Replace(target, tree); err != nil {
		return res, err
	}
	sw.Commit()
	_ = os.RemoveAll(staged)
	res.Path = target

	// ⚠️ BEST EFFORT, AND DELIBERATELY NOT AN ERROR. By this point the new
	// sidecar is on disk and verified; all that is missing is a respawn, which
	// the next daemon start performs anyway and which doctor's skew check
	// reports in the meantime. Failing here would discard a completed ~190MB
	// install over a recoverable condition — and on postinstall's background
	// path nobody is reading the exit code at all.
	if err := opts.restartFn()(); err != nil {
		res.RestartErr = err.Error()
	} else {
		res.Restarted = true
	}
	return res, nil
}

// restartFn is the restart this install should perform: the caller's override
// when it gave one, else the package default. See Opts.Restart.
func (o Opts) restartFn() func() error {
	if o.Restart != nil {
		return o.Restart
	}
	return RestartAfterSwap
}

// DefaultTag is the release this binary belongs to — the tag every caller
// should fetch unless told otherwise.
//
// ⚠️ "LATEST" IS NOT "MINE", AND THE DIFFERENCE HAS NOW DOWNGRADED A MACHINE
// TWICE. An empty tag used to resolve `releases/latest`, which by definition
// EXCLUDES pre-releases — so on any `-rc.N` machine it answers the last stable.
// Measured 2026-09-22 on a 3.0.5-rc.6 daemon: the engine auto-updated to rc.6
// correctly at 16:30, and a bare `keld signal install-sidecar` at 16:37 put
// v3.0.4 over the top of it. The daemon route had already been pinned after the
// first occurrence; the CLI had not, which is what a rule living at ONE caller
// instead of at the shared function buys you.
//
// Empty on a source build ("dev"), which names no release: there the caller
// falls through to LatestTag, the only answer available.
func DefaultTag() string {
	v := version.Normalize(version.CLI)
	if v == "" || v == version.Unknown {
		return ""
	}
	return "v" + v
}

func ReadVersion(tree string) string {
	b, err := os.ReadFile(filepath.Join(tree, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// NewProgressThrottle collapses bursty byte-level progress updates to
// one call per percentage point, so a ~190MB download does not emit hundreds
// of thousands of NDJSON lines. When total is indeterminate (<=0 — no
// Content-Length on the response; Fetcher.Progress's doc comment states this
// is exactly when total is -1) there is no percentage to dedupe on, so every
// call is forwarded unthrottled: the wizard pane renders an indeterminate bar
// from raw byte counts instead. That branch must never fall through to the
// percentage comparison below it — a naive "-1 means unset" sentinel collides
// with the indeterminate percentage itself and silently drops every event for
// the whole transfer.
func NewProgressThrottle(emit func(received, total int64)) func(received, total int64) {
	var lastPct atomic.Int64
	lastPct.Store(-1)
	return func(received, total int64) {
		if total <= 0 {
			emit(received, total)
			return
		}
		pct := received * 100 / total
		if pct == lastPct.Load() {
			return
		}
		lastPct.Store(pct)
		emit(received, total)
	}
}
