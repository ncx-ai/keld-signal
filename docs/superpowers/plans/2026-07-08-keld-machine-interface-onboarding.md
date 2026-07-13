# `keld` machine-interface for installer-driven onboarding — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a machine-readable (NDJSON) interface to `keld login` and `keld signal setup`, keep the interactive behavior unchanged, and make `keld-agent install` TTY-aware so headless GUI-installer invocations don't hang.

**Architecture:** Two additive flags (`--json`, and `--no-browser` on login) stream NDJSON events on stdout. Device-code display becomes an injected `onStart` callback in `internal/auth`; setup progress becomes an injected `Emit` callback in `runSetup`. Wire structs + encoder live in `internal/cli`. `runInstall` gains an injected `isTTY` seam.

**Tech Stack:** Go, cobra, `encoding/json`. Standard-library TTY detection (`os.Stdin.Stat()` + `os.ModeCharDevice`) — no new dependency.

## Global Constraints

- No new runtime dependency; single static binary preserved.
- Zero behavior change to existing interactive `keld login` / `keld signal setup` / `keld-agent install` (TTY) paths — existing tests must stay green.
- In JSON mode, stdout carries **only** NDJSON events (one object per line, `event` discriminator); human console output is suppressed.
- JSON events are written to `console.Out` (swappable in tests), not `os.Stdout` directly.
- Terminal `error` event is paired with a non-zero exit (`errs.ErrSilentExit`).
- Privacy invariant is unaffected (no prompt text involved).
- End commit messages with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

---

### Task 1: Event structs + NDJSON encoder

**Files:**
- Create: `internal/cli/onboard_events.go`
- Test: `internal/cli/onboard_events_test.go` (create)

**Interfaces:**
- Consumes: `internal/console` (`console.Out`).
- Produces:
  - Structs `deviceCodeEvent`, `authorizedEvent`, `toolEvent`, `doneEvent`, `errorEvent`.
  - `SetupEvent struct { Kind, Name, Display, Action, Path, Backup string; Configured int; Endpoint string }` — wire-agnostic event passed to `SetupOpts.Emit` (Task 4).
  - `emitEvent(v any)` — marshals `v` and writes one line to `console.Out`.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/onboard_events_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/console"
)

