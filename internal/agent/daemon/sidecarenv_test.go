package daemon

import (
	"os"
	"strings"
	"testing"
)

// envMap collapses an env slice to a map with exec's "last value wins"
// semantics, so a test sees exactly what the child process would.
func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i >= 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	return m
}

func TestSidecarEnvAppliesTenancyCaps(t *testing.T) {
	m := envMap(sidecarEnv([]string{"PATH=/bin"}, "/models/gliner2", "", nil, false))

	if m["KELD_GLINER2_DIR"] != "/models/gliner2" {
		t.Errorf("KELD_GLINER2_DIR = %q, want /models/gliner2", m["KELD_GLINER2_DIR"])
	}
	// Allocator-arena + thread-pool caps keep the sidecar a good tenant:
	// bounded RSS (no per-thread arena fragmentation) and <=2 CPU cores.
	want := map[string]string{
		"MALLOC_ARENA_MAX":         "2",
		"OMP_NUM_THREADS":          "2",
		"MKL_NUM_THREADS":          "2",
		"OPENBLAS_NUM_THREADS":     "2",
		"NUMEXPR_NUM_THREADS":      "2",
		"KELD_SIDECAR_MAX_THREADS": "2",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}
	if m["PATH"] != "/bin" {
		t.Errorf("inherited PATH lost: %q", m["PATH"])
	}
}

func TestSidecarEnvRespectsOperatorOverride(t *testing.T) {
	base := []string{"OMP_NUM_THREADS=8", "MALLOC_ARENA_MAX=4"}
	m := envMap(sidecarEnv(base, "/m", "", nil, false))

	// Operator-set tunables must win over our defaults.
	if m["OMP_NUM_THREADS"] != "8" {
		t.Errorf("operator OMP_NUM_THREADS override lost: got %q", m["OMP_NUM_THREADS"])
	}
	if m["MALLOC_ARENA_MAX"] != "4" {
		t.Errorf("operator MALLOC_ARENA_MAX override lost: got %q", m["MALLOC_ARENA_MAX"])
	}
	// Tunables the operator did NOT set still get our cap.
	if m["MKL_NUM_THREADS"] != "2" {
		t.Errorf("MKL_NUM_THREADS default lost: got %q", m["MKL_NUM_THREADS"])
	}
}

// The sidecar's /analyze is the one endpoint that opens an arbitrary path as
// this user and returns content derived from it, and the sidecar has no auth.
// It therefore refuses anything outside KELD_ANALYZE_ROOTS — and only the
// daemon knows which roots this machine actually uses (CODEX_HOME,
// KELD_WATCH_ROOTS, the platform's Cowork layout).
func TestSidecarEnvPassesTheAnalysisAllowlist(t *testing.T) {
	roots := []string{"/home/u/.claude/projects", "/home/u/.gemini/tmp"}
	m := envMap(sidecarEnv([]string{"PATH=/bin"}, "/m", "", roots, false))

	want := strings.Join(roots, string(os.PathListSeparator))
	if m["KELD_ANALYZE_ROOTS"] != want {
		t.Errorf("KELD_ANALYZE_ROOTS = %q, want %q", m["KELD_ANALYZE_ROOTS"], want)
	}
}

// Empty must still be SET, not omitted: the sidecar treats an absent variable
// as "use the built-in defaults" and an empty one as "deny everything". A
// daemon that found no roots means the latter, and dropping the assignment
// would silently hand the sidecar a wider allowlist than the daemon computed.
func TestSidecarEnvSetsAnEmptyAllowlistRatherThanOmittingIt(t *testing.T) {
	env := sidecarEnv([]string{"PATH=/bin"}, "/m", "", nil, false)
	if !hasEnvKey(env, "KELD_ANALYZE_ROOTS") {
		t.Fatalf("KELD_ANALYZE_ROOTS omitted for an empty root set: %v", env)
	}
	if v := envMap(env)["KELD_ANALYZE_ROOTS"]; v != "" {
		t.Errorf("KELD_ANALYZE_ROOTS = %q, want empty", v)
	}
}

func TestSidecarEnvRespectsAnOperatorAnalysisAllowlist(t *testing.T) {
	base := []string{"KELD_ANALYZE_ROOTS=/srv/transcripts"}
	m := envMap(sidecarEnv(base, "/m", "", []string{"/home/u/.claude/projects"}, false))
	if m["KELD_ANALYZE_ROOTS"] != "/srv/transcripts" {
		t.Errorf("operator override lost: got %q", m["KELD_ANALYZE_ROOTS"])
	}
}

// ⚠️ THIS IS THE THIRD REQUIREMENT'S MECHANISM: attribution's /attribute needs
// the same encoder KELD_TEXTEMBED gates, but the sidecar's own
// textembed.enabled() reads its OWN copy of KELD_TEXTEMBED out of the
// environment it was spawned with — not whatever fetched the weights. So a
// caller that resolved "the encoder is needed" for ANY reason (encoderNeeded)
// must be able to make the spawned sidecar see KELD_TEXTEMBED=1, without ever
// touching KELD_FEATURES / KELD_FEATURES_PUBLISH — those gate a wholly
// separate, privacy-relevant subsystem (computing and publishing
// message-derived vectors) with its own live toggles, and must stay silent
// here.
func TestSidecarEnvSetsKeldTextembedWhenTheEncoderIsNeeded(t *testing.T) {
	t.Run("needed and unset: KELD_TEXTEMBED=1 is set", func(t *testing.T) {
		env := sidecarEnv([]string{"PATH=/bin"}, "/m", "", nil, true)
		m := envMap(env)
		if m["KELD_TEXTEMBED"] != "1" {
			t.Fatalf("KELD_TEXTEMBED = %q, want \"1\"", m["KELD_TEXTEMBED"])
		}
	})
	t.Run("not needed: KELD_TEXTEMBED stays unset", func(t *testing.T) {
		env := sidecarEnv([]string{"PATH=/bin"}, "/m", "", nil, false)
		if hasEnvKey(env, "KELD_TEXTEMBED") {
			t.Fatalf("KELD_TEXTEMBED set with encoderNeeded=false: %v", env)
		}
	})
	t.Run("operator override wins even when needed", func(t *testing.T) {
		base := []string{"PATH=/bin", "KELD_TEXTEMBED=0"}
		env := sidecarEnv(base, "/m", "", nil, true)
		m := envMap(env)
		if m["KELD_TEXTEMBED"] != "0" {
			t.Fatalf("operator KELD_TEXTEMBED=0 override lost: got %q", m["KELD_TEXTEMBED"])
		}
	})
	t.Run("never sets the features/publish toggles, needed or not", func(t *testing.T) {
		for _, needed := range []bool{true, false} {
			env := sidecarEnv([]string{"PATH=/bin"}, "/m", "", nil, needed)
			if hasEnvKey(env, "KELD_FEATURES") || hasEnvKey(env, "KELD_FEATURES_PUBLISH") {
				t.Fatalf("encoderNeeded=%v set a features/publish toggle; the two subsystems must stay independent: %v", needed, env)
			}
		}
	})
}
