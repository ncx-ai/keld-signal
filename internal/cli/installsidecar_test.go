package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

	res, err := installSidecar(installSidecarOpts{
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

	staged, err := installSidecar(installSidecarOpts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true,
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	res, err := commitStagedSidecar(staged.StagedPath, dest)
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

	if _, err := installSidecar(installSidecarOpts{
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
	res, err := installSidecar(installSidecarOpts{BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest})
	if err != nil {
		t.Fatalf("a missing published hash must not be fatal for the installer: %v", err)
	}
	if res.Version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", res.Version)
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

	staged, err := installSidecar(installSidecarOpts{
		BaseURL: srv.URL, Tag: "v9.9.9", Dest: dest, StageOnly: true,
	})
	if err != nil {
		t.Fatalf("stage: %v", err)
	}

	if _, err := commitStagedSidecar(staged.StagedPath, dest); err == nil {
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
	throttle := newSidecarProgressThrottle(func(received, total int64) {
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
	throttle := newSidecarProgressThrottle(func(received, total int64) {
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
// installSidecarOpts (there was no test at all setting it before this) fires
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
	_, err := installSidecar(installSidecarOpts{
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
