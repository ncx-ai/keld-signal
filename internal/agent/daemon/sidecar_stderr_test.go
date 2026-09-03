package daemon

import (
	"context"
	"os"
	"testing"
)

// TestSidecarSpawnWiresStderrToTheDaemons is the test for a bug whose entire symptom was
// SILENCE.
//
// Go connects a child's Stdout/Stderr to the null device when the fields are left nil, and this
// spawn left both nil for the whole life of the daemon. So every `log.warning` in the sidecar --
// a failed transcript ingest, a failed PII scan, an unopenable reference-series store, and the
// encoder watchdog's "child killed" line -- was written and discarded. Nothing failed, no test
// broke, and the only way to notice was to add a warning, tail the log, see nothing, and
// conclude the code path had never run.
//
// It asserts on the REAL closure rather than rebuilding one, because a copy of the wiring in a
// test is exactly what would keep passing after somebody removed the line it is guarding.
//
// ⚠️ Stdout must stay nil, and that half is not an oversight to be tidied later: the sidecar's
// stdout is uvicorn's access log, one line per request, against a daemon that polls /health
// every second. Forwarding it would bury the warnings this test exists to deliver.
func TestSidecarSpawnWiresStderrToTheDaemons(t *testing.T) {
	// A sidecar binary need not exist: sidecarService builds the supervisor (and its spawn
	// closure) without executing anything, and this test never starts a child.
	_, sup, _, ok, err := sidecarService(context.Background(), nil, false)
	if err != nil || !ok || sup == nil {
		t.Skipf("no sidecar wiring on this host (ok=%v err=%v)", ok, err)
	}

	cmd, err := sup.spawn(0)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if cmd.Stderr != os.Stderr {
		t.Errorf("sidecar stderr = %v, want os.Stderr — sidecar warnings go to /dev/null "+
			"without it, which makes every log.warning in app/main.py dead code", cmd.Stderr)
	}
	if cmd.Stdout != nil {
		t.Errorf("sidecar stdout = %v, want nil — that is uvicorn's per-request access log "+
			"and forwarding it buries the warnings", cmd.Stdout)
	}
}
