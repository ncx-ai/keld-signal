package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

func getUsage(t *testing.T, mux *http.ServeMux, query string) (*httptest.ResponseRecorder, usageWire) {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/usage"+query, nil))
	var w usageWire
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &w); err != nil {
			t.Fatalf("body is not the contract: %v\n%s", err, rec.Body)
		}
	}
	return rec, w
}

func usageMux(store *ledger.Store) *http.ServeMux {
	mux := http.NewServeMux()
	usageRoute(store)(mux, func(h http.Handler) http.Handler { return h })
	return mux
}

func TestUsageRouteServesFiveMinuteSums(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	row := func(key string, at time.Time, src string) ledger.RequestRow {
		return ledger.RequestRow{Source: src, Session: "s1", Key: key, Transcript: "t1", At: at, Model: "m",
			Input: 1, Output: 2, CacheRead: 3, CacheCreation: 4, EstimateUSD: 0.25}
	}
	store.InsertRequests([]ledger.RequestRow{
		row("early", base.Add(-48*time.Hour), "claude_code"), // outside the range, but sets first_at
		row("a", base.Add(time.Minute), "claude_code"),
		row("b", base.Add(2*time.Minute), "claude_code"),
		row("c", base.Add(time.Hour), "codex"),
	})
	rec, w := getUsage(t, usageMux(store), "?since="+strconv.FormatInt(base.Unix(), 10)+"&until="+strconv.FormatInt(base.Add(2*time.Hour).Unix(), 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if w.BucketSeconds != 300 || len(w.Buckets) != 2 {
		t.Fatalf("got %+v", w)
	}
	b := w.Buckets[0]
	if b.At != base.Unix() || b.Source != "claude_code" || b.Transcript != "t1" || b.Requests != 2 ||
		b.Tokens.Input != 2 || b.Tokens.CacheCreation != 8 || b.EstimateUSD != 0.5 {
		t.Fatalf("first bucket %+v", b)
	}
	if w.Sources["claude_code"].FirstAt != base.Add(-48*time.Hour).Unix() || w.Sources["codex"].FirstAt != base.Add(time.Hour).Unix() {
		t.Fatalf("sources %+v", w.Sources)
	}
	if w.BackfillDone {
		t.Fatal("backfill_done true on a ledger with no marker")
	}
}

func TestUsageRouteRefusesABadRange(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	mux := usageMux(ledger.New())
	for _, q := range []string{"", "?since=10", "?since=x&until=20", "?since=20&until=10"} {
		if rec, _ := getUsage(t, mux, q); rec.Code != http.StatusBadRequest {
			t.Fatalf("%q answered %d, want 400", q, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/usage?since=1&until=2", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST answered %d", rec.Code)
	}
}

// An empty range is an empty list, never null, so the page never has to guess.
func TestUsageRouteEmptyIsAnEmptyList(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	rec := httptest.NewRecorder()
	usageMux(ledger.New()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/usage?since=1&until=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if string(raw["buckets"]) != "[]" || string(raw["sources"]) != "{}" {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestUsageRouteIsMounted(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	mux := http.NewServeMux()
	for _, r := range newV3(settings.Settings{}, atlas.Off{}).routes() {
		r(mux, func(h http.Handler) http.Handler { return h })
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/usage?since=1&until=2", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/usage answered %d through v3.routes()", rec.Code)
	}
}
