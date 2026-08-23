# /analyze and the deterministic enrichment path — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the daemon produce and publish deterministic workstream dimensions from transcripts, with GLiNER2 disabled — the first production behaviour change on this branch.

**Architecture:** The sidecar gains `POST /analyze`, which takes window *coordinates* (never text), reads the transcript itself, and returns the workstream payload the already-built `app.analysis` package computes. The Go daemon gains a client method and a Wave-0 extractor that calls it without a `Model`, plus a third `ml_backend` mode so enrichment can run with the model off.

**Tech Stack:** Python 3.12 (sidecar venv) + FastAPI; Go 1.x daemon; the existing `sidecar/app/analysis/` package (12 modules, 1754 lines, already tested and gated).

## Global Constraints

- **Sidecar tests are standalone scripts, never pytest.** Each ends with a `__main__` runner calling every `test_*`. Run from `sidecar/` with `PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_<name>.py`.
- **Go tests:** `go test ./...` must pass. Table-driven where there is more than one case.
- **Coordinates, never text.** `/analyze` receives a path and a prompt id; the daemon must not send prompt text to it. This is the `spool.Pointer` rule.
- **No span, no offset, no prompt text in any response or published payload.** Assert on the wire shape, not by inspection.
- **`/analyze` must not use `_dispatch`/the single-flight runner.** It is not inference and must answer when no model is loaded. `/health`, `/metrics`, `/vocabulary` and `/match` already bypass it.
- **No pandas/numpy in `sidecar/app/analysis/`.**
- **Do not bump `enrich.SchemaVersion`** in tasks 1-5. Task 6 decides it deliberately.
- **Leave alone:** `.gitignore`, `internal/agent/daemon/custom_passes*.go`, `internal/agent/daemon/daemon.go`'s pre-existing uncommitted hunks, `scripts/context_value.py`, untracked `scripts/prompt-v9.md`. These are the user's in-flight work.
- **Gate after any change under `sidecar/app/analysis/` or `scripts/`:** `make fixture-identity-check STUDY_PYTHON=$HOME/.keld/study-venv/bin/python` → `IDENTICAL 210 rows sha=eb7b4def125a`. Exit 2 means the check could not run and is never a pass.

## File Structure

```
sidecar/app/analysis/analyze.py     NEW  window(path, prompt_id, span_minutes) -> payload; pure, no HTTP
sidecar/app/test_analysis_analyze.py NEW
sidecar/app/main.py                 MOD  POST /analyze, bypassing the runner
sidecar/app/test_main.py            MOD  endpoint tests

internal/agent/enrich/sidecar/client.go        MOD  Analyze() + request/response types
internal/agent/enrich/sidecar/analyze_test.go  NEW  httptest-backed
internal/agent/enrich/creddetect_pass.go       NEW  credential detection independent of any Model
internal/agent/enrich/creddetect_pass_test.go  NEW
internal/agent/enrich/extractors.go            MOD  SensitivityExtractor delegates to it
internal/agent/enrich/workstreams.go           NEW  Wave-0 extractor calling Analyze()
internal/agent/enrich/workstreams_test.go      NEW
internal/agent/enrich/types.go                 MOD  Profile gains Workstreams
internal/agent/settings/settings.go            MOD  third ml_backend mode
internal/agent/settings/settings_test.go       MOD
internal/agent/daemon/daemon.go                MOD  wireEnrichment honours the new mode
internal/agent/publish/publish.go              MOD  carry Workstreams on the wire
internal/agent/publish/publish_test.go         MOD
```

---

### Task 1: `analysis/analyze.py` — window analysis as a pure function

**Files:**
- Create: `sidecar/app/analysis/analyze.py`
- Create: `sidecar/app/test_analysis_analyze.py`

**Interfaces:**
- Consumes: `transcript.iter_turns(path)`, `transcript.turns_between(path, start, end)`, `levels.events_for_turns(turns, path, root, repo_root, nlp=None)`, `window.rollup(rows)`, `workstreams.payload(rl)`, `analysis.SCHEMA`
- Produces: `analyze_window(path: str, prompt_id: str, span_minutes: int = 60, nlp=None) -> dict`
  returning `{"schema": int, "session": str, "window_start": str, "window_end": str, "evidence": int, "workstreams": {...}, "inventory": {...}}`,
  and `PromptNotFound` (an `Exception` subclass) when `prompt_id` is not in the transcript.

