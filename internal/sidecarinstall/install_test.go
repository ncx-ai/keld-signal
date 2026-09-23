package sidecarinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/ncx-ai/keld-signal/internal/version"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSidecarTarball builds a minimal keld-agent-sidecar tree: the nested layout
// the macOS/Linux installers use, with a VERSION file at its root (written by
// sidecar/build-freeze.sh) and an executable.
func fakeSidecarTarball(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string]string{
		"keld-agent-sidecar/VERSION":            version + "\n",
		"keld-agent-sidecar/keld-agent-sidecar": "#!/bin/sh\necho sidecar\n",
	}
	for name, body := range files {
		mode := int64(0o644)
		if strings.HasSuffix(name, "/keld-agent-sidecar") {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fakeReleaseServer(t *testing.T, tarball []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(tarball)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			fmt.Fprintf(w, "%s  sidecar.tar.gz\n", hex.EncodeToString(sum[:]))
		default:
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(tarball)
		}
	}))
}

func TestInstallSidecarStageOnlyLeavesExistingInstallUntouched(t *testing.T) {
	srv := fakeReleaseServer(t, fakeSidecarTarball(t, "9.9.9"))
	defer srv.Close()

	dest := t.TempDir()
	// An existing sidecar that must NOT be disturbed by a staging run.
	existing := filepath.Join(dest, "keld-agent-sidecar")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "VERSION"), []byte("1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true,
	})
	if err != nil {
		t.Fatalf("installSidecar: %v", err)
	}
	if res.StagedPath == "" {
		t.Fatal("stage-only returned no staged path")
	}
	if !strings.HasPrefix(res.StagedPath, dest) {
		t.Fatalf("staged outside dest (a cross-device rename): %s", res.StagedPath)
	}
	got, err := os.ReadFile(filepath.Join(existing, "VERSION"))
	if err != nil || strings.TrimSpace(string(got)) != "1.1.1" {
		t.Fatalf("stage-only disturbed the installed sidecar: %q %v", got, err)
	}
}

func TestInstallSidecarCommitReplacesInstalledTree(t *testing.T) {
	srv := fakeReleaseServer(t, fakeSidecarTarball(t, "9.9.9"))
	defer srv.Close()
	dest := t.TempDir()

	staged, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true,
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	res, err := Commit(staged.StagedPath, dest)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if res.Version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", res.Version)
	}
	v, err := os.ReadFile(filepath.Join(dest, "keld-agent-sidecar", "VERSION"))
	if err != nil || strings.TrimSpace(string(v)) != "9.9.9" {
		t.Fatalf("installed VERSION = %q %v", v, err)
	}
	if _, err := os.Stat(staged.StagedPath); !os.IsNotExist(err) {
		t.Fatalf("staging dir still present after commit: %v", err)
	}
}

func TestInstallSidecarChecksumMismatchInstallsNothing(t *testing.T) {
	tarball := fakeSidecarTarball(t, "9.9.9")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			fmt.Fprintln(w, "0000000000000000000000000000000000000000000000000000000000000000  sidecar.tar.gz")
			return
		}
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()
	dest := t.TempDir()

	if _, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true,
	}); err == nil {
		t.Fatal("checksum mismatch must be fatal")
	}
	// Assert directly rather than by scanning for non-dot entries: StageDir
	// creates staging dirs as ".keld-update.*", so that scan is vacuous — it
	// passes whether or not the staging directory leaked.
	if _, err := os.Stat(filepath.Join(dest, "keld-agent-sidecar")); !os.IsNotExist(err) {
		t.Fatalf("mismatch installed a sidecar tree: stat err = %v", err)
	}
	leftover, err := filepath.Glob(filepath.Join(dest, ".keld-update.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftover) != 0 {
		t.Fatalf("mismatch left staging litter behind: %v", leftover)
	}
}

