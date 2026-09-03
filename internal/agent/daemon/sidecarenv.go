package daemon

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// sidecarEnv builds the environment for the spawned GLiNER2 sidecar. It always
// sets KELD_GLINER2_DIR (authoritative), then applies resource-tenancy caps as
// defaults that an operator can override by setting them in `base`.
//
// These caps are the single biggest lever on the sidecar's footprint. Without
// an arena cap, glibc spawns a malloc arena per allocating thread — with the
// model load and inference spread across dozens of OpenMP/MKL threads that is
// tens of arenas, each retaining tens of MB of freed heap, so RSS balloons to
// ~2x the model working set (measured: 6.4 GB vs a ~2.6 GB working set on a
// 20-core host). Without thread-pool caps, OpenMP/MKL size their pools to all
// cores, so a single background enrichment can monopolize the machine.
//
// MALLOC_ARENA_MAX in particular MUST be set by the parent: glibc reads it when
// the child's allocator initializes, long before the Python process could set
// it for itself.
//
// analyzeRoots is the allowlist of transcript directories the sidecar's
// /analyze may read (watch.AnalyzeRoots). The sidecar has no auth — serve.py
// binds 127.0.0.1 and that is all — and /analyze is its only endpoint that
// opens an arbitrary path as this user and returns content derived from it, so
// on a multi-user host an unconfined /analyze hands any local user the
// workspaces, branches and named terms out of anyone else's transcripts.
// Passed set-if-absent like the caps, but ALWAYS assigned even when empty: the
// sidecar reads an absent variable as "use your built-in defaults" and an empty
// one as "deny everything", and a daemon that resolved no roots means the
// latter.
// encoderDir is the text encoder's weights directory, or "" to leave
// KELD_TEXTEMBED_DIR unset. Unlike KELD_GLINER2_DIR it is NOT always assigned:
// see encoderDirForSpawn for why an eager assignment would be a claim rather
// than a configuration. Recomputed per spawn by the caller, so a respawn after
// the fetch landed adopts the weights.
//
// encoderNeeded is set-if-absent onto KELD_TEXTEMBED itself, not just the
// dir. THE PROJECT ATTRIBUTION PATH's /attribute needs the same text encoder
// THE SIGNAL-EMBEDDINGS PATH does, but the sidecar's textembed.enabled() reads
// its OWN KELD_TEXTEMBED strictly ("== 1") out of the environment it was
// spawned with — the daemon turning attribution on and fetching the weights
// is not enough on its own if the sidecar process never sees the flag that
// makes it look for them. So a caller that wants the encoder available for
// ANY reason (attribution, KELD_TEXTEMBED, or both) passes encoderNeeded
// true, and this sets KELD_TEXTEMBED=1 only when the operator hasn't already
// set it — an explicit KELD_TEXTEMBED=0 in `base` still wins, matching every
// other set-if-absent default in this function. ⚠️ It does NOT touch
// KELD_FEATURES / KELD_FEATURES_PUBLISH — those gate a wholly separate
// Go-side subsystem (computing and publishing message-derived feature rows)
// that reads its own toggles live, per sweep; making the encoder reachable
// here must never be read as switching that path on.
//
// ⚠️ SO TURNING ATTRIBUTION ON STARTS ENCODING MESSAGE TEXT ON DEVICE, AND
// THAT DESERVES SAYING OUT LOUD RATHER THAN BEING INFERRED FROM THIS FUNCTION.
// `KELD_TEXTEMBED` ships OFF by default and is described everywhere in this
// repo as the toggle that decides whether text is read in order to keep
// something derived from it; `attribution` is a different-sounding switch, and
// it silently implies this one. What is and is NOT true, precisely:
//
//   - The encoder runs LOCALLY and reads message text on device. That is new
//     behaviour on a machine that had the toggle off.
//   - NOTHING derived from it is PUBLISHED by this. Feature-row publication is
//     gated Go-side by KELD_FEATURES / KELD_FEATURES_PUBLISH and the org's
//     `features` toggles, none of which this touches, and /attribute itself
//     answers with project IDS only — no text, no span, no offset, no vector.
//   - The cost is real and local: ~1.2 GB of weights fetched on demand and a
//     child measured at 1.70 GB resident / 2.35-2.43 GB peak, on a memory
//     budget AGENTS.md already documents as oversubscribed.
//
// An operator who wants attribution WITHOUT on-device encoding sets
// KELD_TEXTEMBED=0 explicitly — set-if-absent means that wins — and accepts
// that /attribute then answers `skipped:disabled` for every block, which is a
// stated skip rather than a silent narrowing.
func sidecarEnv(base []string, modelDir, encoderDir string, analyzeRoots []string, encoderNeeded bool) []string {
	env := make([]string, 0, len(base)+10)
	env = append(env, base...)
	env = append(env, "KELD_GLINER2_DIR="+modelDir)
	if encoderDir != "" {
		env = append(env, "KELD_TEXTEMBED_DIR="+encoderDir)
	}
	if encoderNeeded && !hasEnvKey(base, "KELD_TEXTEMBED") {
		env = append(env, "KELD_TEXTEMBED=1")
	}

	if !hasEnvKey(base, "KELD_ANALYZE_ROOTS") {
		env = append(env, "KELD_ANALYZE_ROOTS="+
			strings.Join(analyzeRoots, string(os.PathListSeparator)))
	}

	// Set-if-absent: an operator-provided value in `base` wins.
	for _, kv := range [...][2]string{
		{"MALLOC_ARENA_MAX", "2"},     // bound glibc arena fragmentation
		{"OMP_NUM_THREADS", "2"},      // cap OpenMP pool -> <=2 cores
		{"MKL_NUM_THREADS", "2"},      // cap MKL pool
		{"OPENBLAS_NUM_THREADS", "2"}, // cap OpenBLAS pool
		{"NUMEXPR_NUM_THREADS", "2"},  // cap numexpr pool
		{"KELD_SIDECAR_MAX_THREADS", strconv.Itoa(encoderThreads(runtime.NumCPU()))},
	} {
		if !hasEnvKey(base, kv[0]) {
			env = append(env, kv[0]+"="+kv[1])
		}
	}
	return env
}