The window **ends** at the prompt and looks back `span_minutes` — the hour of work leading to it. That matches how the study's windows were measured and means the daemon passes only what a `queue.Job` already carries.

- [ ] **Step 1: Write the failing test**

```python
import sys, os, json, tempfile
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.analyze import analyze_window, PromptNotFound


def _write(tmp, rows):
    p = os.path.join(tmp, "abcd1234-0000.jsonl")
    with open(p, "w") as fh:
        for o in rows:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return p


def _turn(ts, uuid=None, text="hello", cwd="/workspace/proj"):
    o = {"type": "user", "timestamp": ts, "cwd": cwd,
         "message": {"content": [{"type": "text", "text": text}]}}
    if uuid:
        o["uuid"] = uuid
    return o


def test_window_ends_at_the_prompt_and_looks_back():
    """The window is the hour of work LEADING TO the prompt, so a turn after it is excluded."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [
            _turn("2026-08-01T10:00:00Z", text="early work"),
            _turn("2026-08-01T10:30:00Z", "target", "the prompt"),
            _turn("2026-08-01T11:00:00Z", text="later work"),
        ])
        out = analyze_window(p, "target", span_minutes=60)
        assert out["window_end"] == "2026-08-01T10:30:00+00:00", out["window_end"]
        assert out["window_start"] == "2026-08-01T09:30:00+00:00", out["window_start"]


def test_unknown_prompt_id_raises_rather_than_returning_an_empty_window():
    """An empty payload and a missing prompt are different facts; conflating them would publish
    'nothing was happening' for what is really a resolution failure."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [_turn("2026-08-01T10:00:00Z", "known")])
        try:
            analyze_window(p, "nope")
        except PromptNotFound:
            return
        raise AssertionError("expected PromptNotFound")


def test_payload_carries_the_schema_and_no_prompt_text():
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [_turn("2026-08-01T10:00:00Z", "t", "secret customer name here")])
        out = analyze_window(p, "t")
        assert out["schema"] >= 1
        assert "secret customer name here" not in json.dumps(out)
        for k in ("text", "span", "start", "end", "offset"):
            assert k not in json.dumps(out), k


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_analyze.py`
Expected: `ModuleNotFoundError: No module named 'app.analysis.analyze'`

- [ ] **Step 3: Write the minimal implementation**

```python
"""One window of a transcript, analysed into the workstream payload.

Takes COORDINATES, never text — the same rule as `spool.Pointer`. The window ENDS at the prompt
and looks back, because the question a cost report asks is "what was this hour of work about",
and the work that produced a prompt precedes it.
"""
from datetime import timedelta

from app.analysis import SCHEMA
from app.analysis import levels, window, workstreams
from app.analysis.transcript import iter_turns, turns_between, _epoch


class PromptNotFound(Exception):
    """The prompt id is not in this transcript. Distinct from an empty window: one is a
    resolution failure, the other is a fact about the work."""


def _prompt_time(path, prompt_id):
    for o in iter_turns(path):
        if o.get("uuid") == prompt_id:
            return o["timestamp"]
    raise PromptNotFound(prompt_id)


def analyze_window(path, prompt_id, span_minutes=60, nlp=None):
    from datetime import datetime
    end_iso = _prompt_time(path, prompt_id)
    end = datetime.fromisoformat(end_iso.replace("Z", "+00:00"))
    start = end - timedelta(minutes=span_minutes)
    turns = turns_between(path, start.isoformat(), end.isoformat())
    rows, _pending, _n = levels.events_for_turns(turns, path, "", ())
    rl = window.rollup(rows)
    out = workstreams.payload(rl)
    out.update(schema=SCHEMA, session=path.split("/")[-1][:8],
               window_start=start.isoformat(), window_end=end.isoformat(),
               evidence=int(sum(n for items in rl.values() for _, n in items)))
    return out
```