func TestInstallSidecarMissingPublishedHashStillInstalls(t *testing.T) {
	tarball := fakeSidecarTarball(t, "9.9.9")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No .sha256 published — auto-update REFUSES here (unattended, no reader
		// who could abort). The installer has a human watching a progress bar,
		// which is exactly the case scripts/install.sh degrades for, so it warns
		// and continues.
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	dest := t.TempDir()
	res, err := Install(Opts{BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest})
	if err != nil {
		t.Fatalf("a missing published hash must not be fatal for the installer: %v", err)
	}
	if res.Version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", res.Version)
	}
}

// TestInstallSidecarMissingPublishedHashWarnsViaCallback pins the --json fix:
// console.Print writes to the same stream as the NDJSON events, and the macOS
// wizard pane drops any line it can't parse as one — so under --json the
// missing-hash warning reached nobody. opts.Warn is what the --json path sets
// so the warning becomes an event the pane can actually render instead.
func TestInstallSidecarMissingPublishedHashWarnsViaCallback(t *testing.T) {
	tarball := fakeSidecarTarball(t, "9.9.9")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	var warnings []string
	dest := t.TempDir()
	_, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest,
		Warn: func(msg string) { warnings = append(warnings, msg) },
	})
	if err != nil {
		t.Fatalf("a missing published hash must not be fatal for the installer: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one Warn call, got %v", warnings)
	}
	if !strings.Contains(warnings[0], "no published SHA-256") {
		t.Fatalf("warning message = %q, want it to name the missing hash", warnings[0])
	}
}

// fakeSidecarTarballNoBinary builds a tree with a VERSION file but no
// keld-agent-sidecar binary at all — the shape a checksum-valid but
// wrong-arch or badly-built release tarball would have: it unpacks cleanly
// and there is nothing runnable inside it.
func fakeSidecarTarballNoBinary(t *testing.T, version string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := version + "\n"
	if err := tw.WriteHeader(&tar.Header{Name: "keld-agent-sidecar/VERSION", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCommitStagedSidecarRefusesATreeWithNoBinary(t *testing.T) {
	srv := fakeReleaseServer(t, fakeSidecarTarballNoBinary(t, "9.9.9"))
	defer srv.Close()
	dest := t.TempDir()

	// A pre-existing installed sidecar that must survive a refused commit.
	existing := filepath.Join(dest, "keld-agent-sidecar")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "VERSION"), []byte("1.1.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	staged, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true,
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	if _, err := Commit(staged.StagedPath, dest); err == nil {
		t.Fatal("commit must refuse a staged tree with no sidecar binary")
	}

	got, err := os.ReadFile(filepath.Join(existing, "VERSION"))
	if err != nil || strings.TrimSpace(string(got)) != "1.1.1" {
		t.Fatalf("a refused commit disturbed the installed sidecar: %q %v", got, err)
	}
}

// TestSidecarProgressThrottleForwardsEveryIndeterminateCall pins Finding 1: a
// naive "-1 means nothing emitted yet" sentinel collides with total == -1
// (Fetcher.Progress's documented value when the server sent no
// Content-Length), so every call for the whole indeterminate transfer was
// silently dropped. The indeterminate branch must never be throttled.
func TestSidecarProgressThrottleForwardsEveryIndeterminateCall(t *testing.T) {
	var calls []int64
	throttle := NewProgressThrottle(func(received, total int64) {
		calls = append(calls, received)
	})
	throttle(10, -1)
	throttle(20, -1)
	throttle(30, -1)
	if len(calls) != 3 {
		t.Fatalf("indeterminate progress calls must never be throttled: got %d calls, want 3 (%v)", len(calls), calls)
	}
}

// TestSidecarProgressThrottleDedupesByPercent pins the determinate half of the
// same function: repeated calls landing on the same percentage collapse to
// one, and a new percentage always gets through.
func TestSidecarProgressThrottleDedupesByPercent(t *testing.T) {
	var calls []int64
	throttle := NewProgressThrottle(func(received, total int64) {
		calls = append(calls, received)
	})
	throttle(0, 1000)   // 0%
	throttle(1, 1000)   // still 0% -> deduped
	throttle(10, 1000)  // 1% -> new
	throttle(500, 1000) // 50% -> new
	if len(calls) != 3 {
		t.Fatalf("want 3 calls (one per distinct percent), got %d: %v", len(calls), calls)
	}
	if calls[0] != 0 || calls[1] != 10 || calls[2] != 500 {
		t.Fatalf("unexpected sequence: %v", calls)
	}
}

// TestInstallSidecarProgressReportsIndeterminateTotal drives the real
// installSidecar path — not just the throttle unit — against a server that
// sends no Content-Length, and confirms the raw Progress callback set on
// Opts (there was no test at all setting it before this) fires
// at least once and reports total == -1, matching Fetcher.Progress's contract.
func TestInstallSidecarProgressReportsIndeterminateTotal(t *testing.T) {
	tarball := fakeSidecarTarball(t, "9.9.9")
	sum := sha256.Sum256(tarball)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			fmt.Fprintf(w, "%s  sidecar.tar.gz\n", hex.EncodeToString(sum[:]))
			return
		}
		// Flushing before the body is written forces chunked transfer
		// encoding, so no Content-Length header is ever sent — the case
		// Fetcher.Progress documents as total == -1.
		w.(http.Flusher).Flush()
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	dest := t.TempDir()
	var calls int
	sawIndeterminate := false
	_, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true,
		Progress: func(received, total int64) {
			calls++
			if total == -1 {
				sawIndeterminate = true
			}
		},
	})
	if err != nil {
		t.Fatalf("installSidecar: %v", err)
	}
	if calls == 0 {
		t.Fatal("Progress callback never fired")
	}
	if !sawIndeterminate {
		t.Fatal("Progress callback never reported total == -1 for a Content-Length-less download")
	}
}

