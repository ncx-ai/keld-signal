package daemon

import (
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
	m := envMap(sidecarEnv([]string{"PATH=/bin"}, "/models/gliner2"))

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
	m := envMap(sidecarEnv(base, "/m"))

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
