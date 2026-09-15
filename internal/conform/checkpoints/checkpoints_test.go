package checkpoints

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fullFacts() Facts {
	return Facts{
		Transcripts:       []string{"projects/x/sess.jsonl"},
		PromptIDs:         []string{"p-1"},
		StorePromptRows:   1,
		StoreEventRows:    9,
		AtlasCounts:       map[string]int{"/v1/logs": 4, "/v1/signal/blocks": 1},
		EnrichmentCorrIDs: []string{"p-1"},
	}
}

func byName(rs []Result, name string) Result {
	for _, r := range rs {
		if r.Name == name {
			return r
		}
	}
	panic("no checkpoint named " + name)
}

func TestEveryCheckpointPassesOnCompleteFacts(t *testing.T) {
	rs := Evaluate(fullFacts(), AllRequired())
	if len(rs) != 5 {
		t.Fatalf("got %d checkpoints, want 5", len(rs))
	}
	want := []string{"transcript", "pointer", "store_rows", "telemetry", "publish"}
	for i, n := range want {
		if rs[i].Name != n {
			t.Fatalf("checkpoint %d = %q, want %q (order is the chain's order)", i, rs[i].Name, n)
		}
		if !rs[i].OK {
			t.Errorf("%s failed on complete facts: %s", n, rs[i].Detail)
		}
	}
	if !Pass(rs) {
		t.Error("Pass = false on complete facts")
	}
}

func TestEachMissingFactFailsExactlyItsOwnCheckpoint(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*Facts)
	}{
		{"transcript", func(f *Facts) { f.Transcripts = nil }},
		{"pointer", func(f *Facts) { f.EnrichmentCorrIDs = nil }},
		{"store_rows", func(f *Facts) { f.StorePromptRows = 0 }},
		{"telemetry", func(f *Facts) { f.AtlasCounts["/v1/logs"] = 0 }},
		{"publish", func(f *Facts) { f.AtlasCounts["/v1/signal/blocks"] = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fullFacts()
			tc.break_(&f)
			rs := Evaluate(f, AllRequired())
			if byName(rs, tc.name).OK {
				t.Errorf("%s passed with its fact removed", tc.name)
			}
			for _, r := range rs {
				// transcript feeds pointer and store_rows (both key on a prompt
				// id read from it), so removing it legitimately fails those too.
				if r.Name != tc.name && !r.OK && tc.name != "transcript" {
					t.Errorf("removing the %s fact also failed %s: %s", tc.name, r.Name, r.Detail)
				}
			}
			if Pass(rs) {
				t.Error("Pass = true with a required checkpoint failed")
			}
		})
	}
}

func TestAnUnexpectedCheckpointDoesNotFailTheStep(t *testing.T) {
	// Codex's store-rows and publish checkpoints are expected:false until the
	// sidecar reader and the Go capture land. An unmet one must READ as
	// not-applicable rather than pass — a checkpoint that reports OK for a lane
	// that was never wired is the "confident negative" this repo forbids.
	f := fullFacts()
	f.StorePromptRows = 0
	e := AllRequired()
	e.StoreRows = false
	rs := Evaluate(f, e)
	got := byName(rs, "store_rows")
	if got.Required {
		t.Error("store_rows reports Required with the expectation off")
	}
	if got.OK {
		t.Error("store_rows reports OK though the fact is absent — say not-applicable, never yes")
	}
	if !Pass(rs) {
		t.Error("Pass = false, but the only unmet checkpoint was not required")
	}
}

func TestAGatherErrorFailsItsCheckpointAndIsNamed(t *testing.T) {
	f := fullFacts()
	f.Errors = map[string]string{"store_rows": "no sqlite reader on PATH"}
	rs := Evaluate(f, AllRequired())
	got := byName(rs, "store_rows")
	if got.OK {
		t.Error("store_rows passed despite its gatherer erroring")
	}
	if !strings.Contains(got.Err, "sqlite") {
		t.Errorf("error = %q, want the gatherer's message", got.Err)
	}
}

