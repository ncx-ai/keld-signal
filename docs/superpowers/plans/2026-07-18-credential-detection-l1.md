# Credential detection — Phase 0 (measurement) + Phase 1 (deterministic layer) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the measurement substrate (a credential eval corpus + `secret_recall`/`secret_fpr` metrics) and the deterministic detection layer (vendored gitleaks regex rules + keyword-prefilter engine, unioned into the `secrets` sensitivity class), then measure the recall jump vs. the GLiNER-only baseline.

**Architecture:** A new `creddetect` package loads a vendored, embedded gitleaks ruleset (parsed with the existing `go-toml/v2`), runs a keyword pre-filter so a rule's RE2 regex only executes when its keyword is present, applies each rule's entropy floor, and returns credential spans. `SensitivityExtractor` runs it over `ctx.Text` alongside the sidecar NER and unions the spans — any credential span ⇒ `sensitivity = secrets`. A SecretBench-informed corpus + two metrics measure recall/FPR.

**Tech Stack:** Go 1.26 (`export PATH="/opt/homebrew/bin:$PATH"`); `github.com/pelletier/go-toml/v2` (already a dep); `//go:embed`; Go `regexp` (RE2); the GLiNER sidecar for the baseline eval only.

**Scope note:** This is Plan #1 of the credential-detection spec. Phases L2 (context-gated entropy), L3 (GLiNER independent recall + precision-gate), and the weekly sync are DEFERRED to a follow-up plan, authored after we measure Phase 1 — the measured recall decides how much L2/L3 is needed. Spec: `docs/superpowers/specs/2026-07-18-credential-leak-detection-design.md`.

## Global Constraints

- Go only. `export PATH="/opt/homebrew/bin:$PATH"` before any `go` command.
- Parse the vendored ruleset with `github.com/pelletier/go-toml/v2` (already a dep — add NO new dependency). Embed the ruleset with `//go:embed`.
- All gitleaks regexes are RE2 (gitleaks is Go). Any rule whose regex fails `regexp.Compile` is **skipped and counted**, never fatal.
- Vendor gitleaks' `config/gitleaks.toml` pinned to a specific gitleaks **release tag**; record the exact tag + source URL + MIT attribution in a header comment / adjacent NOTICE.
- A credential span ⇒ `sensitivity = secrets`. Never emit raw secret text — spans carry only the masked hint (reuse `Mask`).
- Measure-first, strict no-regression: keep Phase 1 only if `secret_recall` rises with `secret_fpr` flat-or-down AND every other facet (incl. non-secret sensitivity classes) stays flat vs. the Phase-0 baseline.
- Keyword pre-filter is REQUIRED (skip rules whose keywords are absent) — never an unconditional N-regex sweep per prompt.

---

### Task 1: Credential eval corpus + `secret_recall` / `secret_fpr` metrics

**Files:**
- Create: `internal/agent/enrich/eval/creds.jsonl` (embedded corpus)
- Modify: `internal/agent/enrich/eval/eval.go` (embed + loader + metrics)
- Create: `internal/agent/enrich/eval/creds_eval_test.go`
- Modify: `internal/agentcli/evalcmd.go` (report the metrics under a `--creds` flag)

**Interfaces:**
- Consumes: `GoldRow` (has `Sensitivity string`), `enrich.Run` → `Profile.Sensitivity.Value`.
- Produces: `LoadCreds() ([]GoldRow, error)`; `SecretRecall(gold, pred) float64` (over rows with gold `sensitivity=="secrets"`, fraction predicted `secrets`); `SecretFPR(gold, pred) float64` (over rows with gold `sensitivity=="none"` AND class `decoy`, fraction predicted `secrets`).

- [ ] **Step 1: Author the corpus** (`creds.jsonl`). One JSON object per line. Include (a) real credentials across formats, gold `sensitivity:"secrets"`, and (b) decoys, gold `sensitivity:"none"`, `class:"decoy"`. Use clearly-fake-but-format-valid values. Minimum set (add more per format as practical):

