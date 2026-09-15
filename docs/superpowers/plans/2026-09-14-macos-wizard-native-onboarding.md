# macOS Wizard-Native Onboarding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the Terminal from the macOS install entirely — the setup code, the
sidecar download and the tool selection all happen in a custom Installer.app wizard
pane, and `postinstall` finishes silently.

**Architecture:** One Installer.app plugin bundle (ObjC, ~300 lines) placed BEFORE the
Install step, carrying a copy of the `keld` CLI in its `Resources`. The pane is a
renderer: it spawns `keld … --json` and draws the NDJSON. It writes one handoff file;
`postinstall` consumes it, applies tool configs, installs the staged sidecar, and
registers the agent — all as the console user.

**Tech Stack:** Go 1.x (cobra CLI), ObjC + `InstallerPlugins.framework` (built with
Command Line Tools `clang`, no Xcode project, no nib), bash (`build-pkg.sh`,
`postinstall`), `pkgbuild`/`productbuild`.

**Spec:** `docs/superpowers/specs/2026-09-14-macos-wizard-native-onboarding-design.md`

## Global Constraints

- **`SectionOrder` entries MUST end in `.bundle`.** A bare name loads nothing, with no
  error and no log line (measured 2026-09-14).
- **The plugin MUST be signed AFTER its executable is compiled, then verified.** A stale
  signature makes the plugin fail to load silently.
- **No pane may run after the Install step** — measured impossible. Everything
  interactive happens pre-install.
- **Nothing that rewrites a user's tool config, `hook.json`, or the LaunchAgent may run
  in the pane.** Those are `postinstall`'s, after the person commits.
- **The setup code is NEVER written to disk.** Only `auth.json` (written by `keld login`).
- **Every user-side command in `postinstall` runs as `launchctl asuser "$uid" sudo -u "$user" -H`.**
  Without `-H`, `os.UserHomeDir()` reads `HOME=/var/root`.
- **`onboard.command` is not deleted and not modified.** It stays the fallback.
- **No production release is cut for testing.** Verification runs through
  `make release-dry` (workflow artifacts, `0.0.0-dryrun`, no tag) and an **atlas-dev**
  setup code.
- **`keld signal setup` must never receive the Atlas ingest token** — `runSetup` fails
  loudly on that; do not route `ob.IngestToken` into `SetupParams.IngestToken`.

---

### Task 1: Download progress hook on `update.Fetcher`

The pane needs a determinate progress bar; `Fetcher.Fetch` today reports nothing until
it finishes. Add an optional callback. Auto-update leaves it nil and is unaffected.

**Files:**
- Modify: `internal/agent/update/fetch.go` (struct `Fetcher`, method `download`)
- Test: `internal/agent/update/fetch_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `Fetcher.Progress func(received, total int64)` — called during `Fetch`;
  `total` is `-1` when the server sends no `Content-Length`.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/update/fetch_test.go`:

```go
func TestFetchReportsProgress(t *testing.T) {
	body := strings.Repeat("x", 4096)
	sum := sha256.Sum256([]byte(body))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			fmt.Fprintf(w, "%s  asset.tar.gz\n", hex.EncodeToString(sum[:]))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	var last int64
	var total int64
	calls := 0
	f := &Fetcher{BaseURL: srv.URL, Policy: fastPolicy(), Progress: func(received, tot int64) {
		calls++
		if received < last {
			t.Errorf("progress went backwards: %d after %d", received, last)
		}
		last, total = received, tot
	}}
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")
	if err := f.Fetch(context.Background(), "v1.2.3", "asset.tar.gz", dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls == 0 {
		t.Fatal("Progress was never called")
	}
	if last != int64(len(body)) {
		t.Fatalf("final received = %d, want %d", last, len(body))
	}
	if total != int64(len(body)) {
		t.Fatalf("total = %d, want %d", total, len(body))
	}
}

func TestFetchWithoutProgressCallbackStillWorks(t *testing.T) {
	body := "hello"
	sum := sha256.Sum256([]byte(body))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			fmt.Fprintf(w, "%s  asset.tar.gz\n", hex.EncodeToString(sum[:]))
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	f := &Fetcher{BaseURL: srv.URL, Policy: fastPolicy()}
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")
	if err := f.Fetch(context.Background(), "v1", "asset.tar.gz", dest); err != nil {
		t.Fatalf("Fetch with nil Progress: %v", err)
	}
}
```

Add any missing imports to the test file: `strconv`, `strings`, `io`, `encoding/hex`,
`crypto/sha256`, `net/http/httptest`, `path/filepath`, `context`, `fmt`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/update/ -run TestFetchReportsProgress -v`
Expected: FAIL — `unknown field Progress in struct literal`.

- [ ] **Step 3: Implement the hook**

In `internal/agent/update/fetch.go`, add the field to `Fetcher`:

```go
// Fetcher downloads and verifies one release asset.
type Fetcher struct {
	HTTP    *http.Client
	BaseURL string
	Policy  retry.Policy

	// Progress, when non-nil, is called as bytes land. total is -1 when the
	// server sent no Content-Length. It exists for the installer's wizard pane,
	// which needs a determinate bar; the unattended update path leaves it nil.
	//
	// It is called from the download goroutine, synchronously, so a slow
	// callback slows the download — keep it to a channel send or an atomic store.
	Progress func(received, total int64)
}
```

In `download`, wrap the copy. Find the `io.Copy`-equivalent write loop and route it
through a counting reader:

```go
// progressReader counts bytes as they are read and reports them.
type progressReader struct {
	r        io.Reader
	total    int64
	received int64
	report   func(received, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.received += int64(n)
		if p.report != nil {
			p.report(p.received, p.total)
		}
	}
	return n, err
}
```

and in `download`, after the response is obtained, replace the reader used for the
copy with:

```go
	var src io.Reader = resp.Body
	if f.Progress != nil {
		src = &progressReader{r: resp.Body, total: resp.ContentLength, report: f.Progress}
	}
```

and replace exactly this line inside `download`'s retry closure:

```go
		n, err := io.Copy(io.MultiWriter(out, h), resp.Body)
```

with:

```go
		var src io.Reader = resp.Body
		if f.Progress != nil {
			// Constructed per ATTEMPT, inside the retry closure: a retried
			// download restarts at zero bytes, and a counter that survived the
			// retry would report a bar running past 100%.
			src = &progressReader{r: resp.Body, total: resp.ContentLength, report: f.Progress}
		}
		n, err := io.Copy(io.MultiWriter(out, h), src)
```

Everything else in `download` — `io.MultiWriter(out, h)`, the short-read check
against `resp.ContentLength`, the `sha256` sum — stays exactly as it is: the hash
must still be computed over the bytes that land.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/update/ -v`
Expected: PASS, including the pre-existing fetch tests.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/update/fetch.go internal/agent/update/fetch_test.go
git commit -m "feat(update): optional download progress hook on Fetcher"
```

---

### Task 2: `keld signal install-sidecar --json`

One command that downloads, verifies, extracts and installs the analysis sidecar,
emitting NDJSON. It replaces the shell copy of this logic in `onboard.command` (which
stays in place as the fallback, so nothing is deleted here).

**Files:**
- Create: `internal/cli/installsidecar.go`
- Create: `internal/cli/installsidecar_test.go`
- Modify: `internal/cli/root.go:36` (register the command in the `signal` group)

**Interfaces:**
- Consumes: `update.Fetcher{Progress: …}` (Task 1), `update.AssetNames`,
  `update.ExtractTarGz`, `update.StageDir`, `update.NewSwap().Replace`.
- Produces: the command `keld signal install-sidecar`, flags
  `--json`, `--tag <tag>`, `--stage-only`, `--commit <stagedPath>`, `--dest <dir>`;
  NDJSON events:
  - `{"event":"progress","received":N,"total":N}`
  - `{"event":"staged","path":"…"}` (`--stage-only`)
  - `{"event":"installed","path":"…","version":"…"}`
  - `{"event":"error","message":"…"}`

- [ ] **Step 1: Write the failing test**

Create `internal/cli/installsidecar_test.go`:

```go
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
		"keld-agent-sidecar/VERSION":           version + "\n",
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
	entries, _ := os.ReadDir(dest)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("mismatch left something behind: %s", e.Name())
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestInstallSidecar -v`
Expected: FAIL — `undefined: installSidecar`, `undefined: installSidecarOpts`.

- [ ] **Step 3: Implement the command**

Create `internal/cli/installsidecar.go`:

```go
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/update"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
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
type installSidecarOpts struct {
	BaseURL   string // release download base; empty = update.DefaultBaseURL
	Tag       string // release tag; empty = resolve the latest release
	Dest      string // directory that holds keld-agent-sidecar/; empty = ~/.local/bin
	StageOnly bool
	Progress  func(received, total int64)
}