Adjust the imports to whatever `transcript.py` actually exports — read it rather than assuming `_epoch` is public. If `turns_between` wants a different argument type than ISO strings, match its real signature.

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_analyze.py`
Expected: `3/3 passed`

- [ ] **Step 5: Confirm the gate is untouched**

Run: `make fixture-identity-check STUDY_PYTHON=$HOME/.keld/study-venv/bin/python`
Expected: `IDENTICAL 210 rows sha=eb7b4def125a`

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/analysis/analyze.py sidecar/app/test_analysis_analyze.py
git commit -m "analysis: window analysis as a pure function over coordinates"
```

---

### Task 2: `POST /analyze`

**Files:**
- Modify: `sidecar/app/main.py`
- Modify: `sidecar/app/test_main.py`

**Interfaces:**
- Consumes: `analyze_window(path, prompt_id, span_minutes, nlp) -> dict`, `PromptNotFound`
- Produces: `POST /analyze` accepting `{"path": str, "prompt_id": str, "span_minutes": int = 60}` and returning the payload; `404` when the prompt is not found.

- [ ] **Step 1: Write the failing test**

Add to `sidecar/app/test_main.py`, following the file's existing client/fixture style:

```python
def test_analyze_returns_a_payload_without_touching_the_runner():
    """/analyze is a transcript read plus regex/spaCy work, not inference. It must answer with no
    worker ever spawned — that is what distinguishes it from /classify."""
    with _client() as c:
        path = _fixture_transcript()          # reuse the file's existing fixture helper
        r = c.post("/analyze", json={"path": path, "prompt_id": _fixture_prompt_id()})
        assert r.status_code == 200, r.text
        body = r.json()
        assert body["schema"] >= 1
        assert "workstreams" in body and "inventory" in body
        m = c.get("/metrics").json()
        assert m["counts"]["submitted"] == 0, "must not have gone through the runner"


def test_analyze_unknown_prompt_is_404_not_an_empty_payload():
    with _client() as c:
        r = c.post("/analyze", json={"path": _fixture_transcript(), "prompt_id": "nope"})
        assert r.status_code == 404, r.status_code


def test_analyze_response_carries_no_prompt_text():
    with _client() as c:
        body = c.post("/analyze", json={"path": _fixture_transcript(),
                                        "prompt_id": _fixture_prompt_id()}).json()
        blob = json.dumps(body)
        for k in ('"text"', '"span"', '"offset"'):
            assert k not in blob, k
```

If `test_main.py` has no `_fixture_transcript` helper, write one that creates a small synthetic transcript in a `tempfile.TemporaryDirectory` — wholly invented content, no real names, matching the convention in `sidecar/app/analysis/testdata/`.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_main.py`
Expected: FAIL — 404 from FastAPI because the route does not exist.

- [ ] **Step 3: Implement the endpoint**

```python
class AnalyzeIn(BaseModel):
    path: str
    prompt_id: str
    span_minutes: int = 60


@app.post("/analyze")
async def analyze(body: AnalyzeIn):
    """Analyse one transcript window into workstream dimensions.

    Deliberately does NOT go through _dispatch/the single-flight runner, for the same reason
    /match does not: this is a transcript read plus regex and spaCy work, not inference, and it
    must answer while the runner is occupied or no worker has ever been spawned. Runs in the
    default executor so a large transcript cannot stall the event loop out from under
    /health and /metrics.
    """
    _count("analyze_served")
    loop = asyncio.get_running_loop()
    try:
        return await loop.run_in_executor(
            None, analyze_window, body.path, body.prompt_id, body.span_minutes, _analysis_nlp())
    except PromptNotFound:
        raise HTTPException(status_code=404, detail="prompt not found in transcript")
```

Add `analyze_served` to `Counts` in `sidecar/app/metrics.py` beside `vocab_installs`/`match_served`, and a lazy `_analysis_nlp()` that loads spaCy once and returns `None` if it is unavailable — the `term` level degrades rather than failing the request.

**Note the deviation and record it in the docstring:** the architecture spec calls for analysis in a third, long-lived worker process, because the FastAPI parent's flat RSS depends on it holding no model. This runs in the parent. The user deprioritised memory sizing on 2026-08-22; state that as the reason so the next reader knows it was a decision, not an oversight.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_main.py`
Expected: all pass, including the three new ones.

