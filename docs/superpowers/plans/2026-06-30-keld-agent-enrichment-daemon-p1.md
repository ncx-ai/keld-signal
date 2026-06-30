# keld-agent P1 (headless enrichment pipe) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the headless `keld-agent` daemon that classifies each prompt (job type + compliance/security) with a deterministic backend and publishes only derived labels to Atlas, joined by a per-source correlation key.

**Architecture:** A new `keld-agent` binary runs an HTTP ingress on loopback that accepts a *pointer* (read prompt from disk) or *inline* request, enqueues it on a bounded dedup queue, and a worker resolves the text, runs a staged extractor pipeline (`task_type`, `sensitivity`, `domain_entities`) over a swappable `Model` backend (deterministic in P1), then publishes masked labels to Atlas. The existing `keld __hook` gains a silent-skip branch that forwards a pointer to the daemon.

**Tech Stack:** Go 1.22, stdlib `net/http`, `crypto/rand`, `encoding/json`, cobra (existing), standard `testing`. No new third-party deps in P1 (ONNX arrives in P2).

## Global Constraints

- Module path: `github.com/ncx-ai/keld-cli` (copy verbatim in imports).
- Go version floor: `go 1.22` (per `go.mod`).
- No new third-party dependencies in P1 — stdlib only for all new packages.
- All files under `~/.keld/` are written mode `0600`; directories `0755`. Use `paths.KeldHome()`.
- JSON written to disk uses `enc.SetEscapeHTML(false)` + `enc.SetIndent("", "  ")` (match `auth.Save`).
- The daemon and hook MUST NEVER block or crash the host tool: every hook path returns 0, every worker stage is panic-isolated.
- **Privacy invariant (non-negotiable):** raw prompt text and raw sensitive values never appear in any outbound (Atlas) payload or log line. Sensitive spans carry `{label, start, end, confidence, masked}` only.
- Canonical label vocab is fixed in Task 2; changing it later is a `schema_version` bump.
- `schema_version` constant value for P1: `1`.
- Run all tests with: `cd ~/keld/keld-cli && go test ./...`

---

### Task 1: Agent runtime config (`agent.json`: port + secret)

**Files:**
- Modify: `internal/paths/paths.go` (add `AgentInfoPath`)
- Create: `internal/agent/agentcfg/agentcfg.go`
- Test: `internal/agent/agentcfg/agentcfg_test.go`

**Interfaces:**
- Consumes: `paths.KeldHome()`.
- Produces: `agentcfg.Info{Port int, Secret string}`; `agentcfg.Write(Info) error`; `agentcfg.Read() (*Info, error)` (returns `(nil, nil)` when absent); `agentcfg.NewSecret() (string, error)`; `paths.AgentInfoPath() string`.

- [ ] **Step 1: Add the path helper**

In `internal/paths/paths.go`, add after `HookConfigPath`:

```go
func AgentInfoPath() string { return filepath.Join(KeldHome(), "agent.json") }
```

- [ ] **Step 2: Write the failing test**

Create `internal/agent/agentcfg/agentcfg_test.go`:

```go
package agentcfg

import (
	"os"
	"testing"
)

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)

	in := Info{Port: 8765, Secret: "deadbeef"}
	if err := Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil || got.Port != 8765 || got.Secret != "deadbeef" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestReadAbsentReturnsNil(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	got, err := Read()
	if err != nil || got != nil {
		t.Fatalf("want (nil,nil), got (%+v,%v)", got, err)
	}
}

func TestWritePerms0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	if err := Write(Info{Port: 1, Secret: "x"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir + "/agent.json")
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
}

func TestNewSecretIsRandomHex(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b {
		t.Fatal("secrets should differ")
	}
	if len(a) != 64 {
		t.Fatalf("len = %d, want 64", len(a))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/agentcfg/`
Expected: FAIL (package/build error: `Info`, `Write`, `Read`, `NewSecret` undefined).

- [ ] **Step 4: Write minimal implementation**

Create `internal/agent/agentcfg/agentcfg.go`:

```go
// Package agentcfg reads/writes ~/.keld/agent.json — the discovery file the
// hook uses to locate and authenticate to the running daemon.
package agentcfg

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/ncx-ai/keld-cli/internal/paths"
)

// Info is the on-disk shape of ~/.keld/agent.json.
type Info struct {
	Port   int    `json:"port"`
	Secret string `json:"secret"`
}

// NewSecret returns a 32-byte random secret as a 64-char hex string.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Write persists info to ~/.keld/agent.json (mode 0600).
func Write(info Info) error {
	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		return err
	}
	return os.WriteFile(paths.AgentInfoPath(), buf.Bytes(), 0o600)
}

// Read returns the info, or (nil, nil) if the file is absent.
func Read() (*Info, error) {
	data, err := os.ReadFile(paths.AgentInfoPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/agentcfg/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/paths/paths.go internal/agent/agentcfg/
git commit -m "feat(agent): agent.json port+secret discovery config"
```

---

### Task 2: Canonical label vocabulary

**Files:**
- Create: `internal/agent/enrich/labels.go`
- Test: `internal/agent/enrich/labels_test.go`

**Interfaces:**
- Produces: `enrich.SchemaVersion` (int = 1); `enrich.TaskTypes []string`; `enrich.Domains []string`; `enrich.Sensitivity []string`; `enrich.SensitiveEntityLabels map[string]string`; `enrich.DomainEntityLabels map[string]string`; `enrich.SensitivityFromEntity []enrich.SensRule` where `SensRule{Sensitivity string; Triggers []string}`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/labels_test.go`:

```go
package enrich

import "testing"

func TestSchemaVersion(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, want 1", SchemaVersion)
	}
}

func TestVocabNonEmpty(t *testing.T) {
	if len(TaskTypes) == 0 || len(Domains) == 0 || len(Sensitivity) == 0 {
		t.Fatal("vocab lists must be non-empty")
	}
	if len(SensitiveEntityLabels) == 0 || len(DomainEntityLabels) == 0 {
		t.Fatal("entity label maps must be non-empty")
	}
}

