package integrations

import (
	"os"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// WiringFacts is what the DISK says about one tool's configuration, read back
// on every call (AC-1: "read back, not remembered").
type WiringFacts struct {
	// ConfigPresent — the tool's config DIRECTORY exists. This is `installed`.
	ConfigPresent bool
	// ConfigMatchesAdapter — the config FILE on disk is what keld's adapter
	// would have written. The drift check doctor already runs
	// (tools.ConfiguredOnDisk), asked of the same file by the same function.
	ConfigMatchesAdapter bool
	// PointsAtProxy — the tool's telemetry destination is the daemon's
	// loopback proxy, not Atlas and not somewhere else. Separate from
	// ConfigMatchesAdapter because a config written by an OLDER keld can be
	// perfectly well-formed and still point at a destination that no longer
	// exists.
	PointsAtProxy bool
	// HookTrusted / HookTrustKnown — Codex records trust for a hook against
	// its HASH, in `[hooks.state."<source>:<event>:i:j"]` tables it writes
	// into its own config.toml, and skips an untrusted hook SILENTLY.
	//
	// ⚠️ The two booleans are not one. `known=false` means the file carries no
	// hooks.state section at all — an older Codex that never wrote one — and
	// must never be reported as "not trusted", which would tell every such
	// machine to go and approve something its Codex cannot show it. Same
	// refusal localagent.ModelState and version.Skew make.
	HookTrusted    bool
	HookTrustKnown bool
	// HookCommandBroken — keld's hook command is present in the tool's config
	// but was written before the quoting rule and cannot execute as it stands.
	// See hookCommandBroken; the repair is Compute returning NotConfigured, so
	// the detector rewrites it through the ONE setup path.
	HookCommandBroken bool

	// ConfigMtime — when keld's config was last written. The other half of the
	// restart question.
	ConfigMtime time.Time
	// NewestSessionStart — when the newest session this tool wrote began.
	// ZERO MEANS UNKNOWN (no transcript, or none carrying a decodable
	// top-level timestamp) and Compute refuses to call a tool
	// `restart_required` on it — see newestSessionStart.
	NewestSessionStart time.Time
	// NewestSessionAdopted — that same session has posted telemetry through
	// the loopback proxy SINCE the config was written, so the process running
	// it has demonstrably read the new config.
	//
	// ⚠️ WITHOUT IT `restart_required` CAN NEVER CLEAR ON A RESUMED SESSION,
	// and a resume is the ordinary way back into work. The start instant is
	// read from the transcript's first line, and `claude --resume` keeps the
	// transcript — so a session resumed into a NEW process, reading the new
	// config, goes on reporting the old process's start time forever. Measured
	// here: config written 15:06:55Z, session first line 14:46Z, and that same
	// session id forwarding telemetry at 15:13:01Z through a proxy it could
	// only reach by having read the config. The row said restart_required
	// across two genuine restarts.
	//
	// It is asked PER SESSION, never per machine: the record is
	// `teleproxy.SessionsOnDisk()`, keyed by the tool's own session id, so a
	// second editor window started after setup cannot vouch for a stale one —
	// the vouching trap this file's otel lane already had to correct.
	NewestSessionAdopted bool
}

// LaneFacts is what each lane last carried for one tool. Every field is a
// pointer because "never" and "long ago" are different facts and only one of
// them is a nil.
type LaneFacts struct {
	LastHookPointer      *time.Time
	LastWatcherPointer   *time.Time
	LastTelemetryForward *time.Time
	// RowsForRecentPointers — did the sidecar's store get rows for this
	// source's recent pointers? nil means WE COULD NOT ASK (no analysis
	// backend, an unreachable sidecar), and Compute lets an unknown lane
	// contribute NEITHER half of `broken`: a check that did not run must not
	// publish a confident negative.
	RowsForRecentPointers *bool
}

// CodexHooksTrusted answers whether Codex holds a trust entry for keld's hooks.
//
// ⚠️ SEAM FOR WS-B — ONE LINE. WS-B ships
// `tools.CodexHooksTrusted(configTOML []byte, hookCommandSubstr string) (trusted, known bool)`
// on a sibling branch. When it lands, the supervisor replaces this stub's body
// with a call to it — or, equivalently, assigns it at wire-up:
//
//	integrations.CodexHooksTrusted = tools.CodexHooksTrusted
//
// Until then it answers (false, false) — NOT (false, true). The difference is
// the whole point: `known=false` means "we cannot tell", so no machine is told
// to approve hooks on the strength of a function that has not been written.
var CodexHooksTrusted = func(configTOML []byte, hookCommandSubstr string) (trusted, known bool) {
	return false, false
}

// Deps are the seams ReadWiring, ReadLanes and ToolVersion read the world
// through. Every field has a real default (withDefaults), so production code
// passes Deps{} and a test overrides one field.
type Deps struct {
	Now func() time.Time
	// Manifest is keld's own record of what it configured. Loaded once per
	// Compute rather than per entry.
	Manifest *config.Manifest
	// Adapter resolves a tools.Adapter by ADAPTER NAME.
	//
	// ⚠️ Callers pass Entry.AdapterName, never Entry.ID. The two disagree for
	// exactly one tool — Gemini's adapter is named "gemini" while its source
	// id is "gemini_cli" — and tools.Get(e.ID) would return an error for it,
	// silently leaving Gemini unconfigured and unwired forever.
	Adapter func(name string) (tools.Adapter, error)
	// TranscriptDirs are the directories this tool writes transcripts to.
	TranscriptDirs func(e Entry) []string
	// ProxyAddr is the loopback address tools are told to post OTLP to.
	ProxyAddr string
	// HookCommandSubstr recognises keld's own hook command inside a tool
	// config.
	HookCommandSubstr string
	// Lanes is the persistent lane record.
	Lanes *Lanes
	// TelemetryForward is the otel lane: when telemetry for this source last
	// reached Atlas. The default reads WS-C2's per-source record and falls
	// back to the machine-wide instant only while that record is empty — see
	// perSourceForward.
	TelemetryForward func(source string) *time.Time
	// RowsForRecentPointers is the reader lane. nil function ⇒ nil answer ⇒
	// unknown ⇒ contributes neither half of broken.
	RowsForRecentPointers func(e Entry) *bool
	// SessionForward answers when ONE session last forwarded telemetry, for
	// the restart question. Default: teleproxy's per-session record.
	SessionForward func(sessionID string) *time.Time
}

func (d Deps) withDefaults() Deps {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Adapter == nil {
		d.Adapter = tools.Get
	}
	if d.TranscriptDirs == nil {
		d.TranscriptDirs = DefaultTranscriptDirs
	}
	if d.ProxyAddr == "" {
		d.ProxyAddr = teleproxy.Addr()
	}
	if d.HookCommandSubstr == "" {
		d.HookCommandSubstr = telemetry.HookCommandSubstr
	}
	if d.TelemetryForward == nil {
		d.TelemetryForward = perSourceForward
	}
	if d.SessionForward == nil {
		d.SessionForward = sessionForward
	}
	return d
}

// perSourceForward is the otel fact, asked PER TOOL.
//
// ⚠️ THE MACHINE-WIDE FALLBACK IT REPLACES MADE `broken` MORE LIKELY, NOT
// LESS, and its own comment claimed the opposite: "a machine with any
// telemetry flowing reports every configured tool's otel lane as active, which
// can only make `broken` LESS likely, never more." Active is one of the two
// halves of `broken`. So Claude Code's telemetry vouched for the otel lane of
// every OTHER configured tool, and any tool nobody had used — its watcher
// silent because there was nothing to watch — was reported broken on the
// strength of a different tool's traffic. Observed on a healthy machine:
// `gemini_cli` read `broken · watcher` with no Gemini installed and its otel
// instant equal, to the microsecond, to Claude Code's.
//
// The per-source record (WS-C2, `teleproxy.persource`) is what the rule always
// wanted; it just was not read here. Three answers, and the middle one is the
// refusal:
//   - an entry for this tool ⇒ that instant;
//   - an EMPTY record ⇒ the machine-wide instant, because a machine that
//     upgraded into this code has forwards but no per-source history, and
//     reading that as "no tool's telemetry has ever arrived" would report
//     every configured tool broken on the day it shipped — the refusal
//     SourcesOnDisk and SessionsOnDisk both make;
//   - a non-empty record with no entry for this tool ⇒ nil, i.e. this tool's
//     telemetry has genuinely never reached Atlas.
//
// ⚠️ UNATTRIBUTED TRAFFIC IS THE THIRD CASE AND IT READS AS NOT-KNOWN. A
// payload whose service name teleproxy does not recognise is recorded under
// `UnknownSource`, so a tool whose telemetry IS arriving under a name nobody
// mapped has no entry of its own — and calling that silence would report it
// broken on the strength of a naming gap. While anything sits under
// UnknownSource the fallback stands, which is the coarse answer and errs
// toward idle. Only a record in which every forward was attributable may say
// a particular tool's otel lane is silent.
func perSourceForward(source string) *time.Time {
	if at, ok := teleproxy.LastForwardForSource(source); ok && !at.IsZero() {
		return &at
	}
	known := teleproxy.SourcesOnDisk()
	if _, unattributed := known[teleproxy.UnknownSource]; len(known) > 0 && !unattributed {
		return nil
	}
	return machineWideForward()
}

// machineWideForward is teleproxy's single recorded last-forward instant, the
// coarse fact perSourceForward falls back to while no per-source history
// exists. It cannot distinguish tools, which is exactly why it is a fallback.
func machineWideForward() *time.Time {
	t, known := teleproxy.LastForwardOnDisk()
	if !known || t.IsZero() {
		return nil
	}
	return &t
}

// DefaultTranscriptDirs maps a catalogue entry to the directories the tool
// writes transcripts into, using the watcher's OWN root discovery so the two
// cannot disagree about where a source's files live.
//
// Pi is the one entry the watcher does not know: it is unsupported, so no root
// exists for it, and its transcripts are read only to answer AC-7's version
// question.
func DefaultTranscriptDirs(e Entry) []string {
	var dirs []string
	for _, r := range watch.DiscoverRoots() {
		if r.SourceID == e.ID {
			dirs = append(dirs, r.Dir)
		}
	}
	if e.ID == "pi" && e.ConfigDir != nil {
		dirs = append(dirs, e.ConfigDir())
	}
	return dirs
}

// ReadWiring answers from DISK, every call. Nothing here is cached and nothing
// is remembered from the last poll: the question "is this tool still wired"
// has exactly one honest source, and it is the files themselves.
func ReadWiring(e Entry, d Deps) WiringFacts {
	d = d.withDefaults()
	var w WiringFacts

	if e.ConfigDir != nil {
		if info, err := os.Stat(e.ConfigDir()); err == nil && info.IsDir() {
			w.ConfigPresent = true
		}
	}
	if !e.Supported || e.AdapterName == "" {
		// An unsupported row, or a tool configured as part of something else
		// (Cowork rides Claude Desktop). There is no adapter to compare
		// against, so there is nothing to claim about drift.
		w.NewestSessionStart = newestSessionStart(d.TranscriptDirs(e))
		return w
	}

	adapter, err := d.Adapter(e.AdapterName)
	if err != nil {
		return w
	}
	var managed map[string]any
	if d.Manifest != nil {
		if tm, ok := d.Manifest.Tools[e.AdapterName]; ok {
			managed = tm.Managed
		}
	}
	w.ConfigMatchesAdapter = tools.ConfiguredOnDisk(adapter, managed)

	current := tools.ReadConfig(adapter)
	if current != nil {
		w.PointsAtProxy = strings.Contains(*current, d.ProxyAddr)
		if e.ID == "codex" {
			w.HookTrusted, w.HookTrustKnown = CodexHooksTrusted([]byte(*current), d.HookCommandSubstr)
		}
		w.HookCommandBroken = hookCommandBroken(*current)
	}
	if info, err := os.Stat(adapter.ConfigPath()); err == nil {
		w.ConfigMtime = info.ModTime().UTC()
	}
	w.NewestSessionStart = newestSessionStart(d.TranscriptDirs(e))
	w.NewestSessionAdopted = sessionAdopted(d, e, w.ConfigMtime)
	return w
}

// sessionAdopted answers NewestSessionAdopted: did the newest session forward
// telemetry after this config was written. A zero ConfigMtime is unknown, and
// an unknown config instant can prove nothing either way.
func sessionAdopted(d Deps, e Entry, configMtime time.Time) bool {
	if configMtime.IsZero() {
		return false
	}
	id := newestSessionID(d.TranscriptDirs(e))
	if id == "" {
		return false
	}
	at := d.SessionForward(id)
	return at != nil && at.After(configMtime)
}

// sessionForward is the default per-session telemetry fact: teleproxy's record
// of which tool session ids it has forwarded for, and when.
//
// ⚠️ An EMPTY record is "not tracked yet", never "this session has sent
// nothing" — the refusal SessionsOnDisk is built around. Here that direction is
// already safe: a missing instant leaves the restart rule exactly as it was.
func sessionForward(sessionID string) *time.Time {
	at, ok := teleproxy.SessionsOnDisk()[sessionID]
	if !ok || at.IsZero() {
		return nil
	}
	return &at
}

// hookCommandBroken reports whether any keld hook command in this config was
// written before the quoting rule and cannot execute as it stands.
//
// ⚠️ Read out of the CONFIG TEXT rather than rebuilt from the current binary
// path, because the question is what the TOOL will try to run — which is
// whatever an older keld wrote, on a machine this keld has never configured.
// Scanning line-wise is enough for both shapes keld writes: Claude Code's JSON
// string and Codex's TOML string both put the command on one line.
func hookCommandBroken(configText string) bool {
	for _, line := range strings.Split(configText, "\n") {
		i := strings.Index(line, telemetry.HookCommandSubstr)
		if i < 0 {
			continue
		}
		// Trim back to the opening quote of the JSON/TOML string value, so the
		// binary half is what the tool would actually execute.
		start := strings.LastIndexAny(line[:i], `"'`)
		if start < 0 {
			continue
		}
		if telemetry.HookCommandNeedsRepair(line[start+1:]) {
			return true
		}
	}
	return false
}

// ReadLanes answers the four lane questions for one entry.
func ReadLanes(e Entry, d Deps) LaneFacts {
	d = d.withDefaults()
	f := LaneFacts{
		LastHookPointer:      d.Lanes.Last(e.ID, OriginHook),
		LastWatcherPointer:   d.Lanes.Last(e.ID, OriginWatcher),
		LastTelemetryForward: d.TelemetryForward(e.ID),
	}
	if d.RowsForRecentPointers != nil {
		f.RowsForRecentPointers = d.RowsForRecentPointers(e)
	}
	return f
}

// Configured reports whether keld's manifest records this tool. It is keyed on
// the ADAPTER name, because that is the key runSetup writes.
func Configured(e Entry, m *config.Manifest) bool {
	if m == nil || e.AdapterName == "" {
		return false
	}
	_, ok := m.Tools[e.AdapterName]
	return ok
}

// BackupPath is where the adapter put the previous config, or "".
func BackupPath(e Entry, m *config.Manifest) string {
	if m == nil || e.AdapterName == "" {
		return ""
	}
	tm, ok := m.Tools[e.AdapterName]
	if !ok || tm.BackupPath == nil {
		return ""
	}
	return *tm.BackupPath
}
