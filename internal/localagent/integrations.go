package localagent

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// IntegrationStates is what `keld signal status` and `keld signal doctor`
// report, and it is the SAME function the loopback route answers from:
// integrations.Snapshot, which calls integrations.Compute, which decides.
//
// ⚠️ NEITHER COMMAND DECIDES ANYTHING (AC-8). There is no second switch over
// states in the CLI — doctor formats the rows Compute produced, and a state it
// does not recognise is printed verbatim rather than mapped, exactly as the
// pane does. A copy of the rule here would be a copy that drifts, and the
// symptom would be doctor and the page disagreeing about one machine.
//
// Disk-only, like ModelState and the update state beside it: no daemon
// round-trip, so "the daemon is not running" can never render as "the tool is
// not wired". It triggers no model load and no download.
func IntegrationStates() integrations.Response {
	set := settings.Load()
	return integrations.Snapshot(integrations.Deps{}, integrations.Options{AutoSetup: set.AutoSetupEnabled()})
}

// IntegrationLines renders the status listing: one line per catalogue entry,
// in catalogue order, carrying the server's state string VERBATIM.
func IntegrationLines(r integrations.Response) []string {
	lines := make([]string, 0, len(r.Integrations))
	for _, in := range r.Integrations {
		line := fmt.Sprintf("  %-14s %s", in.DisplayName, in.State)
		if in.State == integrations.Broken && in.BrokenLane != "" {
			line += " · " + string(in.BrokenLane)
		}
		if in.ToolVersion != "" {
			line += " · v" + in.ToolVersion
		}
		lines = append(lines, line)
	}
	return lines
}

// IntegrationProblems is doctor's half: the rows a person can act on, each
// with the sentence that says what to do.
//
// ⚠️ `idle` IS NOT A PROBLEM, and neither is `not_installed` or `unsupported`.
// A quiet user is not a bug (AC-4), a tool that is not on this machine is not a
// finding, and an unsupported row is a catalogue entry. Reporting any of them
// would make this command noisy on every machine, which is the same as making
// it unread — and unread is exactly what let 40 minutes of dead telemetry pass
// while doctor printed "No problems found", correctly.
func IntegrationProblems(r integrations.Response) []string {
	var out []string
	for _, in := range r.Integrations {
		switch in.State {
		case integrations.Broken:
			out = append(out, fmt.Sprintf(
				"%s: capture is broken — the %s lane saw nothing while another lane did. Report a problem on the Integrations pane.",
				in.DisplayName, in.BrokenLane))
		case integrations.ApprovalRequired, integrations.RestartRequired:
			out = append(out, fmt.Sprintf("%s: %s %s", in.DisplayName, in.State, instructionFor(in)))
		case integrations.NotConfigured:
			// ⚠️ ONLY WHEN NOTHING IS GOING TO FIX IT ON ITS OWN. With
			// auto_setup_integrations on — the default — the daemon's detector
			// configures this tool within a minute, so a finding here would be
			// noise that resolves itself before anyone read it, on every
			// machine, forever. With the toggle off nothing will, and then it
			// is a real finding.
			if !r.AutoSetup {
				out = append(out, fmt.Sprintf(
					"%s: installed but not configured for Keld, and auto-setup is off. Run `keld signal setup` (or Set up on the Integrations pane).",
					in.DisplayName))
			}
		}
	}
	return out
}

// instructionFor is the one sentence the surfaces carry for this row's
// waiting_on. The sentences live once, in integrations.Instructions; this
// never writes one of its own.
func instructionFor(in integrations.Integration) string {
	for _, s := range in.Surfaces {
		if s.Instruction != "" {
			return "— " + s.Instruction
		}
	}
	return ""
}
