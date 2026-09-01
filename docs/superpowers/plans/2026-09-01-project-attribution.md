# Block-Level Project Attribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Closed blocks gain multi-label org-project attribution (embedding + metadata + local verifier), published as an idempotent re-publish to Atlas, fully testable against a local Atlas.

**Architecture:** The sidecar gains `/projects` (cache + embed definitions) and `/attribute` (score a block span; async like textembed). The Go daemon gains a durable attribution job store and an attributor loop that re-publishes blocks with `projects` filled. Wire change is additive: `projects`, `projects_status`, `attribution` meta on `BlockEnrichment`, schema 21→22.

**Tech Stack:** Go (daemon), Python 3.11 sidecar (FastAPI, no pytest — standalone test scripts), llama-cpp-python (new), Qwen3-Embedding-0.6B (existing), Gemma 4 E2B Q4_K_M GGUF (new).

**Spec:** `docs/superpowers/specs/2026-09-01-project-attribution-discovery.md` — AC references below point there.

## Global Constraints

- Branch: create `feat/project-attribution` off `main` before Task 1.
- Privacy invariant: no message text, spans, or offsets cross the wire. Attribution publishes project ids, confidences, enums, and integer timings only. (AC-7)
- Sidecar tests are **standalone scripts** (`sidecar/app/test_*.py`), each runnable as `PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_X.py`, ending with a printed summary. No pytest. (repo law, `AGENTS.md:2052`)
- Go verification: `go test ./...` must stay green after every task.
- A skip is stated, never silent: every disabled/degraded path publishes a status naming itself. (AC-6)
- No GLiNER changes, no removal of unused paths. (spec §1 exclusions)
- The first block publish is never delayed by attribution. (AC-8)
- New env toggles: `KELD_ATTRIBUTION` (feature gate, default off), `KELD_ATTRIBUTION_VERIFIER` (default on *within* the gate), `KELD_PROJECTS_FILE` (mock/local project definitions). Config-file keys `attribution`, `attribution_verifier` mirror them, env wins — same shape as `blocks.Enabled` (`internal/agent/blocks/emitter.go:489`).

---

### Task 1: Project definitions — settings seam + file override (Go)

**Files:**
- Modify: `internal/agent/settings/remote.go` (add key)
- Create: `internal/agent/settings/projects.go`
- Test: `internal/agent/settings/projects_test.go`

**Interfaces:**
- Produces:

```go
// settings package
type RemoteProject struct {
    ID          string   `json:"id"`
    Title       string   `json:"title"`
    Description string   `json:"description"`
    Team        string   `json:"team,omitempty"`
    Repos       []string `json:"repos,omitempty"`
    Keywords    []string `json:"keywords,omitempty"`
    TicketKey   string   `json:"ticket_key,omitempty"`
}
// on settings.Remote:  Projects *[]RemoteProject `json:"projects"`
// LoadProjectsFile(path string) ([]RemoteProject, error)  // strict JSON array
// EnvProjectsFile = "KELD_PROJECTS_FILE"
```

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/settings/projects_test.go
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// AC-1: the remote doc's projects key decodes; absent key stays nil.
func TestRemoteProjectsDecode(t *testing.T) {
	var r Remote
	body := `{"projects":[{"id":"proj_pay","title":"Payments",
	  "description":"Stripe migration","repos":["acme-billing"],"ticket_key":"PAY"}]}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	if r.Projects == nil || len(*r.Projects) != 1 || (*r.Projects)[0].ID != "proj_pay" {
		t.Fatalf("projects = %+v", r.Projects)
	}
	var empty Remote
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil || empty.Projects != nil {
		t.Fatalf("absent key must leave Projects nil, got %+v err %v", empty.Projects, err)
	}
}

// AC-2: the file override loads a mock definition set.
func TestLoadProjectsFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "projects.json")
	os.WriteFile(p, []byte(`[{"id":"proj_a","title":"A","description":"d"}]`), 0o600)
	got, err := LoadProjectsFile(p)
	if err != nil || len(got) != 1 || got[0].ID != "proj_a" {
		t.Fatalf("got %+v err %v", got, err)
	}
	if _, err := LoadProjectsFile(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file must error, not return empty")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte(`{"not":"an array"}`), 0o600)
	if _, err := LoadProjectsFile(bad); err == nil {
		t.Fatal("non-array must error")
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/agent/settings/ -run TestRemoteProjects -v` → FAIL (type undefined).

- [ ] **Step 3: Implement**

In `remote.go`, add to the `Remote` struct, following the `PIIRegions` comment style:

```go
	// Projects is the org's project-definition list for on-device block
	// attribution. A pointer so an absent key ("Atlas does not serve this
	// yet") is distinct from an explicit empty list. Atlas does not serve
	// this key yet; the client seam exists now so adopting it later is a
	// server change alone — the same reason PIIRegions above is modelled.
	Projects *[]RemoteProject `json:"projects"`
```

New `projects.go`:

```go
package settings

import (
	"encoding/json"
	"fmt"
	"os"
)

// RemoteProject is one org project definition, distributed via the settings
// document (or KELD_PROJECTS_FILE while Atlas does not serve the key).
// Descriptions flow DOWN to the device; only project IDs ever flow up.
type RemoteProject struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Team        string   `json:"team,omitempty"`
	Repos       []string `json:"repos,omitempty"`
	Keywords    []string `json:"keywords,omitempty"`
	TicketKey   string   `json:"ticket_key,omitempty"`
}

// EnvProjectsFile points at a local JSON array of RemoteProject — the mock
// path for tests and the smoke runbook. It WINS over the remote key so a
// local run is reproducible regardless of org state.
const EnvProjectsFile = "KELD_PROJECTS_FILE"

// LoadProjectsFile reads a strict JSON array of project definitions.
// A missing or malformed file is an error, never an empty list — silence
// here would make "attribution never ran" indistinguishable from "no
// projects defined".
func LoadProjectsFile(path string) ([]RemoteProject, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []RemoteProject
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("projects file %s: %w", path, err)
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests** — `go test ./internal/agent/settings/ -v` → PASS (all, not just new).

- [ ] **Step 5: Commit** — `git add internal/agent/settings && git commit -m "feat(settings): projects key + KELD_PROJECTS_FILE override (AC-1, AC-2)"`

---

### Task 2: Wire shape — projects on BlockEnrichment, schema 22 (Go)

**Files:**
- Modify: `internal/agent/publish/block.go`, `internal/agent/enrich/labels.go:378`
- Create: `internal/agent/enrich/attribution.go`
- Test: `internal/agent/publish/block_projects_test.go`

**Interfaces:**
- Produces (in `enrich`):

```go
type ProjectAttribution struct {
	ID         string  `json:"id"`
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"` // "embedding" | "metadata" | "verifier"
}
type AttributionMeta struct {
	EmbedMS       int               `json:"embed_ms"`
	VerifyMS      int               `json:"verify_ms"`
	PairsVerified int               `json:"pairs_verified"`
	EncoderState  string            `json:"encoder_state"` // "warm" | "cold" | "absent"
	Verifier      string            `json:"verifier"`      // "used" | "opted_out" | "unavailable" | "not_needed"
	ModelVersions map[string]string `json:"model_versions,omitempty"`
}
// Statuses:
const (
	ProjectsAttributed        = "attributed"
	ProjectsPending           = "pending"
	ProjectsSkippedDisabled   = "skipped:disabled"
	ProjectsSkippedNoProjects = "skipped:no_projects"
	ProjectsDegradedWeights   = "degraded:weights_unavailable"
)
```

- On `publish.BlockEnrichment`: `Projects []enrich.ProjectAttribution `json:"projects,omitempty"``, `ProjectsStatus string `json:"projects_status,omitempty"``, `Attribution *enrich.AttributionMeta `json:"attribution,omitempty"``.
- `publish.BuildBlock` gains no parameters; a new `publish.WithProjects(b BlockEnrichment, ps []enrich.ProjectAttribution, status string, meta *enrich.AttributionMeta) BlockEnrichment` returns a copy with the three fields set (keeps BuildBlock's signature stable for existing callers).
- `enrich.SchemaVersion` becomes `22`.

- [ ] **Step 1: Write the failing test**

```go
// internal/agent/publish/block_projects_test.go
package publish

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// AC-7: the wire carries projects + status + meta, and the meta is numbers
// and enums only — assert the marshalled JSON has no field that could hold
// message text (mirrors TestEnrichmentWireShapeCannotCarryAnalysisInternals).
func TestBlockWireCarriesProjects(t *testing.T) {
	b := BlockEnrichment{SchemaVersion: enrich.SchemaVersion}
	b = WithProjects(b,
		[]enrich.ProjectAttribution{{ID: "proj_pay", Confidence: 0.91, Source: "embedding"}},
		enrich.ProjectsAttributed,
		&enrich.AttributionMeta{EmbedMS: 812, VerifyMS: 0, EncoderState: "warm", Verifier: "not_needed"})
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"projects":[{"id":"proj_pay"`, `"projects_status":"attributed"`, `"embed_ms":812`} {
		if !strings.Contains(s, want) {
			t.Fatalf("wire body missing %s in %s", want, s)
		}
	}
	if enrich.SchemaVersion != 22 {
		t.Fatalf("SchemaVersion = %d, want 22", enrich.SchemaVersion)
	}
}

// The struct must be structurally unable to carry text: every string field of
// ProjectAttribution and AttributionMeta is an id or a closed enum. Guard by
// reflection so a future field addition trips this test.
func TestAttributionShapeHoldsNoText(t *testing.T) {
	allowed := map[string]bool{"ID": true, "Confidence": true, "Source": true,
		"EmbedMS": true, "VerifyMS": true, "PairsVerified": true,
		"EncoderState": true, "Verifier": true, "ModelVersions": true}
	for _, name := range structFieldNames(enrich.ProjectAttribution{}) {
		if !allowed[name] {
			t.Fatalf("new field %q on ProjectAttribution — extend the allowlist only after a privacy review", name)
		}
	}
	for _, name := range structFieldNames(enrich.AttributionMeta{}) {
		if !allowed[name] {
			t.Fatalf("new field %q on AttributionMeta — extend the allowlist only after a privacy review", name)
		}
	}
}
```

Add the tiny helper in the same test file:

```go
func structFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}
```

- [ ] **Step 2: Run to verify it fails** — `go test ./internal/agent/publish/ -run TestBlockWire -v` → FAIL.

- [ ] **Step 3: Implement** — create `internal/agent/enrich/attribution.go` with the types and constants from Interfaces (doc comments explaining each status, in the repo's voice). Add the three fields to `BlockEnrichment` after `EndReason`. Add `WithProjects` beside `BuildBlock`. Bump `labels.go` `SchemaVersion = 22` and note the bump in its comment ("v22: block rows may carry projects/projects_status/attribution").

- [ ] **Step 4: Run** — `go test ./... 2>&1 | tail -5` → all PASS (other packages compile against the bumped constant).

- [ ] **Step 5: Commit** — `git commit -am "feat(publish): projects + attribution meta on block wire, schema v22 (AC-7)"`

---

### Task 3: Sidecar — project store + `/projects` endpoint

**Files:**
- Create: `sidecar/app/analysis/attribution.py` (store half), `sidecar/app/test_attribution_projects.py`
- Modify: `sidecar/app/main.py` (route)

**Interfaces:**
- Produces (module `app.analysis.attribution`):

```python
set_projects(projects: list[dict]) -> str          # returns content hash
current_projects() -> tuple[list[dict], str]       # (projects, hash)
project_doc(p: dict) -> str                        # title (team)\ndescription\nKeywords: ...
project_vectors(encoder) -> dict[str, list[float]] # id -> vector, embedded once per hash
```

- Route: `POST /projects {"projects": [...]}` → `{"count": N, "hash": "..."}`. Idempotent; same hash skips re-embed. Bypasses `_dispatch` (a cache write, not inference), like `/vocabulary`.

- [ ] **Step 1: Write the failing test** (standalone script, repo style)

```python
# sidecar/app/test_attribution_projects.py
"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_projects.py"""
from app.analysis import attribution

PROJECTS = [
    {"id": "proj_pay", "title": "Payments", "team": "Eng",
     "description": "Stripe migration.", "repos": ["acme-billing"],
     "keywords": ["stripe", "dunning"], "ticket_key": "PAY"},
    {"id": "proj_seo", "title": "SEO Push", "team": "Marketing",
     "description": "Grow organic signups.", "repos": [], "keywords": ["backlinks"]},
]

class FakeEncoder:
    calls = 0
    def encode(self, texts):
        FakeEncoder.calls += 1
        return [[1.0, 0.0] if "Payments" in t else [0.0, 1.0] for t in texts]

def test_set_and_hash_stability():
    h1 = attribution.set_projects(PROJECTS)
    h2 = attribution.set_projects(list(PROJECTS))
    assert h1 == h2, "same content must hash identically"
    got, h = attribution.current_projects()
    assert h == h1 and [p["id"] for p in got] == ["proj_pay", "proj_seo"]

def test_project_doc_shape():
    doc = attribution.project_doc(PROJECTS[0])
    assert "Payments" in doc and "stripe" in doc and "Stripe migration." in doc

def test_vectors_embedded_once_per_hash():
    attribution.set_projects(PROJECTS)
    enc = FakeEncoder()
    v1 = attribution.project_vectors(enc)
    v2 = attribution.project_vectors(enc)
    assert set(v1) == {"proj_pay", "proj_seo"} and v1 is v2 or v1 == v2
    assert FakeEncoder.calls == 1, f"re-embedded despite unchanged hash: {FakeEncoder.calls}"

if __name__ == "__main__":
    test_set_and_hash_stability()
    test_project_doc_shape()
    test_vectors_embedded_once_per_hash()
    print("test_attribution_projects: 3 passed")
```

- [ ] **Step 2: Run to verify it fails** — `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_projects.py` → ImportError.

- [ ] **Step 3: Implement the store half of `attribution.py`**

```python
"""Block-level project attribution: org project definitions, their vectors, and
(the scoring half, added by the next change) the hybrid score over a block span.

Definitions flow DOWN from the daemon (POST /projects). Only project IDs are
ever returned upward. Vectors are embedded once per content hash and held in
module state — a handful of short documents, ~1s total on CPU."""
import hashlib
import json
import threading

_lock = threading.Lock()
_projects: list[dict] = []
_hash = ""
_vectors: dict[str, list[float]] | None = None
_vectors_hash = ""


def set_projects(projects):
    global _projects, _hash, _vectors, _vectors_hash
    canon = json.dumps(projects, sort_keys=True, separators=(",", ":"))
    h = hashlib.sha256(canon.encode()).hexdigest()[:16]
    with _lock:
        if h != _hash:
            _projects, _hash = list(projects), h
            if _vectors_hash != h:
                _vectors, _vectors_hash = None, ""
    return h


def current_projects():
    with _lock:
        return list(_projects), _hash


def project_doc(p):
    parts = [f"{p.get('title', '')} ({p.get('team', '')})", p.get("description", "")]
    if p.get("keywords"):
        parts.append("Keywords: " + ", ".join(p["keywords"]))
    return "\n".join(s for s in parts if s.strip())


def project_vectors(encoder):
    """id -> L2-normalised vector, embedded once per content hash."""
    global _vectors, _vectors_hash
    with _lock:
        if _vectors is not None and _vectors_hash == _hash:
            return _vectors
        projects, h = list(_projects), _hash
    vecs = encoder.encode([project_doc(p) for p in projects])
    out = {p["id"]: _l2(v) for p, v in zip(projects, vecs)}
    with _lock:
        _vectors, _vectors_hash = out, h
    return out


def _l2(v):
    n = sum(x * x for x in v) ** 0.5 or 1.0
    return [x / n for x in v]
```

- [ ] **Step 4: Run the test** → `test_attribution_projects: 3 passed`.

- [ ] **Step 5: Add the route in `main.py`** (beside `/vocabulary`, same bypass argument):

```python
class ProjectsIn(BaseModel):
    projects: list[dict]

@app.post("/projects")
async def projects(body: ProjectsIn):
    """Org project definitions for block attribution. A cache write, not
    inference — bypasses _dispatch for the same reason /vocabulary does.
    Embedding happens lazily on the first /attribute that needs vectors."""
    from app.analysis import attribution
    h = attribution.set_projects(body.projects)
    return {"count": len(body.projects), "hash": h}
```

- [ ] **Step 6: Run the sidecar test suite** — `cd sidecar && for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" || break; done` → all pass.

- [ ] **Step 7: Commit** — `git add sidecar && git commit -m "feat(sidecar): project store + /projects route (AC-3 groundwork)"`

---

### Task 4: Sidecar — hybrid scoring (embedding + metadata boost + threshold/band)

**Files:**
- Modify: `sidecar/app/analysis/attribution.py` (scoring half)
- Test: `sidecar/app/test_attribution_scoring.py`

**Interfaces:**
- Produces (same module; ported from `embedding-experiment/src/strategies.py`, constants from the benchmark):

```python
THRESHOLD = 0.49        # env KELD_ATTRIBUTION_THRESHOLD
BAND = 0.08             # env KELD_ATTRIBUTION_BAND
W_REPO, W_TICKET, W_KEYWORD, BOOST_CAP = 0.15, 0.20, 0.05, 0.35

metadata_boost(project: dict, dims: dict, texts: list[str]) -> float
score_block(texts: list[str], dims: dict, encoder | None)
    -> (scores: dict[id, float], borderline: list[id], assigned: list[id], encoder_used: bool)
```

- `dims` is the block's workstream dimensions map (`repo`, `branch` values as strings) as the daemon sends them; `texts` are the block's user-turn texts (Task 5 reads them).
- **⚠️ AMENDED 2026-09-01 (supersedes the original AC-4 wording and the `test_metadata_boost_model_free` assertion below).** With `encoder=None` NOTHING is assigned: `assigned` is empty and `encoder_used` is False, whatever the boost. Exact-match evidence caps at `BOOST_CAP` 0.35, below `THRESHOLD` 0.49, so a boost-only assignment could only exist under a second, unmeasured rule — and there is exactly one attribution path, the benchmarked one. The caller publishes `degraded:weights_unavailable` with no projects and the durable job retries once weights arrive.
- Embedding side reuses `textembed.sentence_chunks` and mean-pooling: embed each text, max-sim per project across texts (per-message max, matching the benchmark's chunked strategy).

- [ ] **Step 1: Write the failing test**

```python
# sidecar/app/test_attribution_scoring.py
"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_scoring.py"""
from app.analysis import attribution

PROJECTS = [
    {"id": "proj_pay", "title": "Payments", "team": "Eng",
     "description": "Stripe billing migration.", "repos": ["acme-billing"],
     "keywords": ["stripe", "dunning"], "ticket_key": "PAY"},
    {"id": "proj_ui", "title": "Design System", "team": "Design",
     "description": "Component library and tokens.", "repos": ["acme-ui"],
     "keywords": ["storybook", "tokens"], "ticket_key": "DS"},
]

class PayEncoder:
    """proj_pay doc -> [1,0]; proj_ui doc -> [0,1]; text about stripe -> [0.9, 0.1]."""
    def encode(self, texts):
        out = []
        for t in texts:
            if "Payments" in t: out.append([1.0, 0.0])
            elif "Design System" in t: out.append([0.0, 1.0])
            elif "stripe" in t.lower(): out.append([0.9, 0.1])
            else: out.append([0.3, 0.3])
        return out

def test_metadata_boost_model_free():          # AC-4 (AMENDED 2026-09-01)
    attribution.set_projects(PROJECTS)
    dims = {"repo": "acme-billing", "branch": "fix/PAY-12-retry"}
    b = attribution.metadata_boost(PROJECTS[0], dims, ["fix the dunning email"])
    assert b >= attribution.W_REPO + attribution.W_TICKET, f"boost {b}"
    # The boost is computed and reported, but with no encoder NOTHING is assigned:
    # one attribution path only, and exact matches alone never cross the threshold.
    scores, borderline, assigned, used = attribution.score_block(
        ["fix the dunning email"], dims, encoder=None)
    assert not used and assigned == [] and borderline == [], (scores, assigned, borderline)
    assert scores["proj_pay"] == round(b, 4), "boost must still be visible in the scores"

def test_embedding_plus_threshold():           # AC-3
    attribution.set_projects(PROJECTS)
    scores, borderline, assigned, used = attribution.score_block(
        ["we migrated stripe webhooks today"], {}, encoder=PayEncoder())
    assert used and scores["proj_pay"] > scores["proj_ui"]
    assert "proj_pay" in assigned and "proj_ui" not in assigned

def test_borderline_band():                    # AC-5 groundwork
    attribution.set_projects(PROJECTS)
    class MidEncoder(PayEncoder):
        def encode(self, texts):
            return [[1.0, 0.0] if "Payments" in t else
                    [0.0, 1.0] if "Design System" in t else
                    [0.47, 0.2] for t in texts]  # cosine vs proj_pay ≈ threshold
    scores, borderline, assigned, used = attribution.score_block(
        ["ambiguous work"], {}, encoder=MidEncoder())
    assert "proj_pay" in borderline, (scores, borderline)

if __name__ == "__main__":
    test_metadata_boost_model_free()
    test_embedding_plus_threshold()
    test_borderline_band()
    print("test_attribution_scoring: 3 passed")
```

- [ ] **Step 2: Run to verify it fails** → AttributeError (`metadata_boost` undefined).

- [ ] **Step 3: Implement the scoring half** (append to `attribution.py`)

```python
import os
import re

THRESHOLD = float(os.environ.get("KELD_ATTRIBUTION_THRESHOLD", "0.49"))
BAND = float(os.environ.get("KELD_ATTRIBUTION_BAND", "0.08"))
W_REPO, W_TICKET, W_KEYWORD, BOOST_CAP = 0.15, 0.20, 0.05, 0.35


def metadata_boost(project, dims, texts):
    """Deterministic boost from exact matches — repo, ticket key, keywords.
    Works with no model resident; that is the point (spec AC-4)."""
    blob = " ".join(str(v) for v in (dims or {}).values()).lower()
    text = "\n".join(texts).lower()
    boost = 0.0
    for repo in project.get("repos") or []:
        if repo.lower() in blob or repo.lower() in text:
            boost += W_REPO
    tk = project.get("ticket_key")
    if tk and re.search(rf"\b{re.escape(tk)}-\d+", text + " " + blob, re.I):
        boost += W_TICKET
    boost += W_KEYWORD * sum(1 for kw in project.get("keywords") or []
                             if kw.lower() in text)
    return min(boost, BOOST_CAP)


def _cos(a, b):
    return sum(x * y for x, y in zip(a, b))


def score_block(texts, dims, encoder):
    """Hybrid score per project over one block's user-turn texts.

    Returns (scores, borderline, assigned, encoder_used). encoder=None is the
    weights-absent path: boost-only, degraded but never silent — the CALLER
    states degraded:weights_unavailable."""
    projects, _ = current_projects()
    encoder_used = False
    sims = {p["id"]: 0.0 for p in projects}
    if encoder is not None and texts:
        pvecs = project_vectors(encoder)
        tvecs = [_l2(v) for v in encoder.encode(texts)]
        for pid, pv in pvecs.items():
            sims[pid] = max((_cos(tv, pv) for tv in tvecs), default=0.0)
        encoder_used = True
    scores, borderline, assigned = {}, [], []
    for p in projects:
        s = sims[p["id"]] + metadata_boost(p, dims, texts)
        scores[p["id"]] = round(s, 4)
        if abs(s - THRESHOLD) < BAND and encoder_used:
            borderline.append(p["id"])
        if s >= THRESHOLD:
            assigned.append(p["id"])
    return scores, borderline, assigned, encoder_used
```

- [ ] **Step 4: Run the test** → `test_attribution_scoring: 3 passed`. Then the whole sidecar suite loop from Task 3 Step 6.

- [ ] **Step 5: Commit** — `git commit -am "feat(sidecar): hybrid attribution scoring, model-free boost path (AC-3, AC-4)"`

---

### Task 5: Sidecar — verifier (llama-cpp-python, band-only, opt-out)

**Files:**
- Create: `sidecar/app/verifier.py`, `sidecar/app/test_attribution_verifier.py`
- Modify: `sidecar/requirements.txt` (add `llama-cpp-python==0.3.*`)

**Interfaces:**
- Produces:

```python
# app.verifier
weights_path() -> str | None      # KELD_VERIFIER_GGUF, else ~/.keld/models/gemma-4-e2b/model.gguf; None if absent
enabled() -> bool                 # KELD_ATTRIBUTION_VERIFIER != "0"/"false"/"off"/"no" (default ON)
class Verifier:                   # lazy llama load, n_gpu_layers=0 (CPU), temperature 0
    verify(block_text: str, dims: dict, project: dict) -> (bool, float)  # (verdict, seconds)
VERIFY_PROMPT: str                # the benchmark prompt, adapted: block text cap 2500 chars
```

- The Verifier's `Llama` import happens inside `__init__`, never at module import (matches textembed's "off is not compute-and-discard" rule).
- Attribution integration (in `attribution.py`): `apply_verifier(texts, dims, scores, borderline, verifier) -> (assigned_overrides: dict[id, bool], pairs: int, ms: int)` — verdict wins over threshold for borderline pairs only (AC-5).

- [ ] **Step 1: Write the failing test** (verifier stub — no model download in tests)

```python
# sidecar/app/test_attribution_verifier.py
"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_verifier.py"""
import os
from app.analysis import attribution
from app import verifier

PROJECTS = [{"id": "proj_pay", "title": "Payments", "team": "Eng",
             "description": "Stripe billing.", "repos": [], "keywords": [], "ticket_key": "PAY"}]

class StubVerifier:
    def __init__(self, verdict): self.verdict, self.calls = verdict, 0
    def verify(self, block_text, dims, project):
        self.calls += 1
        return self.verdict, 0.01

def test_verdict_overrides_borderline():       # AC-5
    attribution.set_projects(PROJECTS)
    v = StubVerifier(True)
    overrides, pairs, ms = attribution.apply_verifier(
        ["ambiguous"], {}, {"proj_pay": 0.45}, ["proj_pay"], v)
    assert overrides == {"proj_pay": True} and pairs == 1 and v.calls == 1

def test_only_borderline_pairs_judged():       # AC-5
    v = StubVerifier(False)
    overrides, pairs, ms = attribution.apply_verifier(
        ["clear"], {}, {"proj_pay": 0.92}, [], v)   # nothing borderline
    assert pairs == 0 and v.calls == 0 and overrides == {}

def test_opt_out_env():                        # AC-6
    os.environ["KELD_ATTRIBUTION_VERIFIER"] = "0"
    try:
        assert verifier.enabled() is False
    finally:
        del os.environ["KELD_ATTRIBUTION_VERIFIER"]
    assert verifier.enabled() is True          # default ON within the gate

def test_no_weights_is_stated_not_fatal():
    old = os.environ.pop("KELD_VERIFIER_GGUF", None)
    os.environ["KELD_VERIFIER_GGUF"] = "/nonexistent/model.gguf"
    try:
        assert verifier.weights_path() is None
    finally:
        os.environ.pop("KELD_VERIFIER_GGUF")
        if old: os.environ["KELD_VERIFIER_GGUF"] = old

if __name__ == "__main__":
    test_verdict_overrides_borderline()
    test_only_borderline_pairs_judged()
    test_opt_out_env()
    test_no_weights_is_stated_not_fatal()
    print("test_attribution_verifier: 4 passed")
```

- [ ] **Step 2: Run to verify it fails** → ImportError.

- [ ] **Step 3: Implement `verifier.py`**

```python
"""The attribution verifier: a small local LLM giving YES/NO on borderline
(block, project) pairs. Gemma 4 E2B Q4_K_M via llama-cpp-python, CPU only.

ON by default WITHIN the attribution gate; KELD_ATTRIBUTION_VERIFIER=0 opts a
slow machine out — the caller states degraded, never silently narrows. The
model is lazy: importing this module loads nothing; a Verifier() loads once."""
import os
import time

MAX_BLOCK_CHARS = 2500

VERIFY_PROMPT = """You are classifying whether a block of work relates to a specific company project.

PROJECT: {title} (team: {team})
{description}
Keywords: {keywords}

WORK CONTEXT: repo={repo} branch={branch}

WORK (may be truncated):
{text}

Question: Is this work part of the project "{title}"? Work on the same general topic but for personal use or a different initiative does NOT count. Answer with exactly one word: YES or NO."""


def enabled():
    return os.environ.get("KELD_ATTRIBUTION_VERIFIER", "").strip().lower() \
        not in ("0", "false", "off", "no")


def weights_path():
    explicit = os.environ.get("KELD_VERIFIER_GGUF")
    if explicit:
        return explicit if os.path.isfile(explicit) else None
    home = os.environ.get("KELD_HOME") or os.path.join(os.path.expanduser("~"), ".keld")
    p = os.path.join(home, "models", "gemma-4-e2b", "model.gguf")
    return p if os.path.isfile(p) else None


class Verifier:
    def __init__(self, path=None, n_threads=None):
        from llama_cpp import Llama  # deliberate: import at load, not at module import
        self._llm = Llama(model_path=path or weights_path(), n_ctx=4096,
                          n_gpu_layers=0, n_threads=n_threads or max(2, os.cpu_count() // 2),
                          verbose=False)

    def verify(self, block_text, dims, project):
        d = dims or {}
        prompt = VERIFY_PROMPT.format(
            title=project.get("title", ""), team=project.get("team", ""),
            description=project.get("description", ""),
            keywords=", ".join(project.get("keywords") or []),
            repo=d.get("repo", "?"), branch=d.get("branch", "?"),
            text=block_text[:MAX_BLOCK_CHARS])
        t0 = time.time()
        out = self._llm.create_chat_completion(
            messages=[{"role": "user", "content": prompt}],
            max_tokens=8, temperature=0)
        text = out["choices"][0]["message"]["content"].strip().upper()
        return text.startswith("YES") or "YES" in text[:12], time.time() - t0
```

And in `attribution.py`:

```python
def apply_verifier(texts, dims, scores, borderline, verifier_obj):
    """Verdicts for borderline pairs only. The verdict WINS over the threshold
    — that is the verifier's whole job (benchmark: fixes exactly the cases the
    threshold cannot call)."""
    if not borderline or verifier_obj is None:
        return {}, 0, 0
    projects, _ = current_projects()
    by_id = {p["id"]: p for p in projects}
    block_text = "\n".join(texts)
    overrides, total = {}, 0.0
    for pid in borderline:
        verdict, secs = verifier_obj.verify(block_text, dims, by_id[pid])
        overrides[pid] = bool(verdict)
        total += secs
    return overrides, len(borderline), int(total * 1000)
```

- [ ] **Step 4: Run the test** → `test_attribution_verifier: 4 passed`. Add `llama-cpp-python==0.3.*` to `sidecar/requirements.txt`.

- [ ] **Step 5: Commit** — `git commit -am "feat(sidecar): E2B verifier, band-only, opt-out stated (AC-5, AC-6)"`

---

### Task 6: Sidecar — `/attribute` endpoint (span text, async, statuses, timings)

**Files:**
- Modify: `sidecar/app/analysis/attribution.py` (orchestration), `sidecar/app/main.py` (route)
- Test: `sidecar/app/test_attribution_endpoint.py`

**Interfaces:**
- Route: `POST /attribute {"path", "session_id", "start", "end", "dims": {...}}` →
  `{"status": "...", "projects": [...], "attribution": {...}}` where `status` uses the Task 2 vocabulary. Coordinates in, ids out — never text in the response.
- Span text: user-turn texts within `[start, end)` read via the same reader `featuretext.TextSource` uses (`textembed`'s message reading, `sidecar/app/analysis/textembed.py:189`). Reuse its module-level read function; do NOT open transcripts any other way.
- Encoder: reuse `textembed`'s encoder acquisition (`weights_dir()`, encoder child). If weights are absent → score with `encoder=None`, `status=degraded:weights_unavailable`, `encoder_state=absent`.
- Confinement: the route copies `/blocks`' allowlist check verbatim (first statements of the `/blocks` handler in `main.py:834`) — same 403 semantics.
- Inference dispatch: verifier calls go through the runner (`_dispatch`) so single-flight holds; the embedding read+score run in the default executor like `/blocks`.
- Response is synchronous-when-cheap: if the encoder child is not warm and texts are non-empty, return `{"status": "pending"}` immediately (the daemon retries next sweep) — textembed's `STATUS_PENDING` argument, `textembed.py:139`.
- Orchestration function produced (unit-testable without HTTP):

```python
attribute_block(texts, dims, encoder, verifier_obj) -> dict
# {"status", "projects": [{"id","confidence","source"}], "attribution": {embed_ms, verify_ms,
#  pairs_verified, encoder_state, verifier, model_versions}}
```

- [ ] **Step 1: Write the failing test**

```python
# sidecar/app/test_attribution_endpoint.py
"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_endpoint.py"""
from app.analysis import attribution

PROJECTS = [
    {"id": "proj_pay", "title": "Payments", "team": "Eng",
     "description": "Stripe billing migration.", "repos": ["acme-billing"],
     "keywords": ["stripe"], "ticket_key": "PAY"},
]

class PayEncoder:
    def encode(self, texts):
        return [[1.0, 0.0] if "Payments" in t else [0.9, 0.1] for t in texts]

class StubVerifier:
    def verify(self, block_text, dims, project): return True, 0.01

def test_attributed_full_path():               # AC-3
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block(
        ["stripe webhook retries again"], {"repo": "acme-billing"},
        encoder=PayEncoder(), verifier_obj=StubVerifier())
    assert out["status"] == "attributed"
    ids = [p["id"] for p in out["projects"]]
    assert ids == ["proj_pay"]
    meta = out["attribution"]
    assert meta["encoder_state"] == "warm" and meta["embed_ms"] >= 0

def test_weights_absent_is_degraded():         # AC-4 (AMENDED 2026-09-01)
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block(
        ["fix PAY-12 dunning"], {"repo": "acme-billing"}, encoder=None, verifier_obj=None)
    assert out["status"] == "degraded:weights_unavailable"
    # No guessing without the model: the caller retries this block once weights land.
    assert out["projects"] == []
    assert out["attribution"]["encoder_state"] == "absent"

def test_no_projects_is_skipped():             # AC-1 status
    attribution.set_projects([])
    out = attribution.attribute_block(["anything"], {}, encoder=PayEncoder(), verifier_obj=None)
    assert out["status"] == "skipped:no_projects" and out["projects"] == []

def test_source_labels():
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block(
        ["stripe webhook retries again"], {}, encoder=PayEncoder(), verifier_obj=None)
    assert out["projects"][0]["source"] in ("embedding", "metadata", "verifier")

if __name__ == "__main__":
    test_attributed_full_path()
    test_weights_absent_is_degraded()
    test_no_projects_is_skipped()
    test_source_labels()
    print("test_attribution_endpoint: 4 passed")
```

- [ ] **Step 2: Run to verify it fails** → AttributeError (`attribute_block` undefined).

- [ ] **Step 3: Implement `attribute_block`** (append to `attribution.py`)

```python
import time as _time


def attribute_block(texts, dims, encoder, verifier_obj):
    projects, _ = current_projects()
    if not projects:
        return {"status": "skipped:no_projects", "projects": [],
                "attribution": _meta(0, 0, 0, "absent" if encoder is None else "warm",
                                     "not_needed")}
    t0 = _time.time()
    scores, borderline, assigned, encoder_used = score_block(texts, dims, encoder)
    embed_ms = int((_time.time() - t0) * 1000)
    overrides, pairs, verify_ms = apply_verifier(texts, dims, scores, borderline, verifier_obj)
    final = []
    for pid in scores:
        inn = pid in assigned
        if pid in overrides:
            inn = overrides[pid]
        if inn:
            src = "verifier" if pid in overrides else \
                  ("embedding" if encoder_used else "metadata")
            final.append({"id": pid, "confidence": scores[pid], "source": src})
    verifier_state = ("used" if pairs else "not_needed") if verifier_obj is not None \
        else ("opted_out" if borderline else "not_needed")
    status = "attributed" if encoder_used else "degraded:weights_unavailable"
    return {"status": status, "projects": final,
            "attribution": _meta(embed_ms, verify_ms, pairs,
                                 "warm" if encoder_used else "absent", verifier_state)}


def _meta(embed_ms, verify_ms, pairs, encoder_state, verifier_state):
    return {"embed_ms": embed_ms, "verify_ms": verify_ms, "pairs_verified": pairs,
            "encoder_state": encoder_state, "verifier": verifier_state,
            "model_versions": {"encoder": "qwen3-embedding-0.6b",
                               "verifier": "gemma-4-e2b-q4km"}}
```

- [ ] **Step 4: Run the test** → 4 passed. Full sidecar loop → green.

- [ ] **Step 5: Wire the HTTP route in `main.py`** — request model + handler. Copy `/blocks`' confinement lines verbatim as the first statements. Read span texts with the same reader textembed uses (its message-reading function over `[start, end)`, user-stream only). Encoder: `textembed.weights_dir()`; when it returns None pass `encoder=None`. When the encoder child would need a cold start and texts exist, return `{"status": "pending", "projects": [], "attribution": null}` and let the daemon retry. Verifier: build once per process (module-level lazy singleton) only when `verifier.enabled()` and `verifier.weights_path()`; verifier inference is submitted through `_dispatch` (the runner), embedding + scoring run in the default executor. Timings from `attribute_block` pass through untouched.

- [ ] **Step 6: Run the whole sidecar suite** → green. **Commit** — `git commit -am "feat(sidecar): /attribute route — span text, statuses, timings (AC-3..AC-6)"`

---

### Task 7: Go — sidecar client methods + durable attribution jobs + re-publish

**Files:**
- Modify: `internal/agent/enrich/sidecar/client.go` (two methods)
- Create: `internal/agent/attrib/attrib.go` (job store + attributor), `internal/agent/attrib/attrib_test.go`
- Modify: `internal/agent/blocks/emitter.go` (schedule hook), `internal/agent/daemon/daemon.go` (wiring)

**Interfaces:**
- Client (mirrors existing method style, `client.go:137`):

```go
func (c *Client) PostProjects(projects []settings.RemoteProject) error
// POST /projects
type AttributeResult struct {
	Status      string                       `json:"status"`
	Projects    []enrich.ProjectAttribution  `json:"projects"`
	Attribution *enrich.AttributionMeta      `json:"attribution"`
}
func (c *Client) Attribute(path, sessionID string, start, end float64,
	dims map[string]string) (AttributeResult, bool)
// POST /attribute; false on transport error (retryable by the caller's sweep)
```

- Attributor (package `attrib`):

```go
type Job struct {
	Source, SessionID, Path string
	Start, End              float64
	Attempts                int
}
type Store struct{ dir string }                    // one JSON file per job under paths.SpoolDir()/attrib
func NewStore(dir string) *Store
func (s *Store) Put(j Job) error                   // filename: sha of (session,start) — idempotent
func (s *Store) List() ([]Job, error)
func (s *Store) Delete(j Job) error
func (s *Store) Quarantine(j Job) error            // moves to dir/bad/

type Attributor struct { /* store, client iface, sender, facts, gates */ }
func New(st *Store, cl AttributeClient, pub blocks.Sender, facts blocks.Facts, actor string) *Attributor
func (a *Attributor) Schedule(b publish.BlockEnrichment, path string)  // Put + in-memory nudge
func (a *Attributor) Run(ctx context.Context, interval time.Duration)  // drain on start, sweep
const MaxAttempts = 4                               // then Quarantine — the death-spiral lesson
```

- **⚠️ AMENDED 2026-09-01 — `pending` does not consume an attempt.** `MaxAttempts` bounds ERRORS only. A `pending` answer (encoder cold, weights not provisioned yet) leaves `Attempts` untouched and re-spools the job unchanged. A multi-gigabyte download outlives any small attempt count, so counting waiting as failing would permanently abandon every block of the provisioning window — precisely what this durable job exists to prevent. Job records therefore track `Attempts` (errors) separately from simply being re-queued.

- Emitter hook: `Emitter` gains an optional `OnPublished func(rows []publish.BlockEnrichment, path string)` field, called after a successful `SendBlocks`. Nil-safe. The daemon sets it to `attributor.Schedule` when the attribution gate is on.
- Re-publish: the attributor rebuilds the row via `Digester.BlocksCharacterised` re-fetch? No — simpler and already idempotent: it holds the published `BlockEnrichment` value in the Job? **No: keep jobs small and durable.** The attributor re-fetches the single block through the same `Digester` the emitter used (blocks are deterministic from the store), applies `publish.WithProjects`, and `SendBlocks([...])` upserts. Job carries only coordinates.
- Per-call deadline: each `Attribute` call runs under `context.WithTimeout(ctx, 2*time.Minute)` bound via `Client.WithContext` — cancels in-flight sidecar work (the death-spiral fix pattern, `client.go:46`).
- Gate: `attrib.Enabled(fromConfig bool) bool` reading `KELD_ATTRIBUTION`, exact copy of `blocks.Enabled` shape (`emitter.go:489`).

- [ ] **Step 1: Write the failing tests**

```go
// internal/agent/attrib/attrib_test.go
package attrib

import (
	"context"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

type fakeClient struct {
	res   AttributeResultLike // alias for the client result shape used by the iface
	calls int
	ok    bool
}
type fakeSender struct{ sent [][]publish.BlockEnrichment }

func (f *fakeSender) SendBlocks(rows []publish.BlockEnrichment) error {
	f.sent = append(f.sent, rows)
	return nil
}

// AC-8: a scheduled job survives a "restart" — a fresh Attributor over the
// same store dir drains it and re-publishes with projects.
func TestAttributionJobSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	st.Put(Job{Source: "claude_code", SessionID: "s1", Path: "/tmp/x.jsonl", Start: 100, End: 700})

	sender := &fakeSender{}
	cl := successClient("proj_pay") // helper: returns status attributed + one project
	a := New(NewStore(dir), cl, sender, nil, "actor@x")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	a.drainOnce(ctx) // exported for tests as DrainOnce or driven via Run+cancel

	if len(sender.sent) != 1 {
		t.Fatalf("expected one re-publish, got %d", len(sender.sent))
	}
	row := sender.sent[0][0]
	if row.ProjectsStatus != enrich.ProjectsAttributed || len(row.Projects) != 1 {
		t.Fatalf("row = %+v", row)
	}
	if left, _ := NewStore(dir).List(); len(left) != 0 {
		t.Fatalf("job not deleted after success: %d left", len(left))
	}
}

// AC-8: pending answers re-try; MaxAttempts quarantines rather than loops.
func TestPendingRetriesThenQuarantine(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)
	st.Put(Job{SessionID: "s1", Path: "/tmp/x.jsonl", Start: 1, End: 2})
	a := New(st, pendingClient(), &fakeSender{}, nil, "actor@x")
	ctx := context.Background()
	for i := 0; i < MaxAttempts; i++ {
		a.drainOnce(ctx)
	}
	if jobs, _ := st.List(); len(jobs) != 0 {
		t.Fatalf("job should be quarantined after %d attempts, %d left", MaxAttempts, len(jobs))
	}
	// quarantined, not deleted: dir/bad holds it
	if bad, _ := NewStore(dir + "/bad").List(); len(bad) != 1 {
		t.Fatalf("expected 1 quarantined job, got %d", len(bad))
	}
}

// AC-8: first publish is never delayed — Schedule only writes a file.
func TestScheduleIsNonBlocking(t *testing.T) {
	st := NewStore(t.TempDir())
	a := New(st, blockingClient(), &fakeSender{}, nil, "actor@x") // client that hangs
	done := make(chan struct{})
	go func() {
		a.Schedule(publish.BlockEnrichment{SessionID: "s1",
			Window: enrich.BlockRef{Start: 1, End: 2}}, "/tmp/x.jsonl")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Schedule blocked on the sidecar")
	}
}
```

(The three tiny client helpers — `successClient`, `pendingClient`, `blockingClient` — are defined in the same test file over the `AttributeClient` interface, each ~5 lines. The `Digester` used in tests is a fake returning one `enrich.BlockCharacterisation` matching the job's coordinates; wire it into `New` via the `facts`-style parameter or a fifth argument `dig blocks.Digester` — implementer's choice, but the production `New` takes the same `Digester` the emitter holds.)

- [ ] **Step 2: Run to verify failure** — `go test ./internal/agent/attrib/ -v` → package missing.

- [ ] **Step 3: Implement** `attrib.go`: Store (one JSON file per job, `os.WriteFile` + rename for atomicity, filename `sha256(session|start)[:16].json`; `bad/` subdir for quarantine), `Enabled` (copy `blocks.Enabled` shape), Attributor with `Schedule` (Put + select-nudge on a buffered chan), `Run` (drain on start; ticker sweep, default `KELD_ATTRIBUTION_INTERVAL` 60s), `drainOnce`: for each job — per-call 2-minute context, `client.Attribute`; on `pending`/transport-false increment Attempts and re-Put; at `MaxAttempts` → `Quarantine` + one `debuglog.Append` line; on any terminal status → re-fetch the block via the Digester, `publish.WithProjects`, `SendBlocks`, `Delete`. Client methods `PostProjects`/`Attribute` in `client.go` follow the existing `post()` helper and body-struct pattern (`client.go:137`).

- [ ] **Step 4: Emitter hook** — add to `Emitter`:

```go
// OnPublished, when non-nil, is called after a successful SendBlocks with the
// rows just published and the transcript path. The attribution path hangs off
// this seam; the emitter itself knows nothing about attribution.
OnPublished func(rows []publish.BlockEnrichment, path string)
```

Call it in the publish success path of `sweepOne`. Nil-check.

- [ ] **Step 5: Daemon wiring** (`daemon.go`, beside `startBlockEmitter`): when `attrib.Enabled(cfgValue)` — build the Store at `filepath.Join(paths.SpoolDir(), "attrib")`, the Attributor over the same sidecar client (`WithContext` per job), set `emitter.OnPublished`, `go attributor.Run(ctx, interval)`. Resolve projects at startup: `KELD_PROJECTS_FILE` wins, else `live` settings' `Projects`; on change (settings poll apply), `client.PostProjects`. When no projects are known the attributor short-circuits jobs to a single re-publish with `ProjectsSkippedNoProjects`.

- [ ] **Step 6: Run** — `go test ./... 2>&1 | tail -3` → PASS. **Commit** — `git commit -am "feat(agent): durable attribution jobs, re-publish with projects (AC-8)"`

---

### Task 8: Go — GGUF provisioning + hardware client-event

**Files:**
- Modify: `internal/agent/provision/provision.go` (sentinel parameter), `internal/agent/daemon/daemon.go` (verifier provisioning + hardware event)
- Create: `internal/agent/hardware/hardware.go`, `internal/agent/hardware/hardware_test.go`
- Test: `internal/agent/provision/gguf_test.go`

**Interfaces:**

```go
// provision
func EnsureFile(ctx context.Context, dir, sentinelName, wantSHA string, f Fetcher) error
// EnsureModel(ctx, dir, sha, f) == EnsureFile(ctx, dir, "model.safetensors", sha, f)

// hardware
type Info struct {
	CPUModel     string `json:"cpu_model"`
	LogicalCores int    `json:"logical_cores"`
	MemTotalGB   int    `json:"mem_total_gb"`
	OSVersion    string `json:"os_version"`
}
func Collect() Info // best-effort per OS; empty strings where unavailable
```

- Provisioning wiring: when the attribution gate is on and `verifier` is not opted out, `EnsureFile(ctx, ~/.keld/models/gemma-4-e2b, "model.gguf", verifierSHA, ggufFetcher)` on demand (same on-demand seam as the encoder, `daemon.go:1420`). The fetcher is `sidecar.NewHFFetcher("unsloth/gemma-4-E2B-it-GGUF", rev)` filtered to the one `Q4_K_M` file — add a `WithFiles(names ...string)` option to HFFetcher if its snapshot fetch cannot already filter (read `hf.go` first; extend, don't fork). Pin SHA-256 in a constant beside `provision.ModelSHA256`. **No download when the gate is off** (AC-10) — the EnsureFile call sits behind the gate check.
- Hardware event: at daemon startup (once, after the emitter exists): `emitter.Emit("agent.hardware", clientevents.SevInfo, map[string]any{...})` from `hardware.Collect()` (AC-12). Darwin: `sysctl -n machdep.cpu.brand_string`, `hw.memsize`, `sw_vers -productVersion`; Linux: `/proc/cpuinfo` model name, `/proc/meminfo` MemTotal, `/etc/os-release` PRETTY_NAME; Windows: leave CPUModel/OSVersion empty this iteration (envelope os/arch still stamp). Cores: `runtime.NumCPU()` everywhere.

- [ ] **Step 1: Write failing tests**

```go
// internal/agent/provision/gguf_test.go
package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

type writeFetcher struct{ name, content string }

func (w writeFetcher) Fetch(_ context.Context, dest string) error {
	return os.WriteFile(filepath.Join(dest, w.name), []byte(w.content), 0o644)
}

// AC-10: EnsureFile verifies by SHA over the named sentinel and is a no-op
// when the file already matches.
func TestEnsureFileGGUF(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gemma-4-e2b")
	content := "fake-gguf-bytes"
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])

	f := writeFetcher{"model.gguf", content}
	if err := EnsureFile(context.Background(), dir, "model.gguf", sha, f); err != nil {
		t.Fatal(err)
	}
	// second call: no re-fetch needed — corrupt-proof by replacing fetcher with a failer
	fail := failFetcher{}
	if err := EnsureFile(context.Background(), dir, "model.gguf", sha, fail); err != nil {
		t.Fatalf("EnsureFile re-fetched despite matching SHA: %v", err)
	}
	if err := EnsureFile(context.Background(), dir, "model.gguf", "0000", f); err == nil {
		t.Fatal("wrong SHA must fail, not accept")
	}
}

type failFetcher struct{}

func (failFetcher) Fetch(context.Context, string) error {
	panic("must not fetch when sentinel matches")
}
```

```go
// internal/agent/hardware/hardware_test.go
package hardware

import (
	"runtime"
	"testing"
)

// AC-12: Collect never fails and always fills the fields the host can answer.
func TestCollectBestEffort(t *testing.T) {
	got := Collect()
	if got.LogicalCores < 1 {
		t.Fatalf("cores = %d", got.LogicalCores)
	}
	if runtime.GOOS == "darwin" && (got.CPUModel == "" || got.MemTotalGB < 1) {
		t.Fatalf("darwin should resolve cpu+mem, got %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify failure** → `EnsureFile` undefined; package `hardware` missing.

- [ ] **Step 3: Implement** — refactor `EnsureModel` body into `EnsureFile(ctx, dir, sentinelName, wantSHA, f)` (the existing body already parameterizes everything except the `sentinel` const; thread it through, keep `EnsureModel` as the two-line wrapper). Implement `hardware.Collect` per-OS (exec `sysctl`/read proc files, errors → zero values, never fatal). Daemon: emit `agent.hardware` once at startup; add the gated `EnsureFile` call for the verifier weights on the encoder's on-demand seam, and pass the resolved path to the sidecar spawn env as `KELD_VERIFIER_GGUF` (same seam as `KELD_TEXTEMBED_DIR`, `textembed.py:171`).

- [ ] **Step 4: Run** — `go test ./internal/agent/provision/ ./internal/agent/hardware/ -v` → PASS; `go test ./...` → PASS.

- [ ] **Step 5: Commit** — `git commit -am "feat(agent): GGUF provisioning + agent.hardware event (AC-10, AC-12)"`

---

### Task 9: Contract test against local Atlas (golden payload)

**Files:**
- Create: `scripts/contract_test_atlas.sh`, `scripts/testdata/golden_block_with_projects.json`

**Interfaces:**
- Consumes: local Atlas (`keld-atlas`: `make dev` + `make dev-seed`), env `ATLAS_URL` (default `http://localhost:8000`), `ATLAS_INGEST_TOKEN`, `ATLAS_DB_URL` (default the compose Postgres DSN — read it from `keld-atlas/docker-compose.yml` when writing the script).
- Produces: exit 0 = AC-9 holds.

- [ ] **Step 1: Write the golden payload** — a full `BlockEnrichment` JSON: realistic span, `workstreams` with repo/branch, `projects: [{"id":"proj_pay","confidence":0.91,"source":"embedding"}]`, `projects_status: "attributed"`, `attribution` meta, `schema_version: 22`, correlation scheme `block` with id `block:<session>:<start>`. Base it on a captured real block body: run `go test ./internal/agent/publish/ -run TestBlockWire -v` marshalling from Task 2, or copy the structure from `block.go` field by field.

- [ ] **Step 2: Write the script**

```bash
#!/usr/bin/env bash
# AC-9: the golden block payload round-trips into local Atlas.
# Prereq: cd ../keld-atlas && make dev && make dev-seed  (note the token it prints)
set -euo pipefail
ATLAS_URL="${ATLAS_URL:-http://localhost:8000}"
: "${ATLAS_INGEST_TOKEN:?set ATLAS_INGEST_TOKEN from make dev-seed output}"
GOLDEN="$(dirname "$0")/testdata/golden_block_with_projects.json"

code=$(curl -s -o /tmp/atlas_block_resp.json -w '%{http_code}' \
  -X POST "$ATLAS_URL/v1/signal/blocks" \
  -H "x-keld-ingest-token: $ATLAS_INGEST_TOKEN" \
  -H "Content-Type: application/json" \
  --data "{\"blocks\": [$(cat "$GOLDEN")]}")
[ "$code" = "201" ] || { echo "FAIL: POST returned $code"; cat /tmp/atlas_block_resp.json; exit 1; }

session=$(python3 -c "import json;print(json.load(open('$GOLDEN'))['session_id'])")
stored=$(docker compose -f ../keld-atlas/docker-compose.yml exec -T db \
  psql -U postgres -d atlas -tAc \
  "select raw->'projects' from blocks where session_id='$session' order by received_at desc limit 1")
want=$(python3 -c "import json;print(json.dumps(json.load(open('$GOLDEN'))['projects'],separators=(',',':')))")
got=$(python3 -c "import json,sys;print(json.dumps(json.loads('''$stored'''),separators=(',',':')))")
[ "$got" = "$want" ] || { echo "FAIL: raw->projects mismatch"; echo "want $want"; echo "got  $got"; exit 1; }
echo "PASS: 201 + blocks.raw->'projects' round-tripped"
```

Adjust the psql service/db names to what `keld-atlas/docker-compose.yml` actually declares (read it; do not guess).

- [ ] **Step 3: Run it** — `cd ../keld-atlas && make dev && make dev-seed`, export the token, then `cd ../keld-signal && ./scripts/contract_test_atlas.sh` → `PASS`.

- [ ] **Step 4: Commit** — `git commit -am "test: golden block contract test against local Atlas (AC-9)"`

---

### Task 10: Quality eval, smoke runbook, changelog

**Files:**
- Create: `sidecar/app/evaldata/attribution/projects.json`, `.../conversations.json` (copied from `~/projects/keld/embedding-experiment/data/`), `sidecar/app/test_attribution_quality.py` (slow, opt-in), `docs/attribution-smoke.md`
- Modify: `CHANGELOG.md`, `README.md` (one paragraph under the enrichment section)

**Interfaces:**
- Consumes: everything above; real models (`KELD_TEXTEMBED_DIR` weights + `KELD_VERIFIER_GGUF`).
- Quality gate: micro-F1 ≥ 0.85 over the 100-conversation benchmark (spec §6; measured 0.929 in the experiment).

- [ ] **Step 1: Port the quality eval** — standalone script, **opt-in like the loadtests** (exits 0 with `SKIPPED: set KELD_ATTRIBUTION_EVAL=1` unless set). It loads the two JSON fixtures, maps each conversation to `texts` (user messages) + `dims` (from its metadata), runs `attribution.attribute_block` with the real encoder + verifier, computes micro-P/R/F1 exactly as `embedding-experiment/src/metrics.py` does (port `evaluate_assignments` inline, ~40 lines), asserts `f1 >= 0.85`, prints the table.

- [ ] **Step 2: Run it once with real models** — document the measured F1 and wall time in the script's docstring.

- [ ] **Step 3: Write `docs/attribution-smoke.md`** (AC-11) — the runbook, exactly these steps: 1) `cd keld-atlas && make dev && make dev-seed`, note token; 2) `keld-agent` env: `KELD_ATTRIBUTION=1`, `KELD_PROJECTS_FILE=$PWD/scripts/testdata/smoke_projects.json` (create it: 3–4 projects matching your real repos, incl. keld-signal itself), Atlas endpoint pointed at localhost, ingest token; 3) hold a real Claude Code session ≥ 25 minutes in a listed repo; 4) watch: `psql ... "select session_id, start_ts, raw->>'projects_status', raw->'projects' from blocks order by received_at desc limit 10"` — expect `pending` rows upserted to `attributed` within ~2 sweeps; 5) check the rail renders the block (`GET /v1/blocks/rail?day=...`); 6) failure triage table: which `projects_status`/`attribution` values point where.

- [ ] **Step 4: CHANGELOG** — one `### Added` entry: block attribution, gates, schema v22, hardware event. README: one paragraph + the two env vars in the existing table.

- [ ] **Step 5: Full verification** — `go test ./...`; sidecar suite loop; `./scripts/contract_test_atlas.sh`. All green.

- [ ] **Step 6: Commit** — `git commit -am "feat: attribution quality eval, smoke runbook, changelog (AC-11)"`

---

## Self-review notes

- **Spec coverage:** AC-1/2 → Task 1; AC-3/4 → Tasks 3–4, 6; AC-5/6 → Tasks 5–6; AC-7 → Task 2; AC-8 → Task 7; AC-9 → Task 9; AC-10 → Task 8; AC-11 → Task 10; AC-12 → Task 8. §2 durability (spool) → Task 7; telemetry meta → Tasks 2, 6; hardware → Task 8.
- **Known deviation from the spec:** the spec's verification column says "pytest"; this repo's law is standalone sidecar test scripts (`AGENTS.md:2052`). Tests keep the AC names and behaviors; the runner differs. Recorded here so the phase-2 reviewer does not grade the word "pytest".
- **Deliberate simplification:** `/attribute` returns `pending` on a cold encoder rather than queueing its own backlog — the daemon's sweep IS the retry loop, and the job store already persists it. No second queue.
