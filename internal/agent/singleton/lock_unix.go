//go:build darwin || linux

package singleton

import (
	"errors"
	"os"
	"syscall"
)

// openLocked opens path and takes an exclusive, non-blocking flock on it.
//
// ⚠️ THE (held, err) SPLIT IS THE POINT OF THIS SIGNATURE. EWOULDBLOCK is the
// kernel ANSWERING the question — someone else holds it — not a failure to ask.
// Folding it into err is how "another daemon is already running", which has a
// one-line explanation and an obvious remedy, reaches the user as an
// unexplained startup crash. Every other errno stays a real error.
//
// flock is bound to the OPEN FILE DESCRIPTION, which is what makes a crashed
// holder self-healing: the kernel drops the lock when the last descriptor for
// that description closes, and process death closes them all whatever killed
// it. Nothing here has to decide whether a recorded pid is still alive.
func openLocked(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		// flock is interruptible; a signal arriving mid-call is not an answer
		// about the lock, so ask again rather than report a fault.
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		break
	}
	if err == nil {
		return f, false, nil
	}
	_ = f.Close()
	// EWOULDBLOCK and EAGAIN are the same value on darwin and linux; both are
	// named because the constant a reader looks up depends on which manual
	// page they came from.
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return nil, true, nil
	}
	return nil, false, err
}

// releaseLock unlocks and closes.
//
// The explicit LOCK_UN is redundant — closing the last descriptor releases the
// lock — but it makes the release ordered rather than incidental, and it must
// come FIRST: f.Fd() after Close names a closed (and possibly reused)
// descriptor, so unlocking there could release something else's lock.
func releaseLock(f *os.File) error {
	if f == nil {
		return nil
	}
	uerr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	cerr := f.Close()
	if uerr != nil {
		return uerr
	}
	return cerr
}

// readHolderPid reads the pid the holder recorded. Advisory only: flock does
// not restrict reads, so this is a plain open, and any failure means the
// refusal message simply omits the pid.
func readHolderPid(path string) (int, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return parsePid(b)
}
