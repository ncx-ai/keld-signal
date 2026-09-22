package cli

import "github.com/ncx-ai/keld-signal/internal/hook"

// pairedEndpoint is where this machine's agent ACTUALLY publishes.
//
// ⚠️ **IT IS READ FROM THE PAIRING, NEVER FROM THE MANIFEST, AND THAT IS A
// CORRECTION.** `~/.keld/manifest.json` carries an `endpoint` field written by
// whichever `keld signal setup` last APPLIED a tool change — and setup's
// "nothing to apply" path (the ordinary state of every upgrade) does not
// rewrite the manifest at all. So the recorded value outlives the machine's
// actual destination: observed on a real machine paired to localhost:3000 while
// the agent published to localhost:8000, with `whoami` reporting the first.
//
// hook.json is the file the daemon reads, and hook.LoadConfig is the daemon's
// own reader — env overrides included — so this reports what the daemon uses
// rather than a second guess at it. That is the same rule
// localagent.EndpointAgreement already follows for doctor's mismatch check.
//
// The manifest keeps being WRITTEN (see adoptOnboarding, which now stamps it on
// every non-dry-run path so it can no longer drift): removing the field would
// break any older reader of a file this repo has always written, and writing a
// field nothing here reads costs nothing. Nothing may read it back.
//
// "" means this machine is not paired yet, which is a normal state, not a
// fault: the daemon collects and holds until it is.
func pairedEndpoint() string {
	cfg, err := hook.LoadConfig()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Endpoint
}
