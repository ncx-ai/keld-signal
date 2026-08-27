package features

import (
	"os"
	"strings"
	"time"
)

// THE THREE TOGGLES, AND WHY ONLY TWO OF THEM LIVE HERE.
//
// The design names three (see the spec's Toggles section):
//
//	capture   the extra ingest rows (say / tok / tool outcome / bin_offset)
//	features  computing and storing the vectors locally
//	publish   sending them to Atlas
//
// `capture` is the SIDECAR'S (KELD_CAPTURE, already shipped) and is emphatically
// not duplicated here. ⚠️ It is the one that is not free to flip: it is
// fingerprinted into `parse_state`, so a change forces a reparse of every
// transcript that sees another append. Separating it is exactly what makes the
// other two free — turning publishing off must not cost a full reparse to turn
// back on — and a Go-side copy of it would be a second place for the
// fingerprinted value to come from.
//
// The two here are both FREE EITHER WAY and both default OFF.

// EnvEnabled switches the signal-embeddings subsystem on Go-side: whether the
// emitter collects feature rows from the analysis service at all.
const EnvEnabled = "KELD_FEATURES"

// EnvPublish switches delivery to Atlas on. Separate from EnvEnabled because
// the two answer different questions and a machine can usefully do the first
// without the second — collect and hold locally while Atlas has no consumer,
// which is the state this ships in.
const EnvPublish = "KELD_FEATURES_PUBLISH"

// EnvTextEmbed is the SIDECAR'S text-embedding toggle, READ here and never set.
//
// ⚠️ It is NOT a fourth Go-side toggle and must not grow an org key by analogy
// to the two above. textembed.enabled() reads this variable out of the
// environment the sidecar was spawned with, and the daemon reads the same
// variable for exactly one purpose: deciding whether to fetch the encoder
// weights at all. No machine whose sidecar will read the toggle as OFF may
// download ~1.2 GB for it.
//
// Which is why the comparison is STRICTLY "1" and does not go through envBool's
// generous vocabulary. textembed.enabled() is `os.environ.get(...) == "1"`, so
// KELD_TEXTEMBED=true is OFF sidecar-side; accepting it here would fetch
// 1,191,586,416 bytes of weights for an encoder that is never asked to run.
// This is the one asymmetry in this file that costs gigabytes to get wrong.
const EnvTextEmbed = "KELD_TEXTEMBED"

// TextEmbedEnabled mirrors textembed.enabled() exactly.
//
// Read once at daemon start rather than per call, and that is not a shortcut:
// the sidecar reads the variable from the environment it was SPAWNED with, so
// a mid-run change to this process's environment cannot reach the running
// sidecar. A live read would let the daemon fetch weights for an encoder the
// already-running sidecar still has switched off.
func TextEmbedEnabled() bool { return os.Getenv(EnvTextEmbed) == "1" }

// EnvInterval overrides the sweep interval.
const EnvInterval = "KELD_FEATURES_INTERVAL"

// DefaultInterval is the sweep cadence, and it is PURE LATENCY: a row is
// collected at most one interval after the sidecar can produce it, and the rows
// themselves are identical whatever the interval, because the sidecar decides
// what is emittable from the store and the clock, not from when it was asked.
//
// Five minutes, for a structural reason rather than a preference. The finest
// anchor grain that carries a structured vector is the 5-minute bin
// (BIN_SECONDS = 300), and a block's edges are bin edges, so nothing coarser
// than a `message` can become available faster than that. A shorter interval
// buys no freshness on two of the three anchor kinds, only repeated queries
// that return what the last one did.
const DefaultInterval = 5 * time.Minute

// Enabled reports the LOCAL value of the `features` toggle: KELD_FEATURES, else
// the agent-config value the caller resolved, else off.
//
// It takes the config value rather than reading agent-config.json itself so the
// precedence is stated in one place and the org override (settings.Live) can be
// layered on top of the result — the same local-then-remote shaping
// IncludeEntityText and PIIRegions use.
func Enabled(fromConfig bool) bool { return envBool(EnvEnabled, fromConfig) }

// PublishEnabled reports the LOCAL value of the `publish` toggle.
func PublishEnabled(fromConfig bool) bool { return envBool(EnvPublish, fromConfig) }

// envBool reads a KELD_* switch, falling back to the config value when the
// variable is unset or blank. "1"/"true"/"on"/"yes" enable, "0"/"false"/"off"/
// "no" disable — BOTH directions, so an operator can turn off what an
// agent-config.json turned on without editing the file.
func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return fallback
}

// IntervalFromEnv is the sweep interval, DefaultInterval unless overridden.
func IntervalFromEnv() time.Duration {
	if v := os.Getenv(EnvInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultInterval
}
