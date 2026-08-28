package ingress

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/spool"
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

// TestDiscardHandlerAccepts202 confirms the ml_backend=off wiring: a validly
// authenticated pointer POST still gets 202 (so the hook does not spool it
// for later delivery), even though — unlike Handler — DiscardHandler is not
// constructed with a *queue.Queue at all, so there is nothing it could enqueue
// to; this is enforced by its signature, not a runtime check.
func TestDiscardHandlerAccepts202(t *testing.T) {
	rr := post(t, DiscardHandler("s3cret"), "s3cret", pointerBody)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rr.Code)
	}
}

func TestDiscardHandlerRejectsBadSecret401(t *testing.T) {
	rr := post(t, DiscardHandler("s3cret"), "wrong", pointerBody)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rr.Code)
	}
}

func TestDiscardHandlerBadBody400(t *testing.T) {
	rr := post(t, DiscardHandler("s"), "s", "{not json")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}

func TestJobFromMapsAllFields(t *testing.T) {
	p := spool.Pointer{
		Source:      spool.Source{ID: "claude_code", Origin: "hook", Version: "1"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: "P1", SessionID: "S1"},
		Pointer:     &spool.Ptr{TranscriptPath: "/t.jsonl", PromptID: "P1", Cwd: "/c"},
	}
	j := JobFrom(p)
	if j.Source != "claude_code" || j.Origin != "hook" || j.Version != "1" ||
		j.Scheme != "prompt_id" || j.ID != "P1" || j.SessionID != "S1" ||
		j.TranscriptPath != "/t.jsonl" || j.PromptID != "P1" || j.Cwd != "/c" {
		t.Fatalf("JobFrom mismapped: %+v", j)
	}
}

// TestDuplicateIsAcceptedNot429 pins the distinction the ingress used to lose.
//
// ⚠️ `Offer` returns false for FOUR different reasons — already in flight,
// recently completed (dedup), queue genuinely full, queue closed — and the
// ingress collapsed all of them into 429. Only the last two are backpressure.
//
// A dedup is the opposite of "try again later": the prompt is already queued or
// already published, so there is nothing for the caller to retry and nothing
// wrong. Returning 429 made the hook (internal/hook/forward.go treats any >=400
// as failure) durably SPOOL a pointer for a prompt the daemon had already
// finished, which is then drained on the next start and offered again. Observed
// live: a POST for a prompt published one second earlier came back 429 while the
// 1024-slot queue held single digits.
//
// 202 is the honest answer, and it is what makes /enrich idempotent: the hook
// and the transcript watcher legitimately see the same prompt, and that overlap
// is DESIGNED (queue.Complete exists for it). It must not read as overload.
func TestDuplicateIsAcceptedNot429(t *testing.T) {
	q := queue.New(10)
	h := Handler(q, "s3cret")

	if rr := post(t, h, "s3cret", pointerBody); rr.Code != http.StatusAccepted {
		t.Fatalf("first offer: code = %d, want 202", rr.Code)
	}
	// Same key again while still queued: in flight, not overloaded.
	if rr := post(t, h, "s3cret", pointerBody); rr.Code != http.StatusAccepted {
		t.Fatalf("in-flight duplicate: code = %d, want 202 (429 makes the hook spool a "+
			"redundant pointer for work already taken on)", rr.Code)
	}

	// And after it has been fully processed, the recent-completion dedup must
	// answer the same way. This is the exact shape that failed live.
	j, ok := q.Next()
	if !ok {
		t.Fatal("queue drained unexpectedly")
	}
	q.Complete(j)
	if rr := post(t, h, "s3cret", pointerBody); rr.Code != http.StatusAccepted {
		t.Fatalf("completed duplicate: code = %d, want 202", rr.Code)
	}
}

// A genuinely full queue is still 429 — that is real backpressure and the hook
// SHOULD spool it, because the work has not been taken on.
func TestFullQueueIsStill429(t *testing.T) {
	q := queue.New(1)
	h := Handler(q, "s3cret")
	// Fill the single slot with a DIFFERENT key, so the rejection below is
	// capacity and not dedup.
	other := `{"source":{"id":"claude_code","origin":"hook"},"correlation":{"scheme":"prompt_id","id":"OTHER"},"pointer":{"transcript_path":"/t","prompt_id":"OTHER"}}`
	if rr := post(t, h, "s3cret", other); rr.Code != http.StatusAccepted {
		t.Fatalf("seed offer: code = %d, want 202", rr.Code)
	}
	if rr := post(t, h, "s3cret", pointerBody); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("full queue: code = %d, want 429", rr.Code)
	}
}