// ⚠️ THE DEFAULT RESTART IS NEUTRALISED FOR THE WHOLE PACKAGE, because
// commitStagedSidecar now bounces the local service and SEVERAL tests here reach
// it — TestInstallSidecar…, the commit tests, anything driving a full install.
// Left alone, `go test ./...` would `launchctl bootout`/`systemctl --user
// restart` the real agent on a developer's machine and on the CI runner: a test
// that mutates the machine it runs on is a worse defect than the one it checks
// for, which this repo has already paid for once (teleproxy writing a real
// ~/.keld — see AGENTS.md).
//
// Tests that care about the restart override this var themselves and restore it
// with t.Cleanup.
func TestMain(m *testing.M) {
	RestartAfterSwap = func() error { return nil }
	os.Exit(m.Run())
}

// stageFakeSidecar builds the shape commitStagedSidecar expects: a staging dir
// holding keld-agent-sidecar/ with a VERSION file and an executable binary.
func stageFakeSidecar(t *testing.T, version string) string {
	t.Helper()
	stage := t.TempDir()
	tree := filepath.Join(stage, "keld-agent-sidecar")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "VERSION"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "keld-agent-sidecar"), []byte("#!/bin/sh\necho sidecar\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return stage
}

// ⚠️ SWAPPING THE TREE ON DISK DOES NOT CHANGE THE SIDECAR THAT IS RUNNING, and
// for one install the two were five seconds apart in the wrong order.
//
// Measured on a real v3.0.1 install (2026-09-16):
//
//	08:41:26  daemon starts, spawns the sidecar   (postinstall: keld-agent install)
//	08:41:31  sidecar tree replaced on disk (v3.0.1)
//	08:41:32  sidecar binary written
//
// postinstall backgrounds the ~190MB fetch deliberately — it must not block the
// install — and then restarts the daemon on its own schedule. So the daemon
// spawned the OLD image and kept it, and `doctor` reported version skew on a
// machine whose disk was entirely correct. A manual `keld-agent restart` cleared
// it instantly, which is the whole diagnosis: only the running process was stale.
//
// The restart therefore lives in commitStagedSidecar, beside the swap itself,
// rather than in either caller: a swap that does not restart is the defect, so
// the two must not be separable by adding a third call site.
func TestCommitRestartsTheServiceSoTheRunningSidecarIsTheOneOnDisk(t *testing.T) {
	restarts := 0
	orig := RestartAfterSwap
	RestartAfterSwap = func() error { restarts++; return nil }
	t.Cleanup(func() { RestartAfterSwap = orig })

	staged := stageFakeSidecar(t, "v9.9.9")
	dest := t.TempDir()

	res, err := Commit(staged, dest)
	if err != nil {
		t.Fatalf("commitStagedSidecar: %v", err)
	}
	if restarts != 1 {
		t.Errorf("service restarted %d times, want exactly 1 — a swapped tree that nothing respawns leaves the old sidecar running", restarts)
	}
	if !res.Restarted {
		t.Error("result does not report the restart, so no caller can tell whether the running sidecar is current")
	}
}

