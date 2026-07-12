# keld signal — local signal service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface the local signal service (keld-agent daemon + sidecar) under the `keld signal` command group — extend `keld signal status` with service health and add `keld signal enrich` / `keld signal metrics`.

**Architecture:** Extract the client-side logic that talks to / inspects the local daemon into a new shared package `internal/localagent`, called in-process by both `keld` (`internal/cli`) and `keld-agent` (`internal/agentcli`). `keld` stays a single static Go binary; `keld-agent`'s existing commands become thin wrappers over `localagent` with no behavior change.

**Tech Stack:** Go, cobra, the existing `internal/agent/{enrich,enrich/sidecar,agentcfg,service}` packages.

## Global Constraints

- Go package directory names: short, all-lowercase, single word, no hyphens/underscores (repo convention: `agentcli`, `agentcfg`, `diffview`).
- Privacy invariant: `enrich` runs locally and prints locally; it MUST NOT publish to Atlas.
- CLI is a single static binary, no runtime deps (importing more Go packages is fine).
- Commit message trailer on every commit: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- Run `go test ./...` and `go build ./...` from the repo root `/home/dg/keld/keld-cli`.

---

## File Structure

- `internal/localagent/localagent.go` (new) — client ops: `ReadPrompt`, `ResolveModel`, `MetricsURL`, `FetchText`, `Metrics`, `RunEnrich`.
- `internal/localagent/health.go` (new) — `Health` struct + `Health()` + `/metrics` parse.
- `internal/localagent/localagent_test.go`, `internal/localagent/health_test.go` (new).
- `internal/agentcli/metrics.go`, `internal/agentcli/enrichcmd.go` (modify) — delegate to `localagent`.
- `internal/agentcli/metrics_test.go`, `internal/agentcli/enrichcmd_test.go` (delete — content moves to `localagent`).
- `internal/cli/signalagent.go` (new) — `keld signal enrich` + `keld signal metrics`.
- `internal/cli/signalagent_test.go` (new).
- `internal/cli/status.go` (modify) — append local-service section; add pure `renderLocalService`.
- `internal/cli/status_localservice_test.go` (new).
- `internal/cli/root.go` (modify) — register the two new signal subcommands.

---

## Task 1: Extract `internal/localagent` and delegate keld-agent commands

Moves the four helpers currently in `internal/agentcli` into a new shared package (exported), adds DRY convenience wrappers `Metrics` and `RunEnrich`, and rewires the keld-agent `metrics`/`enrich` commands to call them. Tree stays green and non-duplicated at task end.

**Files:**
- Create: `internal/localagent/localagent.go`
- Create: `internal/localagent/localagent_test.go`
- Modify: `internal/agentcli/metrics.go`
- Modify: `internal/agentcli/enrichcmd.go`
- Delete: `internal/agentcli/metrics_test.go`, `internal/agentcli/enrichcmd_test.go`

**Interfaces:**
- Produces:
  - `func ReadPrompt(args []string, stdin io.Reader) (string, error)`
  - `func ResolveModel(info *agentcfg.Info, forceDeterministic bool) (enrich.Model, string)`
  - `func MetricsURL(info *agentcfg.Info) (string, error)`
  - `func FetchText(url string) (string, error)`
  - `func Metrics(info *agentcfg.Info) (string, error)`
  - `func RunEnrich(text, source string, info *agentcfg.Info, forceDeterministic bool) (profileJSON string, note string, err error)`

- [ ] **Step 1: Write the failing tests for the moved helpers**

Create `internal/localagent/localagent_test.go`:

