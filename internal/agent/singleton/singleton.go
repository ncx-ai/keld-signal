// Package singleton gives a process an exclusive claim on a resource
// identified by a filesystem path, so a second copy of the daemon can refuse
// to start rather than fight the first one.
//
// ⚠️ WHY THIS EXISTS AT ALL. `internal/agent/daemon/reap_unix.go` kills every
// process matching the sidecar's basename before spawning its own, and its doc
// comment justifies that with "under single-instance service management any
// such process is stale". Nothing in this repo enforced that premise. Two
// `keld-agent run` processes against one KELD_HOME therefore take turns reaping
// each other's sidecar — each spawn kills the other's child — while both write
// to the same refseries.db. This package is the enforcement the reaper already
// assumed it had.
//
// ⚠️ AND WHY IT IS AN OS LOCK RATHER THAN A PID FILE. A pid file OUTLIVES its
// writer: a daemon that is SIGKILLed (launchd `kickstart -k`, the OOM killer,
// power loss) leaves one behind, so the next start has to guess whether the
// recorded pid is a live holder or a corpse — and pids are recycled, so that
// guess is wrong at exactly the moment it matters. A lock held on an OPEN FILE
// DESCRIPTION has no such state: the kernel drops it when the process ends by
// ANY means, including one it could not have run cleanup for. "A crashed daemon
// can never lock Signal out of its own machine" is then free, not a property
// somebody has to keep reasoning about.
//
// The pid IS recorded in the file, but only so a refusal can say who is holding
// it. Nothing decides anything from that number.
package singleton

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ErrHeld reports that another LIVE process holds the lock. Callers test it
// with errors.Is; Acquire wraps it in a message naming the holder's pid where
// that could be read.
//
// ⚠️ It is deliberately its own sentinel rather than a generic error: "another
// daemon is already running" is a normal, explainable startup outcome, and a
// caller that cannot distinguish it from a broken filesystem reports the wrong
// thing to the user.
var ErrHeld = errors.New("another instance is already running")

// Lock is a held claim. The zero value is not usable; get one from Acquire.
type Lock struct {
	path string
	f    *os.File
	once sync.Once
}

// Acquire takes the lock at path, creating the file and its parent directory if
// needed.
//
// It returns an error wrapping ErrHeld when another live process holds it, and
// an ordinary error for anything else — an uncreatable directory, a read-only
// home, a path that is not a file. Those two are never conflated: this package
// does not answer "probably fine".
func Acquire(path string) (*Lock, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("singleton: empty lock path")
	}
	dir := filepath.Dir(path)
	// 0700 / 0600 to match how everything under ~/.keld is treated.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("singleton: creating %s: %w", dir, err)
	}

	f, held, err := openLocked(path)
	if err != nil {
		return nil, fmt.Errorf("singleton: locking %s: %w", path, err)
	}
	if held {
		// Diagnostic only. A pid we cannot read costs the message its detail
		// and nothing else — turning an unreadable holder file into a second
		// error class would make "another daemon is running" report as a
		// filesystem fault.
		if pid, ok := readHolderPid(path); ok {
			return nil, fmt.Errorf("singleton: %s is held by pid %d: %w", path, pid, ErrHeld)
		}
		return nil, fmt.Errorf("singleton: %s is held (holder pid unreadable): %w", path, ErrHeld)
	}

	if err := recordPid(f); err != nil {
		_ = releaseLock(f)
		return nil, fmt.Errorf("singleton: recording pid in %s: %w", path, err)
	}
	return &Lock{path: path, f: f}, nil
}

// Release drops the lock. It is safe on a nil *Lock and safe to call twice, so
// a `defer lock.Release()` beside an explicit shutdown path is not a bug.
//
// Note the lock does not DEPEND on this running: the kernel releases it when the
// process exits. Release exists so a long-lived process can hand the claim back
// without exiting, and so tests can.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	var err error
	l.once.Do(func() { err = releaseLock(l.f) })
	return err
}

// Path returns the lock file's path (empty for a nil *Lock), so a caller can
// name it in a log line without keeping a second copy of the string.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// recordPid writes this process's pid over whatever the file held.
//
// ⚠️ Truncate first. Writing "999" over "123456" leaves "999456" — a pid that
// never held anything, in a file whose whole purpose is to name the holder.
func recordPid(f *os.File) error {
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	if _, err := f.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		return err
	}
	return f.Sync()
}

// parsePid reads the pid out of a lock file's contents. Anything unparseable is
// "not readable", never an error: see the call site in Acquire.
func parsePid(b []byte) (int, bool) {
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