// ⚠️ A FAILED RESTART MUST NOT FAIL THE INSTALL. The new sidecar is already on
// disk and correct at that point; the only thing missing is a respawn, which the
// next daemon start does anyway and which `doctor`'s skew check reports in the
// meantime. Turning that into an install failure would discard a completed,
// verified ~190MB download over a recoverable condition — and on the background
// path there is nobody watching the exit code at all.
func TestCommitSurvivesARestartFailure(t *testing.T) {
	orig := RestartAfterSwap
	RestartAfterSwap = func() error { return errors.New("launchctl: no such service") }
	t.Cleanup(func() { RestartAfterSwap = orig })

	staged := stageFakeSidecar(t, "v9.9.9")
	dest := t.TempDir()

	res, err := Commit(staged, dest)
	if err != nil {
		t.Fatalf("a restart failure must not fail the commit: %v", err)
	}
	if res.Path == "" {
		t.Error("the sidecar was installed but the result does not say where")
	}
	if res.Restarted {
		t.Error("a failed restart is reported as having happened")
	}
	if res.RestartErr == "" {
		t.Error("a failed restart is silent — the caller cannot say the running sidecar may be stale")
	}
}

// Staging is not installing: --stage-only leaves the running sidecar exactly
// where it was, so restarting anything there would bounce the service for a tree
// that has not been put in place yet. The macOS pane stages DURING the wizard,
// minutes before postinstall commits.
func TestStageOnlyDoesNotRestartTheService(t *testing.T) {
	restarts := 0
	orig := RestartAfterSwap
	RestartAfterSwap = func() error { restarts++; return nil }
	t.Cleanup(func() { RestartAfterSwap = orig })

	srv := fakeReleaseServer(t, fakeSidecarTarball(t, "v9.9.9"))
	defer srv.Close()

	if _, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: t.TempDir(), StageOnly: true,
	}); err != nil {
		t.Fatalf("Install(stage-only): %v", err)
	}
	if restarts != 0 {
		t.Errorf("staging restarted the service %d times; nothing was installed yet", restarts)
	}
}

// ⚠️ THE LAUNCHD JOB MUST NOT BE A SHELL SCRIPT, BECAUSE macOS SHOWS ITS NAME TO
// THE PERSON INSTALLING. The fallback fetch ran from
// ~/.keld/logs/.sidecar-fetch.sh, and macOS announced "'.sidecar-fetch.sh' can
// run in the background" — a dot-prefixed script in a log directory, presented
// to someone who just wanted to install Keld. Reported from a real install,
// 2026-09-16.
//
// So the job runs the signed `keld` binary directly, which is what macOS then
// names, and launchd does the logging through StandardOutPath. That leaves one
// thing the shell used to do: delete the plist, without which the job re-runs a
// ~190MB download at every login. --cleanup-job is that, moved into the binary
// where it can be tested.
func TestCleanupJobRemovesThePlistAfterASuccessfulInstall(t *testing.T) {
	plist := filepath.Join(t.TempDir(), "co.keld.sidecar-fetch.plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := fakeReleaseServer(t, fakeSidecarTarball(t, "v9.9.9"))
	defer srv.Close()

	if _, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: t.TempDir(), CleanupJob: plist,
	}); err != nil {
		t.Fatalf("installSidecar: %v", err)
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) {
		t.Error("the launchd job's plist survived a successful install, so the fetch re-runs at every login")
	}
}

