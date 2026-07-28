package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func TestClassifyMultiSendsMultiLabelWire(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"results":{"artifact":[{"label":"source code","confidence":0.8}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, time.Second)
	res := c.ClassifyMulti("write a function", map[string]enrich.MultiTask{
		"artifact": {Labels: []string{"source code", "documentation"}, Threshold: 0.4},
	})
	if len(res["artifact"]) != 1 || res["artifact"][0].Label != "source code" {
		t.Fatalf("unexpected result: %+v", res)
	}
	tasks, _ := got["tasks"].(map[string]any)
	art, _ := tasks["artifact"].(map[string]any)
	if art == nil {
		t.Fatalf("no artifact task in wire: %+v", got)
	}
	if art["multi_label"] != true {
		t.Fatalf("expected multi_label true in wire, got %v", art)
	}
	if th, _ := art["cls_threshold"].(float64); th != 0.4 {
		t.Fatalf("expected cls_threshold 0.4, got %v", art["cls_threshold"])
	}
}
