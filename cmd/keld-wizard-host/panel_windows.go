//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sync"
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

// How long the panel waits for any readiness signal before revealing itself
// anyway. Generous: this is a backstop against a page that never finishes, not
// a latency budget, and firing it early would hide a page that was merely slow.
const panelLoadDeadline = 25 * time.Second

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
	// page knows how big the page is, and the attempt to ASK it is what broke —
	// see the block below.
	chromium := edge.NewChromium()

	// ⚠️ **FIT-TO-CONTENT IS GONE, AND IT IS WHAT MADE THE PANEL GO BLANK.**
	// This used to shrink the box to the page's reported height. Two properties
	// made that unrecoverable rather than merely imperfect:
	//
	//  1. It was ONE-WAY. The rule was `if h >= oh-2 { return }` — "only ever
	//     tighten" — so every report could shrink the panel and none could ever
	//     grow it back. A single early small reading was permanent.
	//  2. The measurement is SELF-REFERENTIAL on this page. Atlas serves
	//     `<html class="h-full">`, so `documentElement.scrollHeight` reports the
	//     VIEWPORT height, not the content's. The script was therefore feeding
	//     the window its own size back, and the one-way rule turned that loop
	//     into a ratchet: measure, shrink, measure smaller, shrink again. The
	//     end state is a sliver, which reads as a blank panel.
	//
	// Both were introduced on 2026-09-15 in the same commit as the loading
	// state, which is exactly when the sign-in page stopped rendering — the
	// symptom was reported as "it used to work", and it did.
	//
	// The panel is now simply the size the wizard gives it. That restores the
	// behaviour that worked and costs the cosmetic win the fit was after: the
	// border hugs the panel rather than the form, so a compact form leaves space
	// inside the box. That is a known, accepted trade — a roomy box beats an
	// empty one. Anything that reintroduces content-fitting must (a) grow as
	// well as shrink and (b) measure something that does not depend on the
	// window's own height, or it rebuilds this exact bug.

	// ⚠️ "embedded" IS NOT "LOADED", AND THE PAGE NEEDS THE SECOND ONE. Embedding
	// succeeds the moment the control exists — before a single byte of Atlas has
	// arrived — so a wizard that reveals the panel then shows an empty rectangle
	// for as long as the network takes. The first size report can only come from
	// a document that has fired `load`, so it doubles as the signal that there is
	// something worth looking at.
	// ⚠️ `loaded` HAS THREE SOURCES, AND IT USED TO HAVE ONE — WHICH HUNG THE
	// WIZARD ON A REAL INSTALL. The only trigger was the injected script's first
	// height message, posted from a `load` listener. `load` waits for EVERY
	// subresource, so one slow font or beacon on the Atlas page means it never
	// fires, no message is ever posted, and the page sits on "Loading the Keld
	// sign-in page…" forever with no timeout and nothing to look at. Measured on
	// a real Windows 11 install 2026-09-25: panel `embedded` at 11:50:26, and no
	// `loaded` ever — WebView2 alive, the URL answering 200, the wizard stuck.
	//
	// Gating a UI reveal on the strictest possible readiness signal, with no
	// fallback, is the defect. The three sources, in order of authority:
	//   1. NavigationCompleted — WebView2's own answer to "did the page load",
	//      which does not wait on stragglers and fires on failure too.
	//   2. the script's first height report — kept, since it means the document
	//      has laid out, and it is what sizes the border.
	//   3. a deadline — because a panel showing a half-drawn page is strictly
	//      better than a wizard that never continues. Reveal, and let the person
	//      decide whether what they see is usable.
	var loadedMu sync.Mutex
	loaded := false
	markLoaded := func(via string) {
		loadedMu.Lock()
		first := !loaded
		loaded = true
		loadedMu.Unlock()
		if first && em != nil {
			em.emitValue(panelEvent{Event: "panel", Status: "loaded", Via: via})
		}
	}
	chromium.NavigationCompletedCallback = func(_ *edge.ICoreWebView2, _ *edge.ICoreWebView2NavigationCompletedEventArgs) {
		markLoaded("navigation")
	}
	// The script's message no longer RESIZES anything — it is kept purely as a
	// readiness signal, and it is the strongest of the three: a message can only
	// arrive from a document that parsed and ran JavaScript, which is more than
	// NavigationCompleted proves.
	chromium.MessageCallback = func(s string) {
		var m struct {
			H float64 `json:"h"`
		}
		if json.Unmarshal([]byte(s), &m) != nil {
			return
		}
		markLoaded("script")
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
	// A READINESS PING, NOT A MEASUREMENT. It used to report the document's
	// height so the box could shrink to it; that is what ratcheted the panel to a
	// sliver (see the block above). All that is wanted now is evidence the
	// document parsed and ran JavaScript — which is a stronger statement than
	// NavigationCompleted makes, since that also fires on failure.
	//
	// It still posts the height, because a number that says how tall the page
	// thinks it is costs nothing and is worth having in a diagnostic; nothing
	// acts on it.
	//
	// NOT THE load EVENT ALONE. It waits for every subresource, so one slow font
	// or beacon withholds it indefinitely - which is how this hung on a real
	// install. DOMContentLoaded fires when the document is usable, which is the
	// question being asked.
	// (No backticks in here: this whole script is a Go raw string literal.)
	chromium.Init(`(function () {
  var sent = false;
  function ping() {
    if (sent) return;
    var d = document.documentElement, b = document.body;
    if (!d || !b) return;
    sent = true;
    try {
      window.chrome.webview.postMessage(JSON.stringify({
        h: Math.max(d.scrollHeight, b.scrollHeight)
      }));
    } catch (e) {}
  }
  function schedule() { requestAnimationFrame(ping); }
  document.addEventListener("DOMContentLoaded", schedule);
  window.addEventListener("load", schedule);
  // And if this script somehow runs after the document is already parsed,
  // neither event is coming.
  if (document.readyState !== "loading") { schedule(); }
})();`)
	chromium.Resize()
	chromium.Navigate(o.URL)
	say("embedded")

	// ⚠️ THE DEADLINE IS THE POINT OF THIS WHOLE BLOCK: a wizard that cannot
	// continue is worse than one showing an imperfect page. Everything above can
	// fail silently — a navigation that never completes, a document that never
	// parses — and without this the person is left on a spinner with no way
	// forward and nothing to report. `emit` is mutex-guarded, so firing this from
	// a goroutine is safe; `markLoaded` only ever emits once.
	go func() {
		time.Sleep(panelLoadDeadline)
		markLoaded("deadline")
	}()

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
