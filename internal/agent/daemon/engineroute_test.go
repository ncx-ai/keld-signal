package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/sidecarinstall"
	"github.com/ncx-ai/keld-signal/internal/version"
)

func engineServer(t *testing.T, m *engineManager) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	engineRoute(m)(mux, func(h http.Handler) http.Handler { return h })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func engineGet(t *testing.T, srv *httptest.Server) (int, engineState) {
	t.Helper()
	resp, err := http.Get(srv.URL + "/v1/engine")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st engineState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode /v1/engine: %v", err)
	}
	return resp.StatusCode, st
}

// A tree with a VERSION file, the shape build-freeze.sh writes.
func fakeEngine(t *testing.T, ver string) func() (string, bool) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "keld-agent-sidecar")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ver != "" {
		if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(ver+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return func() (string, bool) { return bin, true }
}

// The page has to be able to tell three things apart before it offers a
// button: there is no engine, there is one and it is current, and there is one
// that is out of date. A single "ok/not ok" would make the third render as the
// second and leave a machine on a stale engine forever — which is the
// three-week no-blocks outage AGENTS.md documents.
func TestEngineStateSeparatesAbsentCurrentAndOutdated(t *testing.T) {
	version.CLI = "3.0.5"
	t.Cleanup(func() { version.CLI = "dev" })

	t.Run("absent", func(t *testing.T) {
		m := newEngineManager()
		m.locate = func() (string, bool) { return "", false }
		m.mode = func() string { return "deterministic" }
		_, st := engineGet(t, engineServer(t, m))
		if st.Installed || st.Outdated || !st.Needed {
			t.Fatalf("state = %+v, want needed and not installed", st)
		}
		if st.Status != "idle" {
			t.Fatalf("status = %q, want idle", st.Status)
		}
	})

	t.Run("current", func(t *testing.T) {
		m := newEngineManager()
		m.locate = fakeEngine(t, "v3.0.5")
		m.mode = func() string { return "deterministic" }
		_, st := engineGet(t, engineServer(t, m))
		// v3.0.5 against 3.0.5: version.Skew normalises the leading v, so this
		// must NOT read as outdated. Getting that wrong offers a 315 MB
		// download to every machine that is already correct.
		if !st.Installed || st.Outdated {
			t.Fatalf("state = %+v, want installed and current", st)
		}
	})

	t.Run("outdated", func(t *testing.T) {
		m := newEngineManager()
		m.locate = fakeEngine(t, "v3.0.4")
		m.mode = func() string { return "deterministic" }
		_, st := engineGet(t, engineServer(t, m))
		if !st.Installed || !st.Outdated || st.Version != "v3.0.4" {
			t.Fatalf("state = %+v, want installed v3.0.4 and outdated", st)
		}
	})
}

// ⚠️ "dev" ON EITHER HALF MEANS CANNOT TELL, NEVER OUTDATED. A source build and
// `make sidecar`'s venv wrapper both report it, and a page that nags every
// developer machine is one nobody reads on the machine that matters — the same
// refusal version.Skew, localagent.ModelState and the doctor check all make.
func TestADevBuildIsNeverReportedOutdated(t *testing.T) {
	version.CLI = "dev"
	m := newEngineManager()
	m.locate = fakeEngine(t, "v3.0.4")
	m.mode = func() string { return "deterministic" }
	_, st := engineGet(t, engineServer(t, m))
	if st.Outdated {
		t.Fatal("a dev daemon called a stamped engine outdated; cannot-tell must not render as a problem")
	}
}

// A tree with no VERSION predates the stamp, so it cannot be compared — but it
// is still INSTALLED, and saying otherwise would offer a fresh download over a
// working engine.
func TestAnUnstampedTreeIsInstalledButNotComparable(t *testing.T) {
	version.CLI = "3.0.5"
	t.Cleanup(func() { version.CLI = "dev" })
	m := newEngineManager()
	m.locate = fakeEngine(t, "")
	m.mode = func() string { return "deterministic" }
	_, st := engineGet(t, engineServer(t, m))
	if !st.Installed || st.Version != "" || st.Outdated {
		t.Fatalf("state = %+v, want installed with no version and not outdated", st)
	}
}

// ml_backend "off" is a choice. The button must not be the one way to put an
// engine on a machine that has said it wants none.
func TestInstallIsRefusedWhenNoEngineIsNeeded(t *testing.T) {
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "off" }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		t.Fatal("an install ran on a machine with ml_backend off")
		return sidecarinstall.Result{}, nil
	}
	srv := engineServer(t, m)
	resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
}

