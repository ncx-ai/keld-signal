package main

import (
	"os"
	"strings"
	"testing"
)

// ⚠️ THIS TEST EXISTS BECAUSE THE WIZARD HUNG ON A REAL INSTALL, AND NOTHING
// COULD HAVE CAUGHT IT.
//
// The panel reports `embedded` as soon as the WebView2 control exists, and the
// Inno page keeps showing "Loading the Keld sign-in page…" until it also sees
// `loaded`. For the whole life of that code `loaded` had exactly ONE source: a
// height message posted by an injected script from a `load` listener. `load`
// waits for every subresource, so a single slow font or beacon on the Atlas
// page withholds it forever — no message, no `loaded`, no timeout, no way
// forward. Measured 2026-09-25 on a signed install: panel `embedded` at
// 11:50:26, no `loaded` ever, WebView2 alive and the URL answering 200.
//
// The defect is not the choice of `load`. It is that a UI reveal was gated on
// ONE signal with NO fallback. So this test reads the source and asserts the
// two structural properties that keep the failure from returning — it cannot
// run the panel (Windows-only, needs a real WebView2 and a parent HWND, and CI
// has neither), and a test that cannot execute the code can still hold the shape
// of it.
func TestPanelHasMoreThanOneReadinessSourceAndADeadline(t *testing.T) {
	b, err := os.ReadFile("panel_windows.go")
	if err != nil {
		t.Fatalf("read panel_windows.go: %v", err)
	}
	src := string(b)

	// 1. WebView2's own completion signal, which does not wait on stragglers.
	if !strings.Contains(src, "NavigationCompletedCallback") {
		t.Error("the panel does not listen for NavigationCompleted - the injected script would be the only thing that can reveal the page, which is how it hung")
	}

	// 2. A deadline. Everything else can fail silently; this is what guarantees
	//    the wizard continues at all. A panel showing an imperfect page beats a
	//    spinner nobody can get past.
	if !strings.Contains(src, "panelLoadDeadline") {
		t.Error("the panel has no load deadline - a page that never finishes leaves the wizard stuck forever with no way forward")
	}
	if !strings.Contains(src, `markLoaded("deadline")`) {
		t.Error("panelLoadDeadline is declared but nothing fires it")
	}

	// 3. All sources must funnel through one emit-once helper. Three callers
	//    each emitting `loaded` would publish it repeatedly, and the wizard's
	//    state machine reads a one-way transition.
	if n := strings.Count(src, `Status: "loaded"`); n != 1 {
		t.Errorf("`loaded` is emitted from %d places; it must funnel through markLoaded so it is published exactly once", n)
	}

	// 4. The injected script must not wait on `load` alone either — that is the
	//    same single-signal mistake one layer down.
	if !strings.Contains(src, `"DOMContentLoaded"`) {
		t.Error("the injected script listens only for load; DOMContentLoaded is what fires when the document is actually usable")
	}
}

// ⚠️ THE PANEL MUST NOT RESIZE ITSELF TO A HEIGHT THE PAGE REPORTS.
//
// It did, from 2026-09-15 until 2026-09-25, and it rendered the sign-in page
// blank on a real install — reported, exactly, as "this used to work". Two
// properties made it unrecoverable rather than merely imperfect:
//
//   - the rule was ONE-WAY ("only ever tighten"), so any report could shrink the
//     panel and none could grow it back; and
//   - the measurement is SELF-REFERENTIAL on the page it runs against. Atlas
//     serves `<html class="h-full">`, so `documentElement.scrollHeight` is the
//     VIEWPORT height, not the content's. The script fed the window its own size
//     back, and one-way shrinking turned that into a ratchet ending at a sliver.
//
// So the message from the page is a readiness ping and nothing else. Anything
// that reintroduces content-fitting must grow as well as shrink AND measure
// something independent of the window's own height — otherwise it rebuilds this.
func TestPanelDoesNotResizeToPageReportedHeight(t *testing.T) {
	b, err := os.ReadFile("panel_windows.go")
	if err != nil {
		t.Fatalf("read panel_windows.go: %v", err)
	}
	src := string(b)

	if strings.Contains(src, "fitHeight") {
		t.Error("fitHeight is back: shrinking the panel to a page-reported height ratcheted it to a sliver and read as a blank page")
	}
	// The one-way rule is the specific shape that made it unrecoverable.
	if strings.Contains(src, "Only ever tighten") {
		t.Error("a one-way shrink rule is back; a panel that can only get smaller cannot recover from one bad reading")
	}
	// The message handler must not feed the reported height into any sizing call.
	i := strings.Index(src, "chromium.MessageCallback")
	if i < 0 {
		t.Fatal("no MessageCallback - the readiness ping is gone")
	}
	end := strings.Index(src[i:], "\n\t}")
	if end < 0 {
		end = len(src) - i
	}
	body := src[i : i+end]
	if strings.Contains(body, "MoveWindow") || strings.Contains(body, "Resize()") {
		t.Error("the page's reported height is being used to resize the panel again")
	}
}

