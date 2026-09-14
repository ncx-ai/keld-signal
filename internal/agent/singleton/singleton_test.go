package singleton

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ⚠️ EVERY TEST HERE USES t.TempDir() AND NOTHING ELSE. There is a documented
// incident in this repo (AGENTS.md → teleproxy) where a test resolved a real
// path at construction and overwrote the developer's live ~/.keld, erasing
// state a shipped check depended on. A lock file is exactly the kind of thing
// that would do it again: this package's real path is under KELD_HOME, and a
// test that touched it would take, and then drop, the running daemon's claim.

const (
	helperModeEnv = "KELD_SINGLETON_TEST_MODE"
	helperPathEnv = "KELD_SINGLETON_TEST_PATH"

	// Helper exit codes. Distinct values so the parent can tell the three
	// outcomes apart — "refused as held" is an ANSWER and must never be
	// indistinguishable from "the helper blew up", which is the same
	// distinction ErrHeld exists to make one layer down.
	exitAcquired = 0
	exitHeld     = 3
	exitOther    = 4
)

// TestMain re-uses this test binary as the second process. ⚠️ It is what makes
// T2 and T3 mean anything: a lock's whole job is to exclude ANOTHER process,
// and no in-process call can demonstrate that (see
// TestSecondProcessIsRefused's comment).
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperModeEnv); mode != "" {
		os.Exit(helperMain(mode, os.Getenv(helperPathEnv)))
	}
	os.Exit(m.Run())
}

// helperMain runs in the child process. "try" acquires once and reports how it
// went; "hold" acquires and then stays alive until it is killed.
func helperMain(mode, path string) int {
	lock, err := Acquire(path)
	switch {
	case errors.Is(err, ErrHeld):
		// The message is printed so the parent can assert what a real refusal
		// actually says (T4) rather than a message the test composed itself.
		fmt.Println("HELD", err)
		return exitHeld
	case err != nil:
		fmt.Println("ERROR", err)
		return exitOther
	}
	fmt.Println("ACQUIRED", os.Getpid())
	if mode == "try" {
		_ = lock.Release()
		return exitAcquired
	}
	// "hold": keep the process — and therefore the lock — alive. A bare
	// select{} would trip Go's deadlock detector, and the resulting panic would
	// exit the process and DROP the lock, quietly turning T3 into a test that
	// proves nothing.
	for {
		time.Sleep(time.Second)
	}
}

// runTry runs a second process that attempts one Acquire, and returns its exit
// code and stdout.
func runTry(t *testing.T, path string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperModeEnv+"=try", helperPathEnv+"="+path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running the second process: %v (output %q)", err, out)
		}
		return ee.ExitCode(), string(out)
	}
	return 0, string(out)
}

// startHolder starts a second process that acquires path and holds it, and
// returns once that process reports it has the lock. The caller owns killing
// it; a Cleanup is registered so a failing test never leaks the child.
func startHolder(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), helperModeEnv+"=hold", helperPathEnv+"="+path)
	cmd.Stderr = os.Stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the holder process: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(pipe).ReadString('\n')
		line <- s
	}()
	select {
	case s := <-line:
		if !strings.HasPrefix(s, "ACQUIRED ") {
			t.Fatalf("holder process did not take the lock; it said %q", s)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("holder process never reported acquiring the lock")
	}
	return cmd
}

// T1. The base case, plus the one piece of state the lock file carries: the
// holder's pid, which is what a refusal quotes. Nothing DECIDES anything from
// that number — see T3 — so this is checking a diagnostic, not a mechanism.
func TestAcquireOnAFreePathSucceedsAndRecordsOurPid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "daemon.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire on a free path: %v", err)
	}
	defer lock.Release()

	if lock.Path() != path {
		t.Fatalf("Path() = %q, want %q", lock.Path(), path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the lock file: %v", err)
	}
	pid, ok := parsePid(b)
	if !ok {
		t.Fatalf("lock file holds %q, which is not a pid", b)
	}
	if pid != os.Getpid() {
		t.Fatalf("lock file names pid %d, want this process %d", pid, os.Getpid())
	}
}

// T2 — THE TEST THIS PACKAGE EXISTS FOR.
//
// ⚠️ It MUST be a real second OS process. An in-process second Acquire is a
// different question with a platform-dependent answer: flock is bound to the
// open file description, so a descriptor DUPLICATED within one process locks
// without conflict, and Windows' handle-based exclusion has its own rules
// again. A same-process version of this test can therefore pass on the machine
// it was written on while two daemons happily run side by side — reaping each
// other's sidecar and writing the same SQLite store, which is the exact defect
// this package prevents.
func TestSecondProcessIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer lock.Release()

	code, out := runTry(t, path)
	if code != exitHeld {
		t.Fatalf("second process exited %d, want %d (ErrHeld); it said %q", code, exitHeld, out)
	}
}