```json
{"text":"deploy with aws key AKIAIOSFODNN7EXAMPLE and go","sensitivity":"secrets","class":"cred"}
{"text":"here's the token ghp_16C7e42F292c6912E7710c838347Ae178B4a","sensitivity":"secrets","class":"cred"}
{"text":"use stripe sk_live_4eC39HqLyjWDarjtT1zdp7dc for billing","sensitivity":"secrets","class":"cred"}
{"text":"slack hook xoxb-2345678901-2345678901234-AbCdEfGhIjKlMnOpQrStUvWx","sensitivity":"secrets","class":"cred"}
{"text":"google api key AIzaSyDdI0hCZtE6vySjMm-WEfRq3CPzqKqqsHI here","sensitivity":"secrets","class":"cred"}
{"text":"openai key sk-proj-abc123DEF456ghi789JKL012mno345 wired up","sensitivity":"secrets","class":"cred"}
{"text":"bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U","sensitivity":"secrets","class":"cred"}
{"text":"-----BEGIN RSA PRIVATE KEY-----\\nMIIEpAIBAAKCAQEA3Tz2mr7\\n-----END RSA PRIVATE KEY-----","sensitivity":"secrets","class":"cred"}
{"text":"connect to postgres://admin:s3cr3tP@ss@db.example.com:5432/prod","sensitivity":"secrets","class":"cred"}
{"text":"the deploy commit is a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0","sensitivity":"none","class":"decoy"}
{"text":"order id 550e8400-e29b-41d4-a716-446655440000 shipped","sensitivity":"none","class":"decoy"}
{"text":"set the api key to YOUR_API_KEY before running","sensitivity":"none","class":"decoy"}
{"text":"the value was redacted to sk_live_**** in the logs","sensitivity":"none","class":"decoy"}
{"text":"base64 the payload aGVsbG8gd29ybGQgdGhpcyBpcyBmaW5l please","sensitivity":"none","class":"decoy"}
```

- [ ] **Step 2: Write the failing metric tests** (`creds_eval_test.go`)

```go
package eval

import "testing"

func TestSecretRecall(t *testing.T) {
	gold := []GoldRow{{Sensitivity: "secrets"}, {Sensitivity: "secrets"}, {Sensitivity: "none", Class: "decoy"}}
	pred := []Pred{{Sensitivity: "secrets"}, {Sensitivity: "none"}, {Sensitivity: "secrets"}}
	if got := SecretRecall(gold, pred); got != 0.5 {
		t.Fatalf("secret_recall = %.3f, want 0.5", got)
	}
}

func TestSecretFPR(t *testing.T) {
	gold := []GoldRow{{Sensitivity: "none", Class: "decoy"}, {Sensitivity: "none", Class: "decoy"}, {Sensitivity: "secrets"}}
	pred := []Pred{{Sensitivity: "secrets"}, {Sensitivity: "none"}, {Sensitivity: "secrets"}}
	if got := SecretFPR(gold, pred); got != 0.5 {
		t.Fatalf("secret_fpr = %.3f, want 0.5 (1 of 2 decoys flagged)", got)
	}
}

func TestLoadCredsParses(t *testing.T) {
	rows, err := LoadCreds()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 {
		t.Fatalf("creds corpus too small: %d", len(rows))
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run 'TestSecretRecall|TestSecretFPR|TestLoadCreds' -v`
Expected: FAIL — `undefined: SecretRecall/SecretFPR/LoadCreds`.

- [ ] **Step 4: Add the embed + loader + metrics** (`eval.go`)

Near the existing `//go:embed confound.jsonl` block add:

```go
//go:embed creds.jsonl
var credsJSONL string

// LoadCreds parses the embedded credential-detection corpus (class "cred" =
// contains a real credential; class "decoy" = high-entropy/placeholder non-secret).
func LoadCreds() ([]GoldRow, error) { return parseRows(credsJSONL) }
```

Append the metrics (near `LeakageRate`):

