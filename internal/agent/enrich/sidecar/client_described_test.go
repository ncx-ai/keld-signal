package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// captureClassify spins up a server that records the decoded /classify body and
// replies with a canned result, returning the client + a pointer to the body.
func captureClassify(t *testing.T) (*Client, *map[string]any, func()) {
	t.Helper()
	got := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"results":{"support":[{"label":"invoice question","confidence":0.7}]}}`))
	}))
	return New(srv.URL, time.Second), &got, srv.Close
}

func TestClassifyMultiWithoutDescriptionsSendsLabelList(t *testing.T) {
	c, got, done := captureClassify(t)
	defer done()
	c.ClassifyMulti("x", map[string]enrich.MultiTask{
		"topics": {Labels: []string{"pricing", "bug"}, Threshold: 0.5},
	})
	task := (*got)["tasks"].(map[string]any)["topics"].(map[string]any)
	if _, ok := task["labels"].([]any); !ok { // stays a JSON array — unchanged wire
		t.Fatalf("labels should be a list without descriptions, got %T: %v", task["labels"], task["labels"])
	}
}

func TestClassifyMultiWithDescriptionsSendsLabelDict(t *testing.T) {
	c, got, done := captureClassify(t)
	defer done()
	c.ClassifyMulti("x", map[string]enrich.MultiTask{
		"topics": {Labels: []string{"pricing", "bug"}, Threshold: 0.5,
			Descriptions: map[string]string{"pricing": "mentions cost"}},
	})
	task := (*got)["tasks"].(map[string]any)["topics"].(map[string]any)
	labels, ok := task["labels"].(map[string]any) // dict form {label: hint}
	if !ok {
		t.Fatalf("labels should be a {label:hint} dict, got %T: %v", task["labels"], task["labels"])
	}
	if labels["pricing"] != "mentions cost" {
		t.Fatalf("hint not carried for pricing: %v", labels)
	}
	if v, present := labels["bug"]; !present || v != "" {
		t.Fatalf("every label must be a key (empty hint when unauthored): %v", labels)
	}
}

func TestClassifyDescribedSendsSingleLabelDictTask(t *testing.T) {
	c, got, done := captureClassify(t)
	defer done()
	res := c.ClassifyDescribed("why was I charged", map[string]enrich.DescribedTask{
		"support": {Labels: []string{"invoice question", "bug report"},
			Descriptions: map[string]string{"invoice question": "about charges and refunds"}},
	})
	if len(res["support"]) != 1 || res["support"][0].Label != "invoice question" {
		t.Fatalf("unexpected result: %+v", res)
	}
	task := (*got)["tasks"].(map[string]any)["support"].(map[string]any)
	// single-label: multi_label must NOT be set (softmax), and labels is the dict form.
	if _, present := task["multi_label"]; present {
		t.Fatalf("described single-label task must not carry multi_label: %v", task)
	}
	labels := task["labels"].(map[string]any)
	if labels["invoice question"] != "about charges and refunds" {
		t.Fatalf("hint not carried: %v", labels)
	}
}
