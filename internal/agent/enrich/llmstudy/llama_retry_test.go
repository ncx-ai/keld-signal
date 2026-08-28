package llmstudy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

func fastPolicy() retry.Policy {
	p := retry.DefaultPolicy()
	p.BaseDelay = time.Millisecond
	p.MaxDelay = 2 * time.Millisecond
	return p
}

// A response cut off mid-object is a property of one sample, not of the server, so it
// must be re-requested. Before this, retry.IsTransient classified it permanent and the
// digest was simply lost — 5 of 20 in one measured sweep.
func TestTruncatedGenerationIsRetried(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		content := `{"value":` // truncated
		if n >= 2 {
			content = `{"value":"ok"}`
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		})
	}))
	defer srv.Close()

	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	var out struct{ Value string }
	if err := l.call("p", map[string]any{}, &out); err != nil {
		t.Fatalf("truncated generation was not retried: %v", err)
	}
	if out.Value != "ok" || n < 2 {
		t.Fatalf("want a second attempt to succeed, got %q after %d attempts", out.Value, n)
	}
}

// A response that parses but is structurally unusable used to fail validation in the
// CALLER, after the retry loop had returned success, so it could never be retried.
func TestValidationFailureIsRetried(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		v := ""
		if n >= 2 {
			v = "present"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{
				"content": `{"value":"` + v + `"}`}}},
		})
	}))
	defer srv.Close()

	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	var out struct{ Value string }
	err := l.callValid("p", map[string]any{}, &out, func() error {
		if out.Value == "" {
			return firstProblem([]string{"value is empty"})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("validation failure was not retried: %v", err)
	}
	if out.Value != "present" || n < 2 {
		t.Fatalf("want a second attempt, got %q after %d attempts", out.Value, n)
	}
}

// The narrow exception must stay narrow: a 4xx is still permanent, so a genuinely bad
// request is not hammered.
func TestClientErrorStaysPermanent(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	var out struct{ Value string }
	if err := l.call("p", map[string]any{}, &out); err == nil {
		t.Fatal("a 400 must not be treated as retryable")
	}
	if n != 1 {
		t.Errorf("a 400 was retried %d times; it must be permanent", n)
	}
}
