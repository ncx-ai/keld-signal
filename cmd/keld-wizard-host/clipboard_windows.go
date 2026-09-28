//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// ⚠️ THE CLIPBOARD IS READ HERE BECAUSE INNO CANNOT READ IT. Pascal Script has no
// clipboard function of any kind, and hand-rolling OpenClipboard/GlobalLock
// through `external` declarations means pointer arithmetic in a scripting
// language that has no pointers. This process is already running for every other
// step, so it costs one more mode.
//
// ⚠️ IT REPORTS THE TEXT AND JUDGES NOTHING. Whether the text looks like a
// pairing code is decided by the page, against a predicate that mirrors
// installers/macos/plugin/KeldCode.m — the two platforms must agree about what a
// code looks like, and a second opinion in Go would be a third.

var (
	pOpenClipboard    = user32.NewProc("OpenClipboard")
	pCloseClipboard   = user32.NewProc("CloseClipboard")
	pGetClipboardData = user32.NewProc("GetClipboardData")
	pGlobalLock       = kernel32.NewProc("GlobalLock")
	pGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	pLstrcpynW        = kernel32.NewProc("lstrcpynW")
)

const cfUnicodeText = 13

// clipboardText returns the clipboard's text, or "" when there is none. Every
// failure is empty rather than an error: a clipboard that cannot be read is
// exactly as useful to the caller as one holding something else.
func clipboardText() string {
	ok, _, _ := pOpenClipboard.Call(0)
	if ok == 0 {
		return ""
	}
	defer pCloseClipboard.Call()

	h, _, _ := pGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)

	// Walk to the NUL rather than trusting a length nobody gave us, capped so a
	// pathological clipboard cannot be read without bound. The page rejects
	// anything over 128 characters anyway.
	const maxChars = 4096
	// ⚠️ COPIED OUT BY lstrcpynW RATHER THAN READ THROUGH A Go POINTER. Turning
	// the locked handle into an *uint16 means a uintptr -> unsafe.Pointer
	// conversion, which `go vet` flags and is right to: nothing keeps that address
	// valid across the conversion. Handing the address to the OS as a call
	// argument, and letting it write into a Go buffer, has neither problem.
	buf := make([]uint16, maxChars)
	pLstrcpynW.Call(uintptr(unsafe.Pointer(&buf[0])), p, uintptr(maxChars))
	return syscall.UTF16ToString(buf)
}