// ⚠️ THE HELPER MUST DECLARE DPI AWARENESS, AND IT MUST DO SO BEFORE IT TOUCHES
// A WINDOW.
//
// Inno Setup 6 is DPI-aware; a plain Go binary has no DPI manifest and is
// therefore DPI-unaware. The two processes then disagree about what the panel's
// coordinates mean. Measured on a real install at 125% scaling: the page
// rendered at ~80% of its frame in both dimensions — exactly 1/1.25 — with the
// remainder as empty margin right and bottom. It was found by a screenshot,
// because nothing in the logs could show it.
//
// The ordering matters as much as the presence: DPI awareness is process-wide
// and can only be set before the first window exists.
func TestPanelDeclaresDPIAwarenessBeforeCreatingWindows(t *testing.T) {
	b, err := os.ReadFile("panel_windows.go")
	if err != nil {
		t.Fatalf("read panel_windows.go: %v", err)
	}
	src := string(b)

	// ⚠️ Look for the CALL, not the name. `strings.Contains(src, "setDPIAware()")`
	// also matches `func setDPIAware() {`, so deleting the call left this guard
	// passing — caught only by testing that it fails, which is the reason every
	// guard here is verified against a broken copy rather than trusted.
	if !strings.Contains(src, "\n\tsetDPIAware()") {
		t.Fatal("the helper never CALLS setDPIAware; under a DPI-aware installer the page renders at a fraction of its frame")
	}
	call := strings.Index(src, "\n\tsetDPIAware()")
	win := strings.Index(src, "pCreateWindowExW.Call")
	if win >= 0 && call > win {
		t.Error("setDPIAware() runs AFTER a window is created; DPI awareness is process-wide and only settable beforehand")
	}
	// The measured-geometry report is what turns this from "looks wrong in a
	// screenshot" into something a log answers.
	if !strings.Contains(src, `Status: "metrics"`) {
		t.Error("nothing reports the window size and the page's viewport together; a DPI mismatch is then invisible to every log")
	}
}

// The injected script lives in a Go RAW STRING, so a backtick anywhere inside it
// silently terminates the literal — which turns into a compile error some lines
// later that names the JavaScript rather than the quoting. It cost a build here.
// The compiler does catch it, so this only makes the reason obvious.
func TestInjectedScriptContainsNoBackticks(t *testing.T) {
	b, err := os.ReadFile("panel_windows.go")
	if err != nil {
		t.Fatalf("read panel_windows.go: %v", err)
	}
	src := string(b)
	start := strings.Index(src, "chromium.Init(`")
	if start < 0 {
		t.Skip("no injected script in this build")
	}
	rest := src[start+len("chromium.Init(`"):]
	end := strings.Index(rest, "`")
	if end < 0 {
		t.Fatal("the injected script's raw string is never closed")
	}
	if strings.Contains(rest[:end], "`") {
		t.Error("the injected script contains a backtick, which ends its raw string early")
	}
}