```go
package localagent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

func TestReadPromptFromArgs(t *testing.T) {
	got, err := ReadPrompt([]string{"fix", "the", "bug"}, strings.NewReader(""))
	if err != nil || got != "fix the bug" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestReadPromptFromStdin(t *testing.T) {
	got, err := ReadPrompt(nil, strings.NewReader("  refactor the parser\n"))
	if err != nil || got != "refactor the parser" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestReadPromptEmptyErrors(t *testing.T) {
	if _, err := ReadPrompt(nil, strings.NewReader("  \n")); err == nil {
		t.Fatal("want error on empty prompt")
	}
}

func TestMetricsURL(t *testing.T) {
	cases := []struct {
		name, want, wantErr string
		info                *agentcfg.Info
	}{
		{"daemon down", "", "not running", nil},
		{"port zero", "", "not running", &agentcfg.Info{}},
		{"sidecar absent", "", "sidecar", &agentcfg.Info{Port: 8765}},
		{"ok", "http://127.0.0.1:40313/metrics", "", &agentcfg.Info{Port: 8765, SidecarPort: 40313}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := MetricsURL(c.info)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q, err %v", got, err)
			}
		})
	}
}

func TestFetchText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	got, err := FetchText(srv.URL)
	if err != nil || got != `{"ok":true}` {
		t.Fatalf("got %q, err %v", got, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if _, err := FetchText(bad.URL); err == nil {
		t.Fatal("want error on 503")
	}
}

func TestResolveModel(t *testing.T) {
	m, note := ResolveModel(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, false)
	if m == nil || !strings.Contains(note, "sidecar") || !strings.Contains(note, "40313") {
		t.Fatalf("sidecar path: model=%v note=%q", m, note)
	}
	m, note = ResolveModel(&agentcfg.Info{Port: 8765}, false)
	if m == nil || !strings.Contains(note, "deterministic") {
		t.Fatalf("fallback path: note=%q", note)
	}
	m, note = ResolveModel(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, true)
	if m == nil || strings.Contains(note, "sidecar") {
		t.Fatalf("forced path should not use sidecar: note=%q", note)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/localagent/ 2>&1 | head`
Expected: FAIL — `build failed`, undefined `ReadPrompt`/`MetricsURL`/... (package has no non-test files yet).

- [ ] **Step 3: Create the package with the moved helpers + wrappers**

Create `internal/localagent/localagent.go`:

```go
// Package localagent is the client-side access layer for the local keld-agent
// daemon + GLiNER2 sidecar. Both keld (internal/cli) and keld-agent
// (internal/agentcli) call it in-process to run test enrichments, read the
// sidecar's /metrics, and summarize local-service health.
package localagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// ReadPrompt returns the prompt from args (joined) or, if none, from stdin.
func ReadPrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return "", errors.New("no prompt: pass text as an argument or on stdin")
	}
	return text, nil
}

// ResolveModel picks the enrichment backend and a human note naming it. It uses
// the running sidecar (via agent.json) when available, else the deterministic
// backend. forceDeterministic always picks deterministic.
func ResolveModel(info *agentcfg.Info, forceDeterministic bool) (enrich.Model, string) {
	if !forceDeterministic && info != nil && info.SidecarPort != 0 {
		url := fmt.Sprintf("http://127.0.0.1:%d", info.SidecarPort)
		return sidecar.New(url, 30*time.Second), "using live GLiNER2 sidecar at " + url
	}
	if forceDeterministic {
		return enrich.NewDeterministic(), "using deterministic backend (--deterministic)"
	}
	return enrich.NewDeterministic(), "sidecar not running; using deterministic backend"
}

// MetricsURL resolves the running sidecar's /metrics URL from agent.json.
func MetricsURL(info *agentcfg.Info) (string, error) {
	if info == nil || info.Port == 0 {
		return "", errors.New("keld-agent is not running")
	}
	if info.SidecarPort == 0 {
		return "", errors.New("sidecar is not running (ML disabled or deterministic backend in use)")
	}
	return fmt.Sprintf("http://127.0.0.1:%d/metrics", info.SidecarPort), nil
}

// FetchText GETs url and returns the body, erroring on a non-200 response.
func FetchText(url string) (string, error) {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sidecar returned HTTP %d", resp.StatusCode)
	}
	return string(b), nil
}

// Metrics reads agent.json and returns the sidecar's /metrics body.
func Metrics(info *agentcfg.Info) (string, error) {
	url, err := MetricsURL(info)
	if err != nil {
		return "", err
	}
	return FetchText(url)
}

// RunEnrich runs the enrichment pipeline on text and returns the profile as
// indented JSON plus a note naming the backend. Local only; never publishes.
func RunEnrich(text, source string, info *agentcfg.Info, forceDeterministic bool) (string, string, error) {
	model, note := ResolveModel(info, forceDeterministic)
	cwd, _ := os.Getwd()
	meta := enrich.Meta{Repo: cwd, Tool: source}
	profile := enrich.Run(text, source, meta, model)
	b, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return "", note, err
	}
	return string(b), note, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/localagent/ -v 2>&1 | tail -20`
Expected: PASS for all `TestReadPrompt*`, `TestMetricsURL`, `TestFetchText`, `TestResolveModel`.

- [ ] **Step 5: Delegate the keld-agent commands and delete the moved tests**

Replace `internal/agentcli/metrics.go` entirely with:

