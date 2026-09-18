// Package integrations holds the CONTRACT for Signal's integrations surface:
// the closed state vocabulary, the catalogue of tools Signal knows about, and
// the JSON types GET /v1/integrations answers with.
//
// It holds no state logic. One tool's state is decided in exactly one place —
// integrations.Compute (compute.go) — and read by three consumers: the loopback
// route, keld signal doctor, and the client-events emitter. A second copy of
// that rule, in Go or in JavaScript, is a defect (AC-8). The pane renders the
// server's state string verbatim and maps nothing.
//
// Wire contract, state definitions and the instruction sentences:
// docs/signal-integrations-wire.md. Spec:
// docs/superpowers/specs/2026-09-15-signal-integrations-discovery.html (AC-1,
// AC-4, AC-7, AC-9 and the decision table in section 4).
package integrations

import "time"

// State is one tool's integration state. The set is CLOSED: a value outside
// States is not producible by the daemon, and the pane renders an unrecognised
// one as "unknown state: <value>" rather than mapping it to something it knows.
type State string

const (
	// NotInstalled — the tool's config dir is not on this machine.
	NotInstalled State = "not_installed"
	// NotConfigured — installed, but the manifest does not record it (nothing
	// has written keld's blocks into its config).
	NotConfigured State = "not_configured"
	// RestartRequired — configured, but the newest tool session started before
	// the config was written, so that session is still using what it launched
	// with. Never broken: a stale in-memory copy is not a fault (AC-4).
	RestartRequired State = "restart_required"
	// ApprovalRequired — configured, and the tool itself is holding the wiring
	// back pending a human approval. Codex's hook trust is the one case.
	// Never broken (AC-9).
	ApprovalRequired State = "approval_required"
	// Idle — configured and restarted, and nothing arrived on any lane inside
	// the window. A quiet user is not a bug (AC-4).
	Idle State = "idle"
	// Working — every expected lane saw the tool inside the window.
	Working State = "working"
	// Broken — one expected lane saw the tool inside the window and another
	// expected lane did not. Both halves are required (AC-4).
	Broken State = "broken"
	// Unsupported — a catalogue row only: Signal names the tool and its storage
	// class and does not claim to capture it.
	Unsupported State = "unsupported"
)

// SurfaceKind is one wiring lane of one tool.
//
// hook      — the tool runs `keld __hook`, which posts a prompt pointer.
// otel      — the tool posts OTLP to the daemon's loopback telemetry proxy.
// watcher   — the daemon tails the transcripts the tool writes to disk.
// extension — the tool loads a keld extension (Pi's shape; none shipped).
// reader    — the sidecar can parse this tool's transcripts into store rows.
type SurfaceKind string

const (
	SurfaceHook      SurfaceKind = "hook"
	SurfaceOTel      SurfaceKind = "otel"
	SurfaceWatcher   SurfaceKind = "watcher"
	SurfaceExtension SurfaceKind = "extension"
	SurfaceReader    SurfaceKind = "reader"
)

// WaitingOn names what a surface is waiting for, from a closed set. The empty
// string is a member and means "waiting on nothing" — a surface says so
// explicitly rather than by the key being absent.
type WaitingOn string

const (
	WaitingOnNothing  WaitingOn = ""
	WaitingOnRestart  WaitingOn = "restart"
	WaitingOnApproval WaitingOn = "approval"
	WaitingOnReader   WaitingOn = "reader"
)

// Surface is one lane of one integration as published on the wire.
type Surface struct {
	Kind SurfaceKind `json:"kind"`
	// Documented — the TOOL documents this lane. An undocumented lane (tailing
	// a transcript format nobody promised) is the one a tool release breaks.
	Documented bool `json:"documented"`
	// Wired — the config on disk is what the adapter would write, read back
	// now, never remembered (AC-1).
	Wired bool `json:"wired"`
	// Expected — this lane can feed for this tool at its current support level
	// (Entry.ExpectedLanes). A lane that is not expected can never make the
	// tool broken.
	Expected bool `json:"expected"`
	// LastSeen — the last instant this lane carried something for this tool,
	// omitted when the lane has never been seen.
	LastSeen *time.Time `json:"last_seen,omitempty"`
	// WaitingOn — what this lane is waiting for, omitted when it is waiting on
	// nothing.
	WaitingOn WaitingOn `json:"waiting_on,omitempty"`
	// Instruction — one sentence a person can act on, present whenever
	// WaitingOn is set. Instructions holds the sentences.
	Instruction string `json:"instruction,omitempty"`
}

// Integration is one row of GET /v1/integrations.
type Integration struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Installed   bool   `json:"installed"`
	Configured  bool   `json:"configured"`
	Supported   bool   `json:"supported"`
	// StorageClass — how this tool's work would be read: jsonl-tail, db-poll or
	// rpc. Shown on unsupported rows, which is the whole of what they say.
	StorageClass string `json:"storage_class"`
	State        State  `json:"state"`
	// BrokenLane — the expected lane that went silent, set only under Broken.
	BrokenLane SurfaceKind `json:"broken_lane,omitempty"`
	// StaleSessionID — the tool session still running on the previous config,
	// set only under RestartRequired and omitted when there is none.
	//
	// ⚠️ THE ROW NAMES THE WINDOW BECAUSE "restart this tool" DOES NOT, once
	// there are two of them open. The restart rule already asks over every live
	// session — that is what stopped the row flickering between them — so it
	// knows which one is stale; this is that answer reaching the reader.
	// `keld signal doctor` names a stale session id the same way, and both
	// resolve which sessions are live through `agent/sessions`.
	//
	// It is an IDENTIFIER, the class already published as `corr_id`: no text,
	// no span and no offset is read to produce it.
	StaleSessionID string `json:"stale_session_id,omitempty"`
	// ToolVersion — read from the newest transcript, "" when unknown. NEVER
	// guessed (AC-7).
	ToolVersion string    `json:"tool_version"`
	Surfaces    []Surface `json:"surfaces"`
	// BackupPath — where the adapter put the previous config, when this run
	// wrote one.
	BackupPath string `json:"backup_path,omitempty"`
}

// Vocabulary is the closed sets the route publishes beside the rows, so a
// consumer derives its cases from the server rather than hard-coding them. The
// Playwright suite reads this list and fails when a new state has no fixture.
type Vocabulary struct {
	States    []State     `json:"states"`
	WaitingOn []WaitingOn `json:"waiting_on"`
}

// Response is the body of GET /v1/integrations.
type Response struct {
	Integrations []Integration `json:"integrations"`
	Vocabulary   Vocabulary    `json:"vocabulary"`
	// AutoSetup — whether the detector configures a tool that appears after the
	// daemon started (auto_setup_integrations, default on).
	AutoSetup  bool      `json:"auto_setup"`
	ComputedAt time.Time `json:"computed_at"`
}
