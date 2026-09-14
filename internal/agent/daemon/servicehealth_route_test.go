package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

const routeTestSecret = "s3cret"

// mountRoutes builds the loopback mux the same way ingress does — same secret
// middleware, same Route seam — so an auth test here is a test of the thing
// that actually ships rather than of a handler wired specially for the test.
func mountRoutes(t *testing.T, routes ...ingress.Route) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	auth := ingress.RequireSecret(routeTestSecret)
	for _, r := range routes {
		r(mux, auth)
	}
	return mux
}

func authed(method, path string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("x-keld-agent-secret", routeTestSecret)
	return req
}

// TestLedgerAlwaysCarriesTheServiceKey.
//
// ⚠️ Remove this and the key becomes conditional, at which point the page
// cannot tell "this daemon is too old to say" from "everything is fine" — the
// exact confusion the rest of this codebase refuses about a check that did not
// run. It must be present on `ok` too.
func TestLedgerAlwaysCarriesTheServiceKey(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	l := ledger.New()
	h := mountRoutes(t, ledgerRoute(l, func() serviceWire {
		return serviceWire{State: string(serviceOK), Reason: "", Failures: 0}
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authed(http.MethodGet, "/v1/ledger"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	// Decoded as a bare map, because the assertion is about the JSON the page
	// receives, not about a Go type that could be changed to match.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rr.Body.String())
	}
	for _, k := range []string{"generated_at", "health", "blocks", "pending", "service"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("key %q missing from /v1/ledger; keys = %v", k, keysOf(body))
		}
	}

	var svc serviceWire
	if err := json.Unmarshal(body["service"], &svc); err != nil {
		t.Fatalf("service decode: %v", err)
	}
	if svc.State != "ok" {
		t.Fatalf("service.state = %q, want ok", svc.State)
	}
}

// TestServiceIsASiblingOfHealthNotAMemberOfIt.
//
// ⚠️ Remove this and someone folds service health into the `health` array
// because both are "health". They answer different questions: `health` is the
// PIPELINE's per-stage table, and merging them lets a block legitimately
// pending delivery render as a dead sidecar — and a dead sidecar hide inside a
// row about blocks.
func TestServiceIsASiblingOfHealthNotAMemberOfIt(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	l := ledger.New()
	l.SetHealth(ledger.Health{Key: ledger.HealthDaemon, Status: ledger.StatusOK, At: time.Now()})
	h := mountRoutes(t, ledgerRoute(l, func() serviceWire {
		return serviceWire{State: string(serviceStuck), Reason: "restarted twice", Failures: 9}
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authed(http.MethodGet, "/v1/ledger"))

	var body struct {
		Health  []ledger.HealthEntry `json:"health"`
		Service serviceWire          `json:"service"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, e := range body.Health {
		if e.Key == "service" {
			t.Fatal("service health must not appear as a row inside `health`; it is a top-level sibling")
		}
	}
	if body.Service.State != string(serviceStuck) || body.Service.Failures != 9 {
		t.Fatalf("service = %#v, want the stuck state and its failure count", body.Service)
	}
}

// TestLedgerServiceKeyIsNotApplicableBeforeAnOwnerExists.
//
// ⚠️ Reachable in production: the onboarding handler mounts these routes on a
// machine that has no configuration and therefore no analysis service at all.
// Remove this and an unpaired machine's page renders a warning about a service
// that was never supposed to be there.
func TestLedgerServiceKeyIsNotApplicableBeforeAnOwnerExists(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	old := currentServiceHealth.Load()
	currentServiceHealth.Store(nil)
	t.Cleanup(func() { currentServiceHealth.Store(old) })

	h := mountRoutes(t, ledgerRoute(ledger.New(), func() serviceWire {
		return currentServiceHealth.Load().Snapshot()
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authed(http.MethodGet, "/v1/ledger"))

	var body struct {
		Service serviceWire `json:"service"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if body.Service.State != string(serviceNotApplicable) {
		t.Fatalf("service.state = %q with no owner, want %q", body.Service.State, serviceNotApplicable)
	}
	if body.Service.Reason == "" {
		t.Fatal("not_applicable must still say why; an empty sentence is unrenderable")
	}
}

// TestServiceRestartReturns202WithoutBlocking.
//
// ⚠️ Remove this and the obvious implementation — call the restart, wait, then
// answer — holds the page's request open for the stop grace (5s) plus the
// supervisor's ready timeout (30s). The browser times out first and reports a
// failure for a restart that worked, which is worse than no button.
func TestServiceRestartReturns202WithoutBlocking(t *testing.T) {
	slow := make(chan struct{})
	t.Cleanup(func() { close(slow) })
	h := mountRoutes(t, serviceRestartRoute(func() error {
		<-slow // a restart that never finishes within the request
		return nil
	}, newRestartLimiter()))

	done := make(chan int, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, authed(http.MethodPost, "/v1/service/restart"))
		done <- rr.Code
	}()

	select {
	case code := <-done:
		if code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("POST /v1/service/restart blocked on the restart completing")
	}
}

// TestServiceRestartRejectsAWrongOrAbsentSecret.
//
// ⚠️ Remove this and an unauthenticated caller on the machine can restart the
// analysis service at will. The check is ingress.RequireSecret, the same one
// /enrich has always used — this test is what proves the route is actually
// wrapped in it rather than merely mounted next to routes that are.
func TestServiceRestartRejectsAWrongOrAbsentSecret(t *testing.T) {
	var called int
	h := mountRoutes(t, serviceRestartRoute(func() error { called++; return nil }, newRestartLimiter()))

	for _, tc := range []struct{ name, secret string }{
		{"absent", ""},
		{"wrong", "not-the-secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/service/restart", nil)
			if tc.secret != "" {
				req.Header.Set("x-keld-agent-secret", tc.secret)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rr.Code)
			}
		})
	}
	if called != 0 {
		t.Fatalf("restart ran %d times for unauthenticated requests, want 0", called)
	}
}

// TestServiceRestartIsRateLimited.
//
// ⚠️ Remove this and a person holding down the button is a restart loop with a
// human in it — issuing spawns faster than the service can come up, which
// produces exactly the never-ready machine the ladder exists to prevent.
func TestServiceRestartIsRateLimited(t *testing.T) {
	var called int
	lim := newRestartLimiter()
	lim.min = time.Hour
	h := mountRoutes(t, serviceRestartRoute(func() error { called++; return nil }, lim))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, authed(http.MethodPost, "/v1/service/restart"))
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", first.Code)
	}

	second := httptest.NewRecorder()
	h.ServeHTTP(second, authed(http.MethodPost, "/v1/service/restart"))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("a 429 must say when to try again, or the page has to guess")
	}
	if called != 1 {
		t.Fatalf("restart ran %d times, want 1 — the second request must not reach it", called)
	}
}