```go
package agentcli

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/spf13/cobra"
)

// newMetricsCmd builds `keld-agent metrics`: print the running GLiNER2
// sidecar's /metrics JSON to stdout.
func newMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Print the running GLiNER2 sidecar's /metrics JSON to stdout.",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := agentcfg.Read()
			if err != nil {
				return err
			}
			body, err := localagent.Metrics(info)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), body)
			return nil
		},
	}
}
```

Replace `internal/agentcli/enrichcmd.go` entirely with:

```go
package agentcli

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/spf13/cobra"
)

// newEnrichCmd builds `keld-agent enrich`: run the enrichment pipeline on a
// test prompt and print the profile JSON. Local only — never published.
func newEnrichCmd() *cobra.Command {
	var forceDeterministic bool
	var source string
	cmd := &cobra.Command{
		Use:   "enrich [prompt]",
		Short: "Run enrichment on a test prompt and print the profile JSON (local; not published).",
		Long: "Run the enrichment pipeline on a test prompt and print the resulting " +
			"profile as JSON, for quick sanity checking and debugging.\n\n" +
			"The prompt is taken from the arguments, or from stdin if none are given. " +
			"Uses the running GLiNER2 sidecar when available, otherwise the deterministic " +
			"backend. The prompt is processed locally and never published to Atlas.\n\n" +
			"Tip: single-quote the prompt (or pipe it via stdin) so your shell does not " +
			"interpret backticks or $(...) as command substitution and splice command " +
			"output into the text being enriched.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := localagent.ReadPrompt(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			info, _ := agentcfg.Read()
			out, note, err := localagent.RunEnrich(text, source, info, forceDeterministic)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "keld-agent enrich: "+note)
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&forceDeterministic, "deterministic", false,
		"Force the deterministic backend instead of the sidecar.")
	cmd.Flags().StringVar(&source, "source", "claude_code",
		"Tool source to attribute the prompt to (e.g. claude_code, codex).")
	return cmd
}
```

Delete the now-redundant helper tests:

```bash
rm internal/agentcli/metrics_test.go internal/agentcli/enrichcmd_test.go
```

- [ ] **Step 6: Verify build + affected tests pass**

Run: `go build ./... && go test ./internal/localagent/ ./internal/agentcli/ 2>&1 | tail`
Expected: build clean; both packages `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/localagent/ internal/agentcli/metrics.go internal/agentcli/enrichcmd.go
git add -u internal/agentcli/
git commit -m "refactor: extract internal/localagent; keld-agent cmds delegate to it

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: Add `localagent.Health`

Adds the local-service health summary (service state + parsed `/metrics`), with dependencies injected so it is unit-testable without a running daemon.

**Files:**
- Create: `internal/localagent/health.go`
- Create: `internal/localagent/health_test.go`

**Interfaces:**
- Consumes: `agentcfg.Info` (from Task 1's imports).
- Produces:
  - `type Health struct { Service string; DaemonUp bool; Backend string; ModelState string; RSSMB float64; ModelCostMB float64; MetricsOK bool }`
  - `func Health(info *agentcfg.Info, statusFn func() (string, error), fetchFn func(string) (string, error)) Health`

- [ ] **Step 1: Write the failing test**

Create `internal/localagent/health_test.go`:

```go
package localagent

