package publish

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

func send(t *testing.T, status int, contentType, body string) (int, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("content-type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	p := New(srv.URL, func() string { return "tok" }, "actor")
	return p.SendBlocksResult([]BlockEnrichment{{}})
}

// A captive portal answers 200 with an HTML login page. Before this check the
// body went straight to io.Discard, so that looked exactly like a successful
// publish: the emitter advanced its cursor and the blocks were never re-sent.
func TestCaptivePortalIsNotDelivery(t *testing.T) {
	for name, tc := range map[string]struct{ ct, body string }{
		"html content-type": {"text/html; charset=utf-8", "<!doctype html><title>Sign in</title>"},
		"html body only":    {"", "<html><body>Login required</body></html>"},
		"bom then html":     {"", "\xef\xbb\xbf<html>hi</html>"},
	} {
		t.Run(name, func(t *testing.T) {
			status, err := send(t, 200, tc.ct, tc.body)
			if !errors.Is(err, ErrIntercepted) {
				t.Fatalf("want ErrIntercepted, got %v", err)
			}
			if status != 0 {
				t.Fatalf("an intercepted 200 must not be reported as a status, got %d", status)
			}
			// Not a StatusError: nothing was refused, so retrying later is right.
			var se *retry.StatusError
			if errors.As(err, &se) {
				t.Fatal("interception must not read as a rejection by Atlas")
			}
		})
	}
}

func TestRealResponsesKeepTheirStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		ct     string
		body   string
		wantOK bool
	}{
		"201 json":  {201, "application/json", `{"ok":true}`, true},
		"200 json":  {200, "application/json", `{"accepted":1}`, true},
		"202 empty": {202, "", "", true},
		"401":       {401, "application/json", `{"error":"bad token"}`, false},
		"500":       {500, "application/json", `{"error":"boom"}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			status, err := send(t, tc.status, tc.ct, tc.body)
			if status != tc.status {
				t.Fatalf("status %d, want %d", status, tc.status)
			}
			if tc.wantOK && err != nil {
				t.Fatalf("want success, got %v", err)
			}
			if !tc.wantOK {
				var se *retry.StatusError
				if !errors.As(err, &se) || se.Code != tc.status {
					t.Fatalf("want StatusError(%d), got %v", tc.status, err)
				}
			}
		})
	}
}

// SendBlocks keeps its old signature and its old meaning for every existing
// caller; only the status is new.
func TestSendBlocksStillReportsErrorsOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(201)
	}))
	defer srv.Close()
	p := New(srv.URL, func() string { return "tok" }, "actor")
	if err := p.SendBlocks([]BlockEnrichment{{}}); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
	if err := p.SendBlocks(nil); err != nil {
		t.Fatalf("empty batch must be a no-op, got %v", err)
	}
}