// TestServiceRestartSaysSoWhenThereIsNothingToRestart.
//
// ⚠️ Remove this and a machine with no analysis service answers 202 to a
// restart that will never happen — telling the page a restart is under way when
// nothing is. Same refusal the not_applicable state makes one layer up.
func TestServiceRestartSaysSoWhenThereIsNothingToRestart(t *testing.T) {
	h := mountRoutes(t, serviceRestartRoute(func() error { return errNoServiceToRestart }, newRestartLimiter()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authed(http.MethodPost, "/v1/service/restart"))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}

	h2 := mountRoutes(t, serviceRestartRoute(func() error { return ErrSupervisorStopped }, newRestartLimiter()))
	rr2 := httptest.NewRecorder()
	h2.ServeHTTP(rr2, authed(http.MethodPost, "/v1/service/restart"))
	if rr2.Code != http.StatusConflict {
		t.Fatalf("surrendered-supervisor status = %d, want 409", rr2.Code)
	}
}

// TestServiceRestartRejectsNonPost keeps the route from being triggerable by a
// GET — a link, a prefetch, or a browser address bar.
func TestServiceRestartRejectsNonPost(t *testing.T) {
	var called int
	h := mountRoutes(t, serviceRestartRoute(func() error { called++; return nil }, newRestartLimiter()))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, authed(http.MethodGet, "/v1/service/restart"))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rr.Code)
	}
	if called != 0 {
		t.Fatalf("restart ran %d times for a GET, want 0", called)
	}
}

// TestRestartSidecarThroughTheOwnerReportsNoServiceRatherThanSucceeding pins
// the owner side of the 409 above.
func TestRestartSidecarThroughTheOwnerReportsNoServiceRatherThanSucceeding(t *testing.T) {
	h := newTestHealth(t, nil, nil, nil, nil)
	if err := h.RestartSidecar(); !errors.Is(err, errNoServiceToRestart) {
		t.Fatalf("RestartSidecar with no service = %v, want errNoServiceToRestart", err)
	}
	var nilOwner *serviceHealth
	if err := nilOwner.RestartSidecar(); !errors.Is(err, errNoServiceToRestart) {
		t.Fatalf("RestartSidecar on a nil owner = %v, want errNoServiceToRestart", err)
	}
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