func TestTheFailureLineIsMachineParseable(t *testing.T) {
	f := fullFacts()
	f.AtlasCounts["/v1/logs"] = 0
	rep := Report{
		Tool: "claude_code", Chain: "A", Step: "after-signal", Seed: "42",
		Checkpoints: Evaluate(f, AllRequired()),
	}
	rep.Pass = Pass(rep.Checkpoints)

	line := rep.FailureLine()
	if line == "" {
		t.Fatal("no failure line for a failing report")
	}
	var got struct {
		Tool       string `json:"tool"`
		Chain      string `json:"chain"`
		Step       string `json:"step"`
		Checkpoint string `json:"checkpoint"`
		Seed       string `json:"seed"`
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("failure line is not JSON: %v (%s)", err, line)
	}
	if got.Tool != "claude_code" || got.Chain != "A" || got.Step != "after-signal" ||
		got.Checkpoint != "telemetry" || got.Seed != "42" {
		t.Fatalf("failure line = %+v, want the five fields naming the FIRST failed checkpoint", got)
	}
	if strings.Contains(line, "\n") {
		t.Error("the failure line must be one line — the runner reads it with tail -1")
	}
}

func TestAPassingReportHasNoFailureLine(t *testing.T) {
	rep := Report{Checkpoints: Evaluate(fullFacts(), AllRequired())}
	rep.Pass = Pass(rep.Checkpoints)
	if rep.FailureLine() != "" {
		t.Error("a passing report emitted a failure line")
	}
}

// ---- gatherers ----

func TestGatherTranscriptsFindsPromptIDsUnderTheRoot(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "-tmp-work")
	os.MkdirAll(dir, 0o755)
	// A real Claude Code transcript head: bookkeeping lines with no promptId,
	// then the human turn that carries one. Reading the FIRST line for an id
	// would find nothing — the same trap capture.scan documents.
	lines := []string{
		`{"type":"queue-operation","operation":"enqueue","sessionId":"s1"}`,
		`{"type":"user","promptId":"p-abc","uuid":"u-1","sessionId":"s1","message":{"role":"user","content":"x"}}`,
		`{"type":"assistant","uuid":"u-2","sessionId":"s1"}`,
	}
	path := filepath.Join(dir, "s1.jsonl")
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)

	f := Facts{}
	gatherTranscripts(&f, root)
	if len(f.Transcripts) != 1 || !strings.HasSuffix(f.Transcripts[0], "s1.jsonl") {
		t.Fatalf("transcripts = %v, want the one under the root", f.Transcripts)
	}
	if len(f.PromptIDs) != 1 || f.PromptIDs[0] != "p-abc" {
		t.Fatalf("prompt ids = %v, want [p-abc]", f.PromptIDs)
	}
	// Absolute paths never reach the report: a CI artifact carries the whole
	// isolated HOME path otherwise.
	if filepath.IsAbs(f.Transcripts[0]) {
		t.Errorf("transcript %q is absolute; report paths are root-relative", f.Transcripts[0])
	}
}

func TestGatherTranscriptsOnAnEmptyRootIsNotAnError(t *testing.T) {
	f := Facts{}
	gatherTranscripts(&f, t.TempDir())
	if len(f.Transcripts) != 0 {
		t.Errorf("transcripts = %v, want none", f.Transcripts)
	}
	if f.Errors["transcript"] != "" {
		t.Errorf("error = %q; an empty root is a failed checkpoint, not a broken gatherer",
			f.Errors["transcript"])
	}
}