```go
// SecretRecall: over rows whose gold sensitivity is "secrets", the fraction
// predicted "secrets". 0 when there are none.
func SecretRecall(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var tot, hit int
	for i := 0; i < n; i++ {
		if gold[i].Sensitivity != "secrets" {
			continue
		}
		tot++
		if pred[i].Sensitivity == "secrets" {
			hit++
		}
	}
	if tot == 0 {
		return 0
	}
	return float64(hit) / float64(tot)
}

// SecretFPR: over decoy rows (class "decoy", gold sensitivity "none"), the
// fraction wrongly predicted "secrets". 0 when there are none.
func SecretFPR(gold []GoldRow, pred []Pred) float64 {
	n := len(gold)
	if len(pred) < n {
		n = len(pred)
	}
	var tot, wrong int
	for i := 0; i < n; i++ {
		if gold[i].Class != "decoy" {
			continue
		}
		tot++
		if pred[i].Sensitivity == "secrets" {
			wrong++
		}
	}
	if tot == 0 {
		return 0
	}
	return float64(wrong) / float64(tot)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/eval/ -run 'TestSecretRecall|TestSecretFPR|TestLoadCreds' -v`
Expected: PASS.

- [ ] **Step 6: Wire the corpus + metrics into `keld-agent eval`** (`evalcmd.go`)

Add a `--creds` bool flag. When set: load `eval.LoadCreds()`, run the model over them (`eval.RunModelWithContext`), and print `secret_recall=` and `secret_fpr=`. Mirror the existing `--confound` block's structure. (Do not fold creds into the default gold/confound rows.)

- [ ] **Step 7: Build + full eval-package tests**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./... && go test ./internal/agent/enrich/eval/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/enrich/eval/ internal/agentcli/evalcmd.go
git commit -m "test(eval): credential corpus + secret_recall/secret_fpr metrics

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Vendor + parse the gitleaks ruleset (`creddetect` — rules)

**Files:**
- Create: `internal/agent/enrich/creddetect/gitleaks.toml` (vendored, pinned)
- Create: `internal/agent/enrich/creddetect/rules.go`
- Create: `internal/agent/enrich/creddetect/rules_test.go`

**Interfaces:**
- Produces: `type Rule struct { ID string; Regex *regexp.Regexp; Keywords []string; Entropy float64; SecretGroup int }`; `func Rules() []Rule` (parsed once from the embedded TOML; regex-compile failures skipped, exposed via `func SkippedCount() int`).

- [ ] **Step 1: Vendor the ruleset**

Fetch gitleaks' config pinned to the latest stable release tag and save it verbatim:
```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
TAG=$(gh release view -R gitleaks/gitleaks --json tagName -q .tagName)   # record this exact tag
mkdir -p internal/agent/enrich/creddetect
curl -fsSL "https://raw.githubusercontent.com/gitleaks/gitleaks/${TAG}/config/gitleaks.toml" \
  -o internal/agent/enrich/creddetect/gitleaks.toml
echo "vendored gitleaks.toml @ ${TAG}"
head -40 internal/agent/enrich/creddetect/gitleaks.toml   # inspect the real [[rules]] schema before coding
```
Record `${TAG}` and the source URL + "MIT © gitleaks authors" in a `NOTICE` file or a header comment in `rules.go`.

- [ ] **Step 2: Write the failing test** (`rules_test.go`)

```go
package creddetect

import "testing"

func TestRulesLoad(t *testing.T) {
	r := Rules()
	if len(r) < 50 {
		t.Fatalf("expected the vendored gitleaks ruleset (>=50 rules), got %d (skipped=%d)", len(r), SkippedCount())
	}
	// every returned rule must have a compiled regex and at least one keyword-or-empty is fine.
	for _, x := range r {
		if x.Regex == nil {
			t.Fatalf("rule %q has nil regex", x.ID)
		}
	}
}
```

- [ ] **Step 3: Verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/creddetect/ -run TestRulesLoad -v`
Expected: FAIL — package/`Rules` undefined.

- [ ] **Step 4: Implement `rules.go`** (adapt the TOML struct tags to the fields actually present in the vendored file from Step 1 — gitleaks uses `[[rules]]` with `id`, `regex`, `keywords`, `entropy`, `secretGroup`)

```go
// Package creddetect detects leaked credentials in text via a vendored, embedded
// gitleaks ruleset (MIT © gitleaks authors, see NOTICE) plus a keyword pre-filter.
package creddetect

