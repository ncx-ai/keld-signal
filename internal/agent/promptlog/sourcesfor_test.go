package promptlog

import "testing"

// ⚠️ THE TWO PATHS ARE COMPLEMENTS, AND "NEITHER" IS THE FAILURE THIS PINS.
// A tool's usage reaches Atlas either because the tool posts its own OTLP (the
// `tool_otlp` switch wrote an OTEL block into its config) or because this
// mirror reads it out of the transcript. Exactly one, always one.
//
// Both halves shipped correct and the seam between them did not: the switch
// went off by default while this package still defaulted to {cowork}, so
// Claude Code and Codex emitted nothing at all — and the pane read `working`,
// because with the switch off the otel lane is not expected. The conformance
// chain caught it as "telemetry: 0 OTLP forwarded".
func TestTheMirrorIsTheComplementOfTheSwitch(t *testing.T) {
	for _, otlp := range []bool{false, true} {
		got := SourcesFor(otlp)
		for _, src := range []string{sourceClaudeCode, sourceCodex, sourceGemini} {
			if got[src] == otlp {
				state := "off"
				if otlp {
					state = "on"
				}
				if otlp {
					t.Errorf("tool_otlp %s and %s is ALSO mirrored: its usage would be counted twice", state, src)
				} else {
					t.Errorf("tool_otlp %s and %s is not mirrored either: that tool emits no usage at all", state, src)
				}
			}
		}
	}
}

// Cowork is outside the complement: its sandbox blocks its own egress, so the
// host-side mirror is the only path it has ever had, whatever the switch says.
func TestCoworkIsMirroredWhicheverWayTheSwitchIsSet(t *testing.T) {
	for _, otlp := range []bool{false, true} {
		if !SourcesFor(otlp)[sourceCowork] {
			t.Errorf("tool_otlp=%v dropped cowork, whose egress is blocked by design", otlp)
		}
	}
}

// An operator who named the set gets the set they named, in both positions —
// the env override is how a machine opts out of the rule entirely.
func TestTheEnvOverrideWinsOverTheComplement(t *testing.T) {
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "cowork")
	for _, otlp := range []bool{false, true} {
		got := SourcesFor(otlp)
		if got[sourceClaudeCode] || len(got) != 1 || !got[sourceCowork] {
			t.Errorf("tool_otlp=%v ignored the operator's explicit source list: %v", otlp, got)
		}
	}
	t.Setenv("KELD_WATCH_TELEMETRY", "off")
	if got := SourcesFor(false); len(got) != 0 {
		t.Errorf("KELD_WATCH_TELEMETRY=off still mirrored %v", got)
	}
}

// ⚠️ THE MIRROR MUST ANSWER TO THE ID THE WATCHER ACTUALLY EMITS. `watch`
// builds `Root{SourceID: "gemini_cli"}` (roots.go) and `isDocumentSource`
// matches that same string, so that is what reaches `ObserveFile`. This package
// said "gemini", `eligible` missed, and the whole Gemini mirror was dead code —
// silently, because a miss returns rather than errors. Chain A for gemini_cli
// reported transcript PASS / pointer PASS / publish PASS / telemetry 0 while
// the chat file held `tokens: {input 42, output 3}`.
//
// Pinned as a literal rather than by importing `watch`: this package must not
// depend on the watcher to be testable, and the literal is the contract. If
// roots.go ever renames the source, this fails and names the pair.
func TestTheGeminiIdTheWatcherEmitsIsMirrored(t *testing.T) {
	const watcherEmits = "gemini_cli" // watch/roots.go, watch.isDocumentSource
	if !SourcesFor(false)[watcherEmits] {
		t.Fatalf("the mirror does not answer to %q, which is the only id the watcher ever hands it — "+
			"Gemini usage reaches Atlas on no lane at all", watcherEmits)
	}
	// The hook's spelling too: one tool, two names, both live in this product.
	if !SourcesFor(false)["gemini"] {
		t.Error(`the mirror does not answer to "gemini", the name the hook keld writes uses`)
	}
}