func TestGatherAtlasReadsCountsAndEnrichmentCorrIDs(t *testing.T) {
	state := t.TempDir()
	// Bodies the mock Atlas persisted, in its own layout.
	os.MkdirAll(filepath.Join(state, "v1_enrichments"), 0o755)
	os.WriteFile(filepath.Join(state, "v1_enrichments", "0001.json"),
		[]byte(`{"corr_id":"p-abc","corr_scheme":"prompt"}`), 0o644)
	os.MkdirAll(filepath.Join(state, "v1_signal_blocks"), 0o755)
	os.WriteFile(filepath.Join(state, "v1_signal_blocks", "0001.json"),
		[]byte(`{"blocks":[{"corr_id":"s1@1700000000"}]}`), 0o644)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"counts":{"/v1/logs":7,"/v1/signal/blocks":1,"/v1/enrichments":1}}`))
	}))
	defer ts.Close()

	f := Facts{}
	gatherAtlas(&f, ts.URL, state)
	if f.AtlasCounts["/v1/logs"] != 7 {
		t.Errorf("logs count = %d, want 7", f.AtlasCounts["/v1/logs"])
	}
	if len(f.EnrichmentCorrIDs) != 1 || f.EnrichmentCorrIDs[0] != "p-abc" {
		t.Errorf("corr ids = %v, want [p-abc]", f.EnrichmentCorrIDs)
	}
}

func TestGatherAtlasRecordsAnUnreachableServerAsAnError(t *testing.T) {
	f := Facts{}
	// Port 1 on loopback: nothing listens, connection refused immediately.
	gatherAtlas(&f, "http://127.0.0.1:1", t.TempDir())
	if f.Errors["telemetry"] == "" {
		t.Error("an unreachable mock Atlas must be an ERROR, not an empty count that reads as 'nothing arrived'")
	}
	rs := Evaluate(f, AllRequired())
	if byName(rs, "telemetry").OK || byName(rs, "publish").OK {
		t.Error("checkpoints passed against an unreachable mock Atlas")
	}
}

func TestReportMarshalsWithEveryCheckpointNamed(t *testing.T) {
	rep := Report{Tool: "claude_code", Chain: "A", Step: "s", Seed: "1",
		Checkpoints: Evaluate(fullFacts(), AllRequired())}
	rep.Pass = Pass(rep.Checkpoints)
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, n := range []string{"transcript", "pointer", "store_rows", "telemetry", "publish"} {
		if !strings.Contains(string(raw), `"`+n+`"`) {
			t.Errorf("report JSON does not name %s: %s", n, raw)
		}
	}
}

// TestTheRealPublishedEnrichmentYieldsItsPromptID reads a body captured from a
// live run — Claude Code 2.1.271, deterministic backend, hook origin — rather
// than one written to match the parser.
//
// ⚠️ This test exists because the parser and the fixture agreed and production
// did not. The gatherer read a top-level `corr_id` (the name AGENTS.md uses for
// the ATLAS column and for the join to ToolEvent.prompt_id) while the CLIENT
// sends `correlation.id`. Every unit test passed, the daemon log showed
// "published enrichment for …", and the pointer checkpoint reported nothing
// arrived. An oracle that shares the bug proves nothing.
func TestTheRealPublishedEnrichmentYieldsItsPromptID(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "enrichment-claude-2.1.271.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	got := correlationID(raw)
	if got != "26688d07-0146-4617-a9dc-4a0b63953f63" {
		t.Fatalf("correlationID = %q, want the promptId out of correlation.id", got)
	}
	// And the body it came from carries no prompt text, which is what makes it
	// safe to keep in the repo at all.
	for _, banned := range []string{`"text"`, `"prompt"`, `"content"`, `"message"`} {
		if strings.Contains(string(raw), banned) {
			t.Errorf("the captured enrichment carries a %s key — it must publish no text", banned)
		}
	}
}

func TestCorrelationIDAlsoAcceptsTheAtlasSideSpelling(t *testing.T) {
	if got := correlationID([]byte(`{"corr_id":"p-1"}`)); got != "p-1" {
		t.Errorf("corr_id = %q, want p-1", got)
	}
	if got := correlationID([]byte(`{}`)); got != "" {
		t.Errorf("empty body = %q, want empty", got)
	}
}
