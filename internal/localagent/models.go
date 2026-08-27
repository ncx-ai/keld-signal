package localagent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ncx-ai/keld-signal/internal/agent/provision"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ModelState is what a read-only diagnostic can say about one on-device model:
// whether the current LOCAL configuration needs it, and whether its weights
// are on disk. Nothing here downloads, loads, or blocks — presence is a plain
// filesystem stat of the well-known model directory under ~/.keld/models,
// which exists (or doesn't) whether or not keld-agent is even running. That
// is a deliberate choice, not an oversight: neither model's weights-on-disk
// fact is exposed by any daemon/sidecar endpoint today (GLiNER2's isn't
// exposed at all; the encoder's is, in the sidecar's /metrics `embed` block,
// but only while the sidecar is up), and a diagnostic that needed the daemon
// reachable just to answer "is the model there" would make the daemon being
// down look like the model being missing — exactly the confusion
// AGENTS.md's `facets_degraded` discipline (and the task this file exists
// for) says never to produce. Reading the filesystem directly means daemon
// reachability is simply irrelevant to this answer: it is exactly as correct
// with keld-agent stopped as with it running.
//
// PresenceKnown is false only when the stat itself was inconclusive — e.g. a
// permission error on ~/.keld/models — never when a model is legitimately
// absent (os.IsNotExist counts as KNOWN-absent, not undetermined). Callers
// must never report a problem when PresenceKnown is false: "could not tell"
// and "definitely not there" are different facts, and conflating them is the
// one thing rule 3 of this task forbids.
type ModelState struct {
	// Name is a prose-ready identity for problem/status text.
	Name string
	// Dir is where the weights would live.
	Dir string
	// Needed reports whether the current local configuration requires this
	// model at all. False under a mode/toggle that never loads it — reporting
	// an absence there would be the false-positive nag AGENTS.md warns about.
	Needed bool
	// Reason explains why, populated only when Needed.
	Reason string

	PresenceKnown bool
	Present       bool
}

// weightsPresent stats <dir>/model.safetensors — the sentinel
// provision.EnsureModel itself verifies by hash. This is a stat, never a
// hash: cheap presence is the right question for a diagnostic (EnsureModel
// owns correctness), and hashing a multi-GB file on every `doctor` run would
// violate the "never block" rule on its own.
func weightsPresent(dir string) (known, present bool) {
	st, err := os.Stat(filepath.Join(dir, "model.safetensors"))
	switch {
	case err == nil:
		return true, st.Mode().IsRegular() && st.Size() > 0
	case os.IsNotExist(err):
		return true, false
	default:
		// A permission error or similar: the stat was inconclusive, not a
		// confident "absent".
		return false, false
	}
}

// gliner2Dir and encoderDir are where the two provisioners
// (daemon/model_on_demand.go, daemon/encoder_on_demand.go) put the weights.
// Mirrored here as plain path arithmetic (not by importing the daemon
// package, which wires the whole agent) so a read-only CLI command can ask
// the filesystem the identical question the daemon would.
func gliner2Dir() string { return paths.ModelsDir("gliner2-large-v1") }
func encoderDir() string { return paths.ModelsDir(provision.EncoderDirName) }

// GLiNER2State reports the inference model's state given the resolved local
// ml_backend (settings.Settings.MLBackend; "" means the "auto" default).
// GLiNER2 is needed only under "auto": "deterministic" never loads it and
// "off" disables enrichment outright — see settings.Settings.MLEnabled,
// which this mirrors.
func GLiNER2State(mlBackend string) ModelState {
	dir := gliner2Dir()
	s := ModelState{
		Name: "GLiNER2 (fastino/gliner2-large-v1, ~1.9 GB)",
		Dir:  dir,
	}
	s.Needed = mlBackend != "off" && mlBackend != "deterministic"
	if s.Needed {
		mode := mlBackend
		if mode == "" {
			mode = "auto"
		}
		s.Reason = fmt.Sprintf(
			"ml_backend is %q, which runs enrichment on GLiNER2; it is fetched on demand at the first prompt that needs it",
			mode)
	}
	s.PresenceKnown, s.Present = weightsPresent(dir)
	return s
}

// EncoderState reports the text-encoder's state. It is needed only when both
// local toggles gating the signal-embeddings path are on: KELD_TEXTEMBED (the
// sidecar's own switch, mirrored losslessly by features.TextEmbedEnabled) and
// the daemon's local `features` toggle (settings.Settings.FeaturesLocalEnabled).
// See that method's doc for why an org's remote override is invisible here.
func EncoderState(textEmbedEnabled, featuresLocalEnabled bool) ModelState {
	dir := encoderDir()
	s := ModelState{
		Name: "Qwen3-Embedding-0.6B (text encoder, ~1.2 GB)",
		Dir:  dir,
	}
	s.Needed = textEmbedEnabled && featuresLocalEnabled
	if s.Needed {
		s.Reason = "KELD_TEXTEMBED=1 and the local features toggle are both on, which runs the signal-embeddings text encoder; " +
			"it is fetched on demand once a watched transcript advances, and is never waited on"
	}
	s.PresenceKnown, s.Present = weightsPresent(dir)
	return s
}

// ProblemLine returns `doctor`'s problem text for this model, or "" when
// there is nothing to report: not needed (whatever its presence), needed and
// present, or needed but undetermined (never report a problem off an
// inconclusive check).
func (m ModelState) ProblemLine() string {
	if !m.Needed || !m.PresenceKnown || m.Present {
		return ""
	}
	return fmt.Sprintf(
		"%s is not yet on disk (%s). %s. This is not a failure — the fetch is on-demand and non-blocking, and the dependent work queues/spools until it lands.",
		m.Name, m.Dir, m.Reason)
}

// StatusLine returns the informational line for `keld signal status`'s
// on-device-models section, or "" when this model is not worth mentioning:
// not needed and not (confidently) present. That silence is deliberate — see
// AGENTS.md: an absent-and-unneeded model reported as a finding is exactly
// the nag this surface must not produce.
func (m ModelState) StatusLine(label string) string {
	switch {
	case m.Needed && m.Present:
		return fmt.Sprintf("  %-8s ready — %s", label, m.Dir)
	case m.Needed && !m.PresenceKnown:
		return fmt.Sprintf("  %-8s needed, presence could not be determined — %s", label, m.Dir)
	case m.Needed && !m.Present:
		return fmt.Sprintf("  %-8s needed, not yet downloaded — %s", label, m.Dir)
	case !m.Needed && m.Present:
		return fmt.Sprintf("  %-8s downloaded, but not currently needed — %s", label, m.Dir)
	default:
		return ""
	}
}