- [ ] **Step 5: Live check**

```bash
cd sidecar
KELD_GLINER2_DIR=$HOME/.keld/models/gliner2-large-v1 MALLOC_ARENA_MAX=2 \
  PYTHONPATH=. ~/.keld/sidecar-venv/bin/python serve.py --port 33207 &
sleep 20
curl -s -X POST localhost:33207/analyze -H 'content-type: application/json' \
  -d '{"path":"<a real frozen-corpus transcript>","prompt_id":"<a real uuid from it>"}' | head -c 400
curl -s localhost:33207/metrics | grep -o '"submitted":[0-9]*'
kill %1
```

Expected: a payload with `workstreams`, and `"submitted":0` proving the runner was never used. Do not use ports 33037 (orphaned sidecar) or the production daemon's.

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/main.py sidecar/app/metrics.py sidecar/app/test_main.py
git commit -m "sidecar: POST /analyze, off the single-flight runner"
```

---

### Task 3: credential detection independent of any model

**Files:**
- Create: `internal/agent/enrich/creddetect_pass.go`
- Create: `internal/agent/enrich/creddetect_pass_test.go`
- Modify: `internal/agent/enrich/extractors.go` (the `SensitivityExtractor` body around lines 95-120)

**Why:** `creddetect` is pure Go and needs no model, but it currently runs *inside* `SensitivityExtractor`, which calls the sidecar. Disabling ML would therefore also disable credential masking — a privacy feature lost for no reason.

**Interfaces:**
- Produces: `CredentialSpans(text string) ([]Entity, map[string]bool)` — masked spans plus the `found` set that `sensitivityFromEntities` consumes.

- [ ] **Step 1: Write the failing test**

```go
package enrich

import "testing"

func TestCredentialSpansFindsAKeyWithoutAModel(t *testing.T) {
	// The whole point: no Model, no sidecar, no network.
	spans, found := CredentialSpans(`export GITHUB_TOKEN="ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"`)
	if len(spans) == 0 {
		t.Fatalf("expected a credential span, got none")
	}
	if !found["api_key"] {
		t.Fatalf("expected found[api_key], got %v", found)
	}
	for _, s := range spans {
		if s.Text != "" {
			t.Errorf("raw text must never survive: %q", s.Text)
		}
		if s.Masked == "" {
			t.Errorf("span must carry a mask")
		}
	}
}

