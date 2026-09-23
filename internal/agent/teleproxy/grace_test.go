package teleproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// postWith presents the credential in one of the three shapes a tool can use.
// The shapes are not interchangeable: Claude Code and Codex send
// x-keld-ingest-token, Gemini's OTLP SDK cannot send a custom header at all and
// puts the token in the URL, and x-keld-telemetry-secret is this package's own
// name. A grace window honoured in only some of them is a grace window that
// works for some of a person's tools.
func postWith(t *testing.T, p *Proxy, shape, secret string) int {
	t.Helper()
	path := "/v1/logs"
	rr := httptest.NewRecorder()
	var req *http.Request
	switch shape {
	case "ingest-header":
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"resourceLogs":[]}`))
		req.Header.Set("x-keld-ingest-token", secret)
	case "telemetry-header":
		req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"resourceLogs":[]}`))
		req.Header.Set("x-keld-telemetry-secret", secret)
	case "query":
		req = httptest.NewRequest(http.MethodPost, path+"?token="+secret, strings.NewReader(`{"resourceLogs":[]}`))
	case "path":
		req = httptest.NewRequest(http.MethodPost, telemetry.GeminiTokenPath+secret+path, strings.NewReader(`{"resourceLogs":[]}`))
	default:
		t.Fatalf("unknown shape %q", shape)
	}
	p.Handler().ServeHTTP(rr, req)
	return rr.Code
}

var credentialShapes = []string{"ingest-header", "telemetry-header", "query", "path"}

// ⚠️ A ROTATION WITHOUT A GRACE WINDOW IS AN OUTAGE. Tools read their config
// once at startup and keep the credential in memory, so the instant the live
// secret changes every already-running tool is 401'd and nothing on the machine
// can fix it from outside — the same shape as the Atlas token rotation this whole
// package exists to remove. Honouring the outgoing secret for a bounded period
// turns that into a deadline instead.
func TestThePreviousSecretIsAcceptedInsideTheWindow(t *testing.T) {
	p := fast(New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "atlas" }, "new-secret", t.TempDir()))
	p.AcceptPrevious("old-secret", time.Now().Add(-time.Hour))
	for _, shape := range credentialShapes {
		if code := postWith(t, p, shape, "old-secret"); code != http.StatusAccepted {
			t.Errorf("%s: previous secret inside the window = %d, want 202", shape, code)
		}
		if code := postWith(t, p, shape, "new-secret"); code != http.StatusAccepted {
			t.Errorf("%s: current secret = %d, want 202", shape, code)
		}
	}
	p.WaitIdle()
}

// The window is a deadline, not an amnesty: past it the retired secret is as
// good as any other wrong value, or a tool nobody ever reconfigured would go on
// working forever and the rotation would never have happened.
func TestThePreviousSecretIsRejectedAfterTheWindow(t *testing.T) {
	p := fast(New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "atlas" }, "new-secret", t.TempDir()))
	p.AcceptPrevious("old-secret", time.Now().Add(-25*time.Hour)) // default window is 24h
	for _, shape := range credentialShapes {
		if code := postWith(t, p, shape, "old-secret"); code != http.StatusUnauthorized {
			t.Errorf("%s: previous secret past the window = %d, want 401", shape, code)
		}
		if code := postWith(t, p, shape, "new-secret"); code != http.StatusAccepted {
			t.Errorf("%s: current secret = %d, want 202", shape, code)
		}
	}
	p.WaitIdle()
}

func TestTheGraceWindowIsConfigurable(t *testing.T) {
	t.Setenv(EnvSecretGrace, "30m")
	p := fast(New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "atlas" }, "new-secret", t.TempDir()))
	p.AcceptPrevious("old-secret", time.Now().Add(-time.Hour))
	if code := postWith(t, p, "ingest-header", "old-secret"); code != http.StatusUnauthorized {
		t.Fatalf("a shortened window was ignored: %d, want 401", code)
	}
	p.WaitIdle()
}

// ⚠️ AN EMPTY PREVIOUS MUST NEVER AUTHENTICATE ANYTHING. This is the same
// fail-open trap authorized already guards for the live secret:
// ConstantTimeCompare("", "") is 1, so an unset previous plus a caller sending no
// credential at all would be a match. On a route that injects billable usage into
// the org, and on a multi-user host, that is the worst possible default.
func TestAnEmptyPreviousNeverAuthenticates(t *testing.T) {
	p := fast(New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "atlas" }, "new-secret", t.TempDir()))
	p.AcceptPrevious("", time.Now()) // nothing was ever rotated
	for _, shape := range credentialShapes {
		// Not 401 specifically: an empty token in the PATH shape collapses
		// `/t/<token>/v1/logs` to `/t//v1/logs`, which the mux answers with a
		// redirect before auth is reached. What matters is that nothing is
		// ACCEPTED.
		if code := postWith(t, p, shape, ""); code == http.StatusAccepted {
			t.Errorf("%s: empty credential was accepted", shape)
		}
	}
	// And a previous with no recorded instant is not honoured either: there is
	// no window to be inside of.
	p.AcceptPrevious("old-secret", time.Time{})
	if code := postWith(t, p, "ingest-header", "old-secret"); code != http.StatusUnauthorized {
		t.Fatalf("previous with no rotation instant accepted = %d, want 401", code)
	}
	p.WaitIdle()
}