import (
	_ "embed"
	"regexp"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

//go:embed gitleaks.toml
var gitleaksTOML []byte

// Rule is one compiled credential-detection rule.
type Rule struct {
	ID          string
	Regex       *regexp.Regexp
	Keywords    []string
	Entropy     float64
	SecretGroup int
}

type tomlConfig struct {
	Rules []struct {
		ID          string   `toml:"id"`
		Regex       string   `toml:"regex"`
		Keywords    []string `toml:"keywords"`
		Entropy     float64  `toml:"entropy"`
		SecretGroup int      `toml:"secretGroup"`
	} `toml:"rules"`
}

var (
	once    sync.Once
	rules   []Rule
	skipped int
)

func load() {
	var cfg tomlConfig
	if err := toml.Unmarshal(gitleaksTOML, &cfg); err != nil {
		return // leaves rules empty; TestRulesLoad guards this
	}
	for _, r := range cfg.Rules {
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			skipped++ // RE2 incompatibility: skip, never fatal
			continue
		}
		kws := make([]string, len(r.Keywords))
		for i, k := range r.Keywords {
			kws[i] = k // keywords are already lowercase in gitleaks config
		}
		rules = append(rules, Rule{ID: r.ID, Regex: re, Keywords: kws, Entropy: r.Entropy, SecretGroup: r.SecretGroup})
	}
}

// Rules returns the parsed, compiled ruleset (built once).
func Rules() []Rule { once.Do(load); return rules }

// SkippedCount returns how many rules failed to compile as RE2.
func SkippedCount() int { once.Do(load); return skipped }
```

- [ ] **Step 5: Run to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/creddetect/ -run TestRulesLoad -v`
Expected: PASS. If it fails because the TOML field names differ, inspect the vendored file (Step 1 head) and correct the struct tags. If `SkippedCount()` is large, log which rule IDs skipped and report as a concern.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/enrich/creddetect/
git commit -m "feat(creddetect): vendor + parse gitleaks ruleset (embedded, RE2-safe)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: The detector engine (keyword prefilter → regex → entropy → spans)

**Files:**
- Create: `internal/agent/enrich/creddetect/detect.go`
- Create: `internal/agent/enrich/creddetect/detect_test.go`

**Interfaces:**
- Consumes: `Rules()` (Task 2).
- Produces: `type Span struct { RuleID string; Start, End int }`; `func Detect(text string) []Span` — keyword pre-filtered, entropy-gated, de-duplicated by [start,end).

- [ ] **Step 1: Write the failing tests** (`detect_test.go`)

```go
package creddetect

import "testing"

func TestDetectFindsKnownCreds(t *testing.T) {
	cases := []string{
		"deploy with aws key AKIAIOSFODNN7EXAMPLE and go",
		"here's the token ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"use stripe sk_live_4eC39HqLyjWDarjtT1zdp7dc for billing",
	}
	for _, c := range cases {
		if len(Detect(c)) == 0 {
			t.Errorf("expected a credential span in %q", c)
		}
	}
}

func TestDetectSkipsDecoys(t *testing.T) {
	// a git SHA and a UUID must NOT match a credential rule.
	for _, c := range []string{
		"the deploy commit is a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		"order id 550e8400-e29b-41d4-a716-446655440000 shipped",
	} {
		if s := Detect(c); len(s) != 0 {
			t.Errorf("decoy %q wrongly matched %+v", c, s)
		}
	}
}
```

- [ ] **Step 2: Verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/creddetect/ -run TestDetect -v`
Expected: FAIL — `Detect` undefined.

- [ ] **Step 3: Implement `detect.go`**

```go
package creddetect

import (
	"math"
	"strings"
)

// Span is a detected credential location (half-open [Start,End)).
type Span struct {
	RuleID string
	Start  int
	End    int
}

