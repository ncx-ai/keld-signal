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

// ⚠️ THE FETCH MUST NAME THIS DAEMON'S OWN RELEASE. Unpinned,
// sidecarinstall.Install resolves `releases/latest`, and GoReleaser marks every
// `-rc.N` a PRERELEASE — which that endpoint excludes. Shipped that way in
// v3.0.5-rc.4 and measured the same day: a 3.0.5-rc.4 daemon installed v3.0.4
// and the page then reported, correctly, that the engine it had just fetched was
// out of date. Pinning is what postinstall and onboard.command already do.
func TestTheInstallIsPinnedToThisDaemonsOwnRelease(t *testing.T) {
	version.CLI = "3.0.5-rc.4"
	t.Cleanup(func() { version.CLI = "dev" })

	var got string
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.install = func(o sidecarinstall.Opts) (sidecarinstall.Result, error) {
		got = o.Tag
		return sidecarinstall.Result{}, nil
	}
	srv := engineServer(t, m)
	resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "done" })

	if got != "v3.0.5-rc.4" {
		t.Fatalf("Opts.Tag = %q, want v3.0.5-rc.4 — an empty tag resolves releases/latest, which "+
			"excludes pre-releases and installs a STALE engine under this daemon", got)
	}
}

// A source build names no release, so there is nothing to pin to and
// releases/latest is the only answer available — the same branch the installer
// pane took for its "dryrun" version. Nothing to pin to is not a missing pin.
func TestADevBuildLeavesTheTagUnpinned(t *testing.T) {
	version.CLI = "dev"
	var got = "unset"
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.install = func(o sidecarinstall.Opts) (sidecarinstall.Result, error) {
		got = o.Tag
		return sidecarinstall.Result{}, nil
	}
	srv := engineServer(t, m)
	resp, err := http.Post(srv.URL+"/v1/engine/install", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, 5*time.Second, func() bool { _, st := engineGet(t, srv); return st.Status == "done" })
	if got != "" {
		t.Fatalf("Opts.Tag = %q on a dev build, want empty", got)
	}
}

// The tag carries exactly one leading "v" whether or not version.CLI has one —
// the pane's own "vv3.0.0-rc.5" 404 was this mistake in the other direction.
func TestTheTagCarriesExactlyOneLeadingV(t *testing.T) {
	for _, in := range []string{"3.0.5-rc.4", "v3.0.5-rc.4"} {
		version.CLI = in
		if got := engineTag(); got != "v3.0.5-rc.4" {
			t.Errorf("version.CLI %q -> tag %q, want v3.0.5-rc.4", in, got)
		}
	}
	version.CLI = "dev"
}

// ⚠️ THE DAEMON FIXES ITS OWN ENGINE, IT DOES NOT ASK. A mismatched engine is
// version SKEW — the failure that cost three silent weeks (a 2.3.0 daemon
// against an Aug-11 sidecar: /blocks 404s, zero blocks, doctor reporting no
// problems) — and there is no decision in it for a person to make. Every hour a
// button goes unclicked is an hour of work that cuts no blocks.
func TestAutoStartFixesAMismatchedEngineUnasked(t *testing.T) {
	version.CLI = "3.0.5"
	t.Cleanup(func() { version.CLI = "dev" })

	for _, tc := range []struct {
		name    string
		locate  func(*testing.T) func() (string, bool)
		wantRun bool
	}{
		{"absent", func(*testing.T) func() (string, bool) { return func() (string, bool) { return "", false } }, true},
		{"outdated", func(t *testing.T) func() (string, bool) { return fakeEngine(t, "v3.0.4") }, true},
		{"current", func(t *testing.T) func() (string, bool) { return fakeEngine(t, "v3.0.5") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ran bool
			done := make(chan struct{}, 1)
			m := newEngineManager()
			m.locate = tc.locate(t)
			m.mode = func() string { return "deterministic" }
			m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
				ran = true
				done <- struct{}{}
				return sidecarinstall.Result{}, nil
			}
			m.autoStart()
			if tc.wantRun {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("autoStart did not fetch for a mismatched engine")
				}
			} else {
				time.Sleep(150 * time.Millisecond)
			}
			if ran != tc.wantRun {
				t.Fatalf("install ran = %v, want %v", ran, tc.wantRun)
			}
		})
	}
}

// ml_backend "off" is a choice, and self-healing must not be the one thing that
// quietly overrides it.
func TestAutoStartRespectsAMachineThatWantsNoEngine(t *testing.T) {
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "off" }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		t.Fatal("autoStart fetched an engine on a machine with ml_backend off")
		return sidecarinstall.Result{}, nil
	}
	m.autoStart()
	time.Sleep(150 * time.Millisecond)
}

// ⚠️ ONCE PER RUN, NOT ON A TIMER. A fetch that failed will fail the same way in
// thirty seconds, and retrying on a clock turns a flaky release host into a
// download loop. The state keeps its reason, the page offers Try again, and the
// next daemon start tries once more.
func TestAutoStartDoesNotRetryAFailureOnItsOwn(t *testing.T) {
	version.CLI = "3.0.5"
	t.Cleanup(func() { version.CLI = "dev" })
	var calls int
	m := newEngineManager()
	m.locate = func() (string, bool) { return "", false }
	m.mode = func() string { return "deterministic" }
	m.install = func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
		calls++
		return sidecarinstall.Result{}, errors.New("http status 504")
	}
	m.autoStart()
	waitFor(t, 5*time.Second, func() bool { return m.state().Status == "failed" })
	m.autoStart() // a second run of the same daemon must not pile on
	time.Sleep(150 * time.Millisecond)
	if calls != 1 {
		t.Fatalf("the installer ran %d times after a failure, want 1 — a failed fetch must not loop", calls)
	}
	if m.state().Error == "" {
		t.Fatal("the failure lost its reason; the page would show a bar with nothing to act on")
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