import (
	"errors"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

func okStatus() (string, error) { return "active", nil }

func TestHealthDaemonDown(t *testing.T) {
	h := Health(nil, okStatus, func(string) (string, error) { return "", nil })
	if h.Service != "active" || h.DaemonUp {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthDeterministicWhenNoSidecarPort(t *testing.T) {
	h := Health(&agentcfg.Info{Port: 8765}, okStatus, func(string) (string, error) {
		t.Fatal("fetch should not be called without a sidecar port")
		return "", nil
	})
	if !h.DaemonUp || h.Backend != "deterministic (ML disabled)" || h.MetricsOK {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthSidecarUnreachable(t *testing.T) {
	h := Health(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, okStatus,
		func(string) (string, error) { return "", errors.New("connection refused") })
	if h.Backend != "sidecar unreachable" || h.MetricsOK {
		t.Fatalf("got %+v", h)
	}
}

func TestHealthSidecarLoaded(t *testing.T) {
	body := `{"model_state":"loaded","memory":{"rss_mb":2743.1,"model_cost_mb":2650.1}}`
	h := Health(&agentcfg.Info{Port: 8765, SidecarPort: 40313}, okStatus,
		func(string) (string, error) { return body, nil })
	if !h.MetricsOK || h.Backend != "GLiNER2 sidecar" || h.ModelState != "loaded" ||
		h.RSSMB != 2743.1 || h.ModelCostMB != 2650.1 {
		t.Fatalf("got %+v", h)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/localagent/ -run TestHealth 2>&1 | head`
Expected: FAIL — undefined `Health`.

- [ ] **Step 3: Implement**

Create `internal/localagent/health.go`:

```go
package localagent

import (
	"encoding/json"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

// Health is a snapshot of the local signal service for the status view.
type Health struct {
	Service     string  // OS service state ("active", "not running", …)
	DaemonUp    bool    // agent.json present with a port
	Backend     string  // "GLiNER2 sidecar" | "deterministic (ML disabled)" | "sidecar unreachable"
	ModelState  string  // "loaded" / "evicted" / … when the sidecar answered
	RSSMB       float64
	ModelCostMB float64
	MetricsOK   bool
}

type metricsPayload struct {
	ModelState string `json:"model_state"`
	Memory     struct {
		RSSMB       float64 `json:"rss_mb"`
		ModelCostMB float64 `json:"model_cost_mb"`
	} `json:"memory"`
}

// Health combines the OS service state (statusFn) with a parse of the sidecar
// /metrics (fetchFn). Dependencies are injected for testability. All fields are
// best-effort; a failing statusFn yields "unknown".
func Health(info *agentcfg.Info, statusFn func() (string, error), fetchFn func(string) (string, error)) Health {
	h := Health{Service: "unknown"}
	if s, err := statusFn(); err == nil && s != "" {
		h.Service = s
	}
	if info == nil || info.Port == 0 {
		return h
	}
	h.DaemonUp = true
	if info.SidecarPort == 0 {
		h.Backend = "deterministic (ML disabled)"
		return h
	}
	body, err := fetchFn(MetricsURLNoErr(info))
	if err != nil {
		h.Backend = "sidecar unreachable"
		return h
	}
	var p metricsPayload
	if json.Unmarshal([]byte(body), &p) != nil {
		h.Backend = "sidecar unreachable"
		return h
	}
	h.Backend = "GLiNER2 sidecar"
	h.ModelState = p.ModelState
	h.RSSMB = p.Memory.RSSMB
	h.ModelCostMB = p.Memory.ModelCostMB
	h.MetricsOK = true
	return h
}

// MetricsURLNoErr returns the sidecar /metrics URL; caller guarantees a nonzero
// SidecarPort (Health checks it first).
func MetricsURLNoErr(info *agentcfg.Info) string {
	u, _ := MetricsURL(info)
	return u
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/localagent/ -run TestHealth -v 2>&1 | tail`
Expected: PASS for all four `TestHealth*`.

- [ ] **Step 5: Commit**

```bash
git add internal/localagent/health.go internal/localagent/health_test.go
git commit -m "feat(localagent): add Health summary (service state + sidecar metrics)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `keld signal metrics`

**Files:**
- Create: `internal/cli/signalagent.go`
- Create: `internal/cli/signalagent_test.go`
- Modify: `internal/cli/root.go`

**Interfaces:**
- Consumes: `localagent.Metrics`, `agentcfg.Read`.
- Produces: `func newSignalMetricsCmd() *cobra.Command`

- [ ] **Step 1: Write the failing test**

Create `internal/cli/signalagent_test.go`:

```go
package cli

import (
	"bytes"
	"testing"
)

func TestSignalMetricsCmdErrorsWhenDaemonDown(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir()) // no agent.json → daemon down
	cmd := newSignalMetricsCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("want error when keld-agent is not running")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/ -run TestSignalMetrics 2>&1 | head`
Expected: FAIL — undefined `newSignalMetricsCmd`.

- [ ] **Step 3: Implement the command**

Create `internal/cli/signalagent.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/localagent"
)

// newSignalMetricsCmd builds `keld signal metrics`.
func newSignalMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Print the local signal service's GLiNER2 sidecar /metrics JSON.",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := agentcfg.Read()
			if err != nil {
				return err
			}
			body, err := localagent.Metrics(info)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), body)
			return nil
		},
	}
}
```

- [ ] **Step 4: Register on the signal group**

In `internal/cli/root.go`, after `signal.AddCommand(newStatusCmd())`, add:

```go
	signal.AddCommand(newSignalMetricsCmd())
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/cli/ -run TestSignalMetrics -v 2>&1 | tail && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/signalagent.go internal/cli/signalagent_test.go internal/cli/root.go
git commit -m "feat(cli): add keld signal metrics

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: `keld signal enrich`

**Files:**
- Modify: `internal/cli/signalagent.go`
- Modify: `internal/cli/signalagent_test.go`
- Modify: `internal/cli/root.go`

**Interfaces:**
- Consumes: `localagent.ReadPrompt`, `localagent.RunEnrich`, `agentcfg.Read`.
- Produces: `func newSignalEnrichCmd() *cobra.Command`

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/signalagent_test.go`:

```go
func TestSignalEnrichCmdDeterministicPrintsJSON(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	cmd := newSignalEnrichCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().Set("deterministic", "true") // no sidecar needed
	if err := cmd.RunE(cmd, []string{"refactor the auth module"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"task_type"`)) {
		t.Fatalf("expected a profile JSON, got: %s", out.String())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/ -run TestSignalEnrich 2>&1 | head`
Expected: FAIL — undefined `newSignalEnrichCmd`.

- [ ] **Step 3: Implement the command**

Add to `internal/cli/signalagent.go`:

```go
// newSignalEnrichCmd builds `keld signal enrich`.
func newSignalEnrichCmd() *cobra.Command {
	var forceDeterministic bool
	var source string
	cmd := &cobra.Command{
		Use:   "enrich [prompt]",
		Short: "Run enrichment on a test prompt and print the profile JSON (local; not published).",
		Long: "Run the enrichment pipeline on a test prompt and print the resulting " +
			"profile as JSON, for quick sanity checking and debugging. The prompt is " +
			"taken from the arguments, or from stdin if none are given. Uses the running " +
			"GLiNER2 sidecar when available, otherwise the deterministic backend. The " +
			"prompt is processed locally and never published to Atlas.\n\n" +
			"Tip: single-quote the prompt (or pipe it via stdin) so your shell does not " +
			"interpret backticks or $(...) as command substitution and splice command " +
			"output into the text being enriched.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := localagent.ReadPrompt(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			info, _ := agentcfg.Read()
			out, note, err := localagent.RunEnrich(text, source, info, forceDeterministic)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "keld signal enrich: "+note)
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&forceDeterministic, "deterministic", false,
		"Force the deterministic backend instead of the sidecar.")
	cmd.Flags().StringVar(&source, "source", "claude_code",
		"Tool source to attribute the prompt to (e.g. claude_code, codex).")
	return cmd
}
```

- [ ] **Step 4: Register on the signal group**

In `internal/cli/root.go`, after `signal.AddCommand(newSignalMetricsCmd())`, add:

```go
	signal.AddCommand(newSignalEnrichCmd())
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./internal/cli/ -run TestSignalEnrich -v 2>&1 | tail && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/signalagent.go internal/cli/signalagent_test.go internal/cli/root.go
git commit -m "feat(cli): add keld signal enrich

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Extend `keld signal status` with the local-service section

Adds a pure `renderLocalService(Health) []string` and appends its output to the status command.

**Files:**
- Modify: `internal/cli/status.go`
- Create: `internal/cli/status_localservice_test.go`

**Interfaces:**
- Consumes: `localagent.Health`, `service.Status`, `agentcfg.Read`, `localagent.FetchText`.
- Produces: `func renderLocalService(h localagent.Health) []string`

- [ ] **Step 1: Write the failing test**

Create `internal/cli/status_localservice_test.go`:

```go
package cli

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/localagent"
)

func joined(lines []string) string { return strings.Join(lines, "\n") }

func TestRenderLocalServiceDaemonDown(t *testing.T) {
	out := joined(renderLocalService(localagent.Health{Service: "not running", DaemonUp: false}))
	if !strings.Contains(out, "Local signal service:") || !strings.Contains(out, "service") ||
		!strings.Contains(out, "not running") || !strings.Contains(out, "daemon") {
		t.Fatalf("got:\n%s", out)
	}
	if strings.Contains(out, "backend") {
		t.Fatalf("should omit backend when daemon down:\n%s", out)
	}
}

func TestRenderLocalServiceDeterministic(t *testing.T) {
	out := joined(renderLocalService(localagent.Health{
		Service: "active", DaemonUp: true, Backend: "deterministic (ML disabled)",
	}))
	if !strings.Contains(out, "deterministic (ML disabled)") {
		t.Fatalf("got:\n%s", out)
	}
	if strings.Contains(out, "memory") {
		t.Fatalf("should omit memory without metrics:\n%s", out)
	}
}

func TestRenderLocalServiceLoaded(t *testing.T) {
	out := joined(renderLocalService(localagent.Health{
		Service: "active", DaemonUp: true, Backend: "GLiNER2 sidecar",
		ModelState: "loaded", RSSMB: 2743.1, ModelCostMB: 2650.1, MetricsOK: true,
	}))
	for _, want := range []string{"GLiNER2 sidecar", "loaded", "rss 2743 MB", "model 2650"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/ -run TestRenderLocalService 2>&1 | head`
Expected: FAIL — undefined `renderLocalService`.

- [ ] **Step 3: Implement `renderLocalService` and wire it into the command**

In `internal/cli/status.go`, add these imports to the import block:

```go
	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/service"
	"github.com/ncx-ai/keld-signal/internal/localagent"
```

Add the pure renderer at the end of the file:

```go
// renderLocalService formats the local signal service section of `keld signal
// status` from a Health snapshot. Best-effort: lines are omitted when their
// data is unavailable.
func renderLocalService(h localagent.Health) []string {
	lines := []string{"Local signal service:",
		fmt.Sprintf("  %-11s %s", "service", h.Service)}
	if !h.DaemonUp {
		return append(lines, fmt.Sprintf("  %-11s %s", "daemon", "not running"))
	}
	lines = append(lines, fmt.Sprintf("  %-11s %s", "daemon", "reachable"))
	if h.Backend != "" {
		backend := h.Backend
		if h.ModelState != "" {
			backend += " · " + h.ModelState
		}
		lines = append(lines, fmt.Sprintf("  %-11s %s", "backend", backend))
	}
	if h.MetricsOK {
		lines = append(lines, fmt.Sprintf("  %-11s rss %.0f MB (model %.0f)", "memory", h.RSSMB, h.ModelCostMB))
	}
	return lines
}
```

In `newStatusCmd`'s `RunE`, immediately before `return nil`, add:

```go
		info, _ := agentcfg.Read()
		health := localagent.Health(info, service.Status, localagent.FetchText)
		for _, line := range renderLocalService(health) {
			console.Print(line)
		}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/cli/ -run TestRenderLocalService -v 2>&1 | tail && go build ./...`
Expected: PASS for all three; build clean.

- [ ] **Step 5: Full suite + commit**

Run: `go test ./... 2>&1 | grep -c FAIL` → expected `0`.

```bash
git add internal/cli/status.go internal/cli/status_localservice_test.go
git commit -m "feat(cli): show local signal service health in keld signal status

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: Live end-to-end verification

Not a code task — verifies the feature against the real running service. No commit unless a fix is needed (fix via TDD in the relevant task).

- [ ] **Step 1: Rebuild + restart**

Run: `make build-binaries && systemctl --user restart keld-agent`
Expected: `built keld + keld-agent`; service restarts.

- [ ] **Step 2: Verify `keld signal status`** (wait ~10s for the sidecar to load)

Run: `~/.local/bin/keld signal status`
Expected: existing login + tool rows, then a `Local signal service:` block showing `service active`, `daemon reachable`, `backend GLiNER2 sidecar · loaded`, `memory rss <n> MB (model <m>)`.

- [ ] **Step 3: Verify `keld signal metrics`**

Run: `~/.local/bin/keld signal metrics | head`
Expected: the sidecar `/metrics` JSON (contains `"model_state"` and `"rss_mb"`).

- [ ] **Step 4: Verify `keld signal enrich`** (single-quoted to avoid shell substitution)

Run: `~/.local/bin/keld signal enrich 'fix the flaky login test'`
Expected: a `keld signal enrich: using live GLiNER2 sidecar …` note on stderr and a profile JSON with `"task_type"` on stdout.

---

## Self-Review

- **Spec coverage:** shared `internal/localagent` (Tasks 1–2) ✓; `keld signal metrics` (Task 3) ✓; `keld signal enrich` (Task 4) ✓; `keld signal status` local-service section with all three fallbacks (Task 5) ✓; keld-agent delegation with no behavior change (Task 1) ✓; live verification (Task 6) ✓.
- **API deviation from spec:** `Health` takes `info *agentcfg.Info` as an explicit first argument (spec listed only `statusFn`/`fetchFn`) so daemon presence and the metrics URL derive from one read; noted here intentionally.
- **Type consistency:** `Health` struct fields and the `metricsPayload` JSON tags match across Tasks 2 and 5; `renderLocalService` reads only fields defined on `Health`. Command constructors (`newSignalMetricsCmd`, `newSignalEnrichCmd`) match their registrations in `root.go`.
- **Placeholders:** none — every code step is complete.
