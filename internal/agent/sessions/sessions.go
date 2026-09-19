// Package sessions answers ONE question, for everybody who asks it: which tool
// sessions does this machine still have open, and when did each of them start?
//
// ⚠️ **IT EXISTED TWICE, AND THE TWO COPIES WERE ALREADY DRIFTING.** The
// integrations pane walked transcript directories in
// `integrations/version.go` (30-minute window, `agent-*` excluded, start instant
// decoded out of the first 40 records); `keld signal doctor` globbed
// `~/.claude/projects/*/*.jsonl` in `cli/session_telemetry_doctor.go` (2-hour
// scan window, `agent-*` excluded, start instant decoded out of the first 200
// records). Same question, two walks, two head bounds, and a comment in
// `version.go` saying the shared constant could not exist because `localagent`
// imports `integrations`. It can: this package imports nothing of ours, so both
// sides can depend on it.
//
// Two traps live here rather than in each caller, which is the point of the
// move:
//
//   - **`agent-*.jsonl` IS NOT A SESSION.** A subagent transcript shares its
//     parent's OTEL session id, so its basename joins to nothing, and its first
//     timestamp is when the SUBAGENT started rather than when the tool did.
//     They are also the overwhelming majority — measured on the maintainer's
//     machine, 620 of 671 files.
//   - **THE START INSTANT IS DECODED, NEVER PATTERN-MATCHED.** Claude Code opens
//     a transcript with `custom-title`, `mode`, `aiTitle` and
//     `file-history-snapshot` records; the last carries a NESTED `timestamp` and
//     no top-level one, so the first `"timestamp"` in the bytes is routinely the
//     wrong instant — the same trap `capture.scan` documents sidecar-side, where
//     1,135 of 73,449 real lines matched it.
//
// Nothing here reads message text: a basename, a file mtime and a record's own
// `timestamp` field. Session ids are IDENTIFIERS, the class already published as
// `corr_id`.
package sessions

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ActiveWindow is how recently a transcript must have been written for its
// session to count as one the person still has open. A tool closed an hour ago
// cannot be restarted to fix anything, so reporting it would be a nag that
// never clears.
//
// It was 30 minutes in both callers before they shared this; the value is
// unchanged, it simply has one home now.
const ActiveWindow = 30 * time.Minute

// headRecords bounds how far into a transcript Recent looks for the session's
// own start instant.
//
// ⚠️ It is the LARGER of the two bounds the callers used (doctor's 200, against
// the integrations reader's 40), because the two failure modes are not
// symmetric: too generous costs a few decoded lines of a file already open,
// while too tight silently starts answering "unknown" the day Claude Code adds
// another untimestamped bookkeeping record at the top of a transcript.
const headRecords = 200

// Sighting is one tool session observed on disk.
type Sighting struct {
	// Path is the transcript file.
	Path string
	// ID is the session id as the tool's OTLP exporter reports it, which for
	// Claude Code is the transcript's basename. Codex and Gemini name their
	// files something that is not their session id, so a join on this MISSES
	// for them — the honest answer there, not a wrong one.
	ID string
	// StartedAt is when the session's first timestamped record was written.
	// ZERO MEANS UNKNOWN — no decodable top-level timestamp in the head — and
	// must never be read as "long ago".
	StartedAt time.Time
	// LastSeen is the transcript's mtime: when the tool last wrote to it.
	LastSeen time.Time
}

// Live lists the sessions still open right now — written inside ActiveWindow —
// newest first.
func Live(dirs []string, now time.Time) []Sighting {
	return Recent(dirs, now, ActiveWindow)
}

// Recent lists sessions whose transcript was written inside window, newest
// first. A caller wanting a superset it will filter itself (doctor scans wider
// so its own rules decide, rather than this walk deciding for it) passes its
// own window; a caller asking "what is open" calls Live.
//
// A missing or unreadable directory is not an error — it is the ordinary state
// of a tool nobody has run.
func Recent(dirs []string, now time.Time, window time.Duration) []Sighting {
	var out []Sighting
	seen := map[string]bool{}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil // an unreadable subtree is skipped, never fatal
			}
			if de.IsDir() || filepath.Ext(p) != ".jsonl" || !IsSessionTranscript(de.Name()) {
				return nil
			}
			info, err := de.Info()
			if err != nil || now.Sub(info.ModTime()) > window {
				return nil
			}
			if seen[p] {
				return nil // two overlapping roots, one file
			}
			seen[p] = true
			out = append(out, Sighting{
				Path:      p,
				ID:        IDOf(p),
				StartedAt: StartedAt(p),
				LastSeen:  info.ModTime(),
			})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// IsSessionTranscript reports whether a transcript BASE NAME is a session's own
// rather than a subagent's. See the package comment for why the distinction is
// load-bearing.
func IsSessionTranscript(name string) bool {
	return !strings.HasPrefix(name, "agent-")
}

// IDOf names one session as its tool's OTLP session id: the transcript's
// basename without the extension.
func IDOf(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// StartedAt reads one transcript's own start instant: the first TOP-LEVEL
// `timestamp` in its head, decoded. Zero is UNKNOWN, and is a first-class
// answer — the caller then draws no conclusion about this session.
func StartedAt(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Transcript lines hold whole assistant turns; bufio's 64 KB default
	// truncates them and a truncated line decodes as nothing.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for n := 0; n < headRecords && sc.Scan(); n++ {
		var rec struct {
			Timestamp string `json:"timestamp"`
		}
		// An undecodable line is SKIPPED rather than fatal: a half-written
		// trailing line is normal in a file a tool is appending to.
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Timestamp == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
