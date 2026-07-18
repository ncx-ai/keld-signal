# Placeholder precision-gate — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Stop placeholder/redacted values (`YOUR_API_KEY`, `<API_KEY>`, `sk_live_****`) from triggering the `secrets` sensitivity class, cutting `secret_fpr` toward 0 with no recall loss.

**Architecture:** A conservative `IsPlaceholder(text)` predicate in `creddetect`; `SensitivityExtractor.Run` filters sensitive-entity spans (GLiNER Extract results + creddetect spans) through it before they feed `found`/`spans`. Span-level, so a real secret alongside a placeholder is preserved. Measured on the creds corpus.

**Tech Stack:** Go 1.26 (`export PATH="/opt/homebrew/bin:$PATH"`, `gofmt -l` clean before commit); the warm dev sidecar for the final measurement.

**Spec:** `docs/superpowers/specs/2026-07-18-placeholder-precision-gate-design.md`.

## Global Constraints

- Go only; `gofmt -l .` empty before every commit.
- `IsPlaceholder` must be CONSERVATIVE: it must return false for every real corpus secret (`Hunter2!Prod`, `sk-live-9f8a7b6c`, `ghp_ABC123DEF456GHI789JKL0mnop`, `AKIAIOSFODNN7EXAMPLE`, etc.). A real secret misclassified as a placeholder = recall loss = failure.
- Gate is span-level: filter individual spans, never suppress the whole classification.
- Measure-first: keep only if `secret_fpr` ↓ AND `secret_recall` stays 0.917 AND gold sensitivity flat.
- Additive: no other facet changes.

---

### Task 1: `IsPlaceholder` predicate + unit tests

**Files:**
- Create: `internal/agent/enrich/creddetect/placeholder.go`
- Create: `internal/agent/enrich/creddetect/placeholder_test.go`

**Interfaces:**
- Produces: `func IsPlaceholder(s string) bool`.

- [ ] **Step 1: Write the failing tests** (`placeholder_test.go`)

```go
package creddetect

import "testing"

func TestIsPlaceholderPositives(t *testing.T) {
	for _, s := range []string{
		"YOUR_API_KEY", "<API_KEY>", "<YOUR_SECRET_HERE>", "${DATABASE_URL}",
		"{{token}}", "sk_live_****", "AKIA****************", "REPLACE_WITH_YOUR_TOKEN",
		"XXXXXXXX", "PLACEHOLDER", "changeme", "TODO", "your-token-here", "$DATABASE_URL",
	} {
		if !IsPlaceholder(s) {
			t.Errorf("IsPlaceholder(%q) = false, want true", s)
		}
	}
}

func TestIsPlaceholderNegatives_RealSecrets(t *testing.T) {
	// Every one of these is a real (fake-but-realistic) secret from the corpus —
	// a false positive here is RECALL LOSS. They MUST NOT be placeholders.
	for _, s := range []string{
		"AKIAIOSFODNN7EXAMPLE", "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"sk_live_4eC39HqLyjWDarjtT1zdp7dc", "Hunter2!Prod",
		"sk-proj-abc123DEF456ghi789JKL012mno345",
		"xoxb-2345678901-2345678901234-AbCdEfGhIjKlMnOpQrStUvWx",
		"AIzaSyDdI0hCZtE6vySjMm-WEfRq3CPzqKqqsHI",
		"Tr0ub4dor&3", "CorrectHorseBattery9!",
	} {
		if IsPlaceholder(s) {
			t.Errorf("IsPlaceholder(%q) = true, want false (real secret — would lose recall)", s)
		}
	}
}
```

- [ ] **Step 2: Verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/creddetect/ -run TestIsPlaceholder -v`
Expected: FAIL — `undefined: IsPlaceholder`.

- [ ] **Step 3: Implement `placeholder.go`**

```go
package creddetect

import (
	"regexp"
	"strings"
)

