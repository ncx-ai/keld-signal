# Probe: can an Inno wizard page host WebView2?

**Throwaway. Not shipped, not built by any release workflow.** It exists to
answer ONE question before the Windows wizard design commits to it:

> Can a pure-Go (no cgo) process embed a WebView2 surface into an HWND owned by
> **another** process — specifically an Inno Setup custom wizard page's `TPanel`?

That is the pivot for porting the macOS wizard pane (`installers/macos/plugin/`)
to Windows. macOS gets this free: an `InstallerPane` hosts a `WKWebView`
in-process. Inno's Pascal Script cannot host a browser at all, so the page must
hand its panel's HWND to a helper process.

## What was already measured on a dev machine (2026-09-15)

- **Inno hands out a real, usable HWND.** A custom page's `TPanel.Handle` reached
  a separate process, which read `class="TPanel"`, the owning PID, and the
  client rect (551x310 at that DPI). The handoff is not in doubt.
- ⚠️ **`go-webview2`'s `WebViewOptions.Window` IS ACCEPTED AND THEN IGNORED.**
  `NewWithOptions` never reads the field — it calls `CreateWithOptions`, which
  always `CreateWindowExW`s its OWN top-level window (`webview.go:320`) and
  embeds into that. The probe logged `OK controller created` and then found ZERO
  children under the panel: a success report with nothing embedded. Anyone
  reaching for that field will lose a day to it. Hence `pkg/edge` directly, with
  the child window created here.
- **Pure Go holds.** The host builds at ~3.4 MB with `CGO_ENABLED=0`, so
  `make crosscheck`'s no-cgo rule survives. No MSVC, no C++ DLL.

## Why it runs in CI rather than on a dev machine

Smart App Control (on by default on clean Windows 11) blocked every
freshly-built unsigned binary that creates windows and spawns processes, and one
compiled Inno `setup.exe` besides. SAC cannot be turned back on once disabled
without resetting Windows, so the dev machine is limited to *compiling*.
`windows-latest` runners have no SAC and ship the WebView2 runtime.

⚠️ **That block is itself worth acting on separately:** `keld-setup.exe` is
unsigned today (`installers.yml` has no Windows signing step), so SAC is a live
risk for the shipped installer, not just for this probe.

## Running it

Pushing this branch runs `.github/workflows/windows-wv2-probe.yml`. The push
trigger is scoped to this branch alone, and `workflow_dispatch` is there for
re-runs — but note a `workflow_dispatch` workflow is only triggerable once it
exists on the DEFAULT branch, which this one deliberately never will, so the
push is what actually starts it.

The job prints the host log and uploads `panel.png`.

⚠️ **THE VERDICT IS THE JS ROUND-TRIP IN THE LOG, NOT THE SCREENSHOT.** WebView2
composites through DirectComposition, so `BitBlt` of the host window captures a
BLANK panel however well the page rendered — the first run produced exactly that
while a fully wired render-surface tree sat under the panel. The host therefore
injects a `load` listener that posts `document.title`, the URL and an `h1` back
over the WebView2 message bridge: a message arriving there can only have been
sent by script running in a document the embedded control loaded.

## Layout

- `cmd/harness` — stands in for the Inno wizard: a top-level window with a child
  `STATIC` panel, which spawns the host with that panel's HWND, then captures
  the window to a PNG. (The real caller is Inno's `Exec`; the HWND handoff it
  performs was measured separately, above.)
- `cmd/host` — the real candidate: creates a `WS_CHILD` window under the foreign
  panel and calls `edge.Chromium.Embed` on it. We create that window, so its
  messages arrive on our own thread queue and the loop is ours to pump.

Its own `go.mod`, deliberately: the probe must not add a dependency to the
shipped module.