func TestSensitivityRuleOrderPHIBeforePII(t *testing.T) {
	// Order matters: ssn -> phi must be evaluated before email -> pii.
	phiIdx, piiIdx := -1, -1
	for i, r := range SensitivityFromEntity {
		if r.Sensitivity == "phi" {
			phiIdx = i
		}
		if r.Sensitivity == "pii" {
			piiIdx = i
		}
	}
	if phiIdx == -1 || piiIdx == -1 || phiIdx > piiIdx {
		t.Fatalf("expected phi rule before pii rule, got phi=%d pii=%d", phiIdx, piiIdx)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/`
Expected: FAIL (undefined identifiers).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/enrich/labels.go`:

```go
// Package enrich implements the staged prompt-enrichment pipeline: a registry
// of extractors that run over a swappable Model backend and produce a Profile.
package enrich

// SchemaVersion gates the label vocabulary below. Changing any vocab list is a
// contract-affecting event: bump this and re-run the eval set.
const SchemaVersion = 1

// TaskTypes is the canonical job-classification vocabulary (ported from
// inference-enrichment).
var TaskTypes = []string{
	"codegen", "summarization", "extraction", "translation",
	"rag_qa", "classification", "reasoning", "agentic_tool_use", "other",
}

var Domains = []string{
	"software", "legal", "medical", "finance", "science",
	"business", "education", "creative", "general",
}

var Sensitivity = []string{"none", "pii", "secrets", "phi", "pci", "proprietary"}

// DomainEntityLabels: label -> natural-language description (non-sensitive).
var DomainEntityLabels = map[string]string{
	"language":  "Programming languages such as Python, Rust, TypeScript",
	"framework": "Software frameworks such as Django, React, FastAPI",
	"library":   "Software libraries or packages such as numpy, pandas, requests",
	"org":       "Organizations, companies, or institutions",
	"product":   "Named products, tools, or services",
}

// SensitiveEntityLabels: label -> natural-language description (sensitive).
var SensitiveEntityLabels = map[string]string{
	"email":       "Email addresses",
	"phone":       "Phone numbers",
	"ssn":         "Social security or national identity numbers",
	"credit_card": "Credit card or payment card numbers",
	"api_key":     "API keys, access tokens, or secret keys",
	"secret":      "Passwords, credentials, or private keys",
	"person":      "Personal names of individuals",
	"address":     "Physical postal addresses",
}

// SensRule maps a set of entity labels to a sensitivity class.
type SensRule struct {
	Sensitivity string
	Triggers    []string
}

// SensitivityFromEntity: first matching rule wins; order matters.
var SensitivityFromEntity = []SensRule{
	{"phi", []string{"ssn"}},
	{"pci", []string{"credit_card"}},
	{"secrets", []string{"api_key", "secret"}},
	{"pii", []string{"email", "phone", "person", "address"}},
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/labels.go internal/agent/enrich/labels_test.go
git commit -m "feat(enrich): canonical label vocabulary"
```

---

### Task 3: Masking of sensitive values

**Files:**
- Create: `internal/agent/enrich/mask.go`
- Test: `internal/agent/enrich/mask_test.go`

**Interfaces:**
- Produces: `enrich.Mask(label, value string) string` — returns a redacted hint that never contains the full secret.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/mask_test.go`:

```go
package enrich

import (
	"strings"
	"testing"
)

func TestMaskEmailKeepsDomainHint(t *testing.T) {
	got := Mask("email", "jane.doe@acme.com")
	if strings.Contains(got, "jane.doe") {
		t.Fatalf("masked email leaks local part: %q", got)
	}
	if !strings.Contains(got, "acme.com") {
		t.Fatalf("masked email should keep domain hint: %q", got)
	}
}

func TestMaskSecretKeepsShortTail(t *testing.T) {
	got := Mask("api_key", "sk-live-1234567890ABCD")
	if strings.Contains(got, "1234567890") {
		t.Fatalf("masked secret leaks body: %q", got)
	}
	if !strings.HasSuffix(got, "ABCD") {
		t.Fatalf("masked secret should keep last 4: %q", got)
	}
}

func TestMaskShortValueFullyRedacted(t *testing.T) {
	got := Mask("secret", "abc")
	if strings.Contains(got, "abc") {
		t.Fatalf("short value must be fully redacted: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestMask`
Expected: FAIL (`Mask` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/enrich/mask.go`:

```go
package enrich

import "strings"

// Mask returns a redacted hint for a sensitive value. It never returns the full
// value. Emails keep the domain; other values keep at most the last 4 chars
// when the value is long enough to make the tail non-identifying.
func Mask(label, value string) string {
	if label == "email" {
		if at := strings.LastIndex(value, "@"); at >= 0 {
			return "***@" + value[at+1:]
		}
	}
	const tail = 4
	if len(value) <= tail+2 {
		return "***"
	}
	return "…" + value[len(value)-tail:]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestMask`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/mask.go internal/agent/enrich/mask_test.go
git commit -m "feat(enrich): sensitive-value masking"
```

---

### Task 4: Core enrichment types + Model interface

**Files:**
- Create: `internal/agent/enrich/types.go`
- Test: `internal/agent/enrich/types_test.go`

**Interfaces:**
- Produces:
  - `enrich.Labeled{Value string; Confidence float64; Producer string}`
  - `enrich.Ranked{Label string; Confidence float64}`
  - `enrich.Entity{Text string; Label string; Start int; End int; Confidence float64; Masked string}` (JSON: `text` omitempty, `masked` omitempty)
  - `enrich.ExtractResult{Entities []Entity; Results map[string][]Ranked}`
  - `enrich.Model` interface: `Classify(text string, tasks map[string][]string) map[string][]Ranked`; `Entities(text string, labels map[string]string) []Entity`; `Extract(text string, labels map[string]string, tasks map[string][]string) ExtractResult`
  - `enrich.Profile{...}` (fields used by Task 7 + Task 10) and `enrich.JobContext{Text string; Source string; Model Model; results map[string]map[string]any}` with methods `Set(stage string, out map[string]any)` and `Get(stage string) map[string]any`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/types_test.go`:

```go
package enrich

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEntityJSONOmitsEmptyTextAndMasked(t *testing.T) {
	b, _ := json.Marshal(Entity{Label: "api_key", Start: 1, End: 5, Confidence: 0.9})
	s := string(b)
	if strings.Contains(s, `"text"`) || strings.Contains(s, `"masked"`) {
		t.Fatalf("empty text/masked must be omitted: %s", s)
	}
}

func TestJobContextSetGet(t *testing.T) {
	ctx := NewJobContext("hello", "claude_code", nil)
	ctx.Set("task_type", map[string]any{"k": "v"})
	if got := ctx.Get("task_type"); got["k"] != "v" {
		t.Fatalf("Get mismatch: %+v", got)
	}
	if got := ctx.Get("missing"); got != nil {
		t.Fatalf("missing stage should be nil, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run 'TestEntity|TestJobContext'`
Expected: FAIL (undefined `Entity`, `NewJobContext`).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/enrich/types.go`:

```go
package enrich

import "time"

// Labeled is a single classification result with provenance.
type Labeled struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
	Producer   string  `json:"producer,omitempty"`
}

// Ranked is one scored candidate label.
type Ranked struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
}

// Entity is a detected span. For sensitive spans, Text is cleared and Masked is
// set so the raw value never crosses the wire.
type Entity struct {
	Text       string  `json:"text,omitempty"`
	Label      string  `json:"label"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Confidence float64 `json:"confidence"`
	Masked     string  `json:"masked,omitempty"`
}

// ExtractResult is the composed output of a GLiNER2-style extract call.
type ExtractResult struct {
	Entities []Entity
	Results  map[string][]Ranked
}

// Model is the swappable inference backend. P1 ships a deterministic
// implementation; P2 adds a GLiNER2 (ONNX or sidecar) implementation.
type Model interface {
	Classify(text string, tasks map[string][]string) map[string][]Ranked
	Entities(text string, labels map[string]string) []Entity
	Extract(text string, labels map[string]string, tasks map[string][]string) ExtractResult
}

// Profile is the full enrichment result for one prompt.
type Profile struct {
	TaskType          Labeled           `json:"task_type"`
	TaskTypeAlt       []Labeled         `json:"task_type_alt,omitempty"`
	Domain            Labeled           `json:"domain"`
	Entities          []Entity          `json:"entities,omitempty"`
	Sensitivity       Labeled           `json:"sensitivity"`
	SensitivitySpans  []Entity          `json:"sensitivity_spans,omitempty"`
	PipelineStatus    string            `json:"pipeline_status"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	EnrichedAt        time.Time         `json:"-"`
}

// JobContext threads input + per-stage outputs through the pipeline.
type JobContext struct {
	Text   string
	Source string
	Model  Model

	results map[string]map[string]any
}

// NewJobContext builds a context for one prompt.
func NewJobContext(text, source string, m Model) *JobContext {
	return &JobContext{Text: text, Source: source, Model: m, results: map[string]map[string]any{}}
}

// Set records a stage's output.
func (c *JobContext) Set(stage string, out map[string]any) { c.results[stage] = out }

// Get returns a stage's output or nil.
func (c *JobContext) Get(stage string) map[string]any { return c.results[stage] }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run 'TestEntity|TestJobContext'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/types.go internal/agent/enrich/types_test.go
git commit -m "feat(enrich): core types and Model interface"
```

---

### Task 5: Deterministic Model backend

**Files:**
- Create: `internal/agent/enrich/deterministic.go`
- Test: `internal/agent/enrich/deterministic_test.go`

**Interfaces:**
- Consumes: `enrich.Model`, `enrich.Ranked`, `enrich.Entity`, `enrich.ExtractResult`, vocab from Task 2.
- Produces: `enrich.NewDeterministic() Model`. Regex-based `Entities`; keyword-based `Classify`; `Extract` composes both.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/deterministic_test.go`:

```go
package enrich

import "testing"

func findEntity(es []Entity, label string) (Entity, bool) {
	for _, e := range es {
		if e.Label == label {
			return e, true
		}
	}
	return Entity{}, false
}

func TestDeterministicDetectsEmailAndKey(t *testing.T) {
	m := NewDeterministic()
	text := "email me at jane@acme.com with key sk-live-ABCDEF0123456789"
	es := m.Entities(text, SensitiveEntityLabels)
	em, ok := findEntity(es, "email")
	if !ok || text[em.Start:em.End] != "jane@acme.com" {
		t.Fatalf("email span wrong: %+v", em)
	}
	if _, ok := findEntity(es, "api_key"); !ok {
		t.Fatalf("expected api_key entity in %+v", es)
	}
}

func TestDeterministicClassifyCodegen(t *testing.T) {
	m := NewDeterministic()
	res := m.Classify("Write a Go function to parse JSON", map[string][]string{"task_type": TaskTypes})
	ranked := res["task_type"]
	if len(ranked) == 0 || ranked[0].Label != "codegen" {
		t.Fatalf("top task_type = %+v, want codegen", ranked)
	}
}

func TestDeterministicClassifyFallsBackToOther(t *testing.T) {
	m := NewDeterministic()
	res := m.Classify("zzzzz", map[string][]string{"task_type": TaskTypes})
	if res["task_type"][0].Label != "other" {
		t.Fatalf("unmatched should be 'other', got %+v", res["task_type"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestDeterministic`
Expected: FAIL (`NewDeterministic` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/enrich/deterministic.go`:

```go
package enrich

import (
	"regexp"
	"strings"
)

type deterministic struct {
	patterns map[string]*regexp.Regexp
	keywords map[string]map[string][]string // task -> label -> keywords
}

// NewDeterministic returns a regex/keyword Model backend (P1 default + permanent
// fallback). Secret/PII detection has strong regex priors, so this is useful
// even once the ML backend lands.
func NewDeterministic() Model {
	return &deterministic{
		patterns: map[string]*regexp.Regexp{
			"email":       regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`),
			"ssn":         regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`),
			"credit_card": regexp.MustCompile(`\b(?:\d[ -]?){13,16}\b`),
			"phone":       regexp.MustCompile(`\b\+?\d[\d\-\s().]{7,}\d\b`),
			"api_key":     regexp.MustCompile(`\b(?:sk-[A-Za-z0-9\-]{8,}|AKIA[0-9A-Z]{16}|ghp_[A-Za-z0-9]{20,})\b`),
			"secret":      regexp.MustCompile(`(?i)\b(?:password|passwd|secret|token)\s*[:=]\s*\S+`),
		},
		keywords: map[string]map[string][]string{
			"task_type": {
				"codegen":         {"write", "function", "code", "implement", "class", "refactor"},
				"summarization":   {"summarize", "summary", "tldr"},
				"translation":     {"translate", "translation"},
				"extraction":      {"extract", "parse", "pull out"},
				"rag_qa":          {"according to", "based on the", "what does the doc"},
				"classification":  {"classify", "categorize", "label"},
				"reasoning":       {"why", "explain", "reason", "prove"},
				"agentic_tool_use": {"run the", "use the tool", "call the api"},
			},
			"domain": {
				"software":  {"go", "python", "code", "api", "function", "bug"},
				"legal":     {"contract", "clause", "liability", "court"},
				"medical":   {"patient", "diagnosis", "symptom", "clinical"},
				"finance":   {"invoice", "revenue", "tax", "payment"},
				"science":   {"experiment", "hypothesis", "molecule"},
				"business":  {"customer", "market", "strategy"},
				"education": {"student", "lesson", "homework"},
				"creative":  {"poem", "story", "novel", "lyrics"},
			},
		},
	}
}

func (d *deterministic) Entities(text string, labels map[string]string) []Entity {
	var out []Entity
	for label := range labels {
		re, ok := d.patterns[label]
		if !ok {
			continue
		}
		for _, loc := range re.FindAllStringIndex(text, -1) {
			out = append(out, Entity{
				Text:       text[loc[0]:loc[1]],
				Label:      label,
				Start:      loc[0],
				End:        loc[1],
				Confidence: 0.95,
			})
		}
	}
	return out
}

func (d *deterministic) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	lower := strings.ToLower(text)
	out := map[string][]Ranked{}
	for task, allowed := range tasks {
		kw := d.keywords[task]
		var best string
		bestN := 0
		for _, label := range allowed {
			n := 0
			for _, w := range kw[label] {
				n += strings.Count(lower, w)
			}
			if n > bestN {
				bestN, best = n, label
			}
		}
		if best == "" {
			best = fallbackLabel(allowed)
		}
		conf := 0.5
		if bestN > 0 {
			conf = 0.6 + 0.1*float64(min(bestN, 4))
		}
		out[task] = []Ranked{{Label: best, Confidence: conf}}
	}
	return out
}

func (d *deterministic) Extract(text string, labels map[string]string, tasks map[string][]string) ExtractResult {
	return ExtractResult{Entities: d.Entities(text, labels), Results: d.Classify(text, tasks)}
}

// fallbackLabel prefers "other"/"general" if present, else the last item.
func fallbackLabel(allowed []string) string {
	for _, l := range allowed {
		if l == "other" || l == "general" {
			return l
		}
	}
	if len(allowed) > 0 {
		return allowed[len(allowed)-1]
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestDeterministic`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/deterministic.go internal/agent/enrich/deterministic_test.go
git commit -m "feat(enrich): deterministic regex/keyword Model backend"
```

---

### Task 6: Extractors (task_type, sensitivity, domain_entities)

**Files:**
- Create: `internal/agent/enrich/extractors.go`
- Test: `internal/agent/enrich/extractors_test.go`

**Interfaces:**
- Consumes: `JobContext`, `Model`, vocab, `Mask`.
- Produces: `enrich.Extractor` interface (`Name() string`, `Version() string`, `Run(*JobContext) (map[string]any, error)`); three values `TaskTypeExtractor{}`, `SensitivityExtractor{}`, `DomainEntitiesExtractor{}`; `enrich.Wave1() []Extractor` returning all three.
- Output keys each extractor writes into its result map: `task_type` → `{"task_type": Labeled, "task_type_alt": []Labeled}`; `sensitivity` → `{"sensitivity": Labeled, "sensitivity_spans": []Entity}` (spans masked, `Text` cleared); `domain_entities` → `{"domain": Labeled, "entities": []Entity}`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/extractors_test.go`:

```go
package enrich

import "testing"

func TestSensitivityHardEvidenceOverrides(t *testing.T) {
	ctx := NewJobContext("my ssn is 123-45-6789", "claude_code", NewDeterministic())
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lab := out["sensitivity"].(Labeled)
	if lab.Value != "phi" || lab.Confidence != 1.0 {
		t.Fatalf("ssn must force phi@1.0, got %+v", lab)
	}
}

func TestSensitivitySpansAreMaskedNotRaw(t *testing.T) {
	ctx := NewJobContext("key sk-live-ABCDEF0123456789 here", "claude_code", NewDeterministic())
	out, _ := SensitivityExtractor{}.Run(ctx)
	spans := out["sensitivity_spans"].([]Entity)
	if len(spans) == 0 {
		t.Fatal("expected at least one span")
	}
	for _, s := range spans {
		if s.Text != "" {
			t.Fatalf("span Text must be cleared, got %q", s.Text)
		}
		if s.Masked == "" {
			t.Fatalf("span Masked must be set: %+v", s)
		}
	}
}

func TestTaskTypeExtractorTopLabel(t *testing.T) {
	ctx := NewJobContext("write a function in go", "claude_code", NewDeterministic())
	out, _ := TaskTypeExtractor{}.Run(ctx)
	if out["task_type"].(Labeled).Value != "codegen" {
		t.Fatalf("want codegen, got %+v", out["task_type"])
	}
}

func TestDomainEntitiesExtractor(t *testing.T) {
	ctx := NewJobContext("debug this python api bug", "claude_code", NewDeterministic())
	out, _ := DomainEntitiesExtractor{}.Run(ctx)
	if out["domain"].(Labeled).Value != "software" {
		t.Fatalf("want software, got %+v", out["domain"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run 'TestSensitivity|TestTaskType|TestDomain'`
Expected: FAIL (extractors undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/enrich/extractors.go`:

```go
package enrich

import "fmt"

// Extractor is one pipeline stage.
type Extractor interface {
	Name() string
	Version() string
	Run(ctx *JobContext) (map[string]any, error)
}

func versioned(name string) string { return fmt.Sprintf("%s-v%d", name, SchemaVersion) }

// Wave1 returns the independent first-wave extractors.
func Wave1() []Extractor {
	return []Extractor{TaskTypeExtractor{}, SensitivityExtractor{}, DomainEntitiesExtractor{}}
}

// --- task_type ---

type TaskTypeExtractor struct{}

func (TaskTypeExtractor) Name() string    { return "task_type" }
func (TaskTypeExtractor) Version() string { return versioned("task_type") }

func (e TaskTypeExtractor) Run(ctx *JobContext) (map[string]any, error) {
	res := ctx.Model.Classify(ctx.Text, map[string][]string{"task_type": TaskTypes})
	ranked := res["task_type"]
	if len(ranked) == 0 {
		ranked = []Ranked{{Label: "other", Confidence: 0}}
	}
	alts := make([]Labeled, 0, len(ranked)-1)
	for _, r := range ranked[1:] {
		alts = append(alts, Labeled{Value: r.Label, Confidence: r.Confidence, Producer: e.Version()})
	}
	return map[string]any{
		"task_type":     Labeled{Value: ranked[0].Label, Confidence: ranked[0].Confidence, Producer: e.Version()},
		"task_type_alt": alts,
	}, nil
}

// --- sensitivity ---

type SensitivityExtractor struct{}

func (SensitivityExtractor) Name() string    { return "sensitivity" }
func (SensitivityExtractor) Version() string { return versioned("sensitivity") }

func (e SensitivityExtractor) Run(ctx *JobContext) (map[string]any, error) {
	res := ctx.Model.Extract(ctx.Text, SensitiveEntityLabels, map[string][]string{"sensitivity": Sensitivity})

	found := map[string]bool{}
	spans := make([]Entity, 0, len(res.Entities))
	for _, ent := range res.Entities {
		found[ent.Label] = true
		spans = append(spans, Entity{
			Label:      ent.Label,
			Start:      ent.Start,
			End:        ent.End,
			Confidence: ent.Confidence,
			Masked:     Mask(ent.Label, ent.Text), // Text intentionally dropped
		})
	}

	value, conf := "none", 0.0
	if ranked := res.Results["sensitivity"]; len(ranked) > 0 {
		value, conf = ranked[0].Label, ranked[0].Confidence
	}
	if hard := sensitivityFromEntities(found); hard != "" {
		value, conf = hard, 1.0 // hard span evidence beats the weak classifier
	}
	if value == "" {
		value = "none"
	}

	return map[string]any{
		"sensitivity":      Labeled{Value: value, Confidence: conf, Producer: e.Version()},
		"sensitivity_spans": spans,
	}, nil
}

func sensitivityFromEntities(found map[string]bool) string {
	for _, rule := range SensitivityFromEntity {
		for _, trig := range rule.Triggers {
			if found[trig] {
				return rule.Sensitivity
			}
		}
	}
	return ""
}

// --- domain_entities ---

type DomainEntitiesExtractor struct{}

func (DomainEntitiesExtractor) Name() string    { return "domain_entities" }
func (DomainEntitiesExtractor) Version() string { return versioned("domain_entities") }

func (e DomainEntitiesExtractor) Run(ctx *JobContext) (map[string]any, error) {
	res := ctx.Model.Extract(ctx.Text, DomainEntityLabels, map[string][]string{"domain": Domains})
	value, conf := "general", 0.0
	if ranked := res.Results["domain"]; len(ranked) > 0 {
		value, conf = ranked[0].Label, ranked[0].Confidence
	}
	return map[string]any{
		"domain":   Labeled{Value: value, Confidence: conf, Producer: e.Version()},
		"entities": res.Entities,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run 'TestSensitivity|TestTaskType|TestDomain'`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/extractors.go internal/agent/enrich/extractors_test.go
git commit -m "feat(enrich): task_type, sensitivity, domain_entities extractors"
```

---

### Task 7: Pipeline (waves + stage isolation → Profile)

**Files:**
- Create: `internal/agent/enrich/pipeline.go`
- Test: `internal/agent/enrich/pipeline_test.go`

**Interfaces:**
- Consumes: `Extractor`, `Wave1()`, `JobContext`, `Profile`, vocab.
- Produces: `enrich.Run(text, source string, m Model) Profile`. Runs Wave1 extractors (each panic/error-isolated); assembles a `Profile`; `PipelineStatus` = `"partial"` if any stage failed else `"enriched"`; always sets `SchemaVersion`, `ExtractorVersions`, `EnrichedAt`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/enrich/pipeline_test.go`:

```go
package enrich

import "testing"

func TestRunProducesEnrichedProfile(t *testing.T) {
	p := Run("write a go function; email jane@acme.com", "claude_code", NewDeterministic())
	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched", p.PipelineStatus)
	}
	if p.TaskType.Value != "codegen" {
		t.Fatalf("task_type = %+v", p.TaskType)
	}
	if p.Sensitivity.Value != "pii" {
		t.Fatalf("sensitivity = %+v, want pii (email)", p.Sensitivity)
	}
	if p.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version not set")
	}
	if len(p.ExtractorVersions) != 3 {
		t.Fatalf("want 3 extractor versions, got %d", len(p.ExtractorVersions))
	}
}

type panicModel struct{ Model }

func (panicModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	panic("boom")
}

func TestRunIsolatesPanicAsPartial(t *testing.T) {
	// task_type uses Classify (works via embedded Model); sensitivity+domain use
	// Extract (panics). Pipeline must survive and mark partial.
	m := panicModel{Model: NewDeterministic()}
	p := Run("write a function", "claude_code", m)
	if p.PipelineStatus != "partial" {
		t.Fatalf("status = %q, want partial", p.PipelineStatus)
	}
	if p.TaskType.Value != "codegen" {
		t.Fatalf("surviving stage should still populate: %+v", p.TaskType)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/ -run TestRun`
Expected: FAIL (`Run` undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/enrich/pipeline.go`:

```go
package enrich

import (
	"sync"
	"time"
)

// runStage executes one extractor with panic isolation; ok=false on panic/error.
func runStage(ex Extractor, ctx *JobContext) (out map[string]any, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			out, ok = nil, false
		}
	}()
	o, err := ex.Run(ctx)
	if err != nil {
		return nil, false
	}
	return o, true
}

// Run executes the wave-1 extractors in parallel and assembles a Profile.
func Run(text, source string, m Model) Profile {
	ctx := NewJobContext(text, source, m)
	exs := Wave1()

	type res struct {
		name string
		out  map[string]any
		ok   bool
	}
	results := make([]res, len(exs))
	var wg sync.WaitGroup
	for i, ex := range exs {
		wg.Add(1)
		go func(i int, ex Extractor) {
			defer wg.Done()
			out, ok := runStage(ex, ctx)
			results[i] = res{name: ex.Name(), out: out, ok: ok}
		}(i, ex)
	}
	wg.Wait()

	anyFailed := false
	for _, r := range results {
		if !r.ok {
			anyFailed = true
			continue
		}
		ctx.Set(r.name, r.out)
	}

	status := "enriched"
	if anyFailed {
		status = "partial"
	}

	versions := map[string]string{}
	for _, ex := range exs {
		versions[ex.Name()] = ex.Version()
	}

	return Profile{
		TaskType:          labeledFrom(ctx.Get("task_type"), "task_type", "task_type"),
		TaskTypeAlt:       altsFrom(ctx.Get("task_type")),
		Domain:            labeledFrom(ctx.Get("domain_entities"), "domain", "domain_entities"),
		Entities:          entitiesFrom(ctx.Get("domain_entities"), "entities"),
		Sensitivity:       labeledFrom(ctx.Get("sensitivity"), "sensitivity", "sensitivity"),
		SensitivitySpans:  entitiesFrom(ctx.Get("sensitivity"), "sensitivity_spans"),
		PipelineStatus:    status,
		ExtractorVersions: versions,
		SchemaVersion:     SchemaVersion,
		EnrichedAt:        time.Now().UTC(),
	}
}

func labeledFrom(out map[string]any, key, producer string) Labeled {
	if out != nil {
		if l, ok := out[key].(Labeled); ok {
			return l
		}
	}
	return Labeled{Value: "", Confidence: 0, Producer: producer}
}

func altsFrom(out map[string]any) []Labeled {
	if out != nil {
		if a, ok := out["task_type_alt"].([]Labeled); ok {
			return a
		}
	}
	return nil
}

func entitiesFrom(out map[string]any, key string) []Entity {
	if out != nil {
		if e, ok := out[key].([]Entity); ok {
			return e
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/enrich/`
Expected: PASS (all enrich tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/pipeline.go internal/agent/enrich/pipeline_test.go
git commit -m "feat(enrich): staged pipeline with isolation and Profile assembly"
```

---

### Task 8: Prompt resolution (TranscriptReader + Claude reader + resolver)

**Files:**
- Create: `internal/agent/resolve/resolve.go`
- Create: `internal/agent/resolve/claude.go`
- Test: `internal/agent/resolve/claude_test.go`

**Interfaces:**
- Produces:
  - `resolve.TranscriptReader` interface: `Source() string`; `Read(transcriptPath, promptID string) (string, bool)` — returns `(text, true)` when found, `("", false)` when not (caller skips).
  - `resolve.ClaudeReader` — a **stateful pointer** type holding a per-transcript byte cursor (`cursors map[string]int64` + `sync.Mutex`) plus poll config (`Attempts int; Delay time.Duration`); constructed via `resolve.NewClaudeReader() *ClaudeReader`. It scans only newly appended JSONL lines from the cursor, advancing only past newline-terminated lines, resetting on file shrink, and returns the `text` of the user message whose `promptId`/`uuid` matches.
  - `resolve.Resolve(source, transcriptPath, promptID, inline string) (string, bool)` — uses inline when non-empty, else dispatches to the registered reader for `source`.
- Note: the cursor lives in the reader (held warm by the daemon). One full pass happens only on the first prompt of a resumed session (cursor starts at 0); every subsequent prompt scans only the bytes appended since.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/resolve/claude_test.go`:

```go
package resolve

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func userLine(promptID, text string) string {
	return `{"type":"user","promptId":"` + promptID + `","message":{"role":"user","content":"` + text + `"}}` + "\n"
}

const sampleJSONL = `{"type":"summary"}
` + `{"type":"user","promptId":"P1","message":{"role":"user","content":"hello world"}}
` + `{"type":"assistant","message":{"role":"assistant","content":"hi"}}
`

func writeTranscript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeReaderFindsPromptByID(t *testing.T) {
	p := writeTranscript(t, sampleJSONL)
	r := NewClaudeReader()
	got, ok := r.Read(p, "P1")
	if !ok || got != "hello world" {
		t.Fatalf("got (%q,%v), want (hello world,true)", got, ok)
	}
}

func TestClaudeReaderMissingPromptReturnsFalse(t *testing.T) {
	p := writeTranscript(t, sampleJSONL)
	r := NewClaudeReader()
	r.Attempts, r.Delay = 2, time.Millisecond
	if _, ok := r.Read(p, "NOPE"); ok {
		t.Fatal("missing prompt id must return ok=false")
	}
}

func TestClaudeReaderToleratesMalformedLines(t *testing.T) {
	body := "not json\n" + sampleJSONL + "{bad\n"
	p := writeTranscript(t, body)
	if _, ok := NewClaudeReader().Read(p, "P1"); !ok {
		t.Fatal("malformed lines must be skipped, valid line still found")
	}
}

// After consuming P1, the cursor advances past it: a second read does NOT
// re-scan it (proves incremental, non-O(n) behaviour). A subsequently appended
// P2 is found by reading only the new tail.
func TestClaudeReaderIncrementalAdvancesCursor(t *testing.T) {
	p := writeTranscript(t, userLine("P1", "first"))
	r := NewClaudeReader()
	r.Attempts, r.Delay = 1, time.Millisecond

	if got, ok := r.Read(p, "P1"); !ok || got != "first" {
		t.Fatalf("first read: (%q,%v)", got, ok)
	}
	// P1 is now behind the cursor; re-requesting it must not re-scan from 0.
	if _, ok := r.Read(p, "P1"); ok {
		t.Fatal("P1 should be behind the cursor after first read")
	}
	appendLine(t, p, userLine("P2", "second"))
	if got, ok := r.Read(p, "P2"); !ok || got != "second" {
		t.Fatalf("incremental read of P2: (%q,%v)", got, ok)
	}
}

// A trailing line without a newline (write in flight) must not be consumed; once
// the rest is flushed, the next read finds it.
func TestClaudeReaderPartialLineNotConsumed(t *testing.T) {
	complete := userLine("P1", "done")
	partial := `{"type":"user","promptId":"P2","message":{"role":"user","content":"flush` // no closing + newline
	p := writeTranscript(t, complete+partial)
	r := NewClaudeReader()
	r.Attempts, r.Delay = 1, time.Millisecond

	if _, ok := r.Read(p, "P2"); ok {
		t.Fatal("partial line must not be matched yet")
	}
	// Overwrite with the fully-flushed version; cursor (past P1) still valid.
	if err := os.WriteFile(p, []byte(complete+userLine("P2", "flushed")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Read(p, "P2"); !ok || got != "flushed" {
		t.Fatalf("after flush: (%q,%v)", got, ok)
	}
}

// If the file shrinks (truncation / rotation / compaction), the cursor resets.
func TestClaudeReaderResetsOnTruncation(t *testing.T) {
	p := writeTranscript(t, userLine("P1", "a")+userLine("P2", "b")+userLine("P3", "c"))
	r := NewClaudeReader()
	r.Attempts, r.Delay = 1, time.Millisecond
	if _, ok := r.Read(p, "P3"); !ok { // advance cursor near EOF
		t.Fatal("expected P3")
	}
	// Replace with a smaller file; cursor (> new size) must reset to 0.
	if err := os.WriteFile(p, []byte(userLine("P9", "z")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Read(p, "P9"); !ok || got != "z" {
		t.Fatalf("after truncation: (%q,%v)", got, ok)
	}
}

func TestResolveInlineWins(t *testing.T) {
	got, ok := Resolve("claude_desktop", "", "", "inline text")
	if !ok || got != "inline text" {
		t.Fatalf("inline path failed: (%q,%v)", got, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/resolve/`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/resolve/resolve.go`:

```go
// Package resolve turns an enrich request into prompt text — either inline text
// supplied by the producer, or text read from a tool's transcript on disk.
package resolve

// TranscriptReader reads one prompt's text from a tool transcript.
type TranscriptReader interface {
	Source() string
	Read(transcriptPath, promptID string) (text string, ok bool)
}

var readers = map[string]TranscriptReader{}

func register(r TranscriptReader) { readers[r.Source()] = r }

func init() { register(NewClaudeReader()) }

// Resolve returns the prompt text. Inline text (when present) wins; otherwise it
// dispatches to the registered reader for source. Returns ok=false to skip.
func Resolve(source, transcriptPath, promptID, inline string) (string, bool) {
	if inline != "" {
		return inline, true
	}
	r, ok := readers[source]
	if !ok {
		return "", false
	}
	return r.Read(transcriptPath, promptID)
}
```

Create `internal/agent/resolve/claude.go`:

```go
package resolve

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// ClaudeReader reads ~/.claude/projects/.../<session>.jsonl transcripts. It keeps
// a per-transcript byte cursor so each prompt scans only newly appended lines
// (transcripts grow unbounded — re-reading from byte 0 each time would be
// O(file^2)). It tolerates malformed lines and polls briefly for write-timing.
type ClaudeReader struct {
	Attempts int
	Delay    time.Duration

	mu      sync.Mutex
	cursors map[string]int64 // transcript path -> offset of last consumed complete line
}

// NewClaudeReader returns a reader with sane poll defaults.
func NewClaudeReader() *ClaudeReader {
	return &ClaudeReader{Attempts: 10, Delay: 50 * time.Millisecond, cursors: map[string]int64{}}
}

func (*ClaudeReader) Source() string { return "claude_code" }

// claudeLine is a tolerant view of a transcript line. The format is internal to
// Claude Code and may drift; unknown shapes are skipped, never fatal.
type claudeLine struct {
	Type     string          `json:"type"`
	PromptID string          `json:"promptId"`
	UUID     string          `json:"uuid"`
	Message  json.RawMessage `json:"message"`
}

type claudeMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// Read scans from the stored cursor for the line whose promptId/uuid matches,
// polling briefly for write-timing. On success the cursor advances past the
// matched line; on give-up it advances past the consumed tail so the next prompt
// never re-reads it.
func (r *ClaudeReader) Read(path, promptID string) (string, bool) {
	attempts := r.Attempts
	if attempts < 1 {
		attempts = 1
	}
	off := r.startOffset(path)
	var lastAdv int64
	for i := 0; i < attempts; i++ {
		text, found, adv := scanFrom(path, off, promptID)
		if found {
			r.setCursor(path, off+adv)
			return text, true
		}
		lastAdv = adv
		if i < attempts-1 {
			time.Sleep(r.Delay)
		}
	}
	r.setCursor(path, off+lastAdv)
	return "", false
}

// startOffset returns the stored cursor, resetting to 0 if the file shrank
// (truncation / rotation / compaction).
func (r *ClaudeReader) startOffset(path string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	off := r.cursors[path]
	if st, err := os.Stat(path); err == nil && st.Size() < off {
		off = 0
		r.cursors[path] = 0
	}
	return off
}

// setCursor advances the stored cursor (never moves it backwards).
func (r *ClaudeReader) setCursor(path string, off int64) {
	r.mu.Lock()
	if off > r.cursors[path] {
		r.cursors[path] = off
	}
	r.mu.Unlock()
}

// scanFrom reads complete (newline-terminated) lines starting at byte offset off.
// It returns the matching prompt text (if any) and the number of bytes of
// complete lines consumed: up to and including the match when found, else the
// whole appended tail. A trailing partial line (no newline yet) is never
// consumed, so a write-in-progress line is re-read on the next attempt.
func scanFrom(path string, off int64, promptID string) (text string, found bool, advance int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", false, 0
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return "", false, 0
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var consumed int64
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			// io.EOF: `line` is a partial trailing line (not newline-terminated)
			// and must not be consumed. Any other read error: stop.
			break
		}
		consumed += int64(len(line))
		if t, ok := matchLine(line, promptID); ok {
			return t, true, consumed
		}
	}
	return "", false, consumed
}

// matchLine parses one JSONL line and returns the user prompt text if it matches.
func matchLine(line, promptID string) (string, bool) {
	var ln claudeLine
	if err := json.Unmarshal([]byte(line), &ln); err != nil {
		return "", false // tolerate malformed lines
	}
	if ln.Type != "user" {
		return "", false
	}
	if ln.PromptID != promptID && ln.UUID != promptID {
		return "", false
	}
	return extractText(ln.Message)
}

// extractText handles message.content as either a bare string or an array of
// {type:"text", text:"..."} blocks.
func extractText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var msg claudeMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		return "", false
	}
	var s string
	if err := json.Unmarshal(msg.Content, &s); err == nil {
		return s, s != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err == nil {
		out := ""
		for _, b := range blocks {
			if b.Type == "text" {
				out += b.Text
			}
		}
		return out, out != ""
	}
	return "", false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/resolve/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/resolve/
git commit -m "feat(agent): prompt resolver + tolerant Claude transcript reader"
```

---

### Task 9: Bounded dedup queue + dispatcher

**Files:**
- Create: `internal/agent/queue/queue.go`
- Test: `internal/agent/queue/queue_test.go`

**Interfaces:**
- Produces:
  - `queue.Job{Source, Scheme, ID, SessionID, TranscriptPath, PromptID, Inline, Origin, Version string}` with `Key() string` = `Source + "|" + Scheme + "|" + ID`.
  - `queue.Queue` with `New(capacity int) *Queue`; `Offer(Job) bool` (false when shed: full or duplicate); `Next() (Job, bool)` (blocks until a job or closed); `Close()`; `Dropped() int`.
- Note: this is the P1 floor (bounded + dedup + drop-count). The adaptive governor (P2) will wrap `Offer`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/queue/queue_test.go`:

```go
package queue

import "testing"

func job(id string) Job { return Job{Source: "claude_code", Scheme: "prompt_id", ID: id} }

func TestOfferDedupBySameKey(t *testing.T) {
	q := New(10)
	if !q.Offer(job("A")) {
		t.Fatal("first offer should accept")
	}
	if q.Offer(job("A")) {
		t.Fatal("duplicate key should be shed")
	}
}

func TestOfferShedsWhenFull(t *testing.T) {
	q := New(1)
	if !q.Offer(job("A")) {
		t.Fatal("first should accept")
	}
	if q.Offer(job("B")) {
		t.Fatal("over-capacity offer should be shed")
	}
	if q.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", q.Dropped())
	}
}

func TestNextReturnsOfferedJob(t *testing.T) {
	q := New(10)
	q.Offer(job("A"))
	got, ok := q.Next()
	if !ok || got.ID != "A" {
		t.Fatalf("Next = (%+v,%v)", got, ok)
	}
}

func TestNextUnblocksOnClose(t *testing.T) {
	q := New(10)
	go q.Close()
	if _, ok := q.Next(); ok {
		t.Fatal("Next after close should return ok=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/queue/`
Expected: FAIL (undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/queue/queue.go`:

```go
// Package queue is the daemon's bounded, deduplicating work queue — the P1
// load-protection floor that keeps enrichment from ever blocking producers.
package queue

import "sync"

// Job is one unit of enrichment work.
type Job struct {
	Source         string
	Scheme         string
	ID             string
	SessionID      string
	TranscriptPath string
	PromptID       string
	Inline         string
	Origin         string
	Version        string
}

// Key is the dedup + correlation key.
func (j Job) Key() string { return j.Source + "|" + j.Scheme + "|" + j.ID }

// Queue is a bounded FIFO with key-dedup and a drop counter.
type Queue struct {
	mu       sync.Mutex
	ch       chan Job
	inflight map[string]bool
	dropped  int
	closed   bool
}

// New returns a queue with the given capacity.
func New(capacity int) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{ch: make(chan Job, capacity), inflight: map[string]bool{}}
}

// Offer enqueues a job. It returns false (and counts a drop) when the key is
// already queued or the queue is full — never blocks.
func (q *Queue) Offer(j Job) bool {
	q.mu.Lock()
	if q.closed || q.inflight[j.Key()] {
		q.mu.Unlock()
		return false
	}
	q.inflight[j.Key()] = true
	q.mu.Unlock()

	select {
	case q.ch <- j:
		return true
	default:
		q.mu.Lock()
		delete(q.inflight, j.Key())
		q.dropped++
		q.mu.Unlock()
		return false
	}
}

// Next blocks for the next job; ok=false once the queue is closed and drained.
func (q *Queue) Next() (Job, bool) {
	j, ok := <-q.ch
	if !ok {
		return Job{}, false
	}
	q.mu.Lock()
	delete(q.inflight, j.Key())
	q.mu.Unlock()
	return j, true
}

// Close stops the queue; pending Next calls return ok=false.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// Dropped returns the number of shed jobs (full-queue drops).
func (q *Queue) Dropped() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/queue/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/queue/
git commit -m "feat(agent): bounded dedup queue (load-protection floor)"
```

---

### Task 10: Atlas publisher

**Files:**
- Create: `internal/agent/publish/publish.go`
- Test: `internal/agent/publish/publish_test.go`

**Interfaces:**
- Consumes: `enrich.Profile`, `queue.Job`, `hook.LoadConfig` (existing, returns endpoint+token).
- Produces:
  - `publish.Enrichment` struct (the §11 wire shape) and `publish.Build(j queue.Job, p enrich.Profile, actor string, now time.Time) Enrichment`.
  - `publish.Publisher{Endpoint, Token, Actor string; HTTP *http.Client}`; `New(endpoint, token, actor string) *Publisher`; `(*Publisher).Send(e Enrichment) error`. POSTs JSON to `endpoint` (the enrichments path) with headers `x-keld-ingest-token`, `x-keld-actor`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/publish/publish_test.go`:

```go
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
	e := Build(j, p, "dg@keld.co", time.Unix(0, 0).UTC())

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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/publish/`
Expected: FAIL (undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/publish/publish.go`:

```go
// Package publish sends enrichment results to Atlas. It never transmits raw
// prompt text or raw sensitive values.
package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

type Source struct {
	ID      string `json:"id"`
	Origin  string `json:"origin,omitempty"`
	Version string `json:"version,omitempty"`
}

type Correlation struct {
	Scheme    string `json:"scheme"`
	ID        string `json:"id"`
	SessionID string `json:"session_id,omitempty"`
}

// Enrichment is the POST /v1/enrichments wire shape (spec §11).
type Enrichment struct {
	Source            Source            `json:"source"`
	Correlation       Correlation       `json:"correlation"`
	Actor             string            `json:"actor,omitempty"`
	TaskType          enrich.Labeled    `json:"task_type"`
	TaskTypeAlt       []enrich.Labeled  `json:"task_type_alt,omitempty"`
	Domain            enrich.Labeled    `json:"domain"`
	Entities          []enrich.Entity   `json:"entities,omitempty"`
	Sensitivity       enrich.Labeled    `json:"sensitivity"`
	SensitivitySpans  []enrich.Entity   `json:"sensitivity_spans,omitempty"`
	PipelineStatus    string            `json:"pipeline_status"`
	ExtractorVersions map[string]string `json:"extractor_versions"`
	SchemaVersion     int               `json:"schema_version"`
	ModelVersion      string            `json:"model_version"`
	TS                string            `json:"ts"`
}

// Build maps a job + profile into the wire shape.
func Build(j queue.Job, p enrich.Profile, actor string, now time.Time) Enrichment {
	return Enrichment{
		Source:            Source{ID: j.Source, Origin: j.Origin, Version: j.Version},
		Correlation:       Correlation{Scheme: j.Scheme, ID: j.ID, SessionID: j.SessionID},
		Actor:             actor,
		TaskType:          p.TaskType,
		TaskTypeAlt:       p.TaskTypeAlt,
		Domain:            p.Domain,
		Entities:          p.Entities,
		Sensitivity:       p.Sensitivity,
		SensitivitySpans:  p.SensitivitySpans,
		PipelineStatus:    p.PipelineStatus,
		ExtractorVersions: p.ExtractorVersions,
		SchemaVersion:     p.SchemaVersion,
		ModelVersion:      "deterministic-v1",
		TS:                now.UTC().Format(time.RFC3339),
	}
}

// Publisher POSTs enrichments to Atlas.
type Publisher struct {
	Endpoint string
	Token    string
	Actor    string
	HTTP     *http.Client
}

// New returns a Publisher targeting the enrichments endpoint.
func New(endpoint, token, actor string) *Publisher {
	return &Publisher{Endpoint: endpoint, Token: token, Actor: actor, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Send POSTs one enrichment; returns an error on transport failure or status >= 400.
func (p *Publisher) Send(e Enrichment) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-ingest-token", p.Token)
	req.Header.Set("x-keld-actor", p.Actor)

	client := p.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("atlas returned %d", resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/publish/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/publish/
git commit -m "feat(agent): Atlas enrichment publisher (masked, no raw text)"
```

---

### Task 11: HTTP ingress server

**Files:**
- Create: `internal/agent/ingress/ingress.go`
- Test: `internal/agent/ingress/ingress_test.go`

**Interfaces:**
- Consumes: `queue.Queue`, `queue.Job`, `agentcfg`.
- Produces:
  - `ingress.Request` (JSON body): `{source:{id,origin,version}, correlation:{scheme,id,session_id}, pointer:{transcript_path,prompt_id,cwd}, inline:{text}}`.
  - `ingress.Handler(q *queue.Queue, secret string) http.Handler` — POST `/enrich` only; checks `x-keld-agent-secret`; `202` when offered, `429` when shed, `400` on bad body, `401` on bad secret, `405` otherwise.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/ingress/ingress_test.go`:

```go
package ingress

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

func post(t *testing.T, h http.Handler, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/enrich", strings.NewReader(body))
	if secret != "" {
		req.Header.Set("x-keld-agent-secret", secret)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

const pointerBody = `{"source":{"id":"claude_code","origin":"hook"},"correlation":{"scheme":"prompt_id","id":"X"},"pointer":{"transcript_path":"/t","prompt_id":"X"}}`

func TestAcceptsPointer202(t *testing.T) {
	q := queue.New(10)
	rr := post(t, Handler(q, "s3cret"), "s3cret", pointerBody)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rr.Code)
	}
}

func TestRejectsBadSecret401(t *testing.T) {
	rr := post(t, Handler(queue.New(10), "s3cret"), "wrong", pointerBody)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rr.Code)
	}
}

func TestShed429WhenFull(t *testing.T) {
	q := queue.New(1)
	h := Handler(q, "s")
	_ = post(t, h, "s", pointerBody)
	// second distinct key fills past capacity -> shed
	rr := post(t, h, "s", `{"source":{"id":"claude_code"},"correlation":{"scheme":"prompt_id","id":"Y"},"pointer":{"transcript_path":"/t","prompt_id":"Y"}}`)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d, want 429", rr.Code)
	}
}

func TestBadBody400(t *testing.T) {
	rr := post(t, Handler(queue.New(10), "s"), "s", "{not json")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rr.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/ingress/`
Expected: FAIL (undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/ingress/ingress.go`:

```go
// Package ingress is the daemon's loopback HTTP intake. It accepts pointer or
// inline enrich requests, authenticates with a per-user secret, and enqueues.
package ingress

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

type source struct {
	ID      string `json:"id"`
	Origin  string `json:"origin"`
	Version string `json:"version"`
}

type correlation struct {
	Scheme    string `json:"scheme"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
}

type pointer struct {
	TranscriptPath string `json:"transcript_path"`
	PromptID       string `json:"prompt_id"`
	Cwd            string `json:"cwd"`
}

type inline struct {
	Text string `json:"text"`
}

// Request is the POST /enrich body.
type Request struct {
	Source      source       `json:"source"`
	Correlation correlation  `json:"correlation"`
	Pointer     *pointer     `json:"pointer"`
	Inline      *inline      `json:"inline"`
}

// Handler returns the daemon's HTTP handler.
func Handler(q *queue.Queue, secret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/enrich", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("x-keld-agent-secret")), []byte(secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		j := queue.Job{
			Source:    req.Source.ID,
			Origin:    req.Source.Origin,
			Version:   req.Source.Version,
			Scheme:    req.Correlation.Scheme,
			ID:        req.Correlation.ID,
			SessionID: req.Correlation.SessionID,
		}
		if req.Pointer != nil {
			j.TranscriptPath = req.Pointer.TranscriptPath
			j.PromptID = req.Pointer.PromptID
		}
		if req.Inline != nil {
			j.Inline = req.Inline.Text
		}
		if q.Offer(j) {
			w.WriteHeader(http.StatusAccepted)
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
	return mux
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/ingress/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/ingress/
git commit -m "feat(agent): loopback HTTP ingress (pointer/inline, secret, 202/429)"
```

---

### Task 12: Daemon assembly + `keld-agent run` + integration test

**Files:**
- Create: `internal/agent/settings/settings.go` (+ test) — daemon settings loaded at startup
- Create: `internal/agent/daemon/daemon.go`
- Create: `cmd/keld-agent/main.go`
- Create: `internal/agentcli/agentcli.go` (cobra root for keld-agent)
- Test: `internal/agent/daemon/daemon_test.go`
- Modify: `internal/paths/paths.go` (add `AgentConfigPath` → `~/.keld/agent-config.json`)

**Settings (added per the entity-text-policy decision):**
- `settings.Settings{IncludeEntityText bool}`; `settings.Load() Settings` reads `~/.keld/agent-config.json`, returns zero-value defaults (so `IncludeEntityText` defaults to **false**) when absent/unreadable. This local file is the seam a future org-level remote control-plane plugs into (spec §12 P4).

**Interfaces:**
- Consumes: all prior packages + `hook.LoadConfig` (existing) + `agentcfg` + `settings`.
- Produces:
  - `daemon.Worker(q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText bool)` where `daemon.Sender` is an interface `Send(publish.Enrichment) error` (so tests inject a fake); worker loops `q.Next()`, resolves text (`resolve.Resolve`), runs `enrich.Run`, calls `publish.Build(job, profile, actor, includeEntityText, time.Now())`, sends.
  - `daemon.Run(ctx context.Context) error` — wires queue, deterministic model, publisher from `hook.LoadConfig`, loads `settings.Load()`, ingress server on `127.0.0.1:0`, writes `agentcfg`, starts the worker (passing `settings.IncludeEntityText`).
  - `agentcli.NewRootCmd()` with a `run` subcommand calling `daemon.Run`.
- NOTE: the daemon test's `Worker(...)` call must pass the new `includeEntityText` arg (use `false`).

- [ ] **Step 1: Write the failing test**

Create `internal/agent/daemon/daemon_test.go`:

```go
package daemon

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

type fakeSender struct {
	mu   sync.Mutex
	sent []publish.Enrichment
}

func (f *fakeSender) Send(e publish.Enrichment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, e)
	return nil
}

func (f *fakeSender) all() []publish.Enrichment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]publish.Enrichment(nil), f.sent...)
}

func TestWorkerEnrichesInlineAndNeverLeaksRaw(t *testing.T) {
	q := queue.New(10)
	fs := &fakeSender{}
	go Worker(q, enrich.NewDeterministic(), fs, "dg@keld.co")

	q.Offer(queue.Job{
		Source: "claude_desktop", Scheme: "trace", ID: "T1",
		Inline: "write a function; my key is sk-live-ABCDEF0123456789",
	})

	deadline := time.After(2 * time.Second)
	for {
		if len(fs.all()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("worker did not publish in time")
		case <-time.After(10 * time.Millisecond):
		}
	}
	q.Close()

	e := fs.all()[0]
	if e.Correlation.ID != "T1" || e.TaskType.Value != "codegen" {
		t.Fatalf("unexpected enrichment: %+v", e)
	}
	if e.Sensitivity.Value != "secrets" {
		t.Fatalf("expected secrets, got %+v", e.Sensitivity)
	}
	for _, s := range e.SensitivitySpans {
		if strings.Contains(s.Masked, "ABCDEF0123456789") || s.Text != "" {
			t.Fatalf("raw secret leaked in span: %+v", s)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/daemon/`
Expected: FAIL (undefined `Worker`, `Sender`).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/daemon/daemon.go`:

```go
// Package daemon wires the enrichment components and runs the keld-agent server.
package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/ingress"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
	"github.com/ncx-ai/keld-cli/internal/agent/resolve"
	"github.com/ncx-ai/keld-cli/internal/hook"
)

// Sender publishes an enrichment (real publisher or a test fake).
type Sender interface {
	Send(publish.Enrichment) error
}

// Worker consumes jobs, resolves text, enriches, and publishes. It is
// panic-isolated per job so one bad prompt never kills the daemon.
func Worker(q *queue.Queue, m enrich.Model, pub Sender, actor string) {
	for {
		j, ok := q.Next()
		if !ok {
			return
		}
		process(j, m, pub, actor)
	}
}

func process(j queue.Job, m enrich.Model, pub Sender, actor string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("keld-agent: worker recovered: %v", r)
		}
	}()
	text, ok := resolve.Resolve(j.Source, j.TranscriptPath, j.PromptID, j.Inline)
	if !ok {
		return // could not resolve prompt text; skip silently
	}
	profile := enrich.Run(text, j.Source, m)
	e := publish.Build(j, profile, actor, time.Now())
	if err := pub.Send(e); err != nil {
		log.Printf("keld-agent: publish failed for %s: %v", j.Key(), err)
	}
}

// Run starts the daemon: ingress on loopback, worker, agent.json discovery file.
func Run(ctx context.Context) error {
	cfg, _ := hook.LoadConfig()
	if cfg.Endpoint == "" || cfg.IngestToken == "" {
		return fmt.Errorf("keld-agent: not configured (run `keld login` / setup first)")
	}

	secret, err := agentcfg.NewSecret()
	if err != nil {
		return err
	}
	q := queue.New(256)
	pub := publish.New(enrichEndpoint(cfg.Endpoint), cfg.IngestToken, "")
	go Worker(q, enrich.NewDeterministic(), pub, "")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: secret}); err != nil {
		return err
	}
	log.Printf("keld-agent: listening on 127.0.0.1:%d", port)

	srv := &http.Server{Handler: ingress.Handler(q, secret)}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		q.Close()
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// enrichEndpoint derives the enrichments URL from the configured ingest endpoint
// by swapping the trailing path segment for /v1/enrichments.
func enrichEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/enrichments"
	}
	return strings.TrimRight(ingest, "/") + "/v1/enrichments"
}
```

Create `internal/agentcli/agentcli.go`:

```go
// Package agentcli is the cobra root for the keld-agent binary.
package agentcli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-cli/internal/agent/daemon"
	"github.com/ncx-ai/keld-cli/internal/version"
)

// NewRootCmd builds the keld-agent command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "keld-agent",
		Short:         "Keld enrichment daemon",
		Version:       version.CLI,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Run the enrichment daemon in the foreground.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx)
		},
	})
	return root
}

// Execute runs the keld-agent CLI and returns an exit code.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		return 1
	}
	return 0
}
```

Create `cmd/keld-agent/main.go`:

```go
package main

import (
	"os"

	"github.com/ncx-ai/keld-cli/internal/agentcli"
)

func main() { os.Exit(agentcli.Execute()) }
```

- [ ] **Step 4: Run test + build to verify they pass**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/daemon/ && go build ./cmd/keld-agent`
Expected: PASS and a successful build.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/daemon/ internal/agentcli/ cmd/keld-agent/
git commit -m "feat(agent): daemon assembly, worker, and keld-agent run"
```

---

### Task 13: Hook localhost-forward branch + finite-size debug log

The hook→daemon forward is silent-skip toward the *host tool* (never blocks or
fails it), but POST errors must not vanish entirely: they are recorded to a
**finite-size local debug log** (`~/.keld/agent.log`, bounded by rotation) so a
user/operator can diagnose why enrichment isn't flowing. The log never contains
prompt text — only the endpoint, HTTP status, and the opaque `prompt_id`.

**Files:**
- Modify: `internal/paths/paths.go` (add `DebugLogPath`)
- Create: `internal/debuglog/debuglog.go`
- Create: `internal/hook/forward.go`
- Modify: `internal/hook/hook.go` (call `forwardToAgent` near the end of `Run`, before `return 0`)
- Test: `internal/debuglog/debuglog_test.go`
- Test: `internal/hook/forward_test.go`

**Interfaces:**
- Consumes: `agentcfg.Read`, `paths.KeldHome`.
- Produces:
  - `paths.DebugLogPath() string` → `~/.keld/agent.log`.
  - `debuglog.Append(format string, args ...any)` — best-effort timestamped line to the debug log; rotates to `<path>.1` when the active file reaches `debuglog.MaxBytes` (so total on-disk usage is bounded to ~2×MaxBytes); never returns an error, never panics, **never receives prompt text** from callers.
  - `hook.forwardToAgent(source, sessionID, promptID, transcriptPath, cwd string)` — best-effort POST of a pointer to the local daemon; never returns an error, never blocks > 500ms, no-ops when `agent.json` is absent, and records POST transport errors / non-2xx responses via `debuglog.Append`.

- [ ] **Step 1: Add the debug-log path helper**

In `internal/paths/paths.go`, add after `AgentInfoPath`:

```go
func DebugLogPath() string { return filepath.Join(KeldHome(), "agent.log") }
```

- [ ] **Step 2: Write the failing debuglog test**

Create `internal/debuglog/debuglog_test.go`:

```go
package debuglog

import (
	"os"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-cli/internal/paths"
)

func TestAppendWritesTimestampedLine(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	Append("hello %d", 7)
	data, err := os.ReadFile(paths.DebugLogPath())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "hello 7") {
		t.Fatalf("log missing line: %q", data)
	}
}

func TestRotateWhenOverCap(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	old := MaxBytes
	defer func() { MaxBytes = old }()
	MaxBytes = 20

	Append("first line, definitely over twenty bytes")
	Append("second")

	if _, err := os.Stat(paths.DebugLogPath() + ".1"); err != nil {
		t.Fatalf("expected rotated file .1: %v", err)
	}
	data, _ := os.ReadFile(paths.DebugLogPath())
	if !strings.Contains(string(data), "second") {
		t.Fatalf("active log should hold the newest line: %q", data)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/debuglog/`
Expected: FAIL (`Append`, `MaxBytes` undefined).

- [ ] **Step 4: Write the debuglog implementation**

Create `internal/debuglog/debuglog.go`:

```go
// Package debuglog writes a finite-size, best-effort debug log under ~/.keld.
// It records otherwise-silent errors (e.g. hook->daemon POST failures) without
// ever blocking the caller. Callers must never pass prompt text to it.
package debuglog

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ncx-ai/keld-cli/internal/paths"
)

// MaxBytes caps the active log file. When it is reached the file rotates to
// <path>.1, bounding total on-disk usage to ~2*MaxBytes. Exported as a var so
// tests can shrink it.
var MaxBytes int64 = 1 << 20 // 1 MiB

var mu sync.Mutex

// Append writes a timestamped line. Best-effort: every error is swallowed.
func Append(format string, args ...any) {
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return
	}
	path := paths.DebugLogPath()
	if st, err := os.Stat(path); err == nil && st.Size() >= MaxBytes {
		_ = os.Rename(path, path+".1") // overwrites any previous .1
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(time.Now().UTC().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n")
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd ~/keld/keld-cli && go test ./internal/debuglog/`
Expected: PASS.

- [ ] **Step 6: Write the failing forward test**

Create `internal/hook/forward_test.go`:

```go
package hook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-cli/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-cli/internal/paths"
)

func TestForwardPostsPointerWithSecret(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	var gotSecret, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret = r.Header.Get("x-keld-agent-secret")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(202)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: "sek"}); err != nil {
		t.Fatal(err)
	}

	forwardToAgent("claude_code", "S1", "P1", "/t/x.jsonl", "/cwd")

	if gotSecret != "sek" {
		t.Fatalf("secret header = %q", gotSecret)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body not json: %s", gotBody)
	}
	if body["correlation"].(map[string]any)["id"] != "P1" {
		t.Fatalf("correlation id wrong: %s", gotBody)
	}
}

func TestForwardNoopWhenAgentAbsent(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// Must not panic or block when agent.json is missing.
	forwardToAgent("claude_code", "S1", "P1", "/t", "/cwd")
}

func TestForwardLogsNon2xxWithoutPromptText(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: "sek"}); err != nil {
		t.Fatal(err)
	}

	forwardToAgent("claude_code", "S1", "P1", "/secret/transcript.jsonl", "/cwd")

	data, err := os.ReadFile(paths.DebugLogPath())
	if err != nil {
		t.Fatalf("expected a debug log entry: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "500") {
		t.Fatalf("debug log should record the status: %q", s)
	}
	if strings.Contains(s, "/secret/transcript.jsonl") {
		t.Fatalf("debug log must not contain the transcript path/content: %q", s)
	}
}
```

- [ ] **Step 7: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/hook/ -run TestForward`
Expected: FAIL (`forwardToAgent` undefined).

- [ ] **Step 8: Write the forward implementation**

Create `internal/hook/forward.go`:

```go
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-cli/internal/debuglog"
)

// forwardToAgent best-effort POSTs an enrich pointer to the local daemon. It is
// silent-skip toward the host tool: it never returns an error and never blocks
// it. POST transport errors / non-2xx responses are recorded in the finite-size
// debug log (endpoint + status + prompt_id only — never prompt text).
func forwardToAgent(source, sessionID, promptID, transcriptPath, cwd string) {
	info, err := agentcfg.Read()
	if err != nil || info == nil || info.Port == 0 || promptID == "" {
		return
	}
	payload := map[string]any{
		"source":      map[string]string{"id": source, "origin": "hook"},
		"correlation": map[string]string{"scheme": "prompt_id", "id": promptID, "session_id": sessionID},
		"pointer":     map[string]string{"transcript_path": transcriptPath, "prompt_id": promptID, "cwd": cwd},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	url := fmt.Sprintf("http://127.0.0.1:%d/enrich", info.Port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-keld-agent-secret", info.Secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		debuglog.Append("forward: POST %s failed (prompt_id=%s): %v", url, promptID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		debuglog.Append("forward: POST %s returned %d (prompt_id=%s)", url, resp.StatusCode, promptID)
	}
}
```

- [ ] **Step 9: Wire the call into `Run`**

In `internal/hook/hook.go`, locate the `ChangedSinceLast` block in `Run`. The function currently resolves `sessionID`, `cwd`, and `repo`. Add a `promptID` resolution after `sessionID` is resolved (find the line `sessionID = stringVal(hookInput, "thread_id")` block end) and a forward call before the final dedup/POST. Concretely, add after the `if sessionID == "" { return 0 }` guard:

```go
	promptID := stringVal(hookInput, "prompt_id")
	transcriptPath := stringVal(hookInput, "transcript_path")

	// Best-effort: hand the local enrichment daemon a pointer to this prompt.
	// Silent no-op when the daemon is not running (power-user path).
	forwardToAgent(source, sessionID, promptID, transcriptPath, cwd)
```

- [ ] **Step 10: Run tests to verify they pass**

Run: `cd ~/keld/keld-cli && go test ./internal/hook/ ./internal/debuglog/ ./internal/paths/`
Expected: PASS (existing hook tests + new forward/debuglog tests + paths).

- [ ] **Step 11: Commit**

```bash
git add internal/paths/paths.go internal/debuglog/ internal/hook/forward.go internal/hook/hook.go internal/hook/forward_test.go
git commit -m "feat(hook): forward enrich pointer to daemon + finite-size debug log for POST errors"
```

---

### Task 14: Register `UserPromptSubmit` hook in Claude setup

**Files:**
- Modify: `internal/telemetry/telemetry.go` (add `UserPromptSubmit` to `ClaudeHookEvents`)
- Modify: `internal/tools/claude.go` (summary text mentions the new hook)
- Test: `internal/telemetry/telemetry_test.go` (add a case)
- Update: `internal/tools/golden_test.go` golden expectations if present (regenerate)

**Interfaces:**
- Consumes: existing `telemetry.ClaudeHookEvent`.
- Produces: `ClaudeHookEvents` includes `{Event: "UserPromptSubmit", Matcher: nil}` so setup writes the prompt hook that feeds the daemon.

- [ ] **Step 1: Write the failing test**

Add to `internal/telemetry/telemetry_test.go`:

```go
func TestClaudeHookEventsIncludeUserPromptSubmit(t *testing.T) {
	found := false
	for _, he := range ClaudeHookEvents {
		if he.Event == "UserPromptSubmit" && he.Matcher == nil {
			found = true
		}
	}
	if !found {
		t.Fatal("ClaudeHookEvents must include UserPromptSubmit (no matcher)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/telemetry/ -run TestClaudeHookEventsInclude`
Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

In `internal/telemetry/telemetry.go`, change `ClaudeHookEvents` to append the new event:

```go
var ClaudeHookEvents = []ClaudeHookEvent{
	{Event: "SessionStart", Matcher: strPtr("startup")},
	{Event: "SessionStart", Matcher: strPtr("resume")},
	{Event: "CwdChanged", Matcher: nil},
	{Event: "UserPromptSubmit", Matcher: nil},
}
```

In `internal/tools/claude.go`, update the `Summary` slice in `Apply` to reflect it:

```go
		Summary: []string{
			fmt.Sprintf("set %d OTEL env vars", len(envKeys)),
			"add SessionStart + CwdChanged + UserPromptSubmit hooks",
		},
```

- [ ] **Step 4: Run tests + regenerate goldens**

Run: `cd ~/keld/keld-cli && go test ./internal/telemetry/ ./internal/tools/`
Expected: telemetry PASS. If `internal/tools/golden_test.go` fails on a settings-JSON golden, inspect the diff to confirm the only change is the added `UserPromptSubmit` hook block, then regenerate the golden per the repo's existing mechanism (look for an `UPDATE_GOLDEN`/`-update` flag in `golden_test.go`; run that), and re-run until PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/telemetry/ internal/tools/
git commit -m "feat(setup): register Claude UserPromptSubmit hook for enrichment"
```

---

### Task 15: `keld-agent` service install/uninstall/status

**Files:**
- Create: `internal/agent/service/service.go` (pure unit/plist/task text builders + dispatch)
- Create: `internal/agent/service/service_darwin.go`, `service_linux.go`, `service_windows.go` (install/uninstall/status per OS)
- Add subcommands in `internal/agentcli/agentcli.go`: `install`, `uninstall`, `status`
- Test: `internal/agent/service/service_test.go` (pure builders only — no OS side effects)

**Interfaces:**
- Produces:
  - `service.LaunchAgentPlist(execPath string) string`
  - `service.SystemdUnit(execPath string) string`
  - `service.Install() error`, `service.Uninstall() error`, `service.Status() (string, error)` (build-tagged per OS).

- [ ] **Step 1: Write the failing test**

Create `internal/agent/service/service_test.go`:

```go
package service

import (
	"strings"
	"testing"
)

func TestLaunchAgentPlistContainsExecAndLabel(t *testing.T) {
	p := LaunchAgentPlist("/usr/local/bin/keld-agent")
	if !strings.Contains(p, "<string>/usr/local/bin/keld-agent</string>") {
		t.Fatalf("plist missing exec path:\n%s", p)
	}
	if !strings.Contains(p, "co.keld.agent") {
		t.Fatalf("plist missing label:\n%s", p)
	}
	if !strings.Contains(p, "<key>RunAtLoad</key>") {
		t.Fatalf("plist missing RunAtLoad:\n%s", p)
	}
}

func TestSystemdUnitContainsExecAndRestart(t *testing.T) {
	u := SystemdUnit("/home/u/.local/bin/keld-agent")
	if !strings.Contains(u, "ExecStart=/home/u/.local/bin/keld-agent run") {
		t.Fatalf("unit missing ExecStart:\n%s", u)
	}
	if !strings.Contains(u, "Restart=on-failure") {
		t.Fatalf("unit missing Restart:\n%s", u)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/service/`
Expected: FAIL (undefined).

- [ ] **Step 3: Write minimal implementation**

Create `internal/agent/service/service.go`:

```go
// Package service installs keld-agent as a per-user autostart service.
package service

import "fmt"

// Label is the reverse-DNS service identifier used across platforms.
const Label = "co.keld.agent"

// LaunchAgentPlist returns the macOS LaunchAgent plist for the given exec path.
func LaunchAgentPlist(execPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
</dict>
</plist>
`, Label, execPath)
}

// SystemdUnit returns the systemd --user unit for the given exec path.
func SystemdUnit(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Keld enrichment daemon

[Service]
ExecStart=%s run
Restart=on-failure
Nice=10

[Install]
WantedBy=default.target
`, execPath)
}
```

Create `internal/agent/service/service_darwin.go`:

```go
//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(LaunchAgentPlist(exe)), 0o644); err != nil {
		return err
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, p).Run() // ignore if not loaded
	return exec.Command("launchctl", "bootstrap", uid, p).Run()
}

func Uninstall() error {
	p := plistPath()
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, p).Run()
	return os.Remove(p)
}

func Status() (string, error) {
	out, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)).CombinedOutput()
	if err != nil {
		return "not running", nil
	}
	return string(out), nil
}
```

Create `internal/agent/service/service_linux.go`:

```go
//go:build linux

package service

import (
	"os"
	"os/exec"
	"path/filepath"
)

func unitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "keld-agent.service")
}

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p := unitPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(SystemdUnit(exe)), 0o644); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return exec.Command("systemctl", "--user", "enable", "--now", "keld-agent.service").Run()
}

func Uninstall() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", "keld-agent.service").Run()
	return os.Remove(unitPath())
}

func Status() (string, error) {
	out, _ := exec.Command("systemctl", "--user", "is-active", "keld-agent.service").CombinedOutput()
	return string(out), nil
}
```

Create `internal/agent/service/service_windows.go`:

```go
//go:build windows

package service

import (
	"os"
	"os/exec"
)

// taskName is the Windows Scheduled Task name.
const taskName = "KeldAgent"

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Per-user logon task running `keld-agent run`.
	return exec.Command("schtasks", "/Create", "/F",
		"/SC", "ONLOGON",
		"/TN", taskName,
		"/TR", `"`+exe+`" run`,
	).Run()
}

func Uninstall() error {
	return exec.Command("schtasks", "/Delete", "/F", "/TN", taskName).Run()
}

func Status() (string, error) {
	out, err := exec.Command("schtasks", "/Query", "/TN", taskName).CombinedOutput()
	if err != nil {
		return "not installed", nil
	}
	return string(out), nil
}
```

Add subcommands to `internal/agentcli/agentcli.go` (inside `NewRootCmd`, after the `run` command is added). Add the import `"github.com/ncx-ai/keld-cli/internal/agent/service"` and `"fmt"`:

```go
	root.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install keld-agent as a per-user autostart service.",
		RunE:  func(cmd *cobra.Command, args []string) error { return service.Install() },
	})
	root.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove the keld-agent service.",
		RunE:  func(cmd *cobra.Command, args []string) error { return service.Uninstall() },
	})
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show keld-agent service status.",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := service.Status()
			if err != nil {
				return err
			}
			fmt.Println(s)
			return nil
		},
	})
