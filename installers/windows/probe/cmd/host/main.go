// The candidate: host a WebView2 surface inside an HWND owned by ANOTHER
// process — an Inno Setup wizard page's TPanel.
//
// ⚠️ THE OBVIOUS ROUTE MEASURED FALSE. webview2.NewWithOptions{Window: parent}
// accepts the field and ignores it: it calls CreateWithOptions, which always
// CreateWindowExW's its own top-level window (webview.go:320) and embeds into
// that. The controller reported success, EnumChildWindows on the panel found
// nothing, and a floating window appeared instead of an embedded one.
//
// So this owns the window: a WS_CHILD created with the foreign panel as
// hWndParent, then edge.Chromium.Embed on it. We created that window, so its
// messages arrive on OUR thread queue and the loop below is ours to pump —
// which is the part that could not work the other way round, since a thread
// cannot pump messages for a window another process's thread created.
//
// Usage: host.exe <parentHWND decimal> <url> <logpath> [seconds]
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pCreateWindowExW = user32.NewProc("CreateWindowExW")
	pGetMessageW     = user32.NewProc("GetMessageW")
	pTranslateMsg    = user32.NewProc("TranslateMessage")
	pDispatchMsgW    = user32.NewProc("DispatchMessageW")
	pPostQuitMessage = user32.NewProc("PostQuitMessage")
	pGetClientRect   = user32.NewProc("GetClientRect")
	pGetParent       = user32.NewProc("GetParent")
	pEnumChildWin    = user32.NewProc("EnumChildWindows")
	pGetClassNameW   = user32.NewProc("GetClassNameW")
	pGetWindowThread = user32.NewProc("GetWindowThreadProcessId")
	pIsWindowVisible = user32.NewProc("IsWindowVisible")
	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
)

const (
	wsChild   = 0x40000000
	wsVisible = 0x10000000
)

type rect struct{ Left, Top, Right, Bottom int32 }

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

var out *os.File

func logf(format string, a ...any) {
	fmt.Fprintf(out, format+"\n", a...)
	out.Sync()
}

func className(h uintptr) string {
	b := make([]uint16, 256)
	pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return syscall.UTF16ToString(b)
}

func pidOf(h uintptr) uint32 {
	var pid uint32
	pGetWindowThread.Call(h, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func clientSize(h uintptr) (int32, int32) {
	var r rect
	pGetClientRect.Call(h, uintptr(unsafe.Pointer(&r)))
	return r.Right - r.Left, r.Bottom - r.Top
}

// describeTree walks every descendant of root, so the log says whether a
// WebView2 surface actually landed under the panel rather than merely whether a
// call returned true.
func describeTree(root uintptr, label string) int {
	n := 0
	var walk func(uintptr, string)
	walk = func(parent uintptr, indent string) {
		cb := syscall.NewCallback(func(h uintptr, _ uintptr) uintptr {
			p, _, _ := pGetParent.Call(h)
			if p != parent {
				return 1 // EnumChildWindows is recursive; keep one level per pass
			}
			n++
			w, ht := clientSize(h)
			vis, _, _ := pIsWindowVisible.Call(h)
			logf("  [%s]%s hwnd=%d class=%q pid=%d size=%dx%d visible=%d",
				label, indent, h, className(h), pidOf(h), w, ht, vis)
			walk(h, indent+"  ")
			return 1
		})
		pEnumChildWin.Call(parent, cb, 0)
	}
	walk(root, "")
	if n == 0 {
		logf("  [%s] no descendants", label)
	}
	return n
}

func main() {
	// The message loop must stay on the thread that created the window.
	runtime.LockOSThread()

	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: host <parentHWND> <url> <logpath> [seconds]")
		os.Exit(2)
	}
	var err error
	out, err = os.Create(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	defer out.Close()

	h, err := strconv.ParseUint(os.Args[1], 10, 64)
	if err != nil {
		logf("FAIL bad HWND %q: %v", os.Args[1], err)
		os.Exit(2)
	}
	parent := uintptr(h)
	secs := 20
	if len(os.Args) > 4 {
		if n, e := strconv.Atoi(os.Args[4]); e == nil {
			secs = n
		}
	}

	pw, ph := clientSize(parent)
	logf("host pid=%d parent=%d class=%q parentPID=%d size=%dx%d url=%s",
		os.Getpid(), parent, className(parent), pidOf(parent), pw, ph, os.Args[2])
	if pidOf(parent) == uint32(os.Getpid()) {
		logf("FAIL parent window belongs to this process — the probe is not testing cross-process embedding")
		os.Exit(1)
	}
	describeTree(parent, "before")

	hinst, _, _ := pGetModuleHandle.Call(0)
	cls, _ := syscall.UTF16PtrFromString("STATIC")
	empty, _ := syscall.UTF16PtrFromString("")
	child, _, e1 := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(empty)),
		wsChild|wsVisible,
		0, 0, uintptr(pw), uintptr(ph),
		parent, 0, hinst, 0,
	)
	if child == 0 {
		logf("FAIL CreateWindowExW(child of foreign HWND) returned 0: %v", e1)
		os.Exit(1)
	}
	gotParent, _, _ := pGetParent.Call(child)
	if gotParent != parent {
		logf("FAIL child created but GetParent=%d, want %d", gotParent, parent)
		os.Exit(1)
	}
	logf("PASS created WS_CHILD hwnd=%d under foreign panel %d (our pid=%d)", child, parent, os.Getpid())

	chromium := edge.NewChromium()
	if !chromium.Embed(child) {
		logf("FAIL edge.Chromium.Embed returned false — no WebView2 runtime, or embed refused")
		os.Exit(1)
	}
	logf("PASS edge.Chromium.Embed succeeded")
	chromium.Resize()
	chromium.Navigate(os.Args[2])
	logf("navigating to %s", os.Args[2])

	go func() {
		time.Sleep(6 * time.Second)
		n := describeTree(parent, "after")
		if n == 0 {
			logf("FAIL nothing rendered under the panel — Embed reported success but produced no window")
		} else {
			logf("PASS %d descendant window(s) under the panel after navigate", n)
		}
		cw, ch := clientSize(child)
		logf("child client size now %dx%d", cw, ch)
		time.Sleep(time.Duration(secs) * time.Second)
		logf("host: posting quit")
		pPostQuitMessage.Call(0)
	}()

	logf("entering message loop")
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMsg.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMsgW.Call(uintptr(unsafe.Pointer(&m)))
	}
	logf("message loop returned; host done")
}
