//go:build windows

package main

import (
	"encoding/json"
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
	pDefWindowProcW  = user32.NewProc("DefWindowProcW")
	pRegisterClassEx = user32.NewProc("RegisterClassExW")
	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	pGetCurrentThrd  = kernel32.NewProc("GetCurrentThreadId")

	gdi32             = syscall.NewLazyDLL("gdi32.dll")
	pCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
)

// borderColor is the dark gray drawn around the embedded page, as a COLORREF
// (0x00BBGGRR — Windows orders the bytes blue-green-red, not red-green-blue).
const borderColor = 0x00595959

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// registerBorderClass makes a window class whose background is the border
// colour, and returns its name — or "" when registration fails, in which case
// the caller falls back to an unbordered STATIC.
//
// ⚠️ **THE BORDER HAS TO BE A WINDOW WE OWN.** The first attempt drew it by
// insetting the webview and letting Inno's TPanel colour show through the ring,
// which puts the result at the mercy of the wizard's theming: a themed TPanel
// paints its parent's background and ignores Color entirely, so the border
// silently does not appear and nothing says why. A class background brush is
// painted by this process for a window this process created, so no theme, no
// parent and no Inno version can suppress it.
func registerBorderClass(hinst uintptr) *uint16 {
	name, err := syscall.UTF16PtrFromString("KeldWizardBorder")
	if err != nil {
		return nil
	}
	brush, _, _ := pCreateSolidBrush.Call(borderColor)
	if brush == 0 {
		return nil
	}
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   pDefWindowProcW.Addr(),
		hInstance:     hinst,
		hbrBackground: brush,
		lpszClassName: name,
	}
	// A duplicate registration fails harmlessly — this process registers once.
	if atom, _, _ := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return nil
	}
	return name
}

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
	inset := o.Inset
	if inset < 0 {
		inset = 0
	}
	hinst, _, _ := pGetModuleHandle.Call(0)
	empty, _ := syscall.UTF16PtrFromString("")

	// TWO windows, not one. The OUTER fills the host panel and its only job is to
	// be the border — its class background brush is the border colour, painted by
	// this process. The INNER holds the webview, inset by `inset` pixels, so what
	// shows around it is the outer's background.
	//
	// Falling back to STATIC with inset 0 when the class cannot be registered is
	// deliberate: an unbordered page is a cosmetic loss, and refusing to sign
	// anyone in over a border would not be.
	outerCls := registerBorderClass(hinst)
	if outerCls == nil {
		outerCls, _ = syscall.UTF16PtrFromString("STATIC")
		inset = 0
	}
	outer, _, err := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(outerCls)),
		uintptr(unsafe.Pointer(empty)),
		wsChild|wsVisible,
		0, 0, uintptr(pw), uintptr(ph),
		o.Panel, 0, hinst, 0,
	)
	if outer == 0 {
		fmt.Fprintln(os.Stderr, "keld-wizard-host: CreateWindowExW(outer):", err)
		return 2
	}
	defer pDestroyWindow.Call(outer)

	innerCls, _ := syscall.UTF16PtrFromString("STATIC")
	child, _, err := pCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(innerCls)),
		uintptr(unsafe.Pointer(empty)),
		wsChild|wsVisible,
		uintptr(inset), uintptr(inset),
		uintptr(int(pw)-2*inset), uintptr(int(ph)-2*inset),
		outer, 0, hinst, 0,
	)
	if child == 0 {
		fmt.Fprintln(os.Stderr, "keld-wizard-host: CreateWindowExW(inner):", err)
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

	// ⚠️ THE BORDER MUST HUG THE CONTENT, NOT THE PANEL. The panel is whatever
	// space the wizard page has left over, and Atlas's approval page is a compact
	// form — so bordering the panel drew a box with the form in its top-left
	// corner and a large empty region down and to the right. Nothing outside the
	// page knows how big the page is, so the page is ASKED: a script injected at
	// document-create posts its scroll size back, and the two windows shrink to
	// it.
	//
	// ⚠️ SHRINK ONLY, AND BOUNDED. Resizing the window changes the viewport, which
	// can change the reported size, which would resize again — a loop that shows
	// up as a flickering panel. Growing is never needed (the panel is the maximum)
	// and a small adjustment budget ends it regardless.
	chromium := edge.NewChromium()

	fits := 0
	// fitHeight tightens the box VERTICALLY to the height the page needs, and
	// never touches its width.
	//
	// ⚠️ **FITTING THE WIDTH TOO MADE IT WORSE, AND THE REASON IS WHAT
	// `scrollWidth` MEANS.** It reports the content's MINIMUM width — what the
	// layout collapses to — not the width the page wants. Atlas's approval route
	// has no width constraint at all (a `px-5 py-4` wrapper with `w-full`
	// inputs), so it fills whatever viewport it is given and its scrollWidth is
	// far narrower than any comfortable reading width. Shrinking to it squeezed
	// the form into a column, which is worse than the empty space it was meant to
	// remove. The panel's width is the right width; only its height was wrong.
	fitHeight := func(ch int) {
		if ch <= 0 || fits >= 4 {
			return
		}
		h := ch + 2*inset
		if h > int(ph) {
			h = int(ph)
		}
		_, oh := clientSize(outer)
		// Only ever tighten, and ignore noise.
		if h >= int(oh)-2 {
			return
		}
		fits++
		pMoveWindow.Call(outer, 0, 0, uintptr(pw), uintptr(h), 1)
		pMoveWindow.Call(child, uintptr(inset), uintptr(inset),
			uintptr(int(pw)-2*inset), uintptr(h-2*inset), 1)
		chromium.Resize()
	}

	// ⚠️ "embedded" IS NOT "LOADED", AND THE PAGE NEEDS THE SECOND ONE. Embedding
	// succeeds the moment the control exists — before a single byte of Atlas has
	// arrived — so a wizard that reveals the panel then shows an empty rectangle
	// for as long as the network takes. The first size report can only come from
	// a document that has fired `load`, so it doubles as the signal that there is
	// something worth looking at.
	loaded := false
	chromium.MessageCallback = func(s string) {
		var m struct {
			H float64 `json:"h"`
		}
		if json.Unmarshal([]byte(s), &m) != nil {
			return
		}
		if !loaded {
			loaded = true
			say("loaded")
		}
		fitHeight(int(m.H))
	}
	if !chromium.Embed(child) {
		// ⚠️ A DISTINCT EXIT CODE, because the page's response is specific: fall
		// back to opening this URL in the default browser. Collapsing it into a
		// generic failure would hide real errors behind a browser window nobody
		// asked for.
		fmt.Fprintln(os.Stderr, "keld-wizard-host: no WebView2 runtime, or embed refused")
		say("no_runtime")
		return exitNoWebView2
	}
	// Report the content's own size once it has laid out, and again if the page
	// reflows. `requestAnimationFrame` after `load` is what makes the first
	// reading come after layout rather than during it.
	// Report the content's own HEIGHT once it has laid out, and again whenever it
	// changes — an error message appearing under the form makes the page taller,
	// and a box that did not follow would clip it.
	//
	// ⚠️ HEIGHT ONLY. See fitHeight: `scrollWidth` is the content's MINIMUM width,
	// and fitting to it collapses a full-width form into a column.
	chromium.Init(`(function () {
  var last = -1;
  function report() {
    var d = document.documentElement, b = document.body;
    if (!d || !b) return;
    var h = Math.max(d.scrollHeight, b.scrollHeight);
    if (h === last) return;
    last = h;
    window.chrome.webview.postMessage(JSON.stringify({ h: h }));
  }
  function schedule() { requestAnimationFrame(report); }
  window.addEventListener("load", schedule);
  if (window.ResizeObserver) {
    window.addEventListener("load", function () {
      new ResizeObserver(schedule).observe(document.body);
    });
  }
})();`)
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
				pMoveWindow.Call(outer, 0, 0, uintptr(w), uintptr(h), 1)
				pMoveWindow.Call(child, uintptr(inset), uintptr(inset),
					uintptr(int(w)-2*inset), uintptr(int(h)-2*inset), 1)
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