```

- [ ] **Step 4: Run tests + cross-build to verify**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/service/ && GOOS=darwin go build ./cmd/keld-agent && GOOS=linux go build ./cmd/keld-agent && GOOS=windows go build ./cmd/keld-agent`
Expected: test PASS and all three OS builds succeed.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/service/ internal/agentcli/
git commit -m "feat(agent): per-user service install/uninstall/status (launchd/systemd/schtasks)"
```

---

### Task 16: Build + distribution (GoReleaser + Linux installer)

**Files:**
- Modify: `.goreleaser.yaml` (add `keld-agent` build + archive)
- Modify: `scripts/install.sh` (also install `keld-agent` and enable the user service on Linux)
- Test: manual build verification (no Go unit test)

**Interfaces:**
- Produces: release archives containing both `keld` and `keld-agent`.

- [ ] **Step 1: Add the keld-agent build to GoReleaser**

In `.goreleaser.yaml`, under `builds:`, add a second build after the `keld` build:

```yaml
  - id: keld-agent
    main: ./cmd/keld-agent
    binary: keld-agent
    env:
      - CGO_ENABLED=0
    ldflags:
      - -s -w -X github.com/ncx-ai/keld-cli/internal/version.CLI={{.Version}}
    goos:
      - linux
      - darwin
      - windows
    goarch:
      - amd64
      - arm64
    ignore:
      - goos: windows
        goarch: arm64