// The POST answers immediately and the page polls. A request that waited out a
// 315 MB download is the installer's own mistake in a different process.
func TestInstallAnswersAtOnceAndReportsProgressThenDone(t *testing.T) {
	release := make(chan struct{})
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.install = func(o sidecarinstall.Opts) (sidecarinstall.Result, error) {
		o.Progress(50, 100)
		<-release
		return sidecarinstall.Result{Version: "v3.0.5"}, nil
	}
	srv := engineServer(t, m)

	done := make(chan int, 1)
	go func() {
		resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	select {
	case code := <-done:
		if code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("POST /v1/engine/install did not answer while the install was still running — it must not block")
	}

	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "running" && st.Received == 50 })
	close(release)
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "done" })
}

// A second click while one is running must not start a second 315 MB fetch.
func TestASecondInstallIsRefusedWhileOneIsRunning(t *testing.T) {
	release := make(chan struct{})
	var starts int
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		starts++
		<-release
		return sidecarinstall.Result{}, nil
	}
	srv := engineServer(t, m)
	post := func() int {
		resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := post(); code != http.StatusAccepted {
		t.Fatalf("first: %d, want 202", code)
	}
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "running" })
	if code := post(); code != http.StatusConflict {
		t.Fatalf("second: %d, want 409", code)
	}
	close(release)
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "done" })
	if starts != 1 {
		t.Fatalf("the installer ran %d times, want 1", starts)
	}
}

// A failure is a sentence the page can print, not a silent return to idle —
// otherwise a machine that cannot download reads exactly like one nobody has
// asked yet.
func TestAFailedInstallKeepsItsReason(t *testing.T) {
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		return sidecarinstall.Result{}, errors.New("retry: gave up after 5 attempt(s): http status 504")
	}
	srv := engineServer(t, m)
	resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "failed" })
	_, st := engineGet(t, srv)
	if st.Error == "" {
		t.Fatal("a failed install reported no reason; the page would show a dead button")
	}
}

// ⚠️ THE FETCH MUST NAME THIS DAEMON'S OWN RELEASE, AND THE DAEMON MUST NOT
// OVERRIDE THAT. The rule itself now lives in sidecarinstall.DefaultTag (one
// definition, so the CLI inherits it — see that function for the two occasions
// an unpinned fetch downgraded a real machine). What this asserts is the half
// the daemon owns: it passes NO tag, which means "my own release", rather than
// substituting one of its own.
func TestTheDaemonDoesNotOverrideTheDefaultPin(t *testing.T) {
	version.CLI = "3.0.5-rc.6"
	t.Cleanup(func() { version.CLI = "dev" })

	var got string
	var seen bool
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.restart = func() error { return nil }
	m.install = func(o sidecarinstall.Opts) (sidecarinstall.Result, error) {
		got, seen = o.Tag, true
		return sidecarinstall.Result{}, nil
	}
	srv := engineServer(t, m)
	resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "done" })

	if !seen {
		t.Fatal("no install ran")
	}
	if got != "" {
		t.Fatalf("Opts.Tag = %q; the daemon must leave it empty so sidecarinstall.DefaultTag pins it", got)
	}
	if want := sidecarinstall.DefaultTag(); want != "v3.0.5-rc.6" {
		t.Fatalf("DefaultTag() = %q, want v3.0.5-rc.6", want)
	}
}

// The automatic attempt is bounded; a person pressing Try again is not. Both
// halves matter: without the first a flaky host loops, without the second a
// failed machine has no way back except a restart.
func TestTryAgainStillWorksAfterTheAutomaticAttemptIsSpent(t *testing.T) {
	version.CLI = "3.0.5"
	t.Cleanup(func() { version.CLI = "dev" })
	var calls int
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		calls++
		if calls == 1 {
			return sidecarinstall.Result{}, errors.New("http status 504")
		}
		return sidecarinstall.Result{Version: "v3.0.5"}, nil
	}
	m.autoStart()
	waitFor(t, 5*time.Second, func() bool { return m.state().Status == "failed" })

	srv := engineServer(t, m)
	resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "done" })
	if calls != 2 {
		t.Fatalf("installer ran %d times, want 2 (one automatic, one by hand)", calls)
	}
}

