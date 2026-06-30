package ingress

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

func post(t *testing.T, h http.Handler, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/enrich", strings.NewReader(body))
	if secret != "" {
		req.Header.Set("x-keld-agent-secret", secret)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

const pointerBody = `{"source":{"id":"claude_code","origin":"hook"},"correlation":{"scheme":"prompt_id","id":"X"},"pointer":{"transcript_path":"/t","prompt_id":"X"}}`

func TestAcceptsPointer202(t *testing.T) {
	q := queue.New(10)
	rr := post(t, Handler(q, "s3cret"), "s3cret", pointerBody)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rr.Code)
	}
}

func TestRejectsBadSecret401(t *testing.T) {
	rr := post(t, Handler(queue.New(10), "s3cret"), "wrong", pointerBody)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rr.Code)
	}
}

func TestShed429WhenFull(t *testing.T) {
	q := queue.New(1)
	h := Handler(q, "s")
	_ = post(t, h, "s", pointerBody)
	// second distinct key fills past capacity -> shed
	rr := post(t, h, "s", `{"source":{"id":"claude_code"},"correlation":{"scheme":"prompt_id","id":"Y"},"pointer":{"transcript_path":"/t","prompt_id":"Y"}}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rr.Code)
	}
}

func TestBadBody400(t *testing.T) {
	rr := post(t, Handler(queue.New(10), "s"), "s", "{not json")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestNonPostReturns405(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/enrich", nil)
	req.Header.Set("x-keld-agent-secret", "s")
	rr := httptest.NewRecorder()
	Handler(queue.New(10), "s").ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rr.Code)
	}
}