func TestEmitEventWritesOneNDJSONLine(t *testing.T) {
	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	emitEvent(deviceCodeEvent{
		Event:           "device_code",
		VerificationURL: "https://v",
		UserCode:        "WXYZ-1234",
		ExpiresIn:       900,
		Interval:        5,
	})
	emitEvent(authorizedEvent{Event: "authorized", Principal: "dg@keld.co", Org: "acme"})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d: %q", len(lines), buf.String())
	}
	want0 := `{"event":"device_code","verification_url":"https://v","user_code":"WXYZ-1234","expires_in":900,"interval":5}`
	if lines[0] != want0 {
		t.Fatalf("line0=\n%s\nwant\n%s", lines[0], want0)
	}
	want1 := `{"event":"authorized","principal":"dg@keld.co","org":"acme"}`
	if lines[1] != want1 {
		t.Fatalf("line1=\n%s\nwant\n%s", lines[1], want1)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestEmitEvent -v`
Expected: FAIL to compile — `undefined: emitEvent` / event structs.

- [ ] **Step 3: Write minimal implementation**

Create `internal/cli/onboard_events.go`:

```go
package cli

import (
	"encoding/json"
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/console"
)

// NDJSON event payloads emitted by the --json onboarding modes. Field order is
// the struct order; encoding/json preserves it.
type deviceCodeEvent struct {
	Event           string `json:"event"`
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type authorizedEvent struct {
	Event     string `json:"event"`
	Principal string `json:"principal"`
	Org       string `json:"org"`
}

type toolEvent struct {
	Event   string `json:"event"`
	Name    string `json:"name"`
	Display string `json:"display"`
	Action  string `json:"action"` // configured | already_configured | skipped_conflict
	Path    string `json:"path"`
	Backup  string `json:"backup,omitempty"`
}

type doneEvent struct {
	Event      string `json:"event"`
	Configured int    `json:"configured"`
	Endpoint   string `json:"endpoint,omitempty"`
}

type errorEvent struct {
	Event   string `json:"event"`
	Message string `json:"message"`
}

// SetupEvent is the wire-agnostic progress event runSetup passes to SetupOpts.Emit.
// The command layer maps it to toolEvent/doneEvent NDJSON.
type SetupEvent struct {
	Kind       string // "tool" | "done"
	Name       string
	Display    string
	Action     string
	Path       string
	Backup     string
	Configured int
	Endpoint   string
}

// emitEvent marshals v and writes it as one NDJSON line to console.Out.
func emitEvent(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintln(console.Out, string(b))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestEmitEvent -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/onboard_events.go internal/cli/onboard_events_test.go
git commit -m "feat(cli): NDJSON event types + encoder for onboarding machine interface

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `auth` device-code reporting seam

**Files:**
- Modify: `internal/auth/device.go`
- Test: `internal/auth/device_test.go` (create or extend)

**Interfaces:**
- Consumes: `internal/api` (`*api.Client`, `*api.DeviceStart`), `internal/console`.
- Produces:
  - `Login(c *api.Client, openBrowser bool, sleep func(time.Duration), opener func(string) error, onStart func(*api.DeviceStart)) (*AuthData, error)` — now takes `onStart`, invoked immediately after `DeviceStart()`.
  - `RequireAuthReport(noLogin, openBrowser, force bool, onStart func(*api.DeviceStart)) (*AuthData, error)`.
  - `RequireAuth(noLogin, openBrowser, force bool) (*AuthData, error)` — unchanged signature; delegates to `RequireAuthReport` with the default human `onStart`.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/device_test.go`:

```go
package auth

import (
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestLoginInvokesOnStartWithDeviceCode(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	// httptest server implementing device/start then device/poll.
	// Reuse the shape from internal/api/client_test.go.
	srv := newDeviceServer(t) // helper below
	defer srv.Close()
	paths.SetAPIBaseOverride(srv.URL)
	defer paths.SetAPIBaseOverride("")

	var seen *api.DeviceStart
	got, err := Login(
		api.NewClient(srv.URL, ""),
		false, // no browser
		func(time.Duration) {}, // no real sleep
		func(string) error { return nil },
		func(ds *api.DeviceStart) { seen = ds },
	)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if seen == nil || seen.UserCode != "UC" || seen.VerificationURL != "https://v" {
		t.Fatalf("onStart got %+v, want UserCode=UC url=https://v", seen)
	}
	if got == nil || got.Principal != "p" || got.Org != "o" {
		t.Fatalf("auth result %+v, want principal=p org=o", got)
	}
}
```

Add the server helper at the bottom of the same file:

```go
func newDeviceServer(t *testing.T) *httptest.Server {
	t.Helper()
	polls := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":5}`))
		case "/v1/cli/device/poll":
			polls++
			if polls < 2 {
				w.WriteHeader(202) // pending
				return
			}
			w.Write([]byte(`{"access_token":"at","principal":"p","org":"o"}`))
		}
	}))
}
```

Add imports `net/http`, `net/http/httptest` to the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/auth/ -run TestLoginInvokesOnStart -v`
Expected: FAIL to compile — `Login` takes 4 args, test passes 5.

- [ ] **Step 3: Write minimal implementation**

In `internal/auth/device.go`, change `Login`'s signature and replace the inline device-code print with the callback. Replace:

```go
func Login(c *api.Client, openBrowser bool, sleep func(time.Duration), opener func(string) error) (*AuthData, error) {
	ds, err := c.DeviceStart()
	if err != nil {
		return nil, err
	}

	console.Print(fmt.Sprintf(
		"To authorize this device, open:\n  %s\nThe code %s is already filled in — confirm it matches, then approve.",
		ds.VerificationURL, ds.UserCode,
	))

	if openBrowser {
```

with:

```go
func Login(c *api.Client, openBrowser bool, sleep func(time.Duration), opener func(string) error, onStart func(*api.DeviceStart)) (*AuthData, error) {
	ds, err := c.DeviceStart()
	if err != nil {
		return nil, err
	}

	if onStart != nil {
		onStart(ds)
	}

	if openBrowser {
```

Then update `RequireAuth` to delegate. Replace:

