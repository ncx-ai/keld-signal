package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRestart struct {
	n   int32
	err error
}

func (f *fakeRestart) Restart() error { atomic.AddInt32(&f.n, 1); return f.err }
func (f *fakeRestart) count() int     { return int(atomic.LoadInt32(&f.n)) }

type recorded struct {
	mu     sync.Mutex
	events []string
	fields []map[string]any
}

func (r *recorded) emit(code, sev string, f map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, code)
	r.fields = append(r.fields, f)
}

func (r *recorded) has(code string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e == code {
			return true
		}
	}
	return false
}

func (r *recorded) fieldsFor(code string) map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, e := range r.events {
		if e == code {
			return r.fields[i]
		}
	}
	return nil
}

// buildRelease serves a release whose CLI archive holds keld + keld-agent and
// whose sidecar archive holds a nested keld-agent-sidecar tree.
func buildRelease(t *testing.T, version string) (base string, rs *releaseServer) {
	t.Helper()
	rs = newReleaseServer()
	cliName, scName := AssetNames("linux", "amd64")
	cli := tarBytes(t, map[string]string{
		"keld":       "keld-" + version,
		"keld-agent": "agent-" + version,
	})
	sc := tarBytes(t, map[string]string{
		"keld-agent-sidecar/keld-agent-sidecar": "sidecar-" + version,
		"keld-agent-sidecar/lib/a.so":           "lib-" + version,
	})
	rs.assets[cliName] = cli
	rs.assets[scName] = sc
	rs.checksums = fmt.Sprintf("%s  %s\n%s  %s\n", shaOf(cli), cliName, shaOf(sc), scName)
	return rs.start(t), rs
}

func shaOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func tarBytes(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// installed lays out a machine already running `version`.
func installed(t *testing.T, version string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	write(t, filepath.Join(dir, "keld"), "keld-"+version)
	write(t, filepath.Join(dir, "keld-agent"), "agent-"+version)
	write(t, filepath.Join(dir, "keld-agent-sidecar", "keld-agent-sidecar"), "sidecar-"+version)
	write(t, filepath.Join(dir, "keld-agent-sidecar", "lib", "a.so"), "lib-"+version)
	return dir
}

func newTestUpdater(t *testing.T, dir, base, cur string, r *fakeRestart, rec *recorded) *Updater {
	t.Helper()
	nested, _ := DetectSidecarLayout(dir)
	return &Updater{
		Current:     cur,
		StatePath:   filepath.Join(t.TempDir(), "state.json"),
		Dest:        Dest{BinDir: dir, SidecarDir: dir, SidecarNested: nested, HasSidecar: true, Writable: true},
		Fetch:       &Fetcher{HTTP: &http.Client{Timeout: 5 * time.Second}, BaseURL: base, Policy: fastPolicy()},
		Restarter:   r,
		Probe:       func(string) error { return nil },
		Now:         func() time.Time { return now },
		Emit:        rec.emit,
		MinInterval: time.Hour,
		GOOS:        "linux",
		GOARCH:      "amd64",
	}
}

func TestApplyReplacesEverythingAndRestarts(t *testing.T) {
	dir := installed(t, "v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	if read(t, filepath.Join(dir, "keld")) != "keld-v2" {
		t.Fatal("cli not replaced")
	}
	if read(t, filepath.Join(dir, "keld-agent")) != "agent-v2" {
		t.Fatal("daemon not replaced")
	}
	if read(t, filepath.Join(dir, "keld-agent-sidecar", "keld-agent-sidecar")) != "sidecar-v2" {
		t.Fatal("sidecar not replaced")
	}
	if r.count() != 1 {
		t.Fatalf("restarts = %d, want 1", r.count())
	}
	s, err := LoadState(u.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if !s.PendingConfirm || s.To != "v2" || s.From != "v1" || len(s.Prev) == 0 {
		t.Fatalf("state = %+v", s)
	}
	if !rec.has("update.staged") {
		t.Fatalf("events = %v", rec.events)
	}
}

// The pre-flight probe runs the downloaded keld-agent before anything is
// swapped. A wrong-architecture binary hashes correctly and cannot run, and
// catching it here costs milliseconds instead of a rollback cycle.
func TestApplyAbortsBeforeAnySwapWhenTheProbeFails(t *testing.T) {
	dir := installed(t, "v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)
	u.Probe = func(string) error { return errors.New("exec format error") }

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	if read(t, filepath.Join(dir, "keld")) != "keld-v1" {
		t.Fatal("the destination was modified despite a failing probe")
	}
	if r.count() != 0 {
		t.Fatal("must not restart")
	}
	if !rec.has("update.failed") {
		t.Fatalf("events = %v", rec.events)
	}
	s, _ := LoadState(u.StatePath)
	if s.PendingConfirm {
		t.Fatal("nothing was swapped, so nothing is pending")
	}
	if s.LastOutcome != "failed" {
		t.Fatalf("outcome = %q", s.LastOutcome)
	}
}

func TestApplyLeavesTheInstallAloneWhenTheFetchFails(t *testing.T) {
	dir := installed(t, "v1")
	rs := newReleaseServer()
	rs.checksums = "" // nothing published at all
	base := rs.start(t)
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	if read(t, filepath.Join(dir, "keld")) != "keld-v1" {
		t.Fatal("install modified after a failed fetch")
	}
	if r.count() != 0 {
		t.Fatal("must not restart")
	}
	// And no staging litter is left in the destination.
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if len(e.Name()) > 13 && e.Name()[:13] == ".keld-update." {
			t.Fatalf("staging dir left behind: %s", e.Name())
		}
	}
}

// The settings poll calls Maybe on every tick. Two overlapping calls must
// produce one apply, not two racing swaps of the same files.
func TestMaybeIsSingleFlight(t *testing.T) {
	dir := installed(t, "v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); u.Maybe(context.Background(), Target{Version: "v2", Enabled: true}) }()
	}
	wg.Wait()
	u.Wait()
	if r.count() != 1 {
		t.Fatalf("restarts = %d, want exactly 1", r.count())
	}
}

func TestMaybeEmitsTheSkipReasonAndTouchesNothing(t *testing.T) {
	dir := installed(t, "v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: false})
	u.Wait()

	if !rec.has("update.skipped") {
		t.Fatalf("events = %v", rec.events)
	}
	if got := rec.fieldsFor("update.skipped")["reason"]; got != ReasonDisabled {
		t.Fatalf("reason = %v", got)
	}
	if read(t, filepath.Join(dir, "keld")) != "keld-v1" {
		t.Fatal("a skip must not touch disk")
	}
	if _, err := os.Stat(u.StatePath); err == nil {
		t.Fatal("a skip must not write a state marker")
	}
}

// A restart that fails leaves the marker PENDING on purpose: the swap already
// happened, so the next start — by whatever binary the service manager brings
// up — must still see an unproven update and be able to undo it.
func TestARestartFailureLeavesTheUpdatePending(t *testing.T) {
	dir := installed(t, "v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{err: errors.New("systemctl: unit not loaded")}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	s, _ := LoadState(u.StatePath)
	if !s.PendingConfirm {
		t.Fatal("a failed restart must leave the update unproven, not resolved")
	}
	if !rec.has("update.restart_failed") {
		t.Fatalf("events = %v", rec.events)
	}
}

// Quiesce drains the enrichment queue. It must run BEFORE the restart, never
// after — afterwards there is no process left to wait for.
func TestQuiesceRunsBeforeTheRestart(t *testing.T) {
	dir := installed(t, "v1")
	base, _ := buildRelease(t, "v2")
	var order []string
	var mu sync.Mutex
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)
	u.Quiesce = func(context.Context) { mu.Lock(); order = append(order, "quiesce"); mu.Unlock() }
	inner := u.Restarter
	u.Restarter = restartFunc(func() error {
		mu.Lock()
		order = append(order, "restart")
		mu.Unlock()
		return inner.Restart()
	})

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	if len(order) != 2 || order[0] != "quiesce" || order[1] != "restart" {
		t.Fatalf("order = %v", order)
	}
}

// A machine with no sidecar installed (a fresh curl install that failed the
// sidecar fetch, or a Windows box mid-install) still updates its binaries
// rather than refusing outright.
func TestApplyWithNoSidecarInstalled(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "keld"), "keld-v1")
	write(t, filepath.Join(dir, "keld-agent"), "agent-v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)
	u.Dest.HasSidecar = false

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	if read(t, filepath.Join(dir, "keld-agent")) != "agent-v2" {
		t.Fatal("binaries should still update")
	}
	if _, err := os.Stat(filepath.Join(dir, "keld-agent-sidecar")); err == nil {
		t.Fatal("a sidecar must not be conjured onto a machine that had none")
	}
}

// A flat sidecar layout (Windows Inno) must stay flat after an update.
func TestApplyPreservesAFlatSidecarLayout(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "keld"), "keld-v1")
	write(t, filepath.Join(dir, "keld-agent"), "agent-v1")
	write(t, filepath.Join(dir, "keld-agent-sidecar"), "sidecar-v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)
	u.Dest.SidecarNested = false

	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	fi, err := os.Stat(filepath.Join(dir, "keld-agent-sidecar"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.IsDir() {
		t.Fatal("a flat install must not become nested")
	}
	if read(t, filepath.Join(dir, "keld-agent-sidecar")) != "sidecar-v2" {
		t.Fatal("flat sidecar not replaced")
	}
}

type restartFunc func() error

func (f restartFunc) Restart() error { return f() }

// The Windows path must be reachable from a Linux test, or it is only ever
// exercised on the platform where a mistake is most expensive to find.
func TestBinaryNamesAreNamedForTheTargetNotTheHost(t *testing.T) {
	got := binaryNamesFor("windows")
	if len(got) != 2 || got[0] != "keld.exe" || got[1] != "keld-agent.exe" {
		t.Fatalf("got %v", got)
	}
	if l := binaryNamesFor("linux"); l[0] != "keld" {
		t.Fatalf("got %v", l)
	}
}

// KELD_AUTOUPDATE=0 refuses locally whatever Atlas says.
func TestLocalKillSwitchIsHonouredByMaybe(t *testing.T) {
	t.Setenv("KELD_AUTOUPDATE", "0")
	dir := installed(t, "v1")
	base, _ := buildRelease(t, "v2")
	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)
	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()
	if r.count() != 0 || read(t, filepath.Join(dir, "keld")) != "keld-v1" {
		t.Fatal("local kill switch ignored")
	}
	if got := rec.fieldsFor("update.skipped")["reason"]; got != ReasonDisabledLocal {
		t.Fatalf("reason = %v", got)
	}
}

// A release whose sidecar archive is the wrong shape must not leave the
// machine with a half-swapped install.
func TestASidecarArchiveOfTheWrongShapeRollsBack(t *testing.T) {
	dir := installed(t, "v1")
	rs := newReleaseServer()
	cliName, scName := AssetNames("linux", "amd64")
	cli := tarBytes(t, map[string]string{"keld": "keld-v2", "keld-agent": "agent-v2"})
	sc := tarBytes(t, map[string]string{"unexpected/thing": "x"})
	rs.assets[cliName] = cli
	rs.assets[scName] = sc
	rs.checksums = fmt.Sprintf("%s  %s\n%s  %s\n", shaOf(cli), cliName, shaOf(sc), scName)
	base := rs.start(t)

	r := &fakeRestart{}
	rec := &recorded{}
	u := newTestUpdater(t, dir, base, "v1", r, rec)
	u.Maybe(context.Background(), Target{Version: "v2", Enabled: true})
	u.Wait()

	if read(t, filepath.Join(dir, "keld")) != "keld-v1" {
		t.Fatal("binaries were swapped despite an unusable sidecar archive")
	}
	if read(t, filepath.Join(dir, "keld-agent-sidecar", "keld-agent-sidecar")) != "sidecar-v1" {
		t.Fatal("sidecar was damaged")
	}
	if r.count() != 0 {
		t.Fatal("must not restart")
	}
}
