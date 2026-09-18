package integrations

// The closed vocabularies, in the order docs/signal-integrations-wire.md and
// AC-1 document. The route publishes States and WaitingOns as
// Response.Vocabulary, so a consumer derives its cases from the server instead
// of hard-coding them; types_test.go pins each slice against the constants
// declared in types.go, so a constant can never exist outside its vocabulary.
var (
	// States — every state a tool can be in, in the documented order.
	States = []State{
		NotInstalled,
		NotConfigured,
		RestartRequired,
		ApprovalRequired,
		Idle,
		Working,
		Broken,
		Unsupported,
	}

	// WaitingOns — what a surface can be waiting for. "" is a member: waiting
	// on nothing is stated, not implied by an absent key.
	WaitingOns = []WaitingOn{
		WaitingOnNothing,
		WaitingOnRestart,
		WaitingOnApproval,
		WaitingOnReader,
	}

	// SurfaceKinds — every lane kind. Not published in Vocabulary (a consumer
	// reads the kinds off the surfaces it was sent); kept closed here so the
	// catalogue cannot name a lane nothing knows how to render.
	SurfaceKinds = []SurfaceKind{
		SurfaceHook,
		SurfaceOTel,
		SurfaceWatcher,
		SurfaceExtension,
		SurfaceReader,
	}
)

// The instruction sentences, one per waiting_on, written ONCE here because the
// pane, doctor and the wire doc all quote them and three copies would drift.
// Each is one sentence a person can act on; none of them names a file path.
const (
	// InstructionRestart — a tool reads its telemetry config once, at startup,
	// and keeps the copy in memory; nothing on the machine can fix that from
	// outside, which is why this is an instruction rather than a repair.
	InstructionRestart = "Restart this tool to finish — a session that started before its config was written keeps using the settings it launched with."

	// InstructionApproval — AC-9 quotes this VERBATIM. Codex records trust
	// against the hook's hash, so a keld release that edits the hook command
	// returns the row here and this sentence is what the person reads. Do not
	// reword it without moving AC-9.
	InstructionApproval = "Open Codex, run /hooks, approve the two keld hooks. Signal confirms here within a minute."

	// InstructionReader — the capture lanes work; only classification is
	// missing. Row 7b of the decision table: the tool still reads working.
	InstructionReader = "Signal captures this tool but cannot read its transcripts yet, so its prompts are not classified — nothing for you to do."
)

// Instructions maps a waiting_on to its sentence. WaitingOnNothing maps to the
// empty string: a surface waiting on nothing has nothing to say.
var Instructions = map[WaitingOn]string{
	WaitingOnNothing:  "",
	WaitingOnRestart:  InstructionRestart,
	WaitingOnApproval: InstructionApproval,
	WaitingOnReader:   InstructionReader,
}
