package integrations

import (
	"os"
	"path/filepath"
)

// Storage classes — how a tool's work would be READ if Signal read it. Named on
// every row, and the whole of what an unsupported row says.
const (
	// StorageJSONLTail — the tool appends JSONL transcripts to disk; the
	// watcher tails them and the sidecar parses the tail.
	StorageJSONLTail = "jsonl-tail"
	// StorageDBPoll — the tool keeps its history in a local database, so
	// reading it means polling rows, not tailing a file.
	StorageDBPoll = "db-poll"
	// StorageRPC — the tool keeps no readable local history; capture would have
	// to ride an RPC the tool exposes.
	StorageRPC = "rpc"
)

// SupportLevel is what a catalogue entry's expectations are evaluated against.
// It is deliberately a VALUE rather than read from the entry inside the
// expectation func, so a test (and a future settings override) can ask what an
// entry would expect at a level it is not at yet.
type SupportLevel struct {
	// Supported — Signal claims to capture this tool. An unsupported entry
	// expects no lane at all and therefore can never be broken.
	Supported bool
	// ReaderAvailable — the sidecar can parse this tool's transcripts into
	// store rows. Until it can, the reader lane (and, for a tool whose watcher
	// exists only to feed it, the watcher lane) is not expected.
	ReaderAvailable bool
}

// SurfaceSpec is one lane in the catalogue: its kind, whether the TOOL
// documents it, and whether it is expected at a given support level.
type SurfaceSpec struct {
	Kind         SurfaceKind
	Documented   bool
	ExpectedWhen func(SupportLevel) bool
}

// Entry is one tool Signal knows about.
type Entry struct {
	// ID is the source id the daemon already uses for this tool everywhere
	// else: the spool pointer's Source.ID, the watcher root's SourceID, the
	// teleproxy's per-source key. Lane facts join on it.
	ID          string
	DisplayName string
	// AdapterName is the tools.Adapter name for this entry, or "" when no
	// adapter configures it (Cowork inherits Claude Code's; the unsupported
	// rows have none). It exists because the two ids DISAGREE for exactly one
	// tool: the Gemini adapter is named "gemini" while its source id — in
	// watch/roots.go and therefore in every pointer — is "gemini_cli". A
	// detector that called tools.Get(e.ID) would silently never configure
	// Gemini.
	AdapterName string
	// ConfigDir resolves through the user's HOME at CALL time, never at
	// package init, so a test (and the conformance harness) isolates it by
	// setting HOME.
	ConfigDir    func() string
	StorageClass string
	Supported    bool
	// ReaderAvailable — flipped on when the sidecar gains a reader for this
	// source. Codex's flips in WS-D, together with the row in types_test.go
	// that pins it.
	ReaderAvailable bool
	Surfaces        []SurfaceSpec
}

// SupportLevel is the level this entry is at today.
func (e Entry) SupportLevel() SupportLevel {
	return SupportLevel{Supported: e.Supported, ReaderAvailable: e.ReaderAvailable}
}

// ExpectedLanes returns, in catalogue order, the lanes that are expected to
// feed at the given level.
//
// This is the ONE derivation of "expected", and it is what keeps a tool from
// reading broken over a lane it cannot feed: broken needs one expected lane
// active and another expected lane silent, so a lane missing from this list can
// never contribute either half.
func (e Entry) ExpectedLanes(l SupportLevel) []SurfaceKind {
	var out []SurfaceKind
	for _, s := range e.Surfaces {
		if s.ExpectedWhen != nil && s.ExpectedWhen(l) {
			out = append(out, s.Kind)
		}
	}
	return out
}