// ⚠️ A FAILED FETCH MUST KEEP ITS JOB. The plist is the only thing that will try
// again — delete it on failure and the machine is left with a stale sidecar and
// nothing scheduled to fix it, which is the silent state this whole path exists
// to end.
func TestCleanupJobSurvivesAFailedInstall(t *testing.T) {
	plist := filepath.Join(t.TempDir(), "co.keld.sidecar-fetch.plist")
	if err := os.WriteFile(plist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A server that answers nothing useful: the fetch cannot succeed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := Install(Opts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: t.TempDir(), CleanupJob: plist,
	}); err == nil {
		t.Fatal("expected the install to fail")
	}
	if _, err := os.Stat(plist); err != nil {
		t.Error("a failed fetch deleted its own retry job, leaving nothing to try again")
	}
}

// ⚠️ "LATEST" IS NOT "MINE", AND THE DIFFERENCE DOWNGRADED A REAL MACHINE TWICE
// IN ONE DAY. `releases/latest` excludes pre-releases by definition, so on any
// -rc.N machine an unpinned fetch answers the last STABLE release. First
// occurrence: a 3.0.5-rc.4 daemon installed v3.0.4. Second: the daemon route
// had been pinned, the CLI had not, and a bare `keld signal install-sidecar`
// put v3.0.4 over an engine the daemon had just correctly updated to rc.6.
//
// The rule lives HERE, at the shared function, so no caller can be the one that
// forgot — which is exactly how the second occurrence happened.
func TestDefaultTagIsThisBinarysOwnRelease(t *testing.T) {
	prev := version.CLI
	t.Cleanup(func() { version.CLI = prev })

	for _, tc := range []struct{ cli, want string }{
		{"3.0.5-rc.6", "v3.0.5-rc.6"},
		// Exactly one leading v, whichever way the stamp carries it: the
		// installer pane's own "vv3.0.0-rc.5" 404 was this in the other
		// direction.
		{"v3.0.5-rc.6", "v3.0.5-rc.6"},
		{"3.0.4", "v3.0.4"},
		// A source build names no release, so there is nothing to pin to and
		// the caller falls through to LatestTag. Nothing-to-pin-to is not a
		// missing pin.
		{"dev", ""},
		{"", ""},
	} {
		version.CLI = tc.cli
		if got := DefaultTag(); got != tc.want {
			t.Errorf("version.CLI %q -> DefaultTag() %q, want %q", tc.cli, got, tc.want)
		}
	}
}

// An explicit --tag still wins: that is what it is for.
func TestAnExplicitTagBeatsTheDefault(t *testing.T) {
	prev := version.CLI
	version.CLI = "3.0.5-rc.6"
	t.Cleanup(func() { version.CLI = prev })

	srv := fakeReleaseServer(t, fakeSidecarTarball(t, "v9.9.9"))
	dest := t.TempDir()
	res, err := Install(Opts{BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if res.StagedPath == "" {
		t.Fatal("nothing staged")
	}
}

// The dry-run tag lookup reads the mirror's latest.json, not GitHub's API: keld-signal is going
// private, and this default is compiled into every pkg's wizard.
func TestLatestTagDefaultsToTheMirror(t *testing.T) {
	if LatestTagURL != "https://dl.keld.co/latest.json" {
		t.Fatalf("LatestTagURL = %q", LatestTagURL)
	}
}

func TestLatestTagReadsLatestJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tag_name": "v3.0.6", "assets": ["keld-agent-sidecar_darwin_arm64.tar.gz"]}`)
	}))
	defer srv.Close()
	got, err := LatestTag(context.Background(), srv.URL+"/latest.json")
	if err != nil || got != "v3.0.6" {
		t.Fatalf("LatestTag = %q, %v", got, err)
	}
}