```go
func RequireAuth(noLogin bool, openBrowser bool, force bool) (*AuthData, error) {
	existing, err := Load()
```

with:

```go
// defaultDeviceReport prints the human device-code instructions (the pre-seam behavior).
func defaultDeviceReport(ds *api.DeviceStart) {
	console.Print(fmt.Sprintf(
		"To authorize this device, open:\n  %s\nThe code %s is already filled in — confirm it matches, then approve.",
		ds.VerificationURL, ds.UserCode,
	))
}

func RequireAuth(noLogin bool, openBrowser bool, force bool) (*AuthData, error) {
	return RequireAuthReport(noLogin, openBrowser, force, defaultDeviceReport)
}

func RequireAuthReport(noLogin bool, openBrowser bool, force bool, onStart func(*api.DeviceStart)) (*AuthData, error) {
	existing, err := Load()
```

Finally, at the end of `RequireAuthReport`, pass `onStart` to `Login`. Replace:

```go
	return Login(
		api.NewClient(paths.APIBase(), ""),
		openBrowser,
		time.Sleep,
		openURL,
	)
}
```

with:

```go
	return Login(
		api.NewClient(paths.APIBase(), ""),
		openBrowser,
		time.Sleep,
		openURL,
		onStart,
	)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/auth/ -v`
Expected: PASS (new test + any existing auth tests).

- [ ] **Step 5: Verify no caller broke**

Run: `go build ./... && go test ./internal/cli/ ./internal/agentcli/`
Expected: build OK; existing tests pass (the only other `Login`/`RequireAuth` callers are `RequireAuth` itself and the cli commands, which use `RequireAuth`, unchanged).

- [ ] **Step 6: Commit**

```bash
git add internal/auth/device.go internal/auth/device_test.go
git commit -m "refactor(auth): device-code display via onStart callback + RequireAuthReport

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `keld login --json` / `--no-browser`

**Files:**
- Modify: `internal/cli/login.go`
- Test: `internal/cli/login_test.go` (create)

**Interfaces:**
- Consumes: `auth.RequireAuthReport` (Task 2), event structs + `emitEvent` (Task 1).
- Produces: `keld login --json [--no-browser]` NDJSON behavior.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/login_test.go`:

