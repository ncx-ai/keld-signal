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