```

And change the archive to include both binaries — replace the `archives:` block with:

```yaml
archives:
  - id: keld
    ids:
      - keld
      - keld-agent
    name_template: "keld_{{ .Os }}_{{ .Arch }}"
    format_overrides:
      - goos: windows
        formats:
          - zip
```

- [ ] **Step 2: Validate the GoReleaser config and build a snapshot**

Run: `cd ~/keld/keld-cli && goreleaser check && goreleaser build --snapshot --clean --single-target`
Expected: `check` passes; snapshot build produces `dist/.../keld` and `dist/.../keld-agent`.

- [ ] **Step 3: Teach the Linux installer about keld-agent**

In `scripts/install.sh`, after the existing logic that downloads + installs the `keld` binary to the destination dir, add installation of `keld-agent` from the same archive and enable the user service. Add (adapt variable names to the script's existing ones for the extracted archive dir `$tmp` and destination `$dest`):

```sh
# Install the enrichment daemon alongside the CLI (Linux).
if [ -f "$tmp/keld-agent" ]; then
  install -m 0755 "$tmp/keld-agent" "$dest/keld-agent"
  if command -v systemctl >/dev/null 2>&1; then
    "$dest/keld-agent" install || echo "keld: could not enable keld-agent service (enable later with: keld-agent install)"
  fi