```go
package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/console"
)

func TestLoginJSONEmitsDeviceCodeThenAuthorized(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":5}`))
		case "/v1/cli/device/poll":
			polls++
			if polls < 2 {
				w.WriteHeader(202)
				return
			}
			w.Write([]byte(`{"access_token":"at","principal":"p","org":"o"}`))
		}
	}))
	defer srv.Close()

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	cmd := newLoginCmd()
	cmd.SetArgs([]string{"--json", "--no-browser", "--api-url", srv.URL})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %q", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], `"event":"device_code"`) || !strings.Contains(lines[0], `"user_code":"UC"`) {
		t.Fatalf("line0 not device_code: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"event":"authorized"`) || !strings.Contains(lines[1], `"principal":"p"`) {
		t.Fatalf("line1 not authorized: %s", lines[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestLoginJSON -v`
Expected: FAIL — `--json` flag unknown / no device_code output.

- [ ] **Step 3: Write minimal implementation**

In `internal/cli/login.go`, add the flags and a JSON branch. Replace the `RunE` body's auth block:

```go
			// force=true: an explicit `keld login` always re-authenticates rather than
			// trusting stored creds (which may be revoked/rotated). Falls back to the
			// lazy path only under --no-login (no browser available).
			a, err := auth.RequireAuth(noLogin, true, true)
			if err != nil {
				return err
			}
			// Sole "Logged in as …" confirmation (Login() no longer prints it), so the
			// line appears exactly once whether we re-authed or returned stored creds.
			console.Print(fmt.Sprintf("Logged in as %s (org: %s)", a.Principal, a.Org))
			return nil
```

with:

```go
			jsonOut, _ := cmd.Flags().GetBool("json")
			noBrowser, _ := cmd.Flags().GetBool("no-browser")

			if jsonOut {
				onStart := func(ds *api.DeviceStart) {
					emitEvent(deviceCodeEvent{
						Event:           "device_code",
						VerificationURL: ds.VerificationURL,
						UserCode:        ds.UserCode,
						ExpiresIn:       ds.ExpiresIn,
						Interval:        ds.Interval,
					})
				}
				a, err := auth.RequireAuthReport(noLogin, !noBrowser, true, onStart)
				if err != nil {
					emitEvent(errorEvent{Event: "error", Message: err.Error()})
					return errs.ErrSilentExit
				}
				emitEvent(authorizedEvent{Event: "authorized", Principal: a.Principal, Org: a.Org})
				return nil
			}

			// force=true: an explicit `keld login` always re-authenticates rather than
			// trusting stored creds (which may be revoked/rotated). Falls back to the
			// lazy path only under --no-login (no browser available).
			a, err := auth.RequireAuth(noLogin, true, true)
			if err != nil {
				return err
			}
			// Sole "Logged in as …" confirmation (Login() no longer prints it), so the
			// line appears exactly once whether we re-authed or returned stored creds.
			console.Print(fmt.Sprintf("Logged in as %s (org: %s)", a.Principal, a.Org))
			return nil
```

Add the flag registrations (next to the existing flags):

```go
	cmd.Flags().Bool("json", false, "Emit machine-readable NDJSON events on stdout (for installer/automation).")
	cmd.Flags().Bool("no-browser", false, "Do not auto-open the browser (the caller opens the verification URL itself).")
```

Add imports: `"github.com/ncx-ai/keld-signal/internal/api"` and `"github.com/ncx-ai/keld-signal/internal/errs"`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestLoginJSON -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/login.go internal/cli/login_test.go
git commit -m "feat(cli): keld login --json / --no-browser NDJSON mode

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: `keld signal setup --json` (Emit sink in runSetup)

**Files:**
- Modify: `internal/cli/setup.go`
- Test: `internal/cli/setup_test.go` (extend)

**Interfaces:**
- Consumes: `SetupEvent`, `emitEvent`, `toolEvent`, `doneEvent` (Task 1).
- Produces:
  - `SetupOpts.Emit func(SetupEvent)` field.
  - `runSetup` emits `SetupEvent`s and suppresses human output when `Emit != nil`.
  - `keld signal setup --json` (implies non-interactive).

- [ ] **Step 1: Write the failing test**

Add to `internal/cli/setup_test.go`:

```go
func TestRunSetupEmitsEventsWhenEmitSet(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()

	// One adapter that will be configured, one that reports no change.
	changed := &fakeAdapter{
		name: "configured_tool",
		plan: tools.Plan{
			Name: "configured_tool", ConfigPath: filepath.Join(dir, "a.json"),
			AfterText: `{"k":1}`, Managed: map[string]any{}, Summary: []string{"add"}, Changed: true,
		},
	}
	nochange := &fakeAdapter{
		name: "nochange_tool",
		plan: tools.Plan{
			Name: "nochange_tool", ConfigPath: filepath.Join(dir, "b.json"),
			AfterText: "", Managed: map[string]any{}, Changed: false,
		},
	}

	var events []SetupEvent
	ob := &api.Onboarding{Endpoint: "https://ep", IngestToken: "tok", Actor: "actor"}
	p := tools.SetupParams{Endpoint: ob.Endpoint, IngestToken: ob.IngestToken, Actor: ob.Actor}
	opts := SetupOpts{Yes: true, Emit: func(e SetupEvent) { events = append(events, e) }}

	if _, err := runSetup([]tools.Adapter{changed, nochange}, p, &api.Client{}, ob, opts); err != nil {
		t.Fatalf("runSetup: %v", err)
	}

	// Expect: tool(configured_tool, configured), tool(nochange_tool, already_configured), done(configured=1)
	var tool0, tool1, done *SetupEvent
	for i := range events {
		switch {
		case events[i].Kind == "tool" && events[i].Name == "configured_tool":
			tool0 = &events[i]
		case events[i].Kind == "tool" && events[i].Name == "nochange_tool":
			tool1 = &events[i]
		case events[i].Kind == "done":
			done = &events[i]
		}
	}
	if tool0 == nil || tool0.Action != "configured" {
		t.Fatalf("configured_tool event = %+v", tool0)
	}
	if tool1 == nil || tool1.Action != "already_configured" {
		t.Fatalf("nochange_tool event = %+v", tool1)
	}
	if done == nil || done.Configured != 1 {
		t.Fatalf("done event = %+v", done)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestRunSetupEmits -v`
Expected: FAIL to compile — `SetupOpts` has no field `Emit`.

- [ ] **Step 3: Write minimal implementation**

In `internal/cli/setup.go`, add the field to `SetupOpts`:

```go
type SetupOpts struct {
	DryRun          bool
	Yes             bool
	ShowDiff        bool
	Confirm         func(string) bool
	ResolveConflict func(a tools.Adapter, plan tools.Plan) string // returns "skip"/"replace"/"abort"
	Emit            func(SetupEvent)                              // non-nil ⇒ machine mode: emit events, suppress human output
}
```

Then rewrite the body of `runSetup` to add `quiet`/`emit` and guard output. Replace the whole function body (from `func runSetup(...) {` through its closing `}`) with:

```go
func runSetup(adapters []tools.Adapter, p tools.SetupParams, client *api.Client, ob *api.Onboarding, opts SetupOpts) (*config.Manifest, error) {
	quiet := opts.Emit != nil
	emit := func(e SetupEvent) {
		if opts.Emit != nil {
			opts.Emit(e)
		}
	}
	say := func(s string) {
		if !quiet {
			console.Print(s)
		}
	}

	type approved struct {
		adapter tools.Adapter
		plan    tools.Plan
	}
	var approveds []approved

	for _, adapter := range adapters {
		path := adapter.ConfigPath()
		var before *string
		if _, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err == nil {
				s := string(data)
				before = &s
			}
		}

		if !quiet {
			console.Rule(fmt.Sprintf("%s · %s", adapter.DisplayName(), path))
		}

		plan := adapter.Apply(before, p, false)

		if plan.Conflict != "" {
			say(fmt.Sprintf("  conflict: %s", plan.Conflict))
			if opts.DryRun {
				say("  (dry-run: would be skipped)")
				emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
				continue
			}
			if opts.Yes {
				say("  skipped (--yes)")
				emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
				continue
			}
			choice := opts.ResolveConflict(adapter, plan)
			if choice == "abort" {
				say("Aborted.")
				return nil, errs.ErrSilentExit
			}
			if choice == "replace" {
				plan = adapter.Apply(before, p, true)
				if plan.Conflict != "" {
					say(fmt.Sprintf("  can't replace: %s", plan.Conflict))
					say("  skipped")
					emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
					continue
				}
				if !quiet {
					diffview.Render(before, plan.AfterText, plan.ConfigPath)
					for _, line := range plan.Summary {
						console.Print(fmt.Sprintf("  %s", line))
					}
				}
				approveds = append(approveds, approved{adapter, plan})
				continue
			}
			say("  skipped")
			emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
			continue
		}

		if !plan.Changed {
			say("  already configured — no changes")
			emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "already_configured", Path: path})
			continue
		}

		if !quiet {
			if opts.ShowDiff {
				diffview.Render(before, plan.AfterText, plan.ConfigPath)
			}
			for _, line := range plan.Summary {
				console.Print(fmt.Sprintf("  %s", line))
			}
		}
		approveds = append(approveds, approved{adapter, plan})
	}

	say("\nHook · keld __hook (writes ~/.keld/hook.json)")

	if opts.DryRun {
		say("\n--dry-run: no changes written.")
		return config.LoadManifest()
	}
	if len(approveds) == 0 {
		say("\nNothing to apply.")
		emit(SetupEvent{Kind: "done", Configured: 0, Endpoint: ob.Endpoint})
		return config.LoadManifest()
	}
	if !opts.Yes && !opts.Confirm(fmt.Sprintf("Apply %d change(s)?", len(approveds))) {
		say("Aborted.")
		return config.LoadManifest()
	}

	endpoint := ob.Endpoint
	actor := ob.Actor
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools:    map[string]config.ToolManifest{},
	}
	manifest.Hook = &config.HookRecord{Version: version.CLI}
	if err := config.SaveHookConfig(ob.Endpoint, ob.IngestToken); err != nil {
		return nil, err
	}

	for _, a := range approveds {
		backup, err := config.BackupConfig(a.plan.ConfigPath, a.adapter.Name())
		if err != nil {
			return nil, err
		}
		if backup != "" {
			say(fmt.Sprintf("  backed up %s → %s", a.plan.ConfigPath, backup))
		}
		if err := config.WriteAtomic(a.plan.ConfigPath, a.plan.AfterText, false); err != nil {
			return nil, err
		}
		var backupPtr *string
		if backup != "" {
			backupPtr = &backup
		}
		manifest.Tools[a.adapter.Name()] = config.ToolManifest{
			Name:       a.adapter.Name(),
			ConfigPath: a.plan.ConfigPath,
			Managed:    a.plan.Managed,
			BackupPath: backupPtr,
		}
		say(fmt.Sprintf("  ✓ %s", a.adapter.DisplayName()))
		emit(SetupEvent{Kind: "tool", Name: a.adapter.Name(), Display: a.adapter.DisplayName(), Action: "configured", Path: a.plan.ConfigPath, Backup: backup})
	}

	if err := manifest.Save(); err != nil {
		return nil, err
	}
	say("\nSetup complete. Restart any running sessions to pick up the new config.")
	emit(SetupEvent{Kind: "done", Configured: len(manifest.Tools), Endpoint: ob.Endpoint})
	return manifest, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/ -run 'TestRunSetup' -v`
Expected: PASS — the new emit test AND all existing `TestRunSetup*` tests (human path unchanged).

- [ ] **Step 5: Wire the `--json` flag into the setup command**

In `newSetupCmd` (`setup.go`), add the flag and JSON wiring. After the existing `var apiURL string` add `var jsonOut bool`. Add the flag registration next to the others:

```go
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable NDJSON events on stdout (implies --yes).")
```

In the `RunE`, replace the `opts := SetupOpts{...}` block with:

```go
			opts := SetupOpts{
				DryRun:          dryRun,
				Yes:             yes,
				ShowDiff:        showDiff,
				Confirm:         stdinConfirm,
				ResolveConflict: stdinResolveConflict,
			}
			if jsonOut {
				opts.Yes = true
				opts.Emit = func(e SetupEvent) {
					switch e.Kind {
					case "tool":
						emitEvent(toolEvent{Event: "tool", Name: e.Name, Display: e.Display, Action: e.Action, Path: e.Path, Backup: e.Backup})
					case "done":
						emitEvent(doneEvent{Event: "done", Configured: e.Configured, Endpoint: e.Endpoint})
					}
				}
			}
```

Also guard the two pre-`runSetup` `console.Print` lines in `RunE` (the "No supported tools detected." message at `setup.go:223` and any onboarding chatter) so JSON mode stays clean: wrap the "No supported tools detected." branch to emit a `done` with `configured:0` instead when `jsonOut` is set:

```go
			if len(adapters) == 0 {
				if jsonOut {
					emitEvent(doneEvent{Event: "done", Configured: 0})
				} else {
					console.Print("No supported tools detected. Use --tool to target one explicitly.")
				}
				return nil
			}
```

- [ ] **Step 6: Run full cli tests + build**

Run: `go test ./internal/cli/ && go build ./...`
Expected: PASS; build OK.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/setup.go internal/cli/setup_test.go
git commit -m "feat(cli): keld signal setup --json NDJSON mode via Emit sink

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `keld-agent install` TTY guard

**Files:**
- Modify: `internal/agentcli/agentcli.go`
- Test: `internal/agentcli/agentcli_test.go` (extend)

**Interfaces:**
- Consumes: existing `resolveKeld`, `runStep`, `service.Install`.
- Produces:
  - `runInstall(isTTY func() bool, resolveKeld func() (string, error), run stepRunner, installService func() error) error` — new leading `isTTY` param.
  - `stdinIsTTY() bool` — production detector.

- [ ] **Step 1: Update existing tests + add the guard test**

In `internal/agentcli/agentcli_test.go`, update the three existing `runInstall(...)` call sites to pass a TTY-true detector as the new first argument, and add a no-TTY test. Replace each existing call `runInstall(resolve, run, install)` with `runInstall(func() bool { return true }, resolve, run, install)`. Then add:

```go
func TestRunInstallNoTTYSkipsLoginAndSetup(t *testing.T) {
	var calls []string
	resolve := func() (string, error) { return "/fake/keld", nil }
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(func() bool { return false }, resolve, run, install); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("no-TTY must not run login/setup, got %v", calls)
	}
	if !installed {
		t.Fatal("service install must still run in no-TTY mode")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agentcli/ -run TestRunInstall -v`
Expected: FAIL to compile — `runInstall` now called with 4 args in tests but defined with 3.

- [ ] **Step 3: Write minimal implementation**

In `internal/agentcli/agentcli.go`, add the detector and the guard. Add near `resolveKeld`:

```go
// stdinIsTTY reports whether stdin is an interactive terminal. A GUI installer
// invokes `keld-agent install` with no console (Windows runhidden / macOS GUI
// session), so stdin is not a character device — the interactive login/setup
// steps are skipped and the installer's own pages drive `keld --json` instead.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
```

Change `runInstall` to take `isTTY` and gate the steps. Replace:

```go
func runInstall(resolveKeld func() (string, error), run stepRunner, installService func() error) error {
	keld, err := resolveKeld()
	if err != nil {
		return err
	}
	if err := run(keld, "login"); err != nil {
		return fmt.Errorf("keld login: %w", err)
	}
	if err := run(keld, "signal", "setup"); err != nil {
		return fmt.Errorf("keld signal setup: %w", err)
	}
	return installService()
}
```

with:

```go
func runInstall(isTTY func() bool, resolveKeld func() (string, error), run stepRunner, installService func() error) error {
	if isTTY() {
		keld, err := resolveKeld()
		if err != nil {
			return err
		}
		if err := run(keld, "login"); err != nil {
			return fmt.Errorf("keld login: %w", err)
		}
		if err := run(keld, "signal", "setup"); err != nil {
			return fmt.Errorf("keld signal setup: %w", err)
		}
	} else {
		fmt.Println("Service installed. Finish setup by running: keld login && keld signal setup")
	}
	return installService()
}
```

Update the command wiring in `NewRootCmd`. Replace:

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(resolveKeld, runStep, service.Install)
		},
```

with:

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(stdinIsTTY, resolveKeld, runStep, service.Install)
		},
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agentcli/ -v`
Expected: PASS (all runInstall tests, including the new no-TTY case).

- [ ] **Step 5: Full build + suite + manual verification**

```bash
make build-binaries && go test ./...
~/.local/bin/keld-agent install </dev/null   # no TTY: prints finish-setup note, registers service
~/.local/bin/keld login --json --no-browser --api-url http://127.0.0.1:1 ; echo "exit=$?"   # emits an error event + nonzero exit (no server)
```
Expected: build OK; suite green; the `install </dev/null` run prints the finish-setup note (and attempts service registration); the `login --json` run emits a single `{"event":"error",...}` line and exits non-zero.

- [ ] **Step 6: Commit**

```bash
git add internal/agentcli/agentcli.go internal/agentcli/agentcli_test.go
git commit -m "fix(agent): TTY-guard install — headless invocation registers service only

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- `keld login --json` device_code + authorized + error → Tasks 1–3.
- `--no-browser` → Task 3 (`!noBrowser` → openBrowser).
- `keld signal setup --json` tool/done/error, non-interactive → Tasks 1, 4.
- Logic stays in Go, callbacks not re-implemented → Tasks 2 (`onStart`), 4 (`Emit`).
- Human paths unchanged → Task 2 (default reporter), Task 4 (`quiet` guards; existing tests must pass).
- TTY guard on `keld-agent install`, no new dep → Task 5 (`os.ModeCharDevice`).
- Events to `console.Out`, not `os.Stdout` → Task 1 (`emitEvent`).

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `emitEvent`/event structs (Task 1) consumed verbatim in Tasks 3–4. `SetupEvent` fields (Task 1) match `runSetup`'s `emit(...)` calls and the command's mapping (Task 4). `Login`'s new `onStart` param (Task 2) matches the closure in Task 3 and the default reporter. `runInstall`'s new `isTTY` first param (Task 5) matches all updated call sites and the new test.
