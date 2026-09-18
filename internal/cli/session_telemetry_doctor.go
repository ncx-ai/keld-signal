package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/sessions"
	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/localagent"
)

// sessionScanWindow bounds which transcripts are opened at all. A superset of
// localagent's own "is it running now" rule (sessions.ActiveWindow), which does
// the real filtering — this only keeps doctor from reading hundreds of dormant
// files.
const sessionScanWindow = 2 * time.Hour

// sessionTelemetryState assembles the per-session view: which tool sessions are
// being written right now, and which of them the proxy has ever forwarded for.
func sessionTelemetryState(manifest *config.Manifest) localagent.SessionTelemetryState {
	s := localagent.SessionTelemetryState{Now: time.Now()}
	if manifest != nil && len(manifest.Tools) > 0 {
		s.Configured = true
	}
	_, s.Known = teleproxy.LastForwardOnDisk()
	s.Forwarded = teleproxy.SessionsOnDisk()
	s.Active = activeClaudeSessions(claudeProjectDirs(), time.Now())
	return s
}

// claudeProjectDirs is where Claude Code writes its transcripts.
//
// ⚠️ SCOPED TO claude_code. Cowork is excluded because its egress to Atlas is
// blocked by design, so its silence is expected; Codex and Gemini because their
// transcript names are not their OTLP session ids, and a name that joins to
// nothing cannot support a finding either way.
func claudeProjectDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".claude", "projects")}
}

// activeClaudeSessions lists Claude Code transcripts written recently.
//
// ⚠️ **IT IS A MAPPING NOW, NOT A WALK OF ITS OWN.** The walk, the `agent-*`
// exclusion and the decoded start instant all live in `agent/sessions`, which
// the integrations pane's restart rule reads through as well — so the two
// surfaces that both name a stale session id cannot disagree about which
// sessions are live or when one began. They previously each had their own copy,
// already differing in how far into a transcript they looked for its first
// timestamp (the pane 40 records, doctor 200), which is the kind of drift that
// shows up as doctor and the page saying different things about one machine.
func activeClaudeSessions(dirs []string, now time.Time) []localagent.SessionSighting {
	seen := sessions.Recent(dirs, now, sessionScanWindow)
	out := make([]localagent.SessionSighting, 0, len(seen))
	for _, s := range seen {
		out = append(out, localagent.SessionSighting{
			ID:        s.ID,
			StartedAt: s.StartedAt,
			LastSeen:  s.LastSeen,
		})
	}
	return out
}