func TestCredentialSpansSkipsPlaceholders(t *testing.T) {
	_, found := CredentialSpans(`api_key = "YOUR_API_KEY_HERE"`)
	if found["api_key"] {
		t.Errorf("a placeholder is not a credential")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/agent/enrich/ -run TestCredentialSpans -v`
Expected: FAIL — `undefined: CredentialSpans`

- [ ] **Step 3: Extract the function**

Move the credential loop out of `SensitivityExtractor` into `creddetect_pass.go` verbatim — the placeholder gate, the `Mask("api_key", …)` call and the `Text` omission all move unchanged. Then have `SensitivityExtractor` call `CredentialSpans(ctx.Text)` and merge its results, so behaviour is identical.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/...`
Expected: PASS, including the pre-existing `extractors_creddetect_test.go`.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/creddetect_pass.go internal/agent/enrich/creddetect_pass_test.go internal/agent/enrich/extractors.go
git commit -m "enrich: credential detection no longer requires a model

creddetect is pure Go and needs no sidecar, but lived inside SensitivityExtractor,
so disabling ML would have disabled credential masking too."
```

---

### Task 4: a third `ml_backend` mode

**Files:**
- Modify: `internal/agent/settings/settings.go` (`MLEnabled`, around line 29)
- Modify: `internal/agent/settings/settings_test.go`
- Modify: `internal/agent/daemon/daemon.go` (`wireEnrichment`, around line 947)

**Why:** today `ml_backend: "off"` returns `ingress.DiscardHandler` — enrichment is disabled entirely. There is no mode in which deterministic passes run without the model, which is what this whole branch is for.

**Interfaces:**
- Produces: `Settings.MLEnabled() bool` (unchanged meaning: may use the sidecar model) and `Settings.EnrichmentEnabled() bool` (true unless `ml_backend == "off"` **and** no deterministic mode). Mode string: `"deterministic"`.

CLAUDE.md currently says "ML is mandatory — there is no deterministic backend… don't reintroduce a deterministic fallback." That prohibition was written against a *substitute* — a lower-fidelity version of the same facets, silently swapped in when the sidecar is unhealthy. This is a different set of facets, always on, never health-gated. **Update CLAUDE.md and AGENTS.md in this task** so the docs and the code agree; do not leave the prohibition standing.

- [ ] **Step 1: Write the failing test**

```go
func TestDeterministicModeEnablesEnrichmentWithoutTheModel(t *testing.T) {
	cases := []struct {
		backend            string
		wantML, wantEnrich bool
	}{
		{"", true, true},
		{"auto", true, true},
		{"deterministic", false, true},
		{"off", false, false},
	}
	for _, c := range cases {
		s := Settings{MLBackend: c.backend}
		if got := s.MLEnabled(); got != c.wantML {
			t.Errorf("%q: MLEnabled=%v want %v", c.backend, got, c.wantML)
		}
		if got := s.EnrichmentEnabled(); got != c.wantEnrich {
			t.Errorf("%q: EnrichmentEnabled=%v want %v", c.backend, got, c.wantEnrich)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/agent/settings/ -run TestDeterministic -v`
Expected: FAIL — `s.EnrichmentEnabled undefined`

- [ ] **Step 3: Implement**

```go
// MLEnabled reports whether the ML sidecar backend may be used.
func (s Settings) MLEnabled() bool { return s.MLBackend != "off" && s.MLBackend != "deterministic" }

// EnrichmentEnabled reports whether the enrichment worker runs at all. "deterministic" runs the
// passes that need no model — workstream dimensions from tool inputs, credential detection — and
// is NOT a fallback: it produces different facets, always, rather than a lower-fidelity
// substitute for the model's, which is what AGENTS.md forbids.
func (s Settings) EnrichmentEnabled() bool { return s.MLBackend != "off" }
```

Then in `wireEnrichment`, gate the `DiscardHandler` on `!set.EnrichmentEnabled()` rather than `!set.MLEnabled()`, and return a nil `model`/`gate` when ML is off but enrichment is on. **The pipeline must tolerate a nil Model** — check `runStage`/`Extractor` call sites and make a nil `Model` skip model-dependent passes rather than panic; if that needs a guard, add it here with a test.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/settings/... ./internal/agent/daemon/...`
Expected: PASS

- [ ] **Step 5: Update the docs in the same commit**

Amend the "ML is mandatory" paragraphs in `CLAUDE.md` and `AGENTS.md` to describe the three modes and say explicitly why `deterministic` is not the forbidden fallback.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/settings/ internal/agent/daemon/daemon.go CLAUDE.md AGENTS.md
git commit -m "daemon: a third ml_backend mode, deterministic

'off' disabled enrichment entirely, so there was no mode in which the deterministic
passes could run without the model. Not the fallback AGENTS.md forbids: different
facets, always on, never health-gated. Docs updated to match."
```

---

### Task 5: the Go client's `Analyze()`

**Files:**
- Modify: `internal/agent/enrich/sidecar/client.go`
- Create: `internal/agent/enrich/sidecar/analyze_test.go`

**Interfaces:**
- Consumes: `POST /analyze` from Task 2.
- Produces:

```go
type WorkstreamValue struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Count      int     `json:"count"`
}
type AnalyzeResult struct {
	Schema      int                        `json:"schema"`
	Evidence    int                        `json:"evidence"`
	Workstreams map[string]WorkstreamValue `json:"workstreams"`
}
func (c *Client) Analyze(path, promptID string, spanMinutes int) (AnalyzeResult, bool)
```

- [ ] **Step 1: Write the failing test**

```go
package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnalyzeSendsCoordinatesAndNeverText(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{
			"schema": 1, "evidence": 42,
			"workstreams": map[string]any{
				"project": map[string]any{"value": "acme", "confidence": 1.0, "count": 3},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL)
	out, ok := c.Analyze("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("Analyze reported failure")
	}
	if _, leaked := got["text"]; leaked {
		t.Error("request must carry coordinates, never text")
	}
	if got["path"] != "/tmp/t.jsonl" || got["prompt_id"] != "prompt-1" {
		t.Errorf("coordinates not sent: %v", got)
	}
	if out.Workstreams["project"].Value != "acme" || out.Evidence != 42 {
		t.Errorf("bad decode: %+v", out)
	}
}

func TestAnalyzeReportsFailureOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, ok := New(srv.URL).Analyze("/tmp/t.jsonl", "missing", 60); ok {
		t.Error("a 404 must report failure, not an empty success")
	}
}
```

Use whatever constructor `client.go` actually exports instead of `New` if it differs — read it first.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/agent/enrich/sidecar/ -run TestAnalyze -v`
Expected: FAIL — `c.Analyze undefined`

- [ ] **Step 3: Implement, following `Extract`'s shape**

```go
type analyzeReq struct {
	Path        string `json:"path"`
	PromptID    string `json:"prompt_id"`
	SpanMinutes int    `json:"span_minutes"`
}

// Analyze asks the sidecar to characterise the window ending at promptID. It sends COORDINATES,
// never prompt text — the same rule as spool.Pointer.
func (c *Client) Analyze(path, promptID string, spanMinutes int) (AnalyzeResult, bool) {
	var r AnalyzeResult
	if !c.post("/analyze", analyzeReq{path, promptID, spanMinutes}, &r) {
		return AnalyzeResult{}, false
	}
	return r, true
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/enrich/sidecar/...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/sidecar/client.go internal/agent/enrich/sidecar/analyze_test.go
git commit -m "sidecar client: Analyze() over window coordinates"
```

---

### Task 6: the workstream pass, and publishing it

**Files:**
- Modify: `internal/agent/enrich/types.go` (`Profile`)
- Create: `internal/agent/enrich/workstreams.go`
- Create: `internal/agent/enrich/workstreams_test.go`
- Modify: `internal/agent/publish/publish.go` + `publish_test.go`

**Interfaces:**
- Consumes: `sidecar.Client.Analyze(path, promptID, spanMinutes) (AnalyzeResult, bool)`, whose
  `Workstreams` field is `map[string]sidecar.WorkstreamValue` — **not** `map[string]Labeled`.
- Produces: `Profile.Workstreams map[string]Labeled` with json tag `workstreams,omitempty`,
  populated by a Wave-0 extractor that needs no `Model`.

**The conversion is this task's job and must be explicit** — `sidecar.WorkstreamValue{Value,
Confidence, Count}` becomes `enrich.Labeled{Value, Confidence, Producer}`. `Count` has no home on
`Labeled` and is dropped; `Producer` is set to `"workstreams-v1"` so a published dimension is
attributable, matching how every other pass stamps its producer. If you decide `Count` should
survive, say where it goes rather than silently discarding it — the count is the evidence behind
the value and a reviewer will ask.

- [ ] **Step 1: Write the failing test**

```go
func TestWorkstreamsPassPopulatesTheProfileWithoutAModel(t *testing.T) {
	ctx := &JobContext{ /* no Model set — the point of this pass */ }
	// inject a fake analyzer returning one dimension
	got, err := WorkstreamsExtractor{Analyze: func(path, id string, span int) (map[string]Labeled, bool) {
		return map[string]Labeled{"project": {Value: "acme", Confidence: 1.0}}, true
	}}.Run(ctx)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	ws, _ := got["workstreams"].(map[string]Labeled)
	if ws["project"].Value != "acme" {
		t.Errorf("got %+v", got)
	}
}

func TestWorkstreamsPassOmitsTheKeyWhenAnalysisFails(t *testing.T) {
	got, err := WorkstreamsExtractor{Analyze: func(string, string, int) (map[string]Labeled, bool) {
		return nil, false
	}}.Run(&JobContext{})
	if err == nil && got["workstreams"] != nil {
		t.Error("a failed analysis must not publish an empty workstream set — absent and empty are different facts")
	}
}
```

Match `JobContext`'s and `Extractor`'s real shapes — read `types.go` and an existing extractor first, and adapt the injection to however the codebase already fakes a backend (see `enrichtest/`).

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/agent/enrich/ -run TestWorkstreams -v`
Expected: FAIL — undefined

- [ ] **Step 3: Implement the extractor and the Profile field**

```go
// Profile, in types.go — add beside the other facets:
Workstreams map[string]Labeled `json:"workstreams,omitempty"`
```

```go
// workstreams.go
package enrich

// WorkstreamsExtractor publishes the deterministic dimensions a cost report buckets by. It is a
// WAVE 0 pass: it needs no Model, so it must run when the sidecar has no worker and when
// ml_backend is "deterministic". Gating it on inference readiness would defeat the point.
type WorkstreamsExtractor struct {
	// Analyze is injected so the pass is testable without a sidecar. Production wires it to
	// sidecar.Client.Analyze, converting WorkstreamValue -> Labeled.
	Analyze func(path, promptID string, spanMinutes int) (map[string]Labeled, bool)
}

func (WorkstreamsExtractor) Name() string    { return "workstreams" }
func (WorkstreamsExtractor) Version() string { return versioned("workstreams") }

func (e WorkstreamsExtractor) Run(ctx *JobContext) (map[string]any, error) {
	ws, ok := e.Analyze(ctx.TranscriptPath, ctx.PromptID, 60)
	if !ok || len(ws) == 0 {
		// Absent and empty are different facts: a failed analysis must not publish
		// "no dimensions applied", which a report would read as a real answer.
		return nil, errAnalysisUnavailable
	}
	return map[string]any{"workstreams": ws}, nil
}
```

`JobContext` may not carry `TranscriptPath`/`PromptID` today — check `types.go`. If it does not, add
them where the pipeline builds the context from `queue.Job` (which has both), rather than
threading a second argument through `Run`.

- [ ] **Step 4: Carry it on the wire**

```go
// publish.Enrichment — add beside Entities:
Workstreams map[string]enrich.Labeled `json:"workstreams,omitempty"`
```

```go
// in Build(), beside the existing field copies:
Workstreams: p.Workstreams,
```

```go
// publish_test.go
func TestBuildCarriesWorkstreamsAndNoPromptText(t *testing.T) {
	p := enrich.Profile{Workstreams: map[string]enrich.Labeled{
		"project": {Value: "acme", Confidence: 1.0, Producer: "workstreams-v1"},
	}}
	b, err := json.Marshal(Build(queue.Job{}, p, "actor", false, 0, time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, `"project"`) || !strings.Contains(s, `"acme"`) {
		t.Errorf("dimension missing from payload: %s", s)
	}
	for _, forbidden := range []string{`"span"`, `"offset"`, `"prompt_text"`} {
		if strings.Contains(s, forbidden) {
			t.Errorf("payload leaked %s: %s", forbidden, s)
		}
	}
}
```

- [ ] **Step 5: Run everything**

Run: `go test ./... && go build ./...`
Expected: PASS

- [ ] **Step 6: Decide the schema version deliberately**

This changes the published payload shape. Decide whether `enrich.SchemaVersion` bumps, and say why in the commit message. AGENTS.md: "changing any enrichment vocabulary is contract-affecting — bump `enrich.SchemaVersion` and re-run the eval." Adding a field is not changing a vocabulary; make the call and justify it rather than defaulting either way.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/enrich/ internal/agent/publish/
git commit -m "enrich: publish deterministic workstream dimensions"
```

---

## Not in this plan

- **Daemon-side vocabulary push** (settings poll → `POST /vocabulary`), the `RemoteLabel.Match []string` field, and vocabulary-over-model precedence. Needs the Atlas change that serves `kind: "vocabulary"`.
- **Atlas surfacing the workstreams.** Other repo.
- **Baselines and lift** (phase 3) — needs persisted per-machine history.
- **Digest prose** (phase 4) — no consumer.
- **The third analysis worker process.** Task 2 runs analysis in the FastAPI parent; the architecture spec wants its own process. Deferred on the 2026-08-22 memory decision, recorded in the endpoint's docstring.