// Detect returns credential spans in text. A rule's regex runs only if one of
// its keywords is present (pre-filter); a match below the rule's entropy floor
// (when >0) is dropped. Overlapping spans are de-duplicated (first match wins).
func Detect(text string) []Span {
	lower := strings.ToLower(text)
	var out []Span
	for _, r := range Rules() {
		if !keywordPresent(lower, r.Keywords) {
			continue
		}
		for _, loc := range r.Regex.FindAllStringSubmatchIndex(text, -1) {
			s, e := loc[0], loc[1]
			if r.SecretGroup > 0 && 2*r.SecretGroup+1 < len(loc) && loc[2*r.SecretGroup] >= 0 {
				s, e = loc[2*r.SecretGroup], loc[2*r.SecretGroup+1]
			}
			if r.Entropy > 0 && shannon(text[s:e]) < r.Entropy {
				continue
			}
			if !overlaps(out, s, e) {
				out = append(out, Span{RuleID: r.ID, Start: s, End: e})
			}
		}
	}
	return out
}

// keywordPresent reports whether any keyword occurs in the (lowercased) text.
// Empty keyword list ⇒ the rule is unconditional (rare in gitleaks).
func keywordPresent(lower string, kws []string) bool {
	if len(kws) == 0 {
		return true
	}
	for _, k := range kws {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

func overlaps(spans []Span, s, e int) bool {
	for _, x := range spans {
		if s < x.End && x.Start < e {
			return true
		}
	}
	return false
}

// shannon returns the Shannon entropy (bits/char) of s.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/creddetect/ -run TestDetect -v`
Expected: PASS. If a known-cred case misses, check whether the rule's keyword matched and whether the entropy floor is too high for the example value; adjust the test value to a realistic higher-entropy one if needed (do NOT weaken the rule).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/creddetect/detect.go internal/agent/enrich/creddetect/detect_test.go
git commit -m "feat(creddetect): keyword-prefiltered, entropy-gated credential detector

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Integrate into `SensitivityExtractor` + measure the recall jump

**Files:**
- Modify: `internal/agent/enrich/extractors.go` (`SensitivityExtractor.Run`)
- Create: `internal/agent/enrich/extractors_creddetect_test.go`

**Interfaces:**
- Consumes: `creddetect.Detect(ctx.Text)` (Task 3); existing `Mask`, `Entity`, `sensitivityFromEntities`.
- Produces: credential spans unioned into `sensitivity_spans`; `sensitivity == "secrets"` whenever any credential span is found.

- [ ] **Step 1: Write the failing test** (`extractors_creddetect_test.go`, package `enrich`)

```go
package enrich

import "testing"

// A stub model that finds NO entities and abstains on sensitivity, so the result
// is driven purely by the deterministic credential detector.
type emptyModel struct{}

func (emptyModel) Classify(string, map[string][]string) map[string][]Ranked { return nil }
func (emptyModel) Entities(string, map[string]string) []Entity              { return nil }
func (emptyModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestSensitivityCatchesCredentialViaDetector(t *testing.T) {
	// GLiNER (stub) finds nothing; the deterministic layer must still flag secrets.
	ctx := NewJobContext("here's the token ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, emptyModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "secrets" {
		t.Fatalf("sensitivity = %q, want secrets (from deterministic detector)", got)
	}
	spans := out["sensitivity_spans"].([]Entity)
	if len(spans) == 0 {
		t.Fatal("expected a masked credential span")
	}
	for _, s := range spans {
		if s.Text != "" {
			t.Fatalf("span text must be cleared, got %q", s.Text)
		}
	}
}

// ssnModel returns an ssn entity from Extract so we can verify a credential does
// NOT downgrade a higher-severity phi classification (precedence guard).
type ssnModel struct{ emptyModel }

func (ssnModel) Extract(text string, _ map[string]string, _ map[string][]string) ExtractResult {
	i := strings.Index(text, "123-45-6789")
	if i < 0 {
		return ExtractResult{}
	}
	return ExtractResult{Entities: []Entity{{Label: "ssn", Start: i, End: i + 11, Confidence: 1}}}
}

func TestCredentialDoesNotDowngradePHI(t *testing.T) {
	ctx := NewJobContext("my ssn is 123-45-6789 and key ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, ssnModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "phi" {
		t.Fatalf("sensitivity = %q, want phi (ssn present; a credential must not downgrade it)", got)
	}
}
```

(Add `"strings"` to the test file's imports alongside `"testing"`.)

- [ ] **Step 2: Verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/ -run TestSensitivityCatchesCredentialViaDetector -v`
Expected: FAIL — sensitivity is `none` (no deterministic layer yet).

- [ ] **Step 3: Union the detector into `SensitivityExtractor.Run`** (`extractors.go`)

Insert the deterministic layer AFTER the existing sidecar span loop (the one that builds `spans` and `found` from `res.Entities`) and BEFORE the `value, conf := "none", 0.0` line. Feed each credential hit into `found` as an `api_key` entity — this lets the EXISTING `sensitivityFromEntities(found)` decide the class with its precedence ordering (phi > pci > secrets > pii), so a prompt containing BOTH an SSN and a credential correctly stays `phi` and is not downgraded to `secrets`:

```go
	// Deterministic credential layer (creddetect): union its spans and register an
	// api_key entity, so sensitivityFromEntities elevates to "secrets" via the
	// existing rule table WITHOUT overriding a higher-severity class (e.g. phi).
	for _, c := range creddetect.Detect(ctx.Text) {
		found["api_key"] = true
		spans = append(spans, Entity{
			Label:      "api_key",
			Start:      c.Start,
			End:        c.End,
			Confidence: 1.0,
			Masked:     Mask("api_key", ctx.Text[c.Start:c.End]),
		})
	}
```

Leave the existing `if hard := sensitivityFromEntities(found); hard != "" { value, conf = hard, 1.0 }` block UNCHANGED — it now sees the injected `api_key` and returns `secrets` (or a higher class if also present). Add the import `"github.com/ncx-ai/keld-signal/internal/agent/enrich/creddetect"`.

- [ ] **Step 4: Run the unit test + full enrich suite**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/...`
Expected: PASS (new test green; existing sensitivity tests unaffected — the sidecar-driven rows still classify as before, the detector only ADDS secrets hits).

- [ ] **Step 5: Build**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go build ./...`
Expected: no output.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/enrich/extractors.go internal/agent/enrich/extractors_creddetect_test.go
git commit -m "feat(enrich): union deterministic credential detector into sensitivity (secrets)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

- [ ] **Step 7: MEASURE (controller-run, live sidecar)** — Phase-1 checkpoint

Start the warm 8-thread dev sidecar (HANDOFF recipe), then capture baseline-vs-now on the creds corpus AND confirm no regression on the existing sets:

```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
go build -o /tmp/keld-agent-exp ./cmd/keld-agent
# Phase 1 (detector ON — current build):
/tmp/keld-agent-exp eval --creds                 # secret_recall / secret_fpr WITH deterministic layer
/tmp/keld-agent-exp eval --confound --context    # confirm sensitivity_recall + all facets unchanged vs v0.6.0
```
For the **baseline** (GLiNER-only), run `--creds` on a build from the pre-Task-4 commit (or temporarily stub `creddetect.Detect` to return nil) and compare. Record: `secret_recall` before/after, `secret_fpr` after, and confirmation that gold/confound facet accuracies are flat. Success = `secret_recall` materially up, `secret_fpr` low, everything else flat.

- [ ] **Step 8: Record results + next-phase note** in `docs/superpowers/HANDOFF.md`: the measured `secret_recall`/`secret_fpr`, the vendored gitleaks tag, and — based on the residual misses (which credential types the deterministic layer still misses) — what L2 (entropy) and L3 (GLiNER) must target. This measurement is the input to the Phase-2/3 plan.

---

## Notes for the implementer

- Do NOT implement L2 (context-gated entropy), L3 (GLiNER independent detection / precision-gate), or the weekly sync — those are the next plan, authored after Task 4's measurement.
- The detector maps every credential span to label `api_key` for masking purposes (reusing the existing `secrets`-triggering entity label). A future phase may add finer secret sub-labels; not now (YAGNI).
- If `SkippedCount()` (Task 2) is non-trivial, list the skipped rule IDs in your report — a cluster of skips could indicate a TOML schema field we parsed wrong, not genuine RE2 incompatibility.
