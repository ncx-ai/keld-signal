package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// emit builds an argv that writes the given lines to stdout, one per line.
func emit(lines ...string) (string, []string) {
	if runtime.GOOS == "windows" {
		var b strings.Builder
		for i, l := range lines {
			if i > 0 {
				b.WriteString(" & ")
			}
			b.WriteString("echo " + l)
		}
		return "cmd.exe", []string{"/c", b.String()}
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("printf '%s\\n' '" + l + "'; ")
	}
	return "/bin/sh", []string{"-c", b.String()}
}

// readEvents returns the published event files, in order. A ".tmp" is a file
// mid-write and is never returned — which is the property the page depends on.
func readEvents(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		out = append(out, strings.TrimSpace(string(b)))
	}
	return out
}

func waitForEvents(t *testing.T, dir string, want int, within time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if got := readEvents(t, dir); len(got) >= want {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	return readEvents(t, dir)
}

func hasEvent(events []string, needle string) bool {
	for _, e := range events {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}

// ⚠️ Events must appear AS THEY ARRIVE. The page watches this directory to raise
// the approval panel the moment `device_code` lands; a relay that published only
// on exit would freeze the page for the whole of a device flow.
func TestRelayPublishesEventsIncrementally(t *testing.T) {
	events := filepath.Join(t.TempDir(), "events")
	exe, args := emit(`{"event":"device_code"}`, `{"event":"authorized"}`)

	done := make(chan int, 1)
	go func() { done <- relay(options{Exe: exe, Args: args, EventsDir: events}) }()

	got := waitForEvents(t, events, 2, 15*time.Second)
	if len(got) < 2 {
		t.Fatalf("published %v, want at least 2 events", got)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("relay exit code = %d, want 0", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("relay did not return")
	}
}

// ⚠️ Order is the page's only way to sequence: it reads 0001, 0002, … and a
// device_code arriving after authorized would raise a panel over a finished
// sign-in.
func TestRelayPreservesOrder(t *testing.T) {
	events := filepath.Join(t.TempDir(), "events")
	exe, args := emit(`{"event":"one"}`, `{"event":"two"}`, `{"event":"three"}`)
	if code := relay(options{Exe: exe, Args: args, EventsDir: events}); code != 0 {
		t.Fatalf("relay exit = %d", code)
	}
	got := readEvents(t, events)
	if len(got) != 4 { // three plus __exit
		t.Fatalf("published %d events, want 4: %v", len(got), got)
	}
	for i, want := range []string{"one", "two", "three", "__exit"} {
		if !strings.Contains(got[i], want) {
			t.Fatalf("event %d = %q, want it to contain %q", i, got[i], want)
		}
	}
}

// ⚠️ The page cannot otherwise know a run finished: Inno's ewNoWait Exec returns
// no handle and no pid, so "has it stopped?" is unanswerable from Pascal.
func TestRelayAlwaysPublishesExitEvent(t *testing.T) {
	events := filepath.Join(t.TempDir(), "events")
	exe, args := emit(`{"event":"tool"}`)
	if code := relay(options{Exe: exe, Args: args, EventsDir: events}); code != 0 {
		t.Fatalf("relay exit = %d, want 0", code)
	}
	if got := readEvents(t, events); !hasEvent(got, `"__exit"`) {
		t.Fatalf("no __exit event; the page would wait forever: %v", got)
	}
}

// A child that cannot start is still a finished run, and must still say so.
func TestRelayPublishesExitEventWhenChildCannotStart(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events")
	code := relay(options{
		Exe:       filepath.Join(dir, "definitely-not-here"),
		EventsDir: events,
	})
	if code == 0 {
		t.Fatal("a missing executable must not report success")
	}
	if got := readEvents(t, events); !hasEvent(got, `"__exit"`) {
		t.Fatalf("no __exit event on the start-failure path: %v", got)
	}
}

// The child's exit code reaches the page, so a refused setup code is
// distinguishable from a crash.
func TestRelayReportsChildExitCode(t *testing.T) {
	events := filepath.Join(t.TempDir(), "events")
	var exe string
	var args []string
	if runtime.GOOS == "windows" {
		exe, args = "cmd.exe", []string{"/c", "exit 7"}
	} else {
		exe, args = "/bin/sh", []string{"-c", "exit 7"}
	}
	if code := relay(options{Exe: exe, Args: args, EventsDir: events}); code != 7 {
		t.Fatalf("relay exit = %d, want 7", code)
	}
	if got := readEvents(t, events); !hasEvent(got, `"code":7`) {
		t.Fatalf("exit code not reported to the page: %v", got)
	}
}

// ⚠️ A published file is COMPLETE. The page reads whichever numbered files exist,
// so a partially-written one would be a truncated JSON object it silently drops.
func TestRelayLeavesNoPartialFiles(t *testing.T) {
	events := filepath.Join(t.TempDir(), "events")
	exe, args := emit(`{"event":"a"}`, `{"event":"b"}`, `{"event":"c"}`)
	if code := relay(options{Exe: exe, Args: args, EventsDir: events}); code != 0 {
		t.Fatalf("relay exit = %d", code)
	}
	entries, err := os.ReadDir(events)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("left a partial file behind: %s", e.Name())
		}
	}
	for _, ev := range readEvents(t, events) {
		if !strings.HasPrefix(ev, "{") || !strings.HasSuffix(ev, "}") {
			t.Fatalf("published a non-object event: %q", ev)
		}
	}
}

// The sentinel is how a cancelled wizard stops a device-flow poll that would
// otherwise run to expiry with nobody listening.
func TestSentinelIsNoticed(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "cancel")
	fired := make(chan struct{}, 1)
	watchForExit(sentinel, 0, func() { fired <- struct{}{} })

	if err := os.WriteFile(sentinel, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		t.Fatal("sentinel was never noticed")
	}
}

