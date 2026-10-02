package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// A browser sign-in redeems a code bound to a PKCE challenge, so the enroll
// body must carry the verifier (C2). A setup code has none, and its body must
// stay byte-for-byte what it was: no code_verifier key at all.
func TestEnrollWithVerifierSendsIt(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"access_token":"tok","principal":"p","org":"o"}`))
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, "").EnrollWithVerifier("AB12-CD34", "v-123"); err != nil {
		t.Fatal(err)
	}
	if got["code"] != "AB12-CD34" || got["code_verifier"] != "v-123" {
		t.Fatalf("body = %v, want code + code_verifier", got)
	}

	if _, err := NewClient(srv.URL, "").Enroll("AB12-CD34"); err != nil {
		t.Fatal(err)
	}
	if _, present := got["code_verifier"]; present {
		t.Fatalf("a setup-code enroll must not send code_verifier, body = %v", got)
	}
}

// The browser sign-in must tell "that code expired" (410) apart from other
// failures, without changing the message the CLI prints for a setup code.
func TestEnrollGoneCarriesItsStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, "").EnrollWithVerifier("X", "v")
	var se *retry.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusGone {
		t.Fatalf("want a 410 StatusError, got %v", err)
	}
	var ke *errs.Error
	if !errors.As(err, &ke) || ke.Msg != "invalid or expired setup code" {
		t.Fatalf("the printed message must be unchanged, got %v", err)
	}
	if err.Error() != "invalid or expired setup code" {
		t.Fatalf("Error() = %q, want the unchanged message", err.Error())
	}
}
