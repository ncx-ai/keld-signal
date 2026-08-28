package daemon

import (
	"os"
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
func sidecarEnv(base []string, modelDir, encoderDir string, analyzeRoots []string) []string {
	env := make([]string, 0, len(base)+9)
	env = append(env, base...)
	env = append(env, "KELD_GLINER2_DIR="+modelDir)
	if encoderDir != "" {
		env = append(env, "KELD_TEXTEMBED_DIR="+encoderDir)
	}

	if !hasEnvKey(base, "KELD_ANALYZE_ROOTS") {
		env = append(env, "KELD_ANALYZE_ROOTS="+
			strings.Join(analyzeRoots, string(os.PathListSeparator)))
	}

	// Set-if-absent: an operator-provided value in `base` wins.
	for _, kv := range [...][2]string{
		{"MALLOC_ARENA_MAX", "2"},         // bound glibc arena fragmentation
		{"OMP_NUM_THREADS", "2"},          // cap OpenMP pool -> <=2 cores
		{"MKL_NUM_THREADS", "2"},          // cap MKL pool
		{"OPENBLAS_NUM_THREADS", "2"},     // cap OpenBLAS pool
		{"NUMEXPR_NUM_THREADS", "2"},      // cap numexpr pool
		{"KELD_SIDECAR_MAX_THREADS", "2"}, // cap torch intra-op scaler ceiling
	} {
		if !hasEnvKey(base, kv[0]) {
			env = append(env, kv[0]+"="+kv[1])
		}
	}
	return env
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