// Placeholder/template shapes that are NEVER real secrets. Conservative by
// design: a real secret must not match (that would be recall loss).
var (
	// Bracket/brace/var templates: <...>, ${...}, {{...}}, %...%.
	reTemplate = regexp.MustCompile(`^(<.*>|\$\{?[A-Za-z0-9_]+\}?|\{\{.*\}\}|%[A-Za-z0-9_]+%)$`)
	// All-caps token of only A-Z/_/digits with NO lowercase (e.g. API_KEY,
	// SECRET_HERE, REPLACE_WITH_YOUR_TOKEN, AKIA****). Underscores or mask runs ok.
	reAllCaps = regexp.MustCompile(`^[A-Z0-9_]*[A-Z_][A-Z0-9_*]*$`)
	// Runs of mask chars (>=3): ****, xxxx, XXXX.
	reMaskRun = regexp.MustCompile(`(\*{3,}|[xX]{4,}|…{3,}|•{3,})`)
	// Placeholder-ish words (case-insensitive), as whole tokens.
	placeholderWords = map[string]bool{
		"placeholder": true, "example": true, "redacted": true, "changeme": true,
		"change_me": true, "todo": true, "dummy": true, "fake": true, "sample": true,
		"your_token_here": true, "your-token-here": true, "token": true, "secret": true,
	}
	// "YOUR_"/"MY_"/"THE_" prefixes (case-insensitive).
	rePronounPrefix = regexp.MustCompile(`(?i)^(your|my|the)[_\-]`)
)

