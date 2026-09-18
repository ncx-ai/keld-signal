package integrations

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// headRecords bounds how far into a transcript either reader looks.
//
// Four would do for every shape measured on 2026-09-15 — Claude Code carries
// `version` on its fourth record, Codex and Pi on their first — but Claude
// opens with a variable number of untimestamped bookkeeping records (`mode`,
// `custom-title`, `file-history-snapshot`, `aiTitle`, and whatever it invents
// next), so a tight bound would silently start answering "" the day it adds a
// fifth. Forty is generous against that and still one short read.
const headRecords = 40

// ToolVersion reads the tool's own version out of the newest transcript it
// wrote: Claude Code's per-line `version`, Codex's `session_meta.cli_version`,
// Pi's header `version`.
//
// ⚠️ IT ANSWERS "" WHENEVER IT CANNOT TELL, AND NEVER GUESSES (AC-7). There is
// no fallback to a binary on PATH, a package manager, or the version keld was
// built against: a version we inferred rather than read is a fact about our
// inference, and it would travel to Atlas in an `integration.broken` event
// looking exactly like one we measured.
func ToolVersion(e Entry, d Deps) string {
	d = d.withDefaults()
	path, ok := newestTranscript(d.TranscriptDirs(e))
	if !ok {
		return ""
	}
	var found string
	scanHead(path, func(rec map[string]any) bool {
		if v := versionFrom(e.ID, rec); v != "" {
			found = v
			return false
		}
		return true
	})
	return found
}

// versionFrom applies one tool's rule to one decoded record.
func versionFrom(id string, rec map[string]any) string {
	switch id {
	case "codex":
		// {"type":"session_meta","payload":{...,"cli_version":"0.153.4"}}
		if payload, ok := rec["payload"].(map[string]any); ok {
			if s := scalar(payload["cli_version"]); s != "" {
				return s
			}
		}
		if meta, ok := rec["session_meta"].(map[string]any); ok {
			if s := scalar(meta["cli_version"]); s != "" {
				return s
			}
		}
		return ""
	default:
		// Claude Code, Cowork and Pi all carry a top-level `version`; Pi's is a
		// number, which is why scalar formats rather than type-asserts a string.
		return scalar(rec["version"])
	}
}

// scalar renders a JSON value as the version string, or "" for anything that
// is not one. A JSON number arrives as float64 (Pi writes `"version": 3`), and
// rendering it as "3" rather than "3.000000" is the whole reason this exists.
func scalar(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// newestSessionStart is the instant the newest transcript's session began.
//
// ⚠️ IT DECODES LINES AND TAKES A TOP-LEVEL `timestamp`, and that is the whole
// of the care here. Claude Code opens a transcript with `mode`,
// `custom-title`, `aiTitle` and `file-history-snapshot` records that carry no
// top-level timestamp at all — and `file-history-snapshot` carries a NESTED
// one. Reading the first line, or regexing the first `"timestamp"` out of it,
// therefore returns either nothing or a fabricated instant. `capture.scan`
// documents the same trap sidecar-side, where taking a nested timestamp made
// 31 of 40 real transcripts non-monotone.
//
// The zero instant means UNKNOWN and must never be read as "long ago": Compute
// refuses to call a tool `restart_required` on it.
func newestSessionStart(dirs []string) time.Time {
	path, ok := newestSessionTranscript(dirs)
	if !ok {
		return time.Time{}
	}
	var start time.Time
	scanHead(path, func(rec map[string]any) bool {
		s, _ := rec["timestamp"].(string)
		if s == "" {
			return true
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			start = t.UTC()
			return false
		}
		return true
	})
	return start
}

// newestSessionTranscript is newestTranscript with SUBAGENT transcripts
// excluded, and it is what every question about "the newest SESSION" reads.
//
// ⚠️ An `agent-*.jsonl` is not a session. It shares its parent's OTEL session
// id and is written whenever the parent spawns a subagent, so it is usually the
// most recently modified file on the machine — 620 of 671 here — and its own
// first timestamp is when the SUBAGENT started, not when the tool did. Reading
// it as the newest session start makes a session that predates the config look
// fresh, which hides `restart_required` on exactly the machines that need it;
// reading its basename as a session id joins to nothing. Same exclusion
// SessionTelemetryState makes one package over, for the same reason.
func newestSessionTranscript(dirs []string) (string, bool) {
	return newestTranscriptWhere(dirs, func(name string) bool {
		return !strings.HasPrefix(name, "agent-")
	})
}

// newestSessionID names the newest session this tool wrote, as the tool's own
// OTLP session id.
//
// It is the transcript's basename, which holds for Claude Code and is checked
// nowhere else: Codex and Gemini name their files something that is not their
// session id, so the join simply MISSES for them and nothing downstream reads
// an adoption fact it was never given. A miss is the honest answer there, not a
// wrong one.
func newestSessionID(dirs []string) string {
	path, ok := newestSessionTranscript(dirs)
	if !ok {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// newestTranscript returns the most recently modified *.jsonl anywhere under
// any of dirs. A missing directory is not an error — it is the ordinary state
// of a tool nobody has run.
func newestTranscript(dirs []string) (string, bool) {
	return newestTranscriptWhere(dirs, func(string) bool { return true })
}

// newestTranscriptWhere is newestTranscript restricted to files whose BASE NAME
// keep accepts.
func newestTranscriptWhere(dirs []string, keep func(name string) bool) (string, bool) {
	var best string
	var bestMod time.Time
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil // an unreadable subtree is skipped, never fatal
			}
			if de.IsDir() || filepath.Ext(p) != ".jsonl" {
				return nil
			}
			if !keep(de.Name()) {
				return nil
			}
			info, err := de.Info()
			if err != nil {
				return nil
			}
			if best == "" || info.ModTime().After(bestMod) {
				best, bestMod = p, info.ModTime()
			}
			return nil
		})
	}
	return best, best != ""
}

// scanHead decodes up to headRecords lines of path and hands each to fn, which
// returns false to stop. An undecodable line is SKIPPED rather than fatal: a
// half-written trailing line is normal in a file a tool is appending to.
func scanHead(path string, fn func(map[string]any) bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Transcript lines hold whole assistant turns; bufio's 64 KB default
	// truncates them and a truncated line decodes as nothing.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for n := 0; n < headRecords && sc.Scan(); n++ {
		var rec map[string]any
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue
		}
		if !fn(rec) {
			return
		}
	}
}
