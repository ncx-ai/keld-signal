package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/localagent"
)

// sessionScanWindow bounds which transcripts are opened at all. A superset of
// localagent's own "is it running now" rule, which does the real filtering —
// this only keeps doctor from reading hundreds of dormant files.
const sessionScanWindow = 2 * time.Hour

// sessionHeaderLines caps how far into a transcript we look for the session's
// first timestamped record. Claude Code opens a file with untimestamped
// bookkeeping (`custom-title`, `mode`, `file-history-snapshot`), so line 1 is
// usually not it; a handful of lines in, it always is.
const sessionHeaderLines = 200

// sessionTelemetryState assembles the per-session view: which tool sessions are
// being written right now, and which of them the proxy has ever forwarded for.
func sessionTelemetryState(manifest *config.Manifest) localagent.SessionTelemetryState {
	s := localagent.SessionTelemetryState{Now: time.Now()}
	if manifest != nil && len(manifest.Tools) > 0 {
		s.Configured = true
	}
	_, s.Known = teleproxy.LastForwardOnDisk()
	s.Forwarded = teleproxy.SessionsOnDisk()
	s.Active = activeClaudeSessions(time.Now())
	return s
}

// activeClaudeSessions lists Claude Code transcripts written recently.
//
// ⚠️ SCOPED TO claude_code, AND SUBAGENT TRANSCRIPTS ARE EXCLUDED. The filename
// stem is the session id only for a real session: `agent-*.jsonl` files are
// subagent transcripts that share their parent's OTEL `session.id` and so can
// never appear in the forwarded record on their own. They are also the vast
// majority — measured on this machine, 620 of 671 — so including them would turn
// the check into hundreds of false findings. Cowork is excluded for a different
// reason (its egress to Atlas is blocked by design, so its silence is expected),
// and Codex/Gemini because their transcript names are not their OTLP session ids.
func activeClaudeSessions(now time.Time) []localagent.SessionSighting {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", "*.jsonl"))
	if err != nil {
		return nil
	}
	var out []localagent.SessionSighting
	for _, path := range matches {
		name := filepath.Base(path)
		if len(name) > 6 && name[:6] == "agent-" {
			continue
		}
		fi, err := os.Stat(path)
		if err != nil || now.Sub(fi.ModTime()) > sessionScanWindow {
			continue
		}
		out = append(out, localagent.SessionSighting{
			ID:        name[:len(name)-len(".jsonl")],
			StartedAt: firstRecordTime(path),
			LastSeen:  fi.ModTime(),
		})
	}
	return out
}

// firstRecordTime reads the first TOP-LEVEL timestamp in a transcript.
//
// ⚠️ DECODED, NOT PATTERN-MATCHED. A `file-history-snapshot` record carries a
// NESTED timestamp and no top-level one, so the first `"timestamp"` in the bytes
// is routinely the wrong instant — the same trap `capture.scan` documents, where
// 1,135 of 73,449 real lines matched it. A zero return is a first-class answer:
// the caller then draws no conclusion about this session.
func firstRecordTime(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for i := 0; i < sessionHeaderLines && sc.Scan(); i++ {
		var rec struct {
			Timestamp string `json:"timestamp"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Timestamp == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			return t
		}
	}
	return time.Time{}
}
