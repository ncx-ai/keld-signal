package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// captureReq records the decoded body of one request and answers with `resp`.
func captureReq(t *testing.T, resp any) (*httptest.Server, *map[string]any) {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv, &got
}

// THE FACTS REACH THE WIRE, on all three requests that carry them. /analyze and
// /tick because they characterise a window; /ingest because ingest is where the
// sidecar WRITES the repository rows — a series level per turn, not a value
// overlaid on a digest — so a signal without them leaves the series unable to
// name the repository for the bytes it just consumed.
func TestTheResolvedFactsRideEveryRequestThatNeedsThem(t *testing.T) {
	facts := enrich.ResolvedFacts{
		Repo: "github.com/ncx-ai/keld-atlas", GitBranch: "feat/ledger", Project: "keld"}
	want := map[string]any{
		"repo": "github.com/ncx-ai/keld-atlas", "git_branch": "feat/ledger", "project": "keld"}

	for _, tc := range []struct {
		name string
		call func(c *Client)
		resp any
	}{
		{"/analyze", func(c *Client) { c.Analyze("/tmp/t.jsonl", "p1", 60, facts) },
			map[string]any{"schema": 1, "evidence": 1}},
		{"/tick", func(c *Client) {
			c.Tick("/tmp/t.jsonl", []string{"p1"}, nil, time.Unix(1000, 0), 60, 12, facts)
		}, map[string]any{"cursor": 1.0, "windows": []any{}}},
		{"/ingest", func(c *Client) { c.SignalIngest("/tmp/t.jsonl", facts) },
			map[string]any{"new_lines": 0, "reparsed": false}},
	} {
		srv, got := captureReq(t, tc.resp)
		tc.call(New(srv.URL, 5*time.Second))
		srv.Close()

		sub, ok := (*got)["resolved"].(map[string]any)
		if !ok {
			t.Errorf("%s: no `resolved` object on the request: %v", tc.name, *got)
			continue
		}
		for k, v := range want {
			if sub[k] != v {
				t.Errorf("%s: resolved[%q] = %v, want %v", tc.name, k, sub[k], v)
			}
		}
		// STILL COORDINATES AND IDENTIFIERS, NEVER TEXT. The whole reason this
		// channel is a closed three-field set rather than a free dict.
		for _, forbidden := range []string{"text", "prompt", "prompt_text", "content", "cwd"} {
			if _, present := (*got)[forbidden]; present {
				t.Errorf("%s: request carries %q", tc.name, forbidden)
			}
			if _, present := sub[forbidden]; present {
				t.Errorf("%s: resolved carries %q", tc.name, forbidden)
			}
		}
	}
}

// NOTHING RESOLVED SENDS NO OBJECT, not three empty strings — and that
// distinction is load-bearing rather than cosmetic: the sidecar's back-compat
// path is `resolved is None`, and a request from a caller with no cwd must be
// byte-identical to what it was before this field existed. Asserted on all three
// requests, because `resolvedOrNil` is shared and a call site that built the
// pointer itself would be the way to diverge.
func TestNothingResolvedOmitsTheObjectEntirely(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(c *Client)
		resp any
	}{
		{"/analyze", func(c *Client) {
			c.Analyze("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
		}, map[string]any{"schema": 1}},
		{"/tick", func(c *Client) {
			c.Tick("/tmp/t.jsonl", nil, nil, time.Unix(1000, 0), 60, 12, enrich.ResolvedFacts{})
		}, map[string]any{"cursor": 1.0, "windows": []any{}}},
		{"/ingest", func(c *Client) {
			c.SignalIngest("/tmp/t.jsonl", enrich.ResolvedFacts{})
		}, map[string]any{"new_lines": 0}},
	} {
		srv, got := captureReq(t, tc.resp)
		tc.call(New(srv.URL, 5*time.Second))
		srv.Close()
		if v, present := (*got)["resolved"]; present {
			t.Errorf("%s: an unresolved checkout sent `resolved: %v`; it must be omitted so the "+
				"sidecar's own back-compat path runs", tc.name, v)
		}
	}
}

// A PARTIAL resolution still sends the object, with only what was resolved. The
// common real case: a checkout on a detached HEAD, or a repository with no
// .keld.toml. `omitempty` on each field is what keeps the request honest — an
// empty string on the wire would be a value the sidecar has to special-case,
// where an absent key is already its default.
func TestAPartialResolutionSendsOnlyWhatWasResolved(t *testing.T) {
	srv, got := captureReq(t, map[string]any{"schema": 1})
	New(srv.URL, 5*time.Second).Analyze("/tmp/t.jsonl", "p1", 60,
		enrich.ResolvedFacts{Repo: "github.com/ncx-ai/keld-signal"})
	srv.Close()

	sub, ok := (*got)["resolved"].(map[string]any)
	if !ok {
		t.Fatalf("no `resolved` object: %v", *got)
	}
	if sub["repo"] != "github.com/ncx-ai/keld-signal" {
		t.Errorf("repo = %v", sub["repo"])
	}
	for _, absent := range []string{"git_branch", "project"} {
		if v, present := sub[absent]; present {
			t.Errorf("%s = %v; an unresolved field must be absent, not empty", absent, v)
		}
	}
}
