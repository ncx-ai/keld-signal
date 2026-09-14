package atlas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

func TestSendBlocksRecordsTheStatusItGot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	l := &Live{Blocks: publish.New(srv.URL, func() string { return "t" }, "a")}
	if st, _ := l.LastResponse(); st != 0 {
		t.Fatal("nothing tried yet must read as status 0")
	}
	status, err := l.SendBlocks(context.Background(), []publish.BlockEnrichment{{}})
	if err != nil || status != 201 {
		t.Fatalf("want (201, nil), got (%d, %v)", status, err)
	}
	st, at := l.LastResponse()
	if st != 201 || at.IsZero() {
		t.Fatalf("health strip needs the last response: got (%d, %v)", st, at)
	}
}

// A captive portal's 200 must not be recorded as a delivery.
func TestSendBlocksDoesNotBankAnInterceptedResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/html")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("<html>sign in</html>"))
	}))
	defer srv.Close()
	l := &Live{Blocks: publish.New(srv.URL, func() string { return "t" }, "a")}
	status, err := l.SendBlocks(context.Background(), []publish.BlockEnrichment{{}})
	if status != 0 || err == nil {
		t.Fatalf("want (0, error), got (%d, %v)", status, err)
	}
}
