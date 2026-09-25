package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// The page and the helper exchange events as NUMBERED FILES in a directory:
// 0001.json, 0002.json, … each holding exactly one event object.
//
// ⚠️ **THIS IS NOT THE OBVIOUS DESIGN, AND THE OBVIOUS ONE DOES NOT SURVIVE
// PASCAL SCRIPT.** Streaming NDJSON into one file and having the wizard page
// tail it means the page reads a file this process is still writing: Inno's
// file helpers open with their own share mode, a sharing violation is reported
// as "could not read", and a half-flushed line is a truncated JSON object the
// page silently drops. For `authorized` — the LAST line `keld login --json`
// writes — dropping it means a machine that IS paired while the installer says
// the code was refused. That exact failure shipped once already, in the macOS
// pane, for a different reason.
//
// One closed file per event removes the whole class: the page checks whether
// <n>.json exists, reads it whole, and moves on. Write-to-.tmp-then-rename makes
// the appearance of that name atomic, so a file that exists is complete.
type emitter struct {
	dir string
	mu  sync.Mutex
	seq int
}

func newEmitter(dir string) (*emitter, error) {
	if dir == "" {
		return nil, fmt.Errorf("no events directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &emitter{dir: dir}, nil
}

// emit writes one event. A raw JSON line from the child is passed through
// verbatim — the helper must not reinterpret what Go already decided.
func (e *emitter) emit(raw []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	name := fmt.Sprintf("%04d", e.seq)
	tmp := filepath.Join(e.dir, name+".tmp")
	final := filepath.Join(e.dir, name+".json")
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return
	}
	// Rename is what publishes it: the page never sees a partial file.
	_ = os.Rename(tmp, final)
}

func (e *emitter) emitValue(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	e.emit(b)
}

// exitEvent is the last thing every run writes.
//
// ⚠️ **THE PAGE CANNOT KNOW A RUN FINISHED WITHOUT IT.** Inno's `Exec` with
// `ewNoWait` returns no handle and no pid, so "has it stopped?" is unanswerable
// from Pascal Script. This is the answer, and it is written on every path
// including the failures — a run that ends without it leaves the page waiting
// until its own timeout with nothing to show.
type exitEvent struct {
	Event   string `json:"event"`
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

// panelEvent reports whether the WebView2 surface actually came up, so the page
// can fall back to the default browser for the one case that deserves it.
type panelEvent struct {
	Event  string `json:"event"`
	Status string `json:"status"` // embedded | loaded | no_runtime | failed
	// Which of the three readiness sources produced a `loaded`
	// (navigation | script | deadline). Diagnostic only — the wizard keys on
	// Status alone — but without it a panel revealed by the DEADLINE is
	// indistinguishable from one that genuinely loaded, and those want very
	// different follow-up: the first means the page never finished.
	Via string `json:"via,omitempty"`
	// Geometry, reported once, from BOTH sides of the embed: W/H are the child
	// window's client size as this process measures it, VW/VH are the viewport
	// the page believes it has, DPR its devicePixelRatio.
	//
	// ⚠️ They exist because a DPI mismatch between the installer and this helper
	// rendered the page at 80% of its frame, and the only way anyone noticed was
	// a screenshot. Two numbers that should agree, printed side by side, turn
	// that into something a log answers. W==VW means the embed is honest.
	W   int     `json:"w,omitempty"`
	H   int     `json:"h,omitempty"`
	VW  int     `json:"vw,omitempty"`
	VH  int     `json:"vh,omitempty"`
	DPR float64 `json:"dpr,omitempty"`
}

// clipboardEvent carries what the clipboard held, so the wizard page can decide
// whether it looks like a pairing code.
type clipboardEvent struct {
	Event string `json:"event"`
	Text  string `json:"text"`
}

// reportClipboard publishes one clipboard event and exits. It always publishes —
// an empty clipboard is an answer, and a page waiting for a file that never
// arrives is not.
func reportClipboard(o options) int {
	em, err := newEmitter(o.EventsDir)
	if err != nil {
		return 2
	}
	em.emitValue(clipboardEvent{Event: "clipboard", Text: clipboardText()})
	em.emitValue(exitEvent{Event: "__exit", Code: 0})
	return 0
}