// T3. A holder that is SIGKILLed cannot have run any cleanup — no deferred
// Release, no signal handler, nothing. The next Acquire must still succeed.
//
// ⚠️ This is the whole argument for an OS lock over a pid file, and if it ever
// fails, the failure mode is that a crashed daemon locks Signal out of the
// machine permanently and only a manual file deletion recovers it. Process.Kill
// is SIGKILL on Unix and TerminateProcess on Windows: uncatchable on both, which
// is the point.
func TestKilledHolderDoesNotLockUsOutForever(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	holder := startHolder(t, path)

	// Precondition: while it lives, we really are excluded. Without this the
	// test could pass against a lock that was never taken at all.
	if _, err := Acquire(path); !errors.Is(err, ErrHeld) {
		t.Fatalf("expected to be refused while the holder lives, got %v", err)
	}

	if err := holder.Process.Kill(); err != nil {
		t.Fatalf("killing the holder: %v", err)
	}
	if _, err := holder.Process.Wait(); err != nil {
		t.Fatalf("waiting for the killed holder: %v", err)
	}

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire after the holder was killed: %v — a crashed daemon must never lock Signal out", err)
	}
	defer lock.Release()
}

// T4. The refusal names the holder, because "another instance is already
// running" without a pid leaves an operator with nothing to look at. Asserted
// from the SECOND process's own output, so what is checked is the message a
// real refusal produces.
func TestRefusalNamesTheHolderPid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer lock.Release()

	code, out := runTry(t, path)
	if code != exitHeld {
		t.Fatalf("second process exited %d, want %d; it said %q", code, exitHeld, out)
	}
	if !strings.Contains(out, strconv.Itoa(os.Getpid())) {
		t.Fatalf("refusal %q does not name the holder pid %d", strings.TrimSpace(out), os.Getpid())
	}
}

// T5. A directory we cannot write to is a REAL error — not ErrHeld, and
// emphatically not a nil error with an unusable Lock.
//
// ⚠️ Removing this is how "the lock could not be taken" quietly becomes
// "nobody else is running", which would hand a second daemon a claim it never
// obtained. The house rule is that a check which could not run must never
// publish a confident negative.
func TestUnwritableDirectoryIsARealError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory modes do not govern writability on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root: a 0500 directory is still writable, so this proves nothing")
	}
	ro := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatalf("preparing the read-only dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) }) // so t.TempDir can clean up

	lock, err := Acquire(filepath.Join(ro, "nested", "daemon.lock"))
	if err == nil {
		_ = lock.Release()
		t.Fatal("Acquire succeeded under an unwritable directory")
	}
	if errors.Is(err, ErrHeld) {
		t.Fatalf("a filesystem failure was reported as ErrHeld: %v", err)
	}
}

// Release hands the claim back without the process exiting — checked against a
// real second process, since that is who the claim is being handed to.
func TestReleaseFreesTheLockForAnotherProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if code, out := runTry(t, path); code != exitHeld {
		t.Fatalf("precondition: second process exited %d before Release, want %d (%q)", code, exitHeld, out)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if code, out := runTry(t, path); code != exitAcquired {
		t.Fatalf("second process exited %d after Release, want %d (%q)", code, exitAcquired, out)
	}
}

// Release must tolerate being called twice and on a nil *Lock, so `defer
// lock.Release()` beside an explicit shutdown path is not a bug — and so a
// caller that failed to Acquire can still write the defer unconditionally.
//
// ⚠️ Without this, the second Release would unlock and close an fd number the
// runtime may have handed to something else, and the damage would land on an
// unrelated file rather than here.
func TestReleaseIsSafeTwiceAndOnNil(t *testing.T) {
	var nilLock *Lock
	if err := nilLock.Release(); err != nil {
		t.Fatalf("Release on a nil *Lock: %v", err)
	}
	if p := nilLock.Path(); p != "" {
		t.Fatalf("Path on a nil *Lock = %q, want empty", p)
	}

	path := filepath.Join(t.TempDir(), "daemon.lock")
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	// And the lock really is free afterwards — a double Release must not have
	// left the file in a state nobody can take.
	if code, out := runTry(t, path); code != exitAcquired {
		t.Fatalf("after a double Release the second process exited %d, want %d (%q)", code, exitAcquired, out)
	}
}

// An empty path is rejected rather than silently locking something in the
// working directory. filepath.Dir("") is ".", so without the guard this would
// create a lock file wherever the daemon happened to be started from — a
// different resource per launch, i.e. no exclusion at all.
func TestEmptyPathIsRejected(t *testing.T) {
	lock, err := Acquire("")
	if err == nil {
		_ = lock.Release()
		t.Fatal("Acquire(\"\") succeeded")
	}
	if errors.Is(err, ErrHeld) {
		t.Fatalf("an empty path was reported as ErrHeld: %v", err)
	}
}

// A stale pid left by an earlier holder must be OVERWRITTEN, not appended to or
// partially covered: "999" written over "123456" leaves "999456", a pid that
// never held anything, in the one field a refusal quotes.
func TestPidIsTruncatedNotOverwrittenInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")
	if err := os.WriteFile(path, []byte("999999999999\n"), 0o600); err != nil {
		t.Fatalf("seeding a stale pid: %v", err)
	}
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire over a stale pid file: %v", err)
	}
	defer lock.Release()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the lock file: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file holds %q, want exactly %d", got, os.Getpid())
	}
}
