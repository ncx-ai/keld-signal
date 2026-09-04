package daemon

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// fakeProbe answers /health a fixed number of times as "not up yet", then with
// a version — the real shape, where the daemon outruns the sidecar's start.
type fakeProbe struct {
	downFor int32 // answers !ok this many times first
	calls   atomic.Int32
	version string
	ok      bool
}

func (p *fakeProbe) Health(ctx context.Context) (sidecar.HealthReport, bool) {
	if p.calls.Add(1) <= p.downFor {
		return sidecar.HealthReport{}, false
	}
	return sidecar.HealthReport{Ok: true, Version: p.version}, p.ok
}

func withCLIVersion(t *testing.T, v string) {
	t.Helper()
	old := version.CLI
	version.CLI = v
	t.Cleanup(func() { version.CLI = old })
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// AC-5 + AC-6. The two halves disagree: one event, one log line, both naming
// both versions.
func TestSidecarVersionSkewEvent(t *testing.T) {
	withCLIVersion(t, "2.3.0")
	buf := captureLog(t)
	em := enabledEmitter()

	noteSidecarVersion("2.2.1", em)

	ev := findEvent(em.Drain(), "sidecar.version_skew")
	if ev == nil {
		t.Fatal("no sidecar.version_skew event — the fleet cannot see a machine whose " +
			"two halves disagree, which is how this went unnoticed for three weeks")
	}
	if ev.Severity != clientevents.SevWarn {
		t.Errorf("severity = %v, want warn", ev.Severity)
	}
	if ev.Fields["daemon"] != "2.3.0" || ev.Fields["sidecar"] != "2.2.1" {
		t.Errorf("fields = %+v, want both versions named", ev.Fields)
	}
	line := buf.String()
	if !strings.Contains(line, "2.3.0") || !strings.Contains(line, "2.2.1") {
		t.Errorf("the log line must name BOTH versions, or a reader cannot tell which "+
			"half to move: %q", line)
	}
	if !strings.Contains(line, "installer") {
		t.Errorf("the remedy must be the INSTALLER: restarting the daemon fixes nothing "+
			"when the wrong artifact is on disk. got: %q", line)
	}
}

// AC-6, the other half: agreement is SILENT. A line per daemon start that says
// "all is well" is the noise that gets filtered, taking the real one with it.
func TestSidecarVersionMatchIsSilent(t *testing.T) {
	withCLIVersion(t, "2.3.0")
	buf := captureLog(t)
	em := enabledEmitter()

	noteSidecarVersion("v2.3.0", em) // one leading v is not a disagreement

	if ev := findEvent(em.Drain(), "sidecar.version_skew"); ev != nil {
		t.Fatalf("emitted skew for two halves at the same version: %+v", ev.Fields)
	}
	if buf.Len() != 0 {
		t.Fatalf("logged on agreement: %q", buf.String())
	}
}

// AC-9. A "dev" half means the comparison cannot conclude. Reporting it would
// tell every developer their machine is broken, and a check that cries wolf on
// every dev machine is one nobody reads on the machine that matters.
func TestSidecarVersionSkewDevIsSilent(t *testing.T) {
	for _, c := range [][2]string{
		{"dev", "2.2.1"}, // source checkout of the agent against a release sidecar
		{"2.3.0", "dev"}, // release agent against `make sidecar`'s venv wrapper
		{"2.3.0", ""},    // a sidecar too old to carry the field at all
	} {
		withCLIVersion(t, c[0])
		buf := captureLog(t)
		em := enabledEmitter()

		noteSidecarVersion(c[1], em)

		if ev := findEvent(em.Drain(), "sidecar.version_skew"); ev != nil {
			t.Errorf("daemon %q / sidecar %q: emitted skew off an unknowable comparison", c[0], c[1])
		}
		if buf.Len() != 0 {
			t.Errorf("daemon %q / sidecar %q: logged %q", c[0], c[1], buf.String())
		}
	}
}

// The reporter waits for a sidecar that is still coming up rather than
// concluding from its silence — a cold freeze is a ~15,000-file tree, so the
// daemon routinely outruns it, and "no answer" is not "no version".
func TestSidecarVersionSkewWaitsForTheSidecarToComeUp(t *testing.T) {
	withCLIVersion(t, "2.3.0")
	buf := captureLog(t)
	em := enabledEmitter()
	probe := &fakeProbe{downFor: 2, version: "2.2.1", ok: true}

	done := make(chan struct{})
	go func() { defer close(done); reportSidecarVersionSkew(context.Background(), probe, em) }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("reporter never finished")
	}
	if got := probe.calls.Load(); got != 3 {
		t.Errorf("probed %d times, want 3 (two down, then the answer)", got)
	}
	if findEvent(em.Drain(), "sidecar.version_skew") == nil {
		t.Fatalf("no event after the sidecar came up: %q", buf.String())
	}
}

// A sidecar that never answers is NOT reported here. That is a different
// failure with its own event (sidecar.unavailable, from the supervisor), and
// describing one failure twice under two names is how a fleet view stops
// meaning anything.
func TestSidecarVersionSkewSaysNothingWhenTheSidecarNeverAnswers(t *testing.T) {
	withCLIVersion(t, "2.3.0")
	buf := captureLog(t)
	em := enabledEmitter()

	ctx, cancel := context.WithCancel(context.Background())
	probe := &fakeProbe{downFor: 1 << 30}
	done := make(chan struct{})
	go func() { defer close(done); reportSidecarVersionSkew(ctx, probe, em) }()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("reporter did not stop on ctx cancellation")
	}
	if len(em.Drain()) != 0 || buf.Len() != 0 {
		t.Fatalf("reported something about a sidecar that never answered: %q", buf.String())
	}
}

// The production client must satisfy the probe, or the wiring compiles against
// an interface nothing real implements.
var _ versionProbe = (*sidecar.Client)(nil)
