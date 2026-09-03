package sidecar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// THE /attribute STATUS SEAM — WHICH HTTP CODES THE ROUTE RAISES, AND WHAT
// THIS CLIENT CONCLUDES FROM EACH.
//
// ⚠️ THIS FILE EXISTS BECAUSE THE SEAM BLINDNESS THE C1 FIX WAS ABOUT WAS
// REPRODUCED INSIDE THE C1 FIX WAVE. Teaching the client that 404 means "this
// sidecar has no /attribute route" (hold the job forever, never consume an
// attempt, never quarantine) was correct for version skew and silently wrong
// for the OTHER 404 the route raised: an OSError from _span_texts, i.e. a
// deleted, rotated or moved transcript. Those jobs became permanent residents
// of the store, re-POSTed every sweep with no bound, competing for the 24-job
// sweep budget — and the route's own comment had justified choosing 404 by
// leaning on exactly the attempt bound that reading removed.
//
// It slipped through because each half was tested against a fake of the other:
// test_attribution_endpoint.py asserted the SIDECAR returns 404, attrib_test.go
// asserted the DAEMON holds on 404, and nothing compared the two. So this test
// reads the route's ACTUAL Python source and drives every status it can raise
// through the ACTUAL client mapping. A future change to either half fails here.

// attributeRouteSource returns the body of app/main.py's @app.post("/attribute")
// handler — from the decorator to the next top-level decorator or def.
func attributeRouteSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("../../../../sidecar/app/main.py")
	if err != nil {
		t.Fatalf("cannot read the sidecar's main module, so this seam is unpinned: %v", err)
	}
	// Scanned line by line rather than with one regexp: RE2 has no lookahead, so
	// "up to the next top-level def" cannot be expressed without consuming the
	// handler's own def line and terminating immediately.
	lines := strings.Split(string(src), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, `@app.post("/attribute")`) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal(`no @app.post("/attribute") handler found in sidecar/app/main.py`)
	}
	var body []string
	// start+2: skip the decorator and the handler's own `async def` line.
	for i := start + 2; i < len(lines); i++ {
		l := lines[i]
		if strings.HasPrefix(l, "@app.") || strings.HasPrefix(l, "def ") ||
			strings.HasPrefix(l, "async def ") {
			break
		}
		body = append(body, l)
	}
	if len(body) == 0 {
		t.Fatal("the /attribute handler body came out empty; the scan above is wrong")
	}
	return strings.Join(body, "\n")
}

// TestTheAttributeRouteNeverAnswers404ForAnythingButAMissingRoute is the
// load-bearing half: 404 is RESERVED. The client reads it as "no such route",
// which is unbounded-hold semantics, so the route must never be able to produce
// it for a condition of its own.
func TestTheAttributeRouteNeverAnswers404ForAnythingButAMissingRoute(t *testing.T) {
	body := attributeRouteSource(t)
	codes := regexp.MustCompile(`HTTPException\(\s*status_code=(\d{3})`).FindAllStringSubmatch(body, -1)
	if len(codes) == 0 {
		t.Fatal("parsed no HTTPException status codes out of the /attribute handler; " +
			"the comparison would be vacuous — has the route or this regexp changed?")
	}
	seen := map[int]bool{}
	for _, c := range codes {
		n, err := strconv.Atoi(c[1])
		if err != nil {
			t.Fatalf("unparseable status %q", c[1])
		}
		seen[n] = true
		if n == http.StatusNotFound {
			t.Fatalf("the /attribute route raises 404 for a condition of its own.\n" +
				"404 is RESERVED for \"this sidecar has no /attribute route\", which the Go client\n" +
				"maps to RouteUnsupported: the job is held forever, no attempt is consumed and it is\n" +
				"never quarantined. A route-level 404 therefore leaks one unbounded job per affected\n" +
				"block. Use 410 (the /analyze pruned-window precedent) for a permanently unusable\n" +
				"input, or 503 for something a retry could fix.")
		}
	}
	// And the transcript case specifically must still be SOME refusal, or this
	// test would pass just as well against a route that stopped refusing at all.
	if !seen[http.StatusGone] {
		t.Error("the /attribute route no longer raises 410; the unreadable-transcript refusal " +
			"is what this pin was written for — if it moved, update both halves deliberately")
	}

	// Now drive every status the route can raise through the REAL client, so
	// "not 404" is checked against the actual mapping rather than against a
	// belief about it.
	for code := range seen {
		code := code
		t.Run(fmt.Sprintf("status_%d_is_a_genuine_error", code), func(t *testing.T) {
			res, ok := attributeAgainstStatus(t, code, `{"detail":"refused"}`)
			if ok {
				t.Fatalf("status %d decoded as a successful answer", code)
			}
			if res.RouteUnsupported {
				t.Fatalf("status %d mapped to RouteUnsupported: the daemon will hold the job "+
					"forever instead of bounding it with MaxAttempts", code)
			}
		})
	}
}

// The other direction: a genuinely absent route — what a router answers when
// nothing of ours runs — must still reach the hold path, or the I5 fix is gone.
func TestAnAbsentAttributeRouteStillMapsToRouteUnsupported(t *testing.T) {
	res, ok := attributeAgainstStatus(t, http.StatusNotFound, `{"detail":"Not Found"}`)
	if ok {
		t.Fatal("a 404 must not decode as a successful answer")
	}
	if !res.RouteUnsupported {
		t.Fatal("an older sidecar with no /attribute route must be held, not quarantined — " +
			"spool/attrib/bad/ is never re-read, so a quarantine here is permanent loss")
	}
}

// attributeAgainstStatus runs the REAL Client.Attribute against a server that
// answers one status, and returns what the client concluded.
func attributeAgainstStatus(t *testing.T, status int, body string) (AttributeResult, bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	// A short per-call bound: 503 is retryable by design, and this test must not
	// wait out the two-minute production budget to learn that.
	restore := attributeCallTimeout
	attributeCallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { attributeCallTimeout = restore })

	c := New(srv.URL, 5*time.Second).WithContext(context.Background())
	return c.Attribute("/tmp/t.jsonl", "s1", 1, 2, nil)
}