// encoderThreadCeiling bounds what encoderThreads may return however many cores a host has.
// Six because this is a background process on somebody's laptop and the machine's own work has
// to stay responsive; also because the fast cores are what is worth having (an Apple M-series
// host counts its efficiency cores in NumCPU, and a thread landing on one is slower than no
// extra thread at all).
const encoderThreadCeiling = 6

// encoderThreads is the torch intra-op ceiling handed to the sidecar: half the host's cores,
// bounded to [2, encoderThreadCeiling].
//
// ⚠️ **THIS WAS A HARD-CODED 2 AND IT WAS THE ATTRIBUTION BOTTLENECK.** The sidecar has a CPU
// SCALER (app/cpuscale.py) whose whole job is exactly this decision — half the cores on an idle
// host, ramping down to a floor under load — and its own default when the variable is absent is
// `cores // 2`. The daemon then set the variable unconditionally to 2, which is not a cap on
// that policy but a replacement for it: on a 10-core machine the scaler's ceiling became 2 and
// the ramp had nowhere to ramp. Measured cost on an M1 Pro: 194% CPU (two cores flat out) and
// 65-110 s to encode ONE block, against a 120 s deadline the daemon then failed the job on.
//
// The constant was defensible when it was written — an encode was a few short user messages,
// and the value sits beside four allocator/BLAS caps that ARE flat 2s for a different reason
// (bounding thread-pool footprint, not throughput). What changed underneath it is that
// attribution encodes whole blocks, user and assistant turns alike, ~15 chunks at a time.
//
// Deriving from NumCPU rather than deleting the variable keeps the daemon in charge of the
// ceiling — a fleet machine must not be able to take every core because torch defaulted to all
// of them — while letting a capable host actually use what it has. Still set-if-absent, so
// KELD_SIDECAR_MAX_THREADS in the environment continues to win.
func encoderThreads(cores int) int {
	n := cores / 2
	if n < 2 {
		return 2
	}
	if n > encoderThreadCeiling {
		return encoderThreadCeiling
	}
	return n
}

// hasEnvKey reports whether env contains an assignment for key.
func hasEnvKey(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}