// Get returns the catalogue entry with this id.
func Get(id string) (Entry, bool) {
	for _, e := range Catalogue {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// whenSupported — expected wherever Signal supports the tool.
func whenSupported(l SupportLevel) bool { return l.Supported }

// whenReaderAvailable — expected only once a reader exists for this source.
// The watcher rides this too wherever the watcher's only consumer is the
// reader: expecting it earlier would report a lane silent that nothing asked
// to speak.
func whenReaderAvailable(l SupportLevel) bool { return l.Supported && l.ReaderAvailable }

// never — the lane exists and is shown, and is never expected to feed.
func never(SupportLevel) bool { return false }

// homeDir builds a ConfigDir that resolves through HOME when it is called. The
// fallback mirrors the tool adapters': a relative path rather than a panic.
func homeDir(parts ...string) func() string {
	return func() string {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(parts...)
		}
		return filepath.Join(append([]string{home}, parts...)...)
	}
}

// Catalogue is every tool Signal knows about, supported or not. The pane lists
// it whole: a tool Signal cannot capture is a row that says so, not an absence
// a person has to interpret.
var Catalogue = []Entry{
	{
		ID:              "claude_code",
		DisplayName:     "Claude Code",
		AdapterName:     "claude_code",
		ConfigDir:       homeDir(".claude"),
		StorageClass:    StorageJSONLTail,
		Supported:       true,
		ReaderAvailable: true,
		Surfaces: []SurfaceSpec{
			{Kind: SurfaceHook, Documented: true, ExpectedWhen: whenSupported},
			{Kind: SurfaceOTel, Documented: true, ExpectedWhen: whenSupported},
			// The transcript format is ours to read, not theirs to promise —
			// which is exactly why a tool release is what breaks it.
			{Kind: SurfaceWatcher, Documented: false, ExpectedWhen: whenSupported},
			{Kind: SurfaceReader, Documented: false, ExpectedWhen: whenReaderAvailable},
		},
	},
	{
		ID:           "codex",
		DisplayName:  "Codex",
		AdapterName:  "codex",
		ConfigDir:    homeDir(".codex"),
		StorageClass: StorageJSONLTail,
		Supported:    true,
		// No sidecar reader for Codex rollouts yet (WS-D). Expecting the reader
		// lane today would read "broken · reader" by construction on every
		// Codex machine from the day the pane ships.
		ReaderAvailable: false,
		Surfaces: []SurfaceSpec{
			{Kind: SurfaceHook, Documented: true, ExpectedWhen: whenSupported},
			{Kind: SurfaceOTel, Documented: true, ExpectedWhen: whenSupported},
			{Kind: SurfaceWatcher, Documented: false, ExpectedWhen: whenReaderAvailable},
			{Kind: SurfaceReader, Documented: false, ExpectedWhen: whenReaderAvailable},
		},
	},
	{
		ID:          "gemini_cli",
		DisplayName: "Gemini CLI",
		// The one entry whose adapter name is not its id.
		AdapterName:     "gemini",
		ConfigDir:       homeDir(".gemini"),
		StorageClass:    StorageJSONLTail,
		Supported:       true,
		ReaderAvailable: false,
		Surfaces: []SurfaceSpec{
			// No hook lane: Gemini CLI runs no command hook for us, so there is
			// nothing to be silent.
			{Kind: SurfaceOTel, Documented: true, ExpectedWhen: whenSupported},
			{Kind: SurfaceWatcher, Documented: false, ExpectedWhen: whenSupported},
			{Kind: SurfaceReader, Documented: false, ExpectedWhen: whenReaderAvailable},
		},
	},
	{
		ID:          "cowork",
		DisplayName: "Cowork",
		// Configured as part of Claude Desktop; nothing here to apply.
		AdapterName:     "",
		ConfigDir:       homeDir("Library", "Application Support", "Claude", "local-agent-mode-sessions"),
		StorageClass:    StorageJSONLTail,
		Supported:       true,
		ReaderAvailable: true,
		Surfaces: []SurfaceSpec{
			// Cowork runs in a VM. No hook can reach the host daemon from
			// inside it and its egress to Atlas is blocked by design, so the
			// watcher — the daemon reading the transcripts host-side — is the
			// only lane that can ever feed.
			{Kind: SurfaceWatcher, Documented: false, ExpectedWhen: whenSupported},
			{Kind: SurfaceOTel, Documented: true, ExpectedWhen: never},
		},
	},
	{
		ID:          "pi",
		DisplayName: "Pi",
		ConfigDir:   homeDir(".pi", "agent"),
		// Pi writes session files; a reader for them is the next discovery.
		StorageClass: StorageJSONLTail,
		Supported:    false,
		Surfaces: []SurfaceSpec{
			// Pi documents an extension hook (before_agent_start fires with the
			// session file path). Shown because it is the shape a supported Pi
			// would use; expected by nothing until then.
			{Kind: SurfaceExtension, Documented: true, ExpectedWhen: never},
		},
	},
	{
		ID:          "antigravity",
		DisplayName: "Antigravity",
		ConfigDir:   homeDir(".antigravity"),
		// No readable local history: capture would have to ride an RPC.
		StorageClass: StorageRPC,
		Supported:    false,
	},
	{
		ID:          "cursor",
		DisplayName: "Cursor",
		ConfigDir:   homeDir(".cursor"),
		// History lives in a local database, so reading it means polling rows.
		StorageClass: StorageDBPoll,
		Supported:    false,
	},
}
