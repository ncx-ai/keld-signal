//go:build windows

package singleton

import (
	"errors"
	"os"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION (32) — "the process cannot
// access the file because it is being used by another process", i.e. exactly
// the answer EWOULDBLOCK gives on Unix.
//
// Spelled out rather than imported because the standard `syscall` package does
// not export it on Windows (only a short list of ERROR_* constants is), and
// this package is deliberately dependency-free — reaching for
// golang.org/x/sys/windows to name one integer would put a module dependency
// in go.mod for the daemon's start path.
const errSharingViolation = syscall.Errno(32)

// openLocked opens path for exclusive use, using the OPEN itself as the lock:
// dwShareMode carries FILE_SHARE_READ only, so a second writer's CreateFile
// fails outright with ERROR_SHARING_VIOLATION.
//
// ⚠️ Windows has no flock, but it has the property that actually matters: the
// exclusion lives on the HANDLE, and the OS closes every handle a process held
// when it dies, however it died. A killed daemon therefore cannot lock the next
// one out here either.
//
// FILE_SHARE_READ (rather than no sharing at all) is what keeps readHolderPid
// possible, so the refusal can still name the pid. It shares nothing that would
// let a second daemon take the claim: a would-be holder asks for
// GENERIC_READ|GENERIC_WRITE, and write access is not shared.
//
// LockFileEx would be the closer analogue of flock, and is a reasonable future
// change; it is not used because it is likewise unexported by `syscall` on
// Windows, so it would need the same dependency for no behavioural gain here.
func openLocked(path string) (*os.File, bool, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, false, err
	}
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		syscall.FILE_SHARE_READ,
		nil,
		syscall.OPEN_ALWAYS, // create if absent, open if present
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		// ⚠️ ONLY the sharing violation is "held". ERROR_ACCESS_DENIED is
		// deliberately NOT mapped here: it is what a read-only directory or a
		// permissions problem returns, and reporting that as "another daemon is
		// running" would send an operator hunting a process that does not exist.
		if errors.Is(err, errSharingViolation) {
			return nil, true, nil
		}
		return nil, false, err
	}
	return os.NewFile(uintptr(h), path), false, nil
}

// releaseLock closes the handle, which is what drops the exclusion.
func releaseLock(f *os.File) error {
	if f == nil {
		return nil
	}
	return f.Close()
}

// readHolderPid reads the pid the holder recorded, through an open that shares
// with the holder's handle (it must permit the holder's own GENERIC_WRITE, or
// the read would itself hit a sharing violation).
//
// Advisory only: any failure means the refusal message omits the pid.
func readHolderPid(path string) (int, bool) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}
	h, err := syscall.CreateFile(
		p,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return 0, false
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()

	// A pid is a handful of bytes; a bounded read keeps a garbage lock file
	// from being slurped whole.
	buf := make([]byte, 64)
	n, err := f.Read(buf)
	if n <= 0 {
		_ = err
		return 0, false
	}
	return parsePid(buf[:n])
}
