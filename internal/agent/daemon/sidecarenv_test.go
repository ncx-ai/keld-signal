package daemon

import (
	"os"
	"runtime"
	"strconv"
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
	// Allocator-arena + thread-pool caps keep the sidecar a good tenant: bounded RSS (no
	// per-thread arena fragmentation) and a bounded share of the CPU.
	//
	// ⚠️ KELD_SIDECAR_MAX_THREADS is deliberately NOT in this table any more. It used to be a
	// flat "2" beside the four allocator caps and was asserted here as if it were one of them,
	// which is how it stayed at 2 while the work behind it grew from a few short messages to
	// whole blocks. The four below bound a thread POOL's footprint; that one bounds encoder
	// THROUGHPUT, and it is host-derived — see TestEncoderThreadsScalesWithTheHost.
	want := map[string]string{
		"MALLOC_ARENA_MAX":     "2",
		"OMP_NUM_THREADS":      "2",
		"MKL_NUM_THREADS":      "2",
		"OPENBLAS_NUM_THREADS": "2",
		"NUMEXPR_NUM_THREADS":  "2",
	}
	if got, want := m["KELD_SIDECAR_MAX_THREADS"], strconv.Itoa(encoderThreads(runtime.NumCPU())); got != want {
		t.Errorf("KELD_SIDECAR_MAX_THREADS = %q, want %q (derived from %d cores)",
			got, want, runtime.NumCPU())
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

// TestEncoderThreadsScalesWithTheHost pins the ceiling the text encoder actually runs at.
//
// ⚠️ **THIS WAS A HARD-CODED 2 AND IT WAS THE ATTRIBUTION BOTTLENECK.** The sidecar owns a CPU
// scaler (app/cpuscale.py) whose entire job is this decision — half the cores when the host is
// idle, ramping down under load — and whose own default when the variable is unset is
// `cores // 2`. Setting the variable to 2 unconditionally did not cap that policy, it REPLACED
// it: on a 10-core machine the ramp's ceiling became 2 and there was nothing to ramp. Measured
// cost on an M1 Pro: two cores flat out and 65-110s to encode ONE block, against a 120s deadline
// the daemon then counted as a failed attempt.
//
// Half the cores, floored at 2 and capped at 6. The cap is not arithmetic shyness: this is a
// background process on somebody's laptop and their own work has to stay responsive, and on an
// Apple M-series host NumCPU counts efficiency cores that a torch worker gains little from.
func TestEncoderThreadsScalesWithTheHost(t *testing.T) {
	for _, tc := range []struct{ cores, want int }{
		{1, 2}, // a single-core host still gets the floor; below it torch is serial
		{2, 2},
		{4, 2},
		{8, 4},
		{10, 5}, // the M1 Pro this was measured on: 5, not 2
		{12, 6},
		{20, 6}, // the ceiling holds however large the host
		{128, 6},
	} {
		if got := encoderThreads(tc.cores); got != tc.want {
			t.Errorf("encoderThreads(%d) = %d, want %d", tc.cores, got, tc.want)
		}
	}
}

// TestEncoderThreadsStillYieldsToTheOperator: the value is set-if-absent like every other
// tunable here, so a machine that needs a different answer says so and is obeyed. That is what
// makes deriving it safe — it is a better default, not a policy the operator cannot escape.
func TestEncoderThreadsStillYieldsToTheOperator(t *testing.T) {
	m := envMap(sidecarEnv([]string{"KELD_SIDECAR_MAX_THREADS=1"}, "/m", "", nil, false))
	if m["KELD_SIDECAR_MAX_THREADS"] != "1" {
		t.Errorf("operator override lost: got %q", m["KELD_SIDECAR_MAX_THREADS"])
	}
}