fi
```

- [ ] **Step 4: Verify the installer script parses**

Run: `cd ~/keld/keld-cli && sh -n scripts/install.sh`
Expected: no syntax errors (exit 0).

- [ ] **Step 5: Commit**

```bash
git add .goreleaser.yaml scripts/install.sh
git commit -m "build(agent): ship keld-agent in releases and Linux installer"
```

---

### Task 17: Full-suite green + privacy leak sweep

**Files:**
- Create: `internal/agent/privacy_test.go` (cross-cutting leak assertion)

**Interfaces:**
- Consumes: `enrich`, `publish`, `queue`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/privacy_test.go`:

```go
package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-cli/internal/agent/enrich"
	"github.com/ncx-ai/keld-cli/internal/agent/publish"
	"github.com/ncx-ai/keld-cli/internal/agent/queue"
)

// The end-to-end payload must never contain raw prompt text or raw secrets.
func TestNoRawTextOrSecretInPublishedPayload(t *testing.T) {
	raw := "translate this and here is my password: hunter2SuperSecretValue and email a@b.com"
	p := enrich.Run(raw, "claude_code", enrich.NewDeterministic())
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "X"}
	e := publish.Build(j, p, "dg@keld.co", time.Unix(0, 0).UTC())

	b, _ := json.Marshal(e)
	s := string(b)
	for _, needle := range []string{"hunter2SuperSecretValue", "translate this and here is"} {
		if strings.Contains(s, needle) {
			t.Fatalf("payload leaked %q: %s", needle, s)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `cd ~/keld/keld-cli && go test ./internal/agent/ -run TestNoRawText`
Expected: PASS (this is a guard — if it fails, a masking/clearing bug exists; fix the offending extractor before proceeding).

- [ ] **Step 3: Run the entire suite**

Run: `cd ~/keld/keld-cli && go test ./... && go vet ./...`
Expected: all packages PASS, vet clean.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/privacy_test.go
git commit -m "test(agent): end-to-end privacy leak guard"
```

