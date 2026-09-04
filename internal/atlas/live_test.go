package atlas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// The bucket a value belongs to is recoverable ONLY from `team`, because
// wire_projects pools every workstream's values into one flat list and puts the
// workstream's name there. Verified against keld-atlas on 2026-09-04.
func TestFromRemoteProjectsGroupsByTeam(t *testing.T) {
	in := []settings.RemoteProject{
		{ID: "v1", Title: "Keld Signal", Team: "Development", Keywords: []string{"ncx-ai/keld-signal", "agent"}},
		{ID: "v2", Title: "Keld Atlas", Team: "Development", Keywords: []string{"ncx-ai/keld-atlas"}},
		{ID: "v3", Title: "Alpha launch", Team: "Marketing", Description: "the launch page"},
		{ID: "v4", Title: "Loose end"},
	}
	got := FromRemoteProjects(&in)
	if len(got) != 3 {
		t.Fatalf("want 3 buckets, got %d: %+v", len(got), got)
	}
	if got[0].Name != "Development" || got[0].Key != "development" || len(got[0].Values) != 2 {
		t.Fatalf("first bucket wrong: %+v", got[0])
	}
	if got[1].Name != "Marketing" || len(got[1].Values) != 1 {
		t.Fatalf("second bucket wrong: %+v", got[1])
	}
	// A value with no team is still a project; it belongs to the unnamed bucket
	// rather than being dropped or given an invented home.
	if got[2].Key != "ungrouped" || got[2].Name != "" || len(got[2].Values) != 1 {
		t.Fatalf("ungrouped bucket wrong: %+v", got[2])
	}
	if got[0].Values[0].ID != "v1" || got[0].Values[0].Name != "Keld Signal" {
		t.Fatalf("value mapping wrong: %+v", got[0].Values[0])
	}
	// Tags arrive prefix-stripped from Atlas; we pass them through verbatim so
	// the deterministic pass can recognise a repository by shape.
	if len(got[0].Values[0].Tags) != 2 || got[0].Values[0].Tags[0] != "ncx-ai/keld-signal" {
		t.Fatalf("tags must pass through verbatim: %+v", got[0].Values[0].Tags)
	}
}

// An absent key means "this server does not serve projects" and must stay
// distinct from an empty list, which is the org saying it has none.
func TestFromRemoteProjectsAbsentIsNotEmpty(t *testing.T) {
	if got := FromRemoteProjects(nil); got != nil {
		t.Fatalf("absent must yield nil, got %+v", got)
	}
	empty := []settings.RemoteProject{}
	if got := FromRemoteProjects(&empty); got == nil || len(got) != 0 {
		t.Fatalf("empty must yield an empty non-nil slice, got %+v", got)
	}
}

func TestSlugIsStableAndSafe(t *testing.T) {
	for in, want := range map[string]string{
		"Development":     "development",
		"R&D / Platform":  "r-d-platform",
		"  Marketing   ":  "marketing",
		"":                "ungrouped",
		"2026 Initiative": "2026-initiative",
	} {
		if got := slug(in); got != want {
			t.Fatalf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// PatchWorkstream must fail loudly and specifically: Atlas's workstream editor
// is admin-gated behind a user session, so a machine cannot write the org's
// vocabulary at all. Returning ErrUnsupported is what lets the page say "this
// change is local" instead of implying the fleet learned something.
func TestPatchWorkstreamIsUnsupportedNotAttempted(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()
	l := &Live{Blocks: publish.New(srv.URL, func() string { return "t" }, "a")}
	err := l.PatchWorkstream(context.Background(), "development", []Value{{Name: "x"}})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
	if reached {
		t.Fatal("PatchWorkstream must not touch the network at all")
	}
	if errors.Is(err, ErrOffline) {
		t.Fatal("unsupported must stay distinct from offline: one is the server's limit, the other is our choice")
	}
}

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
