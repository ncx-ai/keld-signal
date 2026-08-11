package llmstudy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingServer answers /v1/chat/completions with the supplied contents in order, capturing
// every request body so a test can assert HOW the re-request differed rather than only that one
// happened.
func recordingServer(t *testing.T, contents []string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("undecodable request: %v", err)
		}
		bodies = append(bodies, body)
		c := contents[len(contents)-1]
		if n < len(contents) {
			c = contents[n]
		}
		n++
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": c}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

// beatJSON wraps a beat the way the schema-constrained response does.
func beatJSON(s string) string {
	b, _ := json.Marshal(map[string]string{"beat": s})
	return string(b)
}

// unpunctuated is the measured failure: a headline clause with no terminator anywhere, which
// holds no complete sentence and is therefore rejected. At temperature 0 the re-request returned
// this same string five times and the beat was lost — 2 of 42 asked, in all six sweeps.
const unpunctuated = "Syncing the README with the actual state of the world and the branch it describes"

// A valid replacement: two complete sentences, over BeatMinRunes.
const punctuated = "The README is being brought back in line with what the branch actually does. " +
	"The stale sections about the old prompt scheme are gone and the remaining diff is the beat cadence."

// TestBeatRetryDiffersFromTheFirstAttempt is the fix for the two beats lost per run. The point
// is not that a retry happens — it always did — but that the SECOND REQUEST IS DIFFERENT, which
// at temperature 0 it was not.
//
// Fails before the fix: attempt 1 carries temperature 0 and no seed, so a deterministic server
// (like a real one at temperature 0) returns the identical rejected string and GenerateBeat
// errors.
func TestBeatRetryDiffersFromTheFirstAttempt(t *testing.T) {
	srv, bodies := recordingServer(t, []string{beatJSON(unpunctuated), beatJSON(punctuated)})
	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()

	got, err := l.GenerateBeat("counts: turns=10\n", "user: sync the readme\n")
	if err != nil {
		t.Fatalf("a rejected generation was not recovered: %v", err)
	}
	if !strings.HasPrefix(got, "The README is being brought back") {
		t.Fatalf("unexpected beat: %q", got)
	}
	if len(*bodies) != 2 {
		t.Fatalf("want exactly 2 attempts, got %d", len(*bodies))
	}
	// Attempt 0 must be the greedy request every other caller sends.
	if temp := (*bodies)[0]["temperature"]; temp != float64(0) {
		t.Errorf("attempt 0 temperature = %v, want 0 — the first request must stay greedy so a "+
			"beat that passes first time is byte-identical to before", temp)
	}
	if _, ok := (*bodies)[0]["seed"]; ok {
		t.Error("attempt 0 carries a seed; it must be byte-identical to callValid's own request")
	}
	// Attempt 1 must differ, and differ reproducibly.
	if temp := (*bodies)[1]["temperature"]; temp != beatRetryTempStep {
		t.Errorf("attempt 1 temperature = %v, want %v — an identical re-request cannot recover a "+
			"rejection that is a property of the sample", temp, beatRetryTempStep)
	}
	if seed := (*bodies)[1]["seed"]; seed != float64(1) {
		t.Errorf("attempt 1 seed = %v, want 1 — an unseeded nudge makes the recovered beat "+
			"irreproducible, and reproducibility is what retired 'is it variance?' for this study", seed)
	}
}

// TestBeatRetryScheduleWidensAndStaysConservative pins the schedule itself, because the
// alternative to an unrecoverable retry is not "any temperature".
func TestBeatRetryScheduleWidensAndStaysConservative(t *testing.T) {
	if s := beatSampling(0); s.Temp != 0 || s.Seed != 0 {
		t.Errorf("attempt 0 = %+v, want the greedy request", s)
	}
	prev := 0.0
	for a := 1; a <= 4; a++ {
		s := beatSampling(a)
		if s.Temp <= prev {
			t.Errorf("attempt %d temperature %v does not widen on %v", a, s.Temp, prev)
		}
		if s.Seed != a {
			t.Errorf("attempt %d seed = %d, want %d", a, s.Seed, a)
		}
		prev = s.Temp
	}
	// retry.DefaultPolicy allows 5 attempts, so the last is attempt 4.
	if got := beatSampling(4).Temp; got > 1.0 {
		t.Errorf("the last attempt samples at %v; above 1.0 an instruct model starts inventing, "+
			"and the shape gates cannot catch a fabricated NOUN", got)
	}
}

// TestBeatShapeStandardIsUnchangedByTheRetry is the guard on the fix's boundary. The brief's one
// forbidden fix was lowering the sentence-completeness standard, so a beat that never becomes a
// complete sentence must still be refused after all five attempts rather than stored as a
// fragment.
func TestBeatShapeStandardIsUnchangedByTheRetry(t *testing.T) {
	srv, bodies := recordingServer(t, []string{beatJSON(unpunctuated)})
	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	got, err := l.GenerateBeat("counts: turns=10\n", "user: sync the readme\n")
	if err == nil {
		t.Fatalf("an unpunctuated generation was accepted as a beat: %q", got)
	}
	if !strings.Contains(err.Error(), "no complete sentence") {
		t.Errorf("wrong rejection reason: %v", err)
	}
	if len(*bodies) != l.Policy.MaxAttempts {
		t.Errorf("attempts = %d, want the policy's %d", len(*bodies), l.Policy.MaxAttempts)
	}
	// And every retry after the first genuinely differed, so the five attempts were not five
	// copies of one request.
	seen := map[string]bool{}
	for i, b := range *bodies {
		k := "t=" + jsonString(b["temperature"]) + " s=" + jsonString(b["seed"])
		if seen[k] {
			t.Errorf("attempt %d repeated the sampling of an earlier one (%s)", i, k)
		}
		seen[k] = true
	}
}

// TestCallValidStillSendsTheGreedyRequest is the scope guard: sampling is opt-in, and every
// existing caller must be byte-identical to before. A shared retry loop that quietly started
// sampling would move Classify's labels and every digest in the study.
func TestCallValidStillSendsTheGreedyRequest(t *testing.T) {
	srv, bodies := recordingServer(t, []string{`{"value":""}`, `{"value":""}`, `{"value":"ok"}`})
	l := NewLlama(srv.URL)
	l.Policy = fastPolicy()
	var out struct{ Value string }
	err := l.callValid("p", map[string]any{}, &out, func() error {
		if out.Value == "" {
			return firstProblem([]string{"value is empty"})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(*bodies) != 3 {
		t.Fatalf("want 3 attempts, got %d", len(*bodies))
	}
	for i, b := range *bodies {
		if b["temperature"] != float64(0) {
			t.Errorf("attempt %d temperature = %v, want 0", i, b["temperature"])
		}
		if _, ok := b["seed"]; ok {
			t.Errorf("attempt %d carries a seed; callValid must send no sampling fields", i)
		}
	}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