// IsPlaceholder reports whether s is a placeholder / redacted / template value
// rather than a real secret. Conservative: keys on placeholder SHAPE and the
// absence of secret-like entropy, so real credentials return false.
func IsPlaceholder(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	if reTemplate.MatchString(t) {
		return true
	}
	if reMaskRun.MatchString(t) {
		return true
	}
	if rePronounPrefix.MatchString(t) {
		return true
	}
	low := strings.ToLower(t)
	if placeholderWords[low] {
		return true
	}
	// All-caps-underscore token with no lowercase AND no long high-entropy body.
	// Real all-caps keys (AKIAIOSFODNN7EXAMPLE) are long alnum WITHOUT underscores;
	// require an underscore OR mask char to treat an all-caps token as placeholder.
	if reAllCaps.MatchString(t) && (strings.ContainsAny(t, "_*")) && !strings.ContainsRune(t, ' ') {
		return true
	}
	return false
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/creddetect/ -run TestIsPlaceholder -v`
Expected: PASS. If a real-secret negative fails (e.g. `AKIAIOSFODNN7EXAMPLE` matched), the pattern is too aggressive — tighten it (that string has no underscore/mask, so `reAllCaps`+underscore guard should already exclude it; verify). Do NOT relax a real-secret negative to make a placeholder positive pass.

- [ ] **Step 5: gofmt + commit**

```bash
gofmt -l internal/agent/enrich/creddetect/
git add internal/agent/enrich/creddetect/placeholder.go internal/agent/enrich/creddetect/placeholder_test.go
git commit -m "feat(creddetect): IsPlaceholder predicate (conservative, real-secret-safe)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Gate sensitive spans + measure

**Files:**
- Modify: `internal/agent/enrich/extractors.go` (`SensitivityExtractor.Run`)
- Create: `internal/agent/enrich/extractors_placeholder_test.go`

**Interfaces:**
- Consumes: `creddetect.IsPlaceholder` (Task 1).
- Produces: placeholder sensitive-entity spans are dropped (not in `found`, not emitted).

- [ ] **Step 1: Write the failing test** (`extractors_placeholder_test.go`, package `enrich`)

```go
package enrich

import "testing"

// phModel returns an api_key entity spanning the whole text (so we can test that a
// placeholder-valued sensitive span is gated out of the secrets classification).
type phModel struct{ emptyModel }

func (phModel) Extract(text string, _ map[string]string, tasks map[string][]string) ExtractResult {
	return ExtractResult{Entities: []Entity{{Label: "api_key", Text: text, Start: 0, End: len(text), Confidence: 1}}}
}

func TestPlaceholderSpanDoesNotTriggerSecrets(t *testing.T) {
	// A GLiNER api_key entity whose text is a placeholder must NOT yield secrets.
	ctx := NewJobContext("YOUR_API_KEY", "claude_code", Meta{}, phModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got == "secrets" {
		t.Fatalf("placeholder YOUR_API_KEY must not classify as secrets; got %s", got)
	}
	if spans := out["sensitivity_spans"].([]Entity); len(spans) != 0 {
		t.Fatalf("placeholder span must be dropped, got %+v", spans)
	}
}

func TestRealSecretStillTriggersSecrets(t *testing.T) {
	// A real-looking key must still fire (recall preserved).
	ctx := NewJobContext("ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, phModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "secrets" {
		t.Fatalf("real key must classify as secrets; got %s", got)
	}
}
```
(Reuses `emptyModel` from `extractors_creddetect_test.go`.)

- [ ] **Step 2: Verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/ -run 'TestPlaceholderSpanDoesNotTriggerSecrets|TestRealSecretStillTriggersSecrets' -v`
Expected: the placeholder test FAILS (currently yields secrets).

- [ ] **Step 3: Gate the spans** (`extractors.go`, `SensitivityExtractor.Run`)

In the GLiNER entity loop, skip placeholder-valued sensitive spans:
```go
	for _, ent := range res.Entities {
		if creddetect.IsPlaceholder(ent.Text) {
			continue // precision-gate: placeholder/redacted value, not a real secret
		}
		found[ent.Label] = true
		spans = append(spans, Entity{ ... }) // unchanged
	}
```
And in the creddetect loop (defense-in-depth):
```go
	for _, c := range creddetect.Detect(ctx.Text) {
		if creddetect.IsPlaceholder(ctx.Text[c.Start:c.End]) {
			continue
		}
		found["api_key"] = true
		spans = append(spans, Entity{ ... }) // unchanged
	}
```
(`creddetect` is already imported.)

- [ ] **Step 4: Run the unit tests + full enrich suite**

Run: `export PATH="/opt/homebrew/bin:$PATH"; go test ./internal/agent/enrich/... && go build ./... && gofmt -l internal/agent/enrich/`
Expected: PASS (new tests green; existing sensitivity tests — real ssn/email/key rows — still pass, since those values are not placeholders).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/extractors.go internal/agent/enrich/extractors_placeholder_test.go
git commit -m "feat(enrich): precision-gate placeholder values out of the secrets class

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

- [ ] **Step 6: MEASURE (controller-run, live sidecar)**

Warm 8-thread sidecar (HANDOFF recipe), then before/after on the creds corpus + no-regression:
```bash
export PATH="/opt/homebrew/bin:$PATH"; cd ~/keld-signal
go build -o /tmp/exp-ph ./cmd/keld-agent
/tmp/exp-ph eval --creds                 # secret_fpr should drop toward 0; secret_recall stays 0.917
/tmp/exp-ph eval --confound --context    # sensitivity accuracy + sensitive_recall flat; no other facet moves
```
(Baseline for comparison: pre-Task-2 build, or the recorded 0.167/0.917.) Success: `secret_fpr` materially down (ideally 0), `secret_recall` = 0.917, gold sensitivity flat.

- [ ] **Step 7: Record in HANDOFF** — secret_fpr before/after, secret_recall, which decoy rows flipped from FP→correct, confirmation of no regression. Commit.

---

## Notes for the implementer
- Do NOT touch the GLiNER-independent-recall half of L3, the gitleaks sync, or PII — out of scope.
- The conservative-predicate constraint is the crux: if tuning the predicate, a real-secret negative test failing is a HARD stop (recall loss), a placeholder positive failing is a soft miss (tune more).