// The wizard being killed outright writes no sentinel, so a dead parent must end
// the helper too.
func TestDeadParentIsNoticed(t *testing.T) {
	fired := make(chan struct{}, 1)
	watchForExit("", -1, func() { fired <- struct{}{} })
	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		t.Fatal("a dead parent was never noticed")
	}
}

func TestWatchForExitWithNothingToWatchDoesNotFire(t *testing.T) {
	fired := make(chan struct{}, 1)
	watchForExit("", 0, func() { fired <- struct{}{} })
	select {
	case <-fired:
		t.Fatal("fired with neither a sentinel nor a parent pid")
	case <-time.After(time.Second):
	}
}

func TestParseArgsRun(t *testing.T) {
	o, err := parseArgs([]string{
		"--run", `C:\tmp\keld.exe`, "--events-dir", `C:\tmp\events`,
		"--sentinel", `C:\tmp\cancel`, "--parent-pid", "1234",
		"--", "login", "--json", "--no-browser",
	})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.Mode != "run" || o.Exe != `C:\tmp\keld.exe` {
		t.Fatalf("mode/exe = %q/%q", o.Mode, o.Exe)
	}
	if o.EventsDir != `C:\tmp\events` {
		t.Fatalf("events dir = %q", o.EventsDir)
	}
	if o.ParentPID != 1234 {
		t.Fatalf("parent pid = %d", o.ParentPID)
	}
	want := []string{"login", "--json", "--no-browser"}
	if strings.Join(o.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("child args = %v, want %v", o.Args, want)
	}
}

// ⚠️ Everything after `--` belongs to the child. A keld flag that collides with
// one of ours must reach keld untouched rather than being eaten here.
func TestParseArgsDoesNotEatChildFlags(t *testing.T) {
	o, err := parseArgs([]string{
		"--run", "keld.exe", "--events-dir", "evts",
		"--", "login", "--url", "https://example.com", "--sentinel", "x",
	})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.URL != "" || o.Sentinel != "" {
		t.Fatalf("child flags leaked into our options: url=%q sentinel=%q", o.URL, o.Sentinel)
	}
	if len(o.Args) != 5 {
		t.Fatalf("child args = %v, want 5 of them", o.Args)
	}
}

func TestParseArgsPanel(t *testing.T) {
	o, err := parseArgs([]string{
		"--panel", "123456",
		"--url", "https://atlas.keld.co/cli/installer?code=X",
	})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.Mode != "panel" || o.Panel != 123456 {
		t.Fatalf("mode/panel = %q/%d", o.Mode, o.Panel)
	}
}

func TestValidateRejectsIncompleteInvocations(t *testing.T) {
	for name, o := range map[string]options{
		"no mode":           {},
		"run without exe":   {Mode: "run", EventsDir: "x"},
		"run without dir":   {Mode: "run", Exe: "keld.exe"},
		"panel without h":   {Mode: "panel", URL: "https://x"},
		"panel without url": {Mode: "panel", Panel: 1},
	} {
		if err := validate(o); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
