//go:build windows

package main

import (
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pCreateWindowExW = user32.NewProc("CreateWindowExW")
	pDestroyWindow   = user32.NewProc("DestroyWindow")
	pGetMessageW     = user32.NewProc("GetMessageW")
	pTranslateMsg    = user32.NewProc("TranslateMessage")
	pDispatchMsgW    = user32.NewProc("DispatchMessageW")
	pPostThreadMsgW  = user32.NewProc("PostThreadMessageW")
	pGetClientRect   = user32.NewProc("GetClientRect")
	pIsWindow        = user32.NewProc("IsWindow")
	pMoveWindow      = user32.NewProc("MoveWindow")
	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	pGetCurrentThrd  = kernel32.NewProc("GetCurrentThreadId")
)

const (
	wsChild   = 0x40000000
	wsVisible = 0x10000000
	wmQuit    = 0x0012
)

type winRect struct{ Left, Top, Right, Bottom int32 }

type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

func clientSize(h uintptr) (int32, int32) {
	var r winRect
	pGetClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return r.Right - r.Left, r.Bottom - r.Top
}

// panel embeds a WebView2 surface in a window owned by ANOTHER process — the
// TPanel on the installer's wizard page.
//
// ⚠️ **DO NOT REACH FOR `webview2.NewWithOptions{Window: …}`.** That field is
// accepted and then ignored: NewWithOptions calls CreateWithOptions, which always
// CreateWindowExW's its OWN top-level window (webview.go:320) and embeds into
// that. Measured 2026-09-15 — the controller reported success, EnumChildWindows
// on the panel found nothing, and a floating browser window appeared next to the
// wizard instead of inside it. `pkg/edge` with our own child window is what
// actually works.
//
// ⚠️ **THE CHILD WINDOW HAS TO BE OURS.** A thread cannot pump messages for a
// window another process's thread created, so parenting our own WS_CHILD under
// their panel — rather than embedding directly onto their HWND — is what makes
// the message loop below legal.
func panel(o options) int {
	// The loop must stay on the thread that created the window.
	runtime.LockOSThread()
	mainThread, _, _ := pGetCurrentThrd.Call()

	if alive, _, _ := pIsWindow.Call(o.Panel); alive == 0 {
		fmt.Fprintln(os.Stderr, "keld-wizard-host: panel window does not exist")
		if o.EventsDir != "" {
			if em, err := newEmitter(o.EventsDir); err == nil {
				em.emitValue(panelEvent{Event: "panel", Status: "failed"})
			}
		}
		return 2
	}

	pw, ph := clientSize(o.Panel)
	hinst, _, _ := pGetModuleHandle.Call(0)
	cls, _ := syscall.UTF16PtrFromString("STATIC")
	empty, _ := syscall.UTF16PtrFromString("")
	child, _, err := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(empty)),
		wsChild|wsVisible,
		0, 0, uintptr(pw), uintptr(ph),
		o.Panel, 0, hinst, 0,
	)
	if child == 0 {
		fmt.Fprintln(os.Stderr, "keld-wizard-host: CreateWindowExW:", err)
		return 2
	}
	defer pDestroyWindow.Call(child)

	var em *emitter
	if o.EventsDir != "" {
		em, _ = newEmitter(o.EventsDir)
	}
	say := func(status string) {
		if em != nil {
			em.emitValue(panelEvent{Event: "panel", Status: status})
		}
	}

	chromium := edge.NewChromium()
	if !chromium.Embed(child) {
		// ⚠️ A DISTINCT EXIT CODE, because the page's response is specific: fall
		// back to opening this URL in the default browser. Collapsing it into a
		// generic failure would hide real errors behind a browser window nobody
		// asked for.
		fmt.Fprintln(os.Stderr, "keld-wizard-host: no WebView2 runtime, or embed refused")
		say("no_runtime")
		return exitNoWebView2
	}
	chromium.Resize()
	chromium.Navigate(o.URL)
	say("embedded")

	watchForExit(o.Sentinel, o.ParentPID, func() {
		pPostThreadMsgW.Call(mainThread, wmQuit, 0, 0)
	})

	// Track the panel: an Inno page can be resized with the window, and a webview
	// left at its original size would sit in the corner of a larger panel.
	go func() {
		last := [2]int32{pw, ph}
		for {
			time.Sleep(400 * time.Millisecond)
			if alive, _, _ := pIsWindow.Call(o.Panel); alive == 0 {
				pPostThreadMsgW.Call(mainThread, wmQuit, 0, 0)
				return
			}
			w, h := clientSize(o.Panel)
			if w != last[0] || h != last[1] {
				last = [2]int32{w, h}
				pMoveWindow.Call(child, 0, 0, uintptr(w), uintptr(h), 1)
				chromium.Resize()
			}
		}
	}()

	// ⚠️ **`PostThreadMessageW`, NEVER `PostQuitMessage`.** PostQuitMessage posts
	// WM_QUIT to the CALLING thread's queue; every exit path above runs on a
	// goroutine that is not this thread. Getting this wrong does not fail loudly
	// — it hangs, and the first CI probe burned a full 15-minute job timeout
	// producing no output at all.
	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMsg.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMsgW.Call(uintptr(unsafe.Pointer(&m)))
	}
	return 0
}
