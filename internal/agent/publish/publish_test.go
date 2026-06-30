package publish

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

func TestBuildShapeAndNoRawText(t *testing.T) {
	p := enrich.Run("key sk-live-ABCDEF0123456789 and write a function", "claude_code", enrich.NewDeterministic())
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X", SessionID: "S", Origin: "hook", Version: "2.1"}
	e := Build(j, p, "dg@keld.co", false, time.Unix(0, 0).UTC())

	b, _ := json.Marshal(e)
	if strings.Contains(string(b), "sk-live-ABCDEF0123456789") {
		t.Fatalf("raw secret leaked into payload: %s", b)
	}
	if e.Source.ID != "claude_code" || e.Correlation.ID != "X" {
		t.Fatalf("wire shape wrong: %+v", e)
	}
}

func TestSendPostsHeadersAndBody(t *testing.T) {
	var gotToken, gotActor, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("x-keld-ingest-token")
		gotActor = r.Header.Get("x-keld-actor")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	pub := New(srv.URL, "tok123", "dg@keld.co")
	err := pub.Send(Enrichment{Source: Source{ID: "claude_code"}, Correlation: Correlation{ID: "X"}})
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "tok123" || gotActor != "dg@keld.co" {
		t.Fatalf("headers wrong: token=%q actor=%q", gotToken, gotActor)
	}
	if !strings.Contains(gotBody, `"claude_code"`) {
		t.Fatalf("body missing source: %s", gotBody)
	}
}

func TestSendErrorsOn500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	if err := New(srv.URL, "t", "a").Send(Enrichment{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestBuildDropsEntityTextWhenDisabled(t *testing.T) {
	p := enrich.Profile{Entities: []enrich.Entity{{Label: "org", Text: "AcmeCorpSecret", Start: 0, End: 14}}}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	b, _ := json.Marshal(Build(j, p, "a", false, time.Unix(0, 0).UTC()))
	if strings.Contains(string(b), "AcmeCorpSecret") {
		t.Fatalf("entity text must be dropped when disabled: %s", b)
	}
}

func TestBuildKeepsEntityTextWhenEnabled(t *testing.T) {
	p := enrich.Profile{Entities: []enrich.Entity{{Label: "language", Text: "golang", Start: 0, End: 6}}}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	b, _ := json.Marshal(Build(j, p, "a", true, time.Unix(0, 0).UTC()))
	if !strings.Contains(string(b), "golang") {
		t.Fatalf("entity text should be present when enabled: %s", b)
	}
}