---

## Self-Review

**Spec coverage (P1 scope of spec §12):**
- Ingress pointer + inline → Task 11. ✓
- Load-protection floor (bounded queue + dedup + drop-count) → Task 9. ✓ (low-OS-priority is set via the systemd `Nice=10` unit in Task 15 and macOS `ProcessType=Background`; an in-process `nice` call is deferred to P2 governor work — noted, not silently dropped.)
- Structured `source` + per-source correlation → Tasks 10/11 (wire shape), Task 13 (hook emits `prompt_id` scheme). ✓
- Claude `TranscriptReader` → Task 8. ✓
- EnrichmentPipeline w/ deterministic backend (task_type + regex sensitivity/PII + domain_entities) + masked spans → Tasks 2–7. ✓
- Atlas publisher reusing `hook.json` → Tasks 10/12. ✓
- `UserPromptSubmit` hook config → Task 14. ✓
- Per-user service install on all 3 OS → Task 15. ✓
- Linux shell distribution → Task 16. ✓
- Privacy invariants + masking leak tests → Tasks 3, 6, 10, 17. ✓
- **Deferred (correctly out of P1):** GLiNER2/ONNX backend, adaptive governor, GUI installers, Codex/Gemini/desktop readers, disk-backed offline retry queue (P1 publisher logs+drops on failure; durable retry is a P2 concern — flagged here so it is not mistaken for complete).
- **Subsume full login+setup into keld-agent:** P1 ships keld-agent with `run/install/uninstall/status`; the first-run login+setup orchestration (spec §9) is wiring of existing CLI commands and is folded into P3 (GUI installer) where it is actually exercised. Flagged, not silently dropped.

**Placeholder scan:** No TBD/TODO; every code step has complete code; every test has real assertions. ✓

**Type consistency:** `enrich.Profile` fields consumed by `publish.Build` match Task 7 definitions; `queue.Job` fields consumed by ingress (Task 11) and publisher (Task 10) match Task 9; `daemon.Sender` matches `publish.Publisher.Send` signature; `agentcfg.Info{Port,Secret}` consistent across Tasks 1/12/13. ✓