type installSidecarResult struct {
	StagedPath string // set by StageOnly
	Path       string // set by a full install/commit
	Version    string
}

// sidecarDestDir is where the macOS pkg and scripts/install.sh both put the
// sidecar: a user-writable directory that sidecarBinPath() already searches, so
// no sudo prompt and no extra configuration.
func sidecarDestDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

// latestReleaseTag resolves the newest published release. Used only when no tag
// is supplied — a real pkg always supplies its own version, so this is the
// dry-run path (VERSION reads 0.0.0-dryrun, which is not a release).
func latestReleaseTag(ctx context.Context, api string) (string, error) {
	if api == "" {
		api = "https://api.github.com/repos/ncx-ai/keld-signal/releases/latest"
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
func installSidecar(opts installSidecarOpts) (installSidecarResult, error) {
	var res installSidecarResult
	dest := opts.Dest
	if dest == "" {
		d, err := sidecarDestDir()
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
		t, err := latestReleaseTag(context.Background(), "")
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
		// see the policy note at the top of this file.
		if !strings.Contains(err.Error(), "no published SHA-256") {
			return res, err
		}
		console.Print("  ! no published SHA-256 for " + asset + "; skipping integrity check")
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
	res.Version = readSidecarVersion(tree)
	if opts.StageOnly {
		cleanup = false
		res.StagedPath = stage
		return res, nil
	}
	r, err := commitStagedSidecar(stage, dest)
	if err != nil {
		return res, err
	}
	return r, nil
}

// commitStagedSidecar moves a staged tree into place and removes the staging dir.
func commitStagedSidecar(staged, dest string) (installSidecarResult, error) {
	var res installSidecarResult
	tree := filepath.Join(staged, "keld-agent-sidecar")
	if fi, err := os.Stat(tree); err != nil || !fi.IsDir() {
		return res, fmt.Errorf("no staged sidecar at %s", tree)
	}
	res.Version = readSidecarVersion(tree)
	target := filepath.Join(dest, "keld-agent-sidecar")
	sw := update.NewSwap()
	if err := sw.Replace(target, tree); err != nil {
		return res, err
	}
	sw.Commit()
	_ = os.RemoveAll(staged)
	res.Path = target
	return res, nil
}

func readSidecarVersion(tree string) string {
	b, err := os.ReadFile(filepath.Join(tree, "VERSION"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

type sidecarProgressEvent struct {
	Event    string `json:"event"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
}

type sidecarStagedEvent struct {
	Event   string `json:"event"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

type sidecarInstalledEvent struct {
	Event   string `json:"event"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

func newInstallSidecarCmd() *cobra.Command {
	var (
		jsonOut   bool
		tag       string
		dest      string
		stageOnly bool
		commit    string
		baseURL   string
	)
	cmd := &cobra.Command{
		Use:   "install-sidecar",
		Short: "Download and install the analysis sidecar.",
		Long: "Download, verify and install the Keld analysis sidecar (~190MB).\n" +
			"The macOS installer's setup pane drives this with --json and renders the events.",
		RunE: func(cmd *cobra.Command, args []string) error {
			fail := func(err error) error {
				if jsonOut {
					emitEvent(errorEvent{Event: "error", Message: cleanErrorMessage(err)})
					return errs.ErrSilentExit
				}
				return err
			}
			if commit != "" {
				d := dest
				if d == "" {
					var err error
					if d, err = sidecarDestDir(); err != nil {
						return fail(err)
					}
				}
				res, err := commitStagedSidecar(commit, d)
				if err != nil {
					return fail(err)
				}
				if jsonOut {
					emitEvent(sidecarInstalledEvent{Event: "installed", Path: res.Path, Version: res.Version})
				} else {
					console.Print("  ✓ analysis sidecar → " + res.Path)
				}
				return nil
			}

			// Throttle: the pane redraws on every event and a 190MB download
			// would otherwise emit hundreds of thousands of lines.
			var lastPct atomic.Int64
			lastPct.Store(-1)
			opts := installSidecarOpts{BaseURL: baseURL, Tag: tag, Dest: dest, StageOnly: stageOnly}
			if jsonOut {
				opts.Progress = func(received, total int64) {
					pct := int64(-1)
					if total > 0 {
						pct = received * 100 / total
					}
					if pct == lastPct.Load() {
						return
					}
					lastPct.Store(pct)
					emitEvent(sidecarProgressEvent{Event: "progress", Received: received, Total: total})
				}
			}
			res, err := installSidecar(opts)
			if err != nil {
				return fail(err)
			}
			switch {
			case stageOnly && jsonOut:
				emitEvent(sidecarStagedEvent{Event: "staged", Path: res.StagedPath, Version: res.Version})
			case stageOnly:
				console.Print("  ✓ analysis sidecar staged at " + res.StagedPath)
			case jsonOut:
				emitEvent(sidecarInstalledEvent{Event: "installed", Path: res.Path, Version: res.Version})
			default:
				console.Print("  ✓ analysis sidecar → " + res.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable NDJSON events on stdout.")
	cmd.Flags().StringVar(&tag, "tag", "", "Release tag to fetch (default: the latest release).")
	cmd.Flags().StringVar(&dest, "dest", "", "Directory that holds keld-agent-sidecar/ (default: ~/.local/bin).")
	cmd.Flags().BoolVar(&stageOnly, "stage-only", false, "Download and unpack, but do not replace the installed sidecar.")
	cmd.Flags().StringVar(&commit, "commit", "", "Install a previously staged tree (the path from --stage-only).")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Release download base URL (testing).")
	return cmd
}
```

`FetchUnverified` does not exist yet. Add it to `internal/agent/update/fetch.go`
beside `Fetch`:

```go
// FetchUnverified downloads without a published-hash check. It exists for ONE
// caller — the installer's sidecar fetch, where a human is watching and
// scripts/install.sh has always degraded this way. Auto-update must never call
// it: an unattended swap has no reader who can abort.
func (f *Fetcher) FetchUnverified(ctx context.Context, tag, asset, dest string) error {
	url := fmt.Sprintf("%s/%s/%s", f.base(), tag, asset)
	if _, err := f.download(ctx, url, dest); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}
```

Register the command in `internal/cli/root.go`, after `newSignalEnrichCmd()`:

```go
	signal.AddCommand(newInstallSidecarCmd())
```

- [ ] **Step 4: Pin the missing-checksum policy**

⚠️ This is the one place the installer deliberately DIVERGES from auto-update, so it
gets its own test rather than a comment. Append to `internal/cli/installsidecar_test.go`:

```go
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
```

Run: `go test ./internal/cli/ -run TestInstallSidecarMissingPublishedHash -v`
Expected: PASS (it exercises the `FetchUnverified` branch added in Step 3).

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/cli/ -run TestInstallSidecar -v && go test ./internal/agent/update/`
Expected: PASS.

- [ ] **Step 6: Verify the command is registered**

Run: `go run ./cmd/keld signal install-sidecar --help`
Expected: help text, exit 0.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/installsidecar.go internal/cli/installsidecar_test.go internal/cli/root.go internal/agent/update/fetch.go
git commit -m "feat(cli): keld signal install-sidecar with NDJSON progress"
```

---

### Task 3: `keld signal setup --bin-path`

`keldBinaryPath()` pins the RUNNING binary into each tool's hook command. Run from
inside the plugin bundle, that path disappears when the wizard closes, so every hook
would break silently. This flag is what makes the pane's tool configuration safe.

**Files:**
- Modify: `internal/cli/setup.go:331-335` (the `tools.SetupParams` literal) and
  `internal/cli/setup.go:362` (flag registration)
- Test: `internal/cli/setup_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: flag `--bin-path <abs path>` on `keld signal setup`; when set it
  replaces `keldBinaryPath()` in `SetupParams.BinPath`.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/setup_test.go`:

```go
func TestSetupBinPathFlagOverridesRunningBinary(t *testing.T) {
	cmd := newSetupCmd()
	f := cmd.Flags().Lookup("bin-path")
	if f == nil {
		t.Fatal("keld signal setup must accept --bin-path: the installer pane runs a copy " +
			"of keld from inside the plugin bundle, and pinning THAT path into tool hooks " +
			"breaks every hook the moment the wizard closes")
	}
	if got := resolveSetupBinPath("/usr/local/keld/keld"); got != "/usr/local/keld/keld" {
		t.Fatalf("explicit bin path = %q, want /usr/local/keld/keld", got)
	}
	if got := resolveSetupBinPath(""); got != keldBinaryPath() {
		t.Fatalf("empty bin path = %q, want the running binary %q", got, keldBinaryPath())
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestSetupBinPathFlag -v`
Expected: FAIL — `undefined: resolveSetupBinPath`.

- [ ] **Step 3: Implement**

In `internal/cli/setup.go`, add beside `keldBinaryPath`:

```go
// resolveSetupBinPath picks the keld path pinned into tool hook commands.
//
// ⚠️ IT EXISTS BECAUSE THE macOS INSTALLER RUNS A COPY OF keld FROM INSIDE THE
// WIZARD PLUGIN BUNDLE, at a path that ceases to exist when the wizard closes.
// keldBinaryPath() would pin that temporary path into every tool's hook command
// and the failure would be silent: the config looks right, the hook never runs.
// The installer passes --bin-path /usr/local/keld/keld, which is where the pkg
// actually puts the binary.
func resolveSetupBinPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return keldBinaryPath()
}
```

Declare the flag variable next to `toolNames` (`setup.go:285`):

```go
	var binPath string
```

Change the `SetupParams` literal (`setup.go:331`):

```go
			p := tools.SetupParams{
				Endpoint:    tp.Endpoint,
				IngestToken: tp.Secret,
				BinPath:     resolveSetupBinPath(binPath),
			}
```

Register the flag beside the others (`setup.go:362`):

```go
	cmd.Flags().StringVar(&binPath, "bin-path", "",
		"Absolute path of the keld binary to pin into tool hooks (default: the running binary).")
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/setup.go internal/cli/setup_test.go
git commit -m "feat(cli): keld signal setup --bin-path for the installer pane"
```

---

### Task 4: `keld login --json` reports the resolved API host

The pane must tell `postinstall` which Atlas to run `keld signal setup --api-url`
against (`setup` reads `paths.APIBase()`, not `auth.json`'s host, so a code minted by
atlas-dev otherwise produces a split-brain install). Emitting it keeps pairing-code
parsing out of the ObjC.

**Files:**
- Modify: `internal/cli/onboard_events.go:20-24` (`authorizedEvent`)
- Modify: `internal/cli/login.go:65` (the emit call)
- Test: `internal/cli/login_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `authorizedEvent.APIURL` on the wire as `api_url`.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/login_test.go`:

```go
func TestAuthorizedEventCarriesAPIURL(t *testing.T) {
	b, err := json.Marshal(authorizedEvent{
		Event: "authorized", Principal: "a@b.co", Org: "acme", APIURL: "https://atlas-dev.keld.co",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["api_url"] != "https://atlas-dev.keld.co" {
		t.Fatalf(`authorized event must carry api_url so the installer can pass `+
			`--api-url to signal setup; got %v`, got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/cli/ -run TestAuthorizedEventCarriesAPIURL -v`
Expected: FAIL — `unknown field APIURL`.

- [ ] **Step 3: Implement**

`internal/cli/onboard_events.go`:

```go
type authorizedEvent struct {
	Event     string `json:"event"`
	Principal string `json:"principal"`
	Org       string `json:"org"`
	// APIURL is the host the code resolved to. A setup code may carry its own
	// ("atlas-dev.keld.co/ABCD-EFGH"), and `keld signal setup` reads
	// paths.APIBase() rather than auth.json — so a caller that runs the two
	// separately (the macOS installer: login in the wizard pane, setup in
	// postinstall) must pass --api-url or write the previous endpoint into
	// hook.json. That is the split-brain install runInstall already guards against.
	APIURL string `json:"api_url,omitempty"`
}
```

`internal/cli/login.go:65`:

```go
					emitEvent(authorizedEvent{Event: "authorized", Principal: a.Principal, Org: a.Org, APIURL: paths.APIBase()})
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/onboard_events.go internal/cli/login.go internal/cli/login_test.go
git commit -m "feat(cli): login --json reports the resolved api_url"
```

---

### Task 5: The Installer.app wizard pane

**Files:**
- Create: `installers/macos/plugin/KeldSetup.m`
- Create: `installers/macos/plugin/Info.plist`
- Create: `installers/macos/plugin/InstallerSections.plist`
- Create: `installers/macos/plugin/build-plugin.sh`
- Create: `installers/macos/plugin_test.sh`
- Modify: `Makefile` (add `pkg-plugin-check`)

**Interfaces:**
- Consumes: `keld login --code <code> --json` (emits `authorized` with `api_url`, or
  `error`), `keld signal setup --dry-run --json` (emits one `tool` event per detected
  tool), `keld signal install-sidecar --json --stage-only --tag <tag>` (emits
  `progress` then `staged`).
- Produces: `~/.keld/state/installer-handoff.json`:
  `{"version":"…","paired":bool,"api_url":"…","tools":["claude_code"],"sidecar_staged":"/…"}`

- [ ] **Step 1: Write the plugin source**

Create `installers/macos/plugin/KeldSetup.m`:

```objc
// Keld's Installer.app wizard pane — the whole of macOS onboarding, with no
// Terminal, no browser and no second app.
//
// ⚠️ **IT RENDERS; IT DECIDES NOTHING.** Every step runs the `keld` binary
// carried in this bundle's Resources and draws the NDJSON it emits
// (internal/cli's --json seam). Auth, tool detection, download verification and
// path resolution stay in Go, where they are tested. An ObjC reimplementation
// of any of them is the defect this design exists to avoid.
//
// ⚠️ **THIS PANE RUNS BEFORE THE PAYLOAD IS INSTALLED, and that is forced by
// measurement, not preference** (2026-09-14, macOS 26.5.2): a section ordered
// after Install.bundle enters with installStarted=0 and the plugin's host
// process stops the moment installation completes. There is no post-install UI
// in a pkg. So this pane does only what is safe to abandon — redeem a code
// (auth.json) and stage a download — and postinstall does everything that
// rewrites a user's files.
#import <Cocoa/Cocoa.h>
#import <InstallerPlugins/InstallerPlugins.h>

@interface KeldSetupPane : InstallerPane
@end

@implementation KeldSetupPane {
    NSView *_view;
    NSTextField *_codeField;
    NSButton *_connectButton;
    NSTextField *_codeStatus;
    NSProgressIndicator *_engineBar;
    NSTextField *_engineStatus;
    NSStackView *_toolsStack;
    NSMutableArray<NSButton *> *_toolChecks;
    NSButton *_laterButton;
    BOOL _paired;
    NSString *_apiURL;
    NSString *_stagedSidecar;
}

- (NSString *)title { return @"Set Up Keld"; }

// keldPath is the CLI copy build-pkg.sh places in this bundle. The payload is
// not installed yet, so /usr/local/keld/keld does not exist at this point.
- (NSString *)keldPath {
    return [[NSBundle bundleForClass:[self class]] pathForResource:@"keld" ofType:nil];
}

- (NSString *)bundleVersion {
    NSString *v = [[NSBundle bundleForClass:[self class]] objectForInfoDictionaryKey:@"CFBundleShortVersionString"];
    return v ?: @"";
}

#pragma mark - Running keld

// runKeld spawns the embedded CLI and delivers one parsed NDJSON object per
// line on the MAIN queue. `done` fires once, after exit.
- (void)runKeld:(NSArray<NSString *> *)args
         onEvent:(void (^)(NSDictionary *event))onEvent
            done:(void (^)(int status))done {
    NSString *keld = [self keldPath];
    if (!keld) {
        dispatch_async(dispatch_get_main_queue(), ^{ done(-1); });
        return;
    }
    NSTask *task = [NSTask new];
    task.executableURL = [NSURL fileURLWithPath:keld];
    task.arguments = args;
    NSPipe *out = [NSPipe pipe];
    task.standardOutput = out;
    task.standardError = [NSPipe pipe];   // keep stderr off the console log

    NSMutableData *buffer = [NSMutableData data];
    out.fileHandleForReading.readabilityHandler = ^(NSFileHandle *fh) {
        [buffer appendData:fh.availableData];
        while (YES) {
            NSRange nl = [buffer rangeOfData:[@"\n" dataUsingEncoding:NSUTF8StringEncoding]
                                     options:0 range:NSMakeRange(0, buffer.length)];
            if (nl.location == NSNotFound) break;
            NSData *line = [buffer subdataWithRange:NSMakeRange(0, nl.location)];
            [buffer replaceBytesInRange:NSMakeRange(0, nl.location + 1) withBytes:NULL length:0];
            NSDictionary *obj = [NSJSONSerialization JSONObjectWithData:line options:0 error:nil];
            if ([obj isKindOfClass:[NSDictionary class]]) {
                dispatch_async(dispatch_get_main_queue(), ^{ onEvent(obj); });
            }
        }
    };
    task.terminationHandler = ^(NSTask *t) {
        t.standardOutput = nil;
        out.fileHandleForReading.readabilityHandler = nil;
        dispatch_async(dispatch_get_main_queue(), ^{ done(t.terminationStatus); });
    };
    NSError *err = nil;
    if (![task launchAndReturnError:&err]) {
        dispatch_async(dispatch_get_main_queue(), ^{ done(-1); });
    }
}

#pragma mark - View

- (NSTextField *)labelWithText:(NSString *)s bold:(BOOL)bold {
    NSTextField *l = [NSTextField labelWithString:s];
    if (bold) l.font = [NSFont boldSystemFontOfSize:[NSFont systemFontSize]];
    return l;
}

- (NSView *)contentView {
    if (_view) return _view;
    _toolChecks = [NSMutableArray array];

    NSStackView *root = [[NSStackView alloc] initWithFrame:NSMakeRect(0, 0, 620, 340)];
    root.orientation = NSUserInterfaceLayoutOrientationVertical;
    root.alignment = NSLayoutAttributeLeading;
    root.spacing = 14;

    [root addArrangedSubview:[self labelWithText:@"Your setup code" bold:YES]];
    _codeField = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 320, 24)];
    _codeField.placeholderString = @"atlas.keld.co/ABCD-EFGH";
    _connectButton = [NSButton buttonWithTitle:@"Connect" target:self action:@selector(connect:)];
    NSStackView *codeRow = [NSStackView stackViewWithViews:@[_codeField, _connectButton]];
    codeRow.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    [root addArrangedSubview:codeRow];
    _codeStatus = [self labelWithText:@"Paste the code from your Keld download page." bold:NO];
    _codeStatus.textColor = [NSColor secondaryLabelColor];
    [root addArrangedSubview:_codeStatus];

    [root addArrangedSubview:[self labelWithText:@"Analysis engine" bold:YES]];
    _engineBar = [[NSProgressIndicator alloc] initWithFrame:NSMakeRect(0, 0, 420, 16)];
    _engineBar.style = NSProgressIndicatorStyleBar;
    _engineBar.indeterminate = YES;
    _engineBar.minValue = 0;
    _engineBar.maxValue = 100;
    [_engineBar startAnimation:nil];
    [root addArrangedSubview:_engineBar];
    _engineStatus = [self labelWithText:@"Preparing…" bold:NO];
    _engineStatus.textColor = [NSColor secondaryLabelColor];
    [root addArrangedSubview:_engineStatus];

    [root addArrangedSubview:[self labelWithText:@"Your AI tools" bold:YES]];
    _toolsStack = [[NSStackView alloc] initWithFrame:NSZeroRect];
    _toolsStack.orientation = NSUserInterfaceLayoutOrientationVertical;
    _toolsStack.alignment = NSLayoutAttributeLeading;
    _toolsStack.spacing = 4;
    [root addArrangedSubview:_toolsStack];

    _laterButton = [NSButton buttonWithTitle:@"Set up later" target:self action:@selector(setUpLater:)];
    _laterButton.bezelStyle = NSBezelStyleInline;
    [root addArrangedSubview:_laterButton];

    _view = root;
    return _view;
}

#pragma mark - Pane lifecycle

- (void)didEnterPane:(InstallerSectionDirection)dir {
    (void)[self contentView];
    // ⚠️ Continue is disabled until the code is accepted (or "Set up later" is
    // clicked). Catching a bad code HERE is the point: there is no pane after
    // the install to catch it in.
    self.nextEnabled = _paired;
    [self startSidecarDownload];
}

// shouldExitPane writes the handoff postinstall consumes. Returning YES always:
// by this point either pairing succeeded or the person chose to finish later,
// and both are states postinstall knows how to complete.
- (BOOL)shouldExitPane:(InstallerSectionDirection)dir {
    if (dir == InstallerDirectionForward) [self writeHandoff];
    return YES;
}

#pragma mark - Steps

- (void)connect:(id)sender {
    NSString *code = [_codeField.stringValue stringByTrimmingCharactersInSet:
                      [NSCharacterSet whitespaceAndNewlineCharacterSet]];
    if (code.length == 0) {
        _codeStatus.stringValue = @"Enter the setup code from your Keld download page.";
        return;
    }
    _connectButton.enabled = NO;
    _codeStatus.stringValue = @"Connecting…";
    __weak typeof(self) weakSelf = self;
    __block NSString *failure = nil;
    [self runKeld:@[@"login", @"--code", code, @"--json"]
          onEvent:^(NSDictionary *e) {
        typeof(self) s = weakSelf; if (!s) return;
        NSString *kind = e[@"event"];
        if ([kind isEqualToString:@"authorized"]) {
            s->_paired = YES;
            s->_apiURL = e[@"api_url"] ?: @"";
            s->_codeStatus.stringValue = [NSString stringWithFormat:@"Connected — %@ · %@",
                                          e[@"principal"] ?: @"", e[@"org"] ?: @""];
            s.nextEnabled = YES;
            [s loadTools];
        } else if ([kind isEqualToString:@"error"]) {
            failure = e[@"message"] ?: @"that code was not accepted";
        }
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        s->_connectButton.enabled = YES;
        if (!s->_paired) {
            s->_codeStatus.stringValue = failure ?: @"That code was not accepted. Check it and try again.";
        }
    }];
}

// loadTools enumerates what is installed WITHOUT writing anything: --dry-run
// emits one `tool` event per detected tool and returns before any write.
- (void)loadTools {
    __weak typeof(self) weakSelf = self;
    NSMutableArray<NSDictionary *> *found = [NSMutableArray array];
    NSMutableArray<NSString *> *args = [@[@"signal", @"setup", @"--dry-run", @"--json"] mutableCopy];
    if (_apiURL.length) { [args addObjectsFromArray:@[@"--api-url", _apiURL]]; }
    [self runKeld:args onEvent:^(NSDictionary *e) {
        if ([e[@"event"] isEqualToString:@"tool"]) [found addObject:e];
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        [s renderTools:found];
    }];
}

- (void)renderTools:(NSArray<NSDictionary *> *)tools {
    for (NSView *v in [_toolsStack.arrangedSubviews copy]) [_toolsStack removeArrangedSubview:v], [v removeFromSuperview];
    [_toolChecks removeAllObjects];
    if (tools.count == 0) {
        [_toolsStack addArrangedSubview:[self labelWithText:@"No supported AI tools found on this Mac." bold:NO]];
        return;
    }
    for (NSDictionary *t in tools) {
        NSString *display = t[@"display"] ?: t[@"name"];
        NSString *action = t[@"action"] ?: @"";
        NSString *title = [action isEqualToString:@"skipped_conflict"]
            ? [NSString stringWithFormat:@"%@ — has its own telemetry settings; leave it alone", display]
            : display;
        NSButton *check = [NSButton checkboxWithTitle:title target:nil action:nil];
        check.identifier = t[@"name"];
        check.state = [action isEqualToString:@"skipped_conflict"] ? NSControlStateValueOff : NSControlStateValueOn;
        [_toolsStack addArrangedSubview:check];
        [_toolChecks addObject:check];
    }
    [_toolsStack addArrangedSubview:[self labelWithText:
        @"Restart these apps after setup — they read their settings once, at startup." bold:NO]];
}

// The download starts on its own and NEVER gates Continue: a late sidecar costs
// nothing (jobs spool until it lands), while a blocked wizard costs a person
// several minutes of staring on a slow connection.
- (void)startSidecarDownload {
    NSString *version = [self bundleVersion];
    NSMutableArray<NSString *> *args = [@[@"signal", @"install-sidecar", @"--json", @"--stage-only"] mutableCopy];
    // A dry-run build carries no real release tag; the Go side then resolves the
    // latest release, which is what onboard.command already does.
    if (version.length && [version rangeOfString:@"dryrun"].location == NSNotFound) {
        [args addObjectsFromArray:@[@"--tag", [@"v" stringByAppendingString:version]]];
    }
    __weak typeof(self) weakSelf = self;
    __block NSString *failure = nil;
    [self runKeld:args onEvent:^(NSDictionary *e) {
        typeof(self) s = weakSelf; if (!s) return;
        NSString *kind = e[@"event"];
        if ([kind isEqualToString:@"progress"]) {
            long long got = [e[@"received"] longLongValue], total = [e[@"total"] longLongValue];
            if (total > 0) {
                s->_engineBar.indeterminate = NO;
                s->_engineBar.doubleValue = (double)got * 100.0 / (double)total;
                s->_engineStatus.stringValue = [NSString stringWithFormat:@"Downloading… %lld MB of %lld MB",
                                                got / 1048576, total / 1048576];
            }
        } else if ([kind isEqualToString:@"staged"]) {
            s->_stagedSidecar = e[@"path"];
        } else if ([kind isEqualToString:@"error"]) {
            failure = e[@"message"];
        }
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        [s->_engineBar stopAnimation:nil];
        s->_engineBar.indeterminate = NO;
        if (s->_stagedSidecar.length) {
            s->_engineBar.doubleValue = 100;
            s->_engineStatus.stringValue = @"Ready. Nothing multi-gigabyte is downloaded, now or later.";
        } else {
            s->_engineBar.doubleValue = 0;
            s->_engineStatus.stringValue = failure
                ? [NSString stringWithFormat:@"Could not download it (%@). Keld will retry in the background.", failure]
                : @"Could not download it. Keld will retry in the background.";
        }
    }];
}

- (void)setUpLater:(id)sender {
    _codeStatus.stringValue = @"Skipping for now — nothing is collected until you add a code.";
    self.nextEnabled = YES;
}

#pragma mark - Handoff

// ⚠️ THE SETUP CODE IS NEVER WRITTEN HERE. It was redeemed already; auth.json
// (written by `keld login`) is the one credential artifact, exactly as today.
- (void)writeHandoff {
    NSMutableArray<NSString *> *tools = [NSMutableArray array];
    for (NSButton *c in _toolChecks) {
        if (c.state == NSControlStateValueOn && c.identifier) [tools addObject:(NSString *)c.identifier];
    }
    NSDictionary *payload = @{
        @"version": [self bundleVersion] ?: @"",
        @"paired": @(_paired),
        @"api_url": _apiURL ?: @"",
        @"tools": tools,
        @"sidecar_staged": _stagedSidecar ?: @"",
    };
    NSString *dir = [NSHomeDirectory() stringByAppendingPathComponent:@".keld/state"];
    [[NSFileManager defaultManager] createDirectoryAtPath:dir
                             withIntermediateDirectories:YES
                                              attributes:@{NSFilePosixPermissions: @(0700)}
                                                   error:nil];
    NSData *d = [NSJSONSerialization dataWithJSONObject:payload options:0 error:nil];
    NSString *path = [dir stringByAppendingPathComponent:@"installer-handoff.json"];
    [d writeToFile:path atomically:YES];
    [[NSFileManager defaultManager] setAttributes:@{NSFilePosixPermissions: @(0600)}
                                     ofItemAtPath:path error:nil];
}

@end

@interface KeldSetupSection : InstallerSection
@end

@implementation KeldSetupSection {
    KeldSetupPane *_pane;
}
- (NSString *)title { return @"Set Up Keld"; }
// No nib: InstallerSection.h sanctions a subclass supplying its own pane when no
// NSMainNibFile is declared. A nib would need Xcode and buy nothing here.
- (InstallerPane *)firstPane {
    if (!_pane) _pane = [[KeldSetupPane alloc] initWithSection:self];
    return _pane;
}
@end
```

- [ ] **Step 2: Write the bundle metadata**

Create `installers/macos/plugin/Info.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>en</string>
  <key>CFBundleExecutable</key><string>KeldSetup</string>
  <key>CFBundleIdentifier</key><string>co.keld.installer.setup</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>KeldSetup</string>
  <key>CFBundlePackageType</key><string>BNDL</string>
  <key>CFBundleShortVersionString</key><string>0.0.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>NSPrincipalClass</key><string>KeldSetupSection</string>
  <key>InstallerSectionTitle</key><string>Set Up Keld</string>
</dict>
</plist>
```

Create `installers/macos/plugin/InstallerSections.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <!-- ⚠️ EVERY ENTRY MUST END IN .bundle. A bare name (e.g. "Introduction")
       matches nothing, silently: the section simply does not load, with no error
       and no log line. Measured 2026-09-14 — a list of bare built-in names left
       KeldSetup as the only recognised section, so it rendered FIRST and looked
       like proof that a post-install pane works. It is not.
       KeldSetup sits BEFORE Install.bundle because a pane after it never
       appears: the plugin's host process stops when installation completes. -->
  <key>SectionOrder</key>
  <array>
    <string>Introduction.bundle</string>
    <string>ReadMe.bundle</string>
    <string>License.bundle</string>
    <string>TargetSelect.bundle</string>
    <string>PackageSelection.bundle</string>
    <string>KeldSetup.bundle</string>
    <string>Install.bundle</string>
    <string>Summary.bundle</string>
  </array>
</dict>
</plist>
```

- [ ] **Step 3: Write the plugin build script**

Create `installers/macos/plugin/build-plugin.sh`:

```bash
#!/usr/bin/env bash
# Build KeldSetup.bundle — the Installer.app wizard pane.
#
# Command Line Tools are enough: clang against InstallerPlugins.framework, no
# Xcode project and no nib (the section supplies its pane programmatically).
#
# Usage: build-plugin.sh <out-dir> <version> <keld-binary>
set -euo pipefail
OUT="${1:?output dir (the --plugins dir)}"
VERSION="${2:?version}"
KELD="${3:?path to the keld binary to embed}"
ROOT="$(cd "$(dirname "$0")" && pwd)"
SDK="$(xcrun --show-sdk-path)"

BUNDLE="$OUT/KeldSetup.bundle"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/Contents/MacOS" "$BUNDLE/Contents/Resources"

clang -bundle -fobjc-arc -arch arm64 \
  -isysroot "$SDK" -mmacosx-version-min=12.0 \
  -framework Cocoa -framework InstallerPlugins \
  -o "$BUNDLE/Contents/MacOS/KeldSetup" "$ROOT/KeldSetup.m"

cp "$ROOT/Info.plist" "$BUNDLE/Contents/Info.plist"
plutil -replace CFBundleShortVersionString -string "$VERSION" "$BUNDLE/Contents/Info.plist"

# The pane drives this copy: the payload is not installed when it runs.
cp "$KELD" "$BUNDLE/Contents/Resources/keld"
chmod +x "$BUNDLE/Contents/Resources/keld"

cp "$ROOT/InstallerSections.plist" "$OUT/InstallerSections.plist"

# ⚠️ SIGN AFTER BUILDING, THEN VERIFY. A bundle whose executable was recompiled
# after signing FAILS TO LOAD WITH NO DIAGNOSTIC AT ALL — no pane, no error
# dialog, nothing in `log show`. Measured 2026-09-14; it cost three runs.
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  codesign --force --options runtime --timestamp \
    --sign "$APPLE_DEVELOPER_ID_APP" "$BUNDLE/Contents/Resources/keld"
  codesign --force --options runtime --timestamp \
    --sign "$APPLE_DEVELOPER_ID_APP" "$BUNDLE"
else
  # Unsigned-first, matching build-pkg.sh: ad-hoc so a LOCAL test still loads.
  codesign --force --sign - "$BUNDLE/Contents/Resources/keld"
  codesign --force --sign - "$BUNDLE"
fi
codesign --verify --strict --verbose=2 "$BUNDLE"
echo "built $BUNDLE"
```

Then: `chmod +x installers/macos/plugin/build-plugin.sh`

- [ ] **Step 4: Write the static assertion test**

Create `installers/macos/plugin_test.sh`:

```bash
#!/usr/bin/env bash
# Static assertions over the wizard plugin. These pin the two failure modes that
# are SILENT at runtime, which is why they are asserted rather than trusted.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
p="$d/plugin"
fail() { echo "FAIL: $1"; exit 1; }

test -f "$p/KeldSetup.m" || fail "missing KeldSetup.m"
test -x "$p/build-plugin.sh" || fail "build-plugin.sh is not executable"

# ⚠️ Every SectionOrder entry must end in .bundle — a bare name loads nothing.
python3 - "$p/InstallerSections.plist" <<'PY' || fail "SectionOrder entries must all end in .bundle"
import plistlib, sys
order = plistlib.load(open(sys.argv[1], 'rb'))["SectionOrder"]
bad = [s for s in order if not s.endswith(".bundle")]
sys.exit(1 if bad else 0)
PY

# The pane must come BEFORE Install.bundle: a section after it never appears.
python3 - "$p/InstallerSections.plist" <<'PY' || fail "KeldSetup.bundle must be ordered before Install.bundle"
import plistlib, sys
order = plistlib.load(open(sys.argv[1], 'rb'))["SectionOrder"]
sys.exit(0 if order.index("KeldSetup.bundle") < order.index("Install.bundle") else 1)
PY

# Sign-after-build, then verify.
grep -q 'clang -bundle' "$p/build-plugin.sh" || fail "build-plugin.sh does not compile the bundle"
awk '/clang -bundle/{c=NR} /codesign --force/{if (!s) s=NR} /codesign --verify/{v=NR} END{exit !(c && s && v && c<s && s<v)}' \
  "$p/build-plugin.sh" || fail "build-plugin.sh must compile, THEN sign, THEN verify"

# The pane must not reimplement Go logic.
grep -q 'login", @"--code' "$p/KeldSetup.m" || fail "pane does not redeem the code via keld login --json"
grep -q 'install-sidecar' "$p/KeldSetup.m" || fail "pane does not drive keld signal install-sidecar"
grep -q 'installer-handoff.json' "$p/KeldSetup.m" || fail "pane writes no handoff file"
grep -q 'nextEnabled' "$p/KeldSetup.m" || fail "pane never gates Continue"

# The setup code must never be persisted.
grep -qE 'writeToFile.*code|@"code"' "$p/KeldSetup.m" && fail "the setup code must never be written to disk" || true

echo "plugin_test.sh: OK"
```

Then: `chmod +x installers/macos/plugin_test.sh`

- [ ] **Step 5: Add the make target**

In `Makefile`, beside the other check targets:

```make
# Compile + sign + verify the macOS wizard plugin. macOS-only; a no-op elsewhere.
# This is what catches the stale-signature failure, which is invisible at runtime.
.PHONY: pkg-plugin-check
pkg-plugin-check:
	@[ "$$(uname -s)" = "Darwin" ] || { echo "pkg-plugin-check: macOS only — skipping"; exit 0; }
	@bash installers/macos/plugin_test.sh
	@go build -o /tmp/keld-plugin-check-keld ./cmd/keld
	@bash installers/macos/plugin/build-plugin.sh /tmp/keld-plugin-check-plugins 0.0.0-check /tmp/keld-plugin-check-keld
	@rm -rf /tmp/keld-plugin-check-plugins /tmp/keld-plugin-check-keld
	@echo "pkg-plugin-check: OK"
```

Add it to the `make help` list beside the other checks.

- [ ] **Step 6: Run it**

Run: `make pkg-plugin-check`
Expected: `plugin_test.sh: OK`, a built bundle, `codesign --verify` passing,
`pkg-plugin-check: OK`.

- [ ] **Step 7: Commit**

```bash
git add installers/macos/plugin installers/macos/plugin_test.sh Makefile
git commit -m "feat(installer): Installer.app wizard pane driving keld --json"
```

---

### Task 6: Wire the plugin into `build-pkg.sh`

**Files:**
- Modify: `installers/macos/build-pkg.sh` (staging, signing sweep, `productbuild`)
- Modify: `installers/macos/build_pkg_notarization_test.sh` (static assertions)

**Interfaces:**
- Consumes: `installers/macos/plugin/build-plugin.sh <out-dir> <version> <keld>` (Task 5).
- Produces: a pkg whose product archive contains `PlugIns/`.

- [ ] **Step 1: Write the failing assertions**

Append to `installers/macos/build_pkg_notarization_test.sh`:

```bash
# ── The wizard plugin ────────────────────────────────────────────────────────
# The pane is what removes the Terminal; these pin that it is actually built,
# actually signed, and actually handed to productbuild.
b="$d/build-pkg.sh"
grep -qF 'plugin/build-plugin.sh' "$b" || { echo "build-pkg.sh does not build the wizard plugin"; exit 1; }
grep -qF -- '--plugins' "$b" || { echo "build-pkg.sh does not pass --plugins to productbuild"; exit 1; }
# ⚠️ The plugin lives OUTSIDE $STAGE, so sign-macho.sh's sweep does not see it.
# An unsigned Mach-O anywhere in a submission fails notarization for the whole pkg.
grep -qF 'codesign --verify --strict --verbose=2 "$PLUGIN_DIR/KeldSetup.bundle"' "$b" \
  || { echo "build-pkg.sh does not verify the plugin's signature"; exit 1; }
```

- [ ] **Step 2: Run to verify it fails**

Run: `bash installers/macos/build_pkg_notarization_test.sh`
Expected: FAIL — "build-pkg.sh does not build the wizard plugin".

- [ ] **Step 3: Implement**

In `installers/macos/build-pkg.sh`, after the app component is built (just before the
`PB=(productbuild …)` line), add:

```bash
# ── The wizard pane ──────────────────────────────────────────────────────────
# Onboarding happens INSIDE the wizard: a custom Installer.app section, ordered
# before the Install step, that redeems the setup code, downloads the analysis
# sidecar with a progress bar, and collects which AI tools to configure. It
# carries its own copy of `keld` because the payload is not installed while it
# runs.
#
# ⚠️ A pane cannot be placed AFTER the install — measured 2026-09-14: the
# section enters with installStarted=0 and the plugin's host process stops the
# moment installation completes. postinstall does the rest, silently.
PLUGIN_DIR="$TMP/plugins"
mkdir -p "$PLUGIN_DIR"
"$ROOT/plugin/build-plugin.sh" "$PLUGIN_DIR" "$VERSION" "$STAGE/keld"
# build-plugin.sh signs and verifies internally (sign-after-build, or the bundle
# fails to load with no diagnostic); verify again here for the same reason the
# payload binaries are verified — an opaque notarization rejection is the
# alternative.
codesign --verify --strict --verbose=2 "$PLUGIN_DIR/KeldSetup.bundle"
```

Then add `--plugins` to the `productbuild` invocation:

```bash
PB=(productbuild --distribution "$ROOT/distribution.xml" --resources "$ROOT/../resources" \
    --plugins "$PLUGIN_DIR" --package-path "$TMP" "$OUT")
```

- [ ] **Step 4: Run the assertions**

Run: `bash installers/macos/build_pkg_notarization_test.sh && bash installers/macos/onboard_command_test.sh`
Expected: both OK (the onboard test still passes — `onboard.command` is untouched).

- [ ] **Step 5: Build a real unsigned pkg locally and confirm the plugin is inside**

```bash
make build-binaries
mkdir -p /tmp/keld-stage && cp bin/keld bin/keld-agent /tmp/keld-stage/ 2>/dev/null || \
  { go build -o /tmp/keld-stage/keld ./cmd/keld && go build -o /tmp/keld-stage/keld-agent ./cmd/keld-agent; }
KELD_APP_BUNDLE="${KELD_APP_BUNDLE:-}" bash installers/macos/build-pkg.sh 0.0.0-local /tmp/keld-stage arm64
pkgutil --expand keld-0.0.0-local-arm64.pkg /tmp/keld-pkg-expanded
ls /tmp/keld-pkg-expanded | grep -q PlugIns && echo "PlugIns present"
```

Expected: `PlugIns present`.
(If `KELD_APP_BUNDLE` is unset and no app bundle has been built, build-pkg.sh fails by
design — set `KELD_APP_BUNDLE` to a previously built `Keld Signal.app`, or build it.)

- [ ] **Step 6: Commit**

```bash
git add installers/macos/build-pkg.sh installers/macos/build_pkg_notarization_test.sh
git commit -m "build(installer): ship the wizard plugin in the pkg"
```

---

### Task 7: `postinstall` finishes the job silently

**Files:**
- Modify: `installers/macos/scripts/postinstall`
- Create: `installers/macos/postinstall_test.sh`

**Interfaces:**
- Consumes: `~/.keld/state/installer-handoff.json` (Task 5).
- Produces: a configured, running machine; no Terminal on the success path.

- [ ] **Step 1: Write the failing assertions**

Create `installers/macos/postinstall_test.sh`:

```bash
#!/usr/bin/env bash
# Static assertions over postinstall. Every one of these pins a failure that is
# silent on a real machine.
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
p="$d/scripts/postinstall"
code="$(sed 's/#.*//' "$p")"
fail() { echo "FAIL: $1"; exit 1; }

# ⚠️ -H or the LaunchAgent lands in /var/root/Library/LaunchAgents and never loads:
# service_darwin.go builds the plist path from os.UserHomeDir(), i.e. $HOME.
printf '%s' "$code" | grep -qF 'sudo -u "$user" -H' || fail "user-side commands must run with sudo -H"

# The agent must be registered by postinstall now — nothing else does it.
printf '%s' "$code" | grep -qF 'keld-agent install' || fail "postinstall never registers the agent"

# The handoff is what the wizard pane leaves behind.
printf '%s' "$code" | grep -qF 'installer-handoff.json' || fail "postinstall ignores the wizard handoff"

# ⚠️ THE FALLBACK IS MANDATORY. The plugin can fail to load with NO diagnostic
# (stale signature, bad plist). Without this, such a machine installs and then
# sits unconfigured with nothing on screen ever having asked for a code.
printf '%s' "$code" | grep -qF 'onboard.command' || fail "no fallback when the wizard pane did not run"

# The tool half must be pointed at the INSTALLED keld, never the plugin's copy.
printf '%s' "$code" | grep -qF -- '--bin-path' || fail "signal setup must pin the installed keld path"

echo "postinstall_test.sh: OK"
```

Then: `chmod +x installers/macos/postinstall_test.sh`

- [ ] **Step 2: Run to verify it fails**

Run: `bash installers/macos/postinstall_test.sh`
Expected: FAIL — "user-side commands must run with sudo -H".

- [ ] **Step 3: Implement**

Replace the tail of `installers/macos/scripts/postinstall` (everything from the
`launchctl kickstart` line to `exit 0`) with:

```bash
# ── Finish what the wizard pane started ──────────────────────────────────────
# The pane (installers/macos/plugin) ran BEFORE this, redeemed the setup code and
# staged the analysis sidecar; it left a handoff file naming what it did. This
# script does everything that rewrites the user's files, which deliberately does
# NOT happen in the pane: until the install is committed, a person can still
# cancel, and a cancelled install must not leave rewritten tool configs behind.
#
# ⚠️ EVERY user-side command runs `launchctl asuser <uid> sudo -u <user> -H`.
# Without -H, sudo keeps HOME=/var/root, and service_darwin.go builds the
# LaunchAgent path from os.UserHomeDir() — so the job would be written into
# root's home and never load, with nothing reporting it.
asuser() { launchctl asuser "$uid" sudo -u "$user" -H "$@"; }

HANDOFF="$userhome/.keld/state/installer-handoff.json"
paired=false
api_url=""
tools=""
staged=""
if [ -f "$HANDOFF" ]; then
  paired=$(/usr/bin/plutil -extract paired raw -o - "$HANDOFF" 2>/dev/null || echo false)
  api_url=$(/usr/bin/plutil -extract api_url raw -o - "$HANDOFF" 2>/dev/null || echo "")
  staged=$(/usr/bin/plutil -extract sidecar_staged raw -o - "$HANDOFF" 2>/dev/null || echo "")
  tools=$(/usr/bin/plutil -extract tools json -o - "$HANDOFF" 2>/dev/null \
          | tr -d '[]"' | tr ',' ' ' || echo "")
fi

# 1. The analysis sidecar the pane already downloaded and verified: a rename into
#    place, in the user's own ~/.local/bin (same filesystem, so ~15,000 files move
#    atomically rather than being copied).
if [ -n "$staged" ] && [ -d "$staged" ]; then
  asuser "$PREFIX/keld" signal install-sidecar --commit "$staged" || true
else
  # No staged tree (no network in the pane, or the pane never ran). Retry in the
  # background: enrichment spools until it lands, so this is late, not fatal.
  asuser /bin/sh -c "\"$PREFIX/keld\" signal install-sidecar >/dev/null 2>&1 &" || true
fi

# 2. The AI tools the person ticked. --bin-path pins the INSTALLED keld: the pane
#    ran a copy from inside the plugin bundle, and pinning THAT path into hook
#    commands breaks every hook the moment the wizard closes.
if [ "$paired" = "true" ] || [ "$paired" = "1" ]; then
  set -- signal setup --yes --bin-path "$PREFIX/keld"
  [ -n "$api_url" ] && set -- "$@" --api-url "$api_url"
  for t in $tools; do
    [ -n "$t" ] && set -- "$@" --tool "$t"
  done
  asuser "$PREFIX/keld" "$@" || true
fi

# 3. Register and start the agent. This is what used to happen only inside the
#    Terminal script, which is why a machine that never opened it collected
#    nothing.
asuser "$PREFIX/keld-agent" install || true

rm -f "$HANDOFF"

# ⚠️ FALLBACK — and it is not optional. The wizard plugin can fail to load with
# NO diagnostic whatsoever (a stale code signature does exactly this; measured
# 2026-09-14). A machine in that state has installed cleanly and been asked for
# nothing, so without this it would sit idle forever. The old Terminal path is
# kept precisely to be this.
if [ ! -f "$userhome/.keld/hook.json" ] && [ -f "$PREFIX/onboard.command" ]; then
  chmod +x "$PREFIX/onboard.command" || true
  launchctl asuser "$uid" sudo -u "$user" open "$PREFIX/onboard.command" || true
fi

exit 0
```

Keep everything above it (the `PREFIX`, `mkdir -p /usr/local/bin`, the symlinks, the
`uid`/`user`/`userhome` resolution, the stray-copy repointing) exactly as it is.

- [ ] **Step 4: Run the assertions**

Run: `bash installers/macos/postinstall_test.sh`
Expected: `postinstall_test.sh: OK`.

- [ ] **Step 5: Shell-lint both scripts**

Run: `bash -n installers/macos/scripts/postinstall && bash -n installers/macos/plugin/build-plugin.sh`
Expected: no output, exit 0.

- [ ] **Step 6: Commit**

```bash
git add installers/macos/scripts/postinstall installers/macos/postinstall_test.sh
git commit -m "feat(installer): postinstall finishes setup silently from the wizard handoff"
```

---

### Task 8: CI wiring, docs, and the manual verification runbook

The wizard itself cannot be tested by any CI check — a human has to click it. This task
makes the static half automatic and the manual half written down.

**Files:**
- Modify: `.github/workflows/ci.yml` (run the three installer test scripts)
- Modify: `AGENTS.md` (the macOS onboarding bullet)
- Modify: `CHANGELOG.md`
- Create: `docs/macos-wizard-onboarding.md`

- [ ] **Step 1: Wire the static tests into CI**

In `.github/workflows/ci.yml`, the `installer-guards` job (line 94) already runs
`onboard_command_test.sh`. Add the two new scripts to it, after the
"macOS onboarding contract" step:

```yaml
      - name: macOS wizard pane contract
        run: bash installers/macos/plugin_test.sh
      - name: macOS postinstall contract
        run: bash installers/macos/postinstall_test.sh
```

These are static (grep/plist) assertions and run on `ubuntu-latest` like the rest of
that job. `make pkg-plugin-check` is NOT added: it compiles ObjC and needs macOS.
`build_pkg_notarization_test.sh` already runs in the `shell-tests` job (`ci.yml:36`).

- [ ] **Step 2: Write the runbook**

Create `docs/macos-wizard-onboarding.md`:

```markdown
# macOS onboarding — the wizard pane

**Status:** implemented 2026-09-14. Spec:
`docs/superpowers/specs/2026-09-14-macos-wizard-native-onboarding-design.md`.

Installing Keld on macOS involves no Terminal, no browser and no second app. The
pkg carries a custom Installer.app section (`installers/macos/plugin/`) that runs
BEFORE the payload is installed and:

1. redeems the setup code (`keld login --code --json`),
2. downloads and verifies the analysis sidecar with a progress bar
   (`keld signal install-sidecar --json --stage-only`),
3. collects which AI tools to configure (`keld signal setup --dry-run --json`).

`scripts/postinstall` then commits the sidecar, applies the tool configuration
(`--bin-path /usr/local/keld/keld`), and registers the agent — all as the console
user, via `launchctl asuser <uid> sudo -u <user> -H`.

## Things that fail SILENTLY here

- **A `SectionOrder` entry without `.bundle`** does not load its section. No error,
  no log line. `installers/macos/plugin_test.sh` asserts against it.
- **A bundle signed before its executable was recompiled** does not load. Same
  silence. `build-plugin.sh` signs last and verifies; `make pkg-plugin-check`
  re-runs that locally.
- **A pane ordered after `Install.bundle`** never appears — the plugin's host
  process stops when installation completes. Measured, not assumed.
- **`sudo` without `-H`** writes the LaunchAgent into `/var/root`.

If the pane does not run at all, `postinstall` falls back to opening
`onboard.command`, which is the pre-wizard Terminal flow and is kept for exactly
this.

## Verifying a build (no production release)

1. `make release-dry` — builds installers as workflow artifacts. **No tag, no
   release**, version `0.0.0-dryrun`. Nothing published, so no live machine can
   resolve it (`install.sh` reads *latest release*; `agent_release` is not served).
2. Download the macOS artifact, run it on a test Mac (`make scaleway-up` for a
   cloud Apple-silicon host).
3. Pair with an **atlas-dev** setup code (`atlas-dev.keld.co/ABCD-EFGH`) so no
   machine lands in the production org.
4. Check, in order:
   - the "Set Up Keld" pane appears after Introduction/License;
   - a bad code is refused inline and **Continue stays disabled**;
   - a good code turns the row green and enables Continue;
   - the engine progress bar advances and does not gate Continue;
   - the tool list shows what is installed, ticked;
   - after the install: `~/.keld/hook.json` holds an `ingest_token`,
     `launchctl print gui/$(id -u)/co.keld.agent` shows the job,
     `~/.local/bin/keld-agent-sidecar/VERSION` matches the pkg,
     and **no Terminal window ever opened**.
5. Signing and notarization: a `workflow_dispatch` run receives the Apple secrets,
   so signing can be exercised without cutting a release. Verdicts have been
   landing in ~25s.
```

- [ ] **Step 3: Update AGENTS.md**

In the "macOS onboarding UI" gotcha bullet, replace the first sentence and keep the
rest of the bullet intact:

```markdown
- **macOS onboarding UI:** onboarding happens INSIDE the installer wizard — a
  custom Installer.app section (`installers/macos/plugin/`, ordered before the
  Install step) that redeems the setup code, downloads the analysis sidecar with a
  progress bar, and collects which AI tools to configure. It renders the NDJSON
  emitted by `keld … --json` and reimplements none of it.
  ⚠️ **A pane CANNOT be placed after the Install step** (measured 2026-09-14,
  macOS 26.5.2: it enters with `installStarted=0` and the plugin's host process
  stops when installation completes), which is why everything interactive is
  pre-install and `scripts/postinstall` does every destructive step afterwards.
  ⚠️ **Two failure modes here are completely silent** — a `SectionOrder` entry
  missing `.bundle` loads nothing, and a bundle signed before its executable was
  recompiled fails to load with no diagnostic at all. Both are pinned by
  `installers/macos/plugin_test.sh`, and `postinstall` falls back to opening
  `onboard.command` whenever the handoff file is absent, because a silently
  missing pane would otherwise leave a machine installed and never asked for a
  code. `installers/macos/onboard.command` is retained for that fallback and for
  MDM; it is no longer opened on the success path.
  See `docs/macos-wizard-onboarding.md`.
```

- [ ] **Step 4: Update the changelog**

Under `## [Unreleased]`, add:

```markdown
### Changed
- **macOS: the installer no longer opens a Terminal.** Onboarding runs inside the
  wizard — a custom Installer.app pane redeems the setup code, downloads the
  analysis sidecar with a progress bar, and collects which AI tools to configure;
  `postinstall` then applies them and starts the agent silently.
  `onboard.command` is retained as the fallback when the pane cannot run.

### Added
- `keld signal install-sidecar` — download, verify and install the analysis
  sidecar, with `--json` NDJSON progress. Replaces the shell copy of that logic.
- `keld signal setup --bin-path` — pin a specific `keld` path into tool hooks.
- `keld login --json` now reports the resolved `api_url`.
```

- [ ] **Step 5: Run everything**

```bash
go test ./...
bash installers/macos/onboard_command_test.sh
bash installers/macos/build_pkg_notarization_test.sh
bash installers/macos/postinstall_test.sh
bash installers/macos/plugin_test.sh
make pkg-plugin-check     # macOS only
```

Expected: all pass. Paste the output — do not claim it passes without it.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/ci.yml AGENTS.md CHANGELOG.md docs/macos-wizard-onboarding.md
git commit -m "docs(installer): wizard onboarding runbook, CI assertions, changelog"
```

- [ ] **Step 7: Manual verification (cannot be automated)**

Follow `docs/macos-wizard-onboarding.md` § "Verifying a build" end to end on a real
Mac, with an atlas-dev code. This is the only check that proves a human can complete
the install, and it is required before any release carrying this change.
