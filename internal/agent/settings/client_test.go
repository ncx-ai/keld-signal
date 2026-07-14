package settings

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

func TestClientFetchParsesAndSendsToken(t *testing.T) {
	var gotTok string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTok = r.Header.Get("x-keld-ingest-token")
		w.Write([]byte(`{"include_entity_text": false}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, func() string { return "tok123" }, 5*time.Second)
	r, err := c.Fetch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if gotTok != "tok123" {
		t.Fatalf("token header = %q, want tok123", gotTok)
	}
	if r.IncludeEntityText == nil || *r.IncludeEntityText != false {
		t.Fatalf("include_entity_text = %v, want present false", r.IncludeEntityText)
	}
}

func TestClientFetchErrorsOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // older Atlas without the endpoint
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, func() string { return "t" }, time.Second).Fetch(t.Context()); err == nil {
		t.Fatal("404 should surface as an error (poller keeps last-known)")
	}
}

func TestClientFetchErrorIsTypedStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, func() string { return "t" }, time.Second).Fetch(t.Context())
	var se *retry.StatusError
	if !errors.As(err, &se) {
		t.Fatalf("expected *retry.StatusError, got %T (%v)", err, err)
	}
	if se.Code != http.StatusUnauthorized {
		t.Fatalf("StatusError.Code = %d, want %d", se.Code, http.StatusUnauthorized)
	}
}