// ⚠️ THE DAEMON MUST NOT BOUNCE THE SERVICE IT IS RUNNING INSIDE. sidecarinstall
// restarts the local service after a swap — right for the CLI, which is a
// short-lived process installing for somebody else, and fatal here, because
// this process IS that service.
//
// Measured on a real machine 2026-09-22: the engine landed correctly
// (v3.0.5-rc.4 on disk), the daemon then took itself down mid-commit and did
// not come back, and the page sat polling a dead port frozen on
// "Updating… 100%" — a successful install that looked exactly like a hang, and
// left the machine with no daemon at all.
//
// The daemon supervises the sidecar CHILD directly and only ever needed that
// restarted; it is the same call the page's Restart button makes.
func TestTheDaemonRestartsTheSidecarChildNotItself(t *testing.T) {
	version.CLI = "3.0.5"
	t.Cleanup(func() { version.CLI = "dev" })

	var restarted bool
	var gotRestart func() error
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.restart = func() error { restarted = true; return nil }
	m.install = func(o sidecarinstall.Opts) (sidecarinstall.Result, error) {
		gotRestart = o.Restart
		if o.Restart != nil {
			_ = o.Restart()
		}
		return sidecarinstall.Result{Version: "v3.0.5"}, nil
	}
	srv := engineServer(t, m)
	resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "done" })

	if gotRestart == nil {
		t.Fatal("Opts.Restart was nil, so sidecarinstall would bounce the whole service — the daemon would " +
			"kill the process running this very install")
	}
	if !restarted {
		t.Fatal("the sidecar child was never restarted, so the daemon keeps supervising the OLD engine image")
	}
}

// ⚠️ THE MACHINE MUST NEVER SIT IN A STATE NOTHING RE-CHECKS. Measured
// 2026-09-22: the daemon updated the engine to rc.6 correctly at 16:30, a bare
// `keld signal install-sidecar` put v3.0.4 over it at 16:37, and nothing fixed
// it — the one automatic attempt was spent and the page has no button, because
// it reports rather than asks. A red "out of date" with no way to act until
// somebody restarted the daemon.
func TestANewMismatchIsFixedEvenAfterTheFirstAttempt(t *testing.T) {
	version.CLI = "3.0.5-rc.6"
	t.Cleanup(func() { version.CLI = "dev" })

	installed := "v3.0.5-rc.5"
	var calls int
	m := newEngineManager()
	m.mode = func() string { return "deterministic" }
	m.restart = func() error { return nil }
	m.locate = func() (string, bool) { return fakeEngineAt(t, &installed), true }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		calls++
		installed = "v3.0.5-rc.6" // the fetch lands the right one
		return sidecarinstall.Result{}, nil
	}

	m.autoStart()
	waitFor(t, 5*time.Second, func() bool { return calls == 1 })
	waitFor(t, 5*time.Second, func() bool { return m.state().Status == "done" })

	// Same state again: nothing to do, and nothing that could loop.
	m.autoStart()
	time.Sleep(100 * time.Millisecond)
	if calls != 1 {
		t.Fatalf("installs = %d after re-checking a CORRECT engine, want 1", calls)
	}

	// Something downgrades it underneath us — a hand install, a stale tree.
	installed = "v3.0.4"
	m.autoStart()
	waitFor(t, 5*time.Second, func() bool { return calls == 2 })
	if got := m.state().Version; got != "v3.0.5-rc.6" {
		t.Fatalf("engine = %q after the downgrade was corrected, want v3.0.5-rc.6", got)
	}
}

// And the loop guard still holds: a fetch that FAILS leaves the same installed
// version, so re-checking must not hammer a flaky release host.
func TestAFailedFetchIsNotRetriedByRechecking(t *testing.T) {
	version.CLI = "3.0.5-rc.6"
	t.Cleanup(func() { version.CLI = "dev" })

	installed := "v3.0.5-rc.5"
	var calls int
	m := newEngineManager()
	m.mode = func() string { return "deterministic" }
	m.restart = func() error { return nil }
	m.locate = func() (string, bool) { return fakeEngineAt(t, &installed), true }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		calls++
		return sidecarinstall.Result{}, errors.New("http status 504")
	}

	m.autoStart()
	waitFor(t, 5*time.Second, func() bool { return m.state().Status == "failed" })
	for i := 0; i < 5; i++ {
		m.autoStart()
	}
	time.Sleep(150 * time.Millisecond)
	if calls != 1 {
		t.Fatalf("installs = %d after five re-checks of the same failure, want 1 — a flaky host "+
			"must not become a download loop", calls)
	}
}

// A tree whose version can change between reads, for the two tests above.
func fakeEngineAt(t *testing.T, ver *string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "keld-agent-sidecar")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(*ver+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return bin
}
