package ingress

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/queue"
)

func TestExtraRoutesMountBehindTheSecret(t *testing.T) {
	q := queue.New(8)
	defer q.Close()
	hit := 0
	route := Route(func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("/v1/probe", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit++
			w.WriteHeader(http.StatusNoContent)
		})))
	})
	for name, h := range map[string]http.Handler{
		"handler": Handler(q, "s3cret", route),
		"discard": DiscardHandler("s3cret", route),
	} {
		srv := httptest.NewServer(h)
		defer srv.Close()
		// no secret → 401, handler not reached
		res, err := http.Get(srv.URL + "/v1/probe")
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: no secret: want 401, got %d", name, res.StatusCode)
		}
		// header secret → reached
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/probe", nil)
		req.Header.Set("x-keld-agent-secret", "s3cret")
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: header secret: want 204, got %d", name, res.StatusCode)
		}
		// cookie secret → reached (the page's path)
		req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/probe", nil)
		req.AddCookie(&http.Cookie{Name: "keld_secret", Value: "s3cret"})
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: cookie secret: want 204, got %d", name, res.StatusCode)
		}
	}
	if hit != 4 {
		t.Fatalf("handler reached %d times, want 4", hit)
	}
}

func TestEmptySecretNeverAuthorizes(t *testing.T) {
	route := Route(func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("/v1/probe", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	})
	srv := httptest.NewServer(DiscardHandler("", route))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/probe", nil)
	req.Header.Set("x-keld-agent-secret", "")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty secret must not authorize, got %d", res.StatusCode)
	}
}
