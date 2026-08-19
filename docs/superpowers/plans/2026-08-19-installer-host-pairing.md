# Installer Host Pairing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a Keld Signal install pair with the host it came from, so re-installing can move a machine between deploys instead of silently re-pairing to the host it was already on.

**Architecture:** One precedence rule for the API base — `--api-url` > host carried by the setup code > `KELD_API_URL` > stored `auth.json` (credential-reuse path only) > `DefaultAPIURL`. Two delivery channels supply the host: the `curl | sh` line sets `KELD_API_URL` (the script already reads it), and the `.pkg` setup code carries its origin as `atlas.keld.co/ABCD-EFGH`. A single parser, `auth.ParsePairingCode`, is the only place that format is understood.

**Tech Stack:** Go 1.26 (keld-signal), Python 3.12 / FastAPI (keld-atlas API), Next.js + vitest (keld-atlas web).

**Spec:** `docs/superpowers/specs/2026-08-19-installer-host-pairing-design.md` (in this repo)

## Global Constraints

- **Two repos.** Tasks 1-4 are in `keld-signal` (`~/keld-signal`, branch `feat/installer-host-pairing`). Tasks 5-6 are in `keld-atlas` (`~/keld-atlas`) and need their own branch — see Task 5 Step 1. Commit to each repo separately; never mix.
- **Do not modify** `scripts/install.sh`, `installers/macos/onboard.command`, `installers/macos/scripts/postinstall`, `services/api/app/main.py`, or `services/web/next.config.ts`. `/install.sh` keeps redirecting to GitHub; serving it from Atlas needs storage that does not exist yet.
- **Setup code alphabet** is `ABCDEFGHJKLMNPQRSTUVWXYZ23456789` (uppercase; `I`, `O`, `0`, `1` excluded as ambiguous), formatted `XXXX-XXXX`. `POST /v1/cli/enroll` looks it up as a raw Redis key, so it is case-sensitive server-side.
- **Default API base** is the exact string `https://atlas.keld.co` (`internal/paths/paths.go:9`).
- **Go tests:** `cd ~/keld-signal && go test ./internal/...`
- **Python tests:** run in Docker, never the host interpreter:
  ```
  cd ~/keld-atlas && docker compose run --rm --no-deps \
    -e KELD_TEST_DATABASE_URL=postgresql+asyncpg://keld:keld@postgres:5432/keld_test \
    --entrypoint sh api -c "pip install -e .[test] && pytest -q <paths>"
  ```
- **Web tests:** `cd ~/keld-atlas/services/web && pnpm run test`
- `paths.SetAPIBaseOverride` mutates **package-level globals**. Every test that touches it must reset with `defer paths.SetAPIBaseOverride("")`.

### Deviation from the spec (deliberate)

The spec's parse step 1 says "strip a single trailing `/`". **Do not implement that.** It creates an ambiguity: `atlas.keld.co/` would trim to `atlas.keld.co`, which then looks like a bare code and gets uppercased into `ATLAS.KELD.CO`. Instead, leave a trailing slash in place so the code segment comes out empty and errors clearly. This is Task 1 Step 1's `trailing slash` case.

The spec also says to "narrow `TestRequireAuthTargetsStoredAPIURL` to the reuse path". It is **already** on the reuse path (it calls `RequireAuth(false, false, false)` — `force=false`), so it needs no change and must keep passing as-is.

---

### Task 1: `ParsePairingCode`

A pure function, no I/O. Everything else depends on it.

**Files:**
- Create: `~/keld-signal/internal/auth/code.go`
- Test: `~/keld-signal/internal/auth/code_test.go`

**Interfaces:**
- Consumes: `internal/errs` (existing; `errs.New(string) error`).
- Produces: `auth.ParsePairingCode(s string) (apiBase, code string, err error)`. `apiBase` is `""` when the input carried no host, and otherwise a scheme-qualified base with no trailing slash. `code` is uppercased.

- [ ] **Step 1: Write the failing test**

Create `~/keld-signal/internal/auth/code_test.go`:

```go
package auth

import "testing"

func TestParsePairingCode(t *testing.T) {
	cases := []struct {
		name, in, wantBase, wantCode string
		wantErr                      bool
	}{
		{name: "bare code", in: "ABCD-EFGH", wantBase: "", wantCode: "ABCD-EFGH"},
		{name: "bare host", in: "atlas.keld.co/ABCD-EFGH", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "explicit https", in: "https://atlas.keld.co/ABCD-EFGH", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "explicit http", in: "http://dev.example/ABCD-EFGH", wantBase: "http://dev.example", wantCode: "ABCD-EFGH"},
		{name: "localhost with port", in: "localhost:8000/ABCD-EFGH", wantBase: "http://localhost:8000", wantCode: "ABCD-EFGH"},
		{name: "loopback ip", in: "127.0.0.1:8000/ABCD-EFGH", wantBase: "http://127.0.0.1:8000", wantCode: "ABCD-EFGH"},
		{name: "path bearing host", in: "https://example.com/keld/ABCD-EFGH", wantBase: "https://example.com/keld", wantCode: "ABCD-EFGH"},
		{name: "lowercase code is uppercased", in: "abcd-efgh", wantBase: "", wantCode: "ABCD-EFGH"},
		{name: "lowercase code with host", in: "atlas.keld.co/abcd-efgh", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "surrounding whitespace", in: "  atlas.keld.co/ABCD-EFGH \n", wantBase: "https://atlas.keld.co", wantCode: "ABCD-EFGH"},
		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "trailing slash", in: "atlas.keld.co/", wantErr: true},
		{name: "trailing slash after code", in: "atlas.keld.co/ABCD-EFGH/", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base, code, err := ParsePairingCode(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePairingCode(%q) = (%q, %q, nil), want error", tc.in, base, code)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePairingCode(%q): %v", tc.in, err)
			}
			if base != tc.wantBase || code != tc.wantCode {
				t.Fatalf("ParsePairingCode(%q) = (%q, %q), want (%q, %q)", tc.in, base, code, tc.wantBase, tc.wantCode)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld-signal && go test ./internal/auth/ -run TestParsePairingCode`
Expected: FAIL — `undefined: ParsePairingCode`.

- [ ] **Step 3: Write the implementation**

Create `~/keld-signal/internal/auth/code.go`:

```go
package auth

import (
	"strings"

	"github.com/ncx-ai/keld-signal/internal/errs"
)

// ParsePairingCode splits a setup code that may carry the Keld host that minted
// it. "atlas.keld.co/ABCD-EFGH" yields ("https://atlas.keld.co", "ABCD-EFGH");
// a bare "ABCD-EFGH" yields ("", "ABCD-EFGH") so the caller falls back to its
// normal API-base resolution and codes minted by an older Atlas keep working.
//
// The split is on the LAST "/", which is unambiguous because a setup code never
// contains one — it is XXXX-XXXX. That handles a scheme ("https://host/CODE")
// and a path-bearing deploy ("https://host/keld/CODE") with no special cases.
//
// A trailing slash is deliberately NOT trimmed: trimming it would turn
// "atlas.keld.co/" into something indistinguishable from a bare code, which
// would then be uppercased and sent to the server as "ATLAS.KELD.CO". Leaving
// it produces an empty code segment and a clear error instead.
func ParsePairingCode(s string) (apiBase, code string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", errs.New("setup code is empty")
	}
	host := ""
	if i := strings.LastIndex(s, "/"); i >= 0 {
		host, code = s[:i], s[i+1:]
	} else {
		code = s
	}
	if code == "" {
		return "", "", errs.New(`invalid setup code (expected "ABCD-EFGH" or "atlas.keld.co/ABCD-EFGH")`)
	}
	// The server's alphabet is uppercase and /v1/cli/enroll looks the code up as a
	// raw Redis key, so a lowercase-typed code would 410. No lowercase code is ever
	// minted, so uppercasing cannot collide. The host is left exactly as typed.
	code = strings.ToUpper(code)
	if host == "" {
		return "", code, nil
	}
	if !strings.Contains(host, "://") {
		scheme := "https://"
		if isLoopbackHost(host) {
			scheme = "http://"
		}
		host = scheme + host
	}
	return strings.TrimRight(host, "/"), code, nil
}

// isLoopbackHost reports whether a scheme-less host[:port][/path] names the
// local machine, which is served over http in dev.
func isLoopbackHost(host string) bool {
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host == "localhost" || host == "127.0.0.1"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd ~/keld-signal && go test ./internal/auth/ -run TestParsePairingCode -v`
Expected: PASS, all 14 subtests.

- [ ] **Step 5: Commit**

```bash
cd ~/keld-signal
git add internal/auth/code.go internal/auth/code_test.go
git commit -m "feat(auth): parse a setup code that carries its own Keld host"
```

---

### Task 2: Stop the stored-host pin from capturing a forced login

**Files:**
- Modify: `~/keld-signal/internal/auth/device.go:110-128` (`RequireAuthReport`)
- Test: `~/keld-signal/internal/auth/device_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: no new symbols. Behavior change only — after this task, `RequireAuth(noLogin=false, ..., force=true)` resolves `paths.APIBase()` to `DefaultAPIURL` when no override and no `KELD_API_URL` are set, regardless of what `auth.json` holds.

- [ ] **Step 1: Write the failing test**

Append to `~/keld-signal/internal/auth/device_test.go`:

```go
// A forced `keld login` is exactly when the user may be moving this machine to a
// different deploy, so it must NOT inherit the stored token's host. Regression for
// a reinstall aimed at prod silently re-pairing to a stored localhost.
func TestRequireAuthForcedLoginIgnoresStoredAPIURL(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_API_URL", "")
	paths.SetAPIBaseOverride("")
	defer paths.SetAPIBaseOverride("")
	if err := Save(AuthData{AccessToken: "T", Principal: "p", Org: "o", APIURL: "http://localhost:8000"}); err != nil {
		t.Fatal(err)
	}
	// force=true, noLogin=false. The login itself will fail (no server, no browser);
	// we only care that the API base was not pinned to the stored host first.
	_, _ = RequireAuth(false, false, true)
	if paths.APIBase() != paths.DefaultAPIURL {
		t.Fatalf("forced login should target %q, got %q", paths.DefaultAPIURL, paths.APIBase())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld-signal && go test ./internal/auth/ -run TestRequireAuthForcedLoginIgnoresStoredAPIURL`
Expected: FAIL — `forced login should target "https://atlas.keld.co", got "http://localhost:8000"`.

- [ ] **Step 3: Move the pin inside the reuse branch**

In `~/keld-signal/internal/auth/device.go`, replace the body of `RequireAuthReport` from `existing, err := Load()` down to the closing brace of the `if !(force && !noLogin)` block with:

```go
	existing, err := Load()
	if err != nil {
		return nil, err
	}
	if !(force && !noLogin) {
		// A stored token is only valid at the server it was minted on (its APIURL).
		// Unless an explicit --api-url flag already set an override, target that
		// server for every subsequent command — otherwise `keld signal setup` sends
		// a local token to atlas.keld.co and gets 401 "invalid CLI token".
		//
		// Deliberately NOT applied to a forced `keld login`: that is exactly when the
		// user may be moving this machine to a different deploy. Pinning there made a
		// re-install unable to ever leave the host it was already paired to, silently.
		if existing != nil && existing.APIURL != "" && paths.APIBaseOverride() == "" {
			paths.SetAPIBaseOverride(existing.APIURL)
		}
		if existing != nil {
			return existing, nil
		}
		if noLogin {
			return nil, errs.New("not logged in (run `keld login`; --no-login was set)")
		}
	}
```

Delete the original pin block that sat above the `if !(force && !noLogin)` line, along with its comment. Everything after this block (the `return Login(...)`) is unchanged.

- [ ] **Step 4: Run the whole auth package**

Run: `cd ~/keld-signal && go test ./internal/auth/ -v`
Expected: PASS. `TestRequireAuthForcedLoginIgnoresStoredAPIURL` now passes, and both
`TestRequireAuthTargetsStoredAPIURL` and `TestRequireAuthFlagOverrideBeatsStoredAPIURL`
still pass **unchanged** — they use `force=false`, which keeps the pin.

- [ ] **Step 5: Commit**

```bash
cd ~/keld-signal
git add internal/auth/device.go internal/auth/device_test.go
git commit -m "fix(auth): a forced login no longer inherits the stored host"
```

---

### Task 3: `keld login --code` honours the code's host

**Files:**
- Modify: `~/keld-signal/internal/cli/login.go:32-51` (the `code != ""` branch)
- Test: `~/keld-signal/internal/cli/login_test.go`

**Interfaces:**
- Consumes: `auth.ParsePairingCode` (Task 1).
- Produces: no new symbols. `keld login --code atlas.keld.co/ABCD-EFGH` now enrolls against `https://atlas.keld.co` and prints `Pairing with https://atlas.keld.co`.

- [ ] **Step 1: Write the failing test**

Append to `~/keld-signal/internal/cli/login_test.go`. It stands up a stub enroll server and passes a pairing code whose host is that server, asserting the request landed there:

```go
// A pairing code carries the host that minted it: `keld login --code host/CODE`
// must enroll against that host, not the built-in default or a stored one.
func TestLoginCodeUsesHostFromPairingCode(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	paths.SetAPIBaseOverride("")
	defer paths.SetAPIBaseOverride("")

	var gotPath, gotCode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotCode = body["code"]
		_, _ = w.Write([]byte(`{"access_token":"AT","principal":"p@keld.co","org":"Keld"}`))
	}))
	defer srv.Close()

	cmd := newLoginCmd()
	// srv.URL is http://127.0.0.1:PORT — a scheme-qualified host, used as-is.
	cmd.SetArgs([]string{"--code", srv.URL + "/abcd-efgh"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if gotPath != "/v1/cli/enroll" {
		t.Fatalf("path = %q", gotPath)
	}
	// The bare, uppercased code reaches the server — not the whole pairing string.
	if gotCode != "ABCD-EFGH" {
		t.Fatalf("code = %q, want ABCD-EFGH", gotCode)
	}
	stored, err := auth.Load()
	if err != nil || stored == nil {
		t.Fatalf("Load: %v %v", stored, err)
	}
	if stored.APIURL != srv.URL {
		t.Fatalf("stored APIURL = %q, want %q", stored.APIURL, srv.URL)
	}
}
```

Add whatever of `encoding/json`, `net/http`, `net/http/httptest`, `testing`, `github.com/ncx-ai/keld-signal/internal/auth`, `github.com/ncx-ai/keld-signal/internal/paths` the file does not already import.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ~/keld-signal && go test ./internal/cli/ -run TestLoginCodeUsesHostFromPairingCode`
Expected: FAIL — the request goes to `https://atlas.keld.co` (or the stub is never called), and `gotCode` is the full pairing string rather than `ABCD-EFGH`.

- [ ] **Step 3: Wire the parser into the code branch**

In `~/keld-signal/internal/cli/login.go`, replace the whole `if code != "" { ... }` block with:

```go
			code, _ := cmd.Flags().GetString("code")
			if code != "" {
				host, bare, perr := auth.ParsePairingCode(code)
				if perr != nil {
					if jsonOut {
						emitEvent(errorEvent{Event: "error", Message: cleanErrorMessage(perr)})
						return errs.ErrSilentExit
					}
					return perr
				}
				code = bare
				// An explicit --api-url is the higher-precedence signal and already set
				// the override above; the code's host only fills the gap when it didn't.
				if host != "" && apiURL == "" {
					paths.SetAPIBaseOverride(host)
				}
				if !jsonOut {
					console.Print("")
					console.Print(fmt.Sprintf("Pairing with %s", paths.APIBase()))
					console.Print("Signing in…")
				}
				a, err := auth.LoginWithCode(api.NewClient(paths.APIBase(), ""), code)
				if err != nil {
					if jsonOut {
						emitEvent(errorEvent{Event: "error", Message: cleanErrorMessage(err)})
						return errs.ErrSilentExit
					}
					return err
				}
				if jsonOut {
					emitEvent(authorizedEvent{Event: "authorized", Principal: a.Principal, Org: a.Org})
				} else {
					console.Print(fmt.Sprintf("  ✓ %s · org %s", a.Principal, a.Org))
				}
				return nil
			}
```

- [ ] **Step 4: Run the cli package**

Run: `cd ~/keld-signal && go test ./internal/cli/ -v -run TestLogin`
Expected: PASS, including the pre-existing login tests.

- [ ] **Step 5: Commit**

```bash
cd ~/keld-signal
git add internal/cli/login.go internal/cli/login_test.go
git commit -m "feat(login): --code honours the host carried by a pairing code"
```

---

### Task 4: `keld-agent install` forwards the code's host to both children

This is the task most likely to regress: if only `login` learns the host, `signal setup` re-resolves independently and writes the old endpoint into `hook.json`.

**Files:**
- Modify: `~/keld-signal/internal/agentcli/agentcli.go:110-121` (top of `runInstall`)
- Test: `~/keld-signal/internal/agentcli/agentcli_test.go`

**Interfaces:**
- Consumes: `auth.ParsePairingCode` (Task 1).
- Produces: no new symbols. `runInstall` now passes the **bare** code to `keld login --code` and `--api-url <host>` to **both** `login` and `signal setup`.

- [ ] **Step 1: Write the failing test**

Append to `~/keld-signal/internal/agentcli/agentcli_test.go`:

```go
// A pairing code must set --api-url on BOTH child commands. If only login learned
// the host, `signal setup` would re-resolve on its own and write the previous
// endpoint into hook.json — a split-brain install that looks like it worked.
func TestRunInstallPairingCodeSetsAPIURLOnBothSteps(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "atlas.keld.co/abcd-efgh"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{
		"/fake/keld login --api-url https://atlas.keld.co --code ABCD-EFGH",
		"/fake/keld signal setup --api-url https://atlas.keld.co --yes",
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}

// An explicit --api-url outranks the host carried by the code.
func TestRunInstallFlagBeatsPairingCodeHost(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "atlas.keld.co/ABCD-EFGH", apiURL: "http://localhost:8000"},
		func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	for _, c := range *calls {
		if !strings.Contains(c, "--api-url http://localhost:8000") {
			t.Fatalf("explicit flag should win in every step, got %v", *calls)
		}
	}
}

// A bare code still works exactly as before — no host, no --api-url.
func TestRunInstallBareCodeUnchanged(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "ABCD-EFGH"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login --code ABCD-EFGH", "/fake/keld signal setup --yes"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd ~/keld-signal && go test ./internal/agentcli/ -run TestRunInstall`
Expected: the two new pairing tests FAIL (no `--api-url` is emitted, and the code is passed through un-parsed and lowercase); `TestRunInstallBareCodeUnchanged` and the pre-existing sequence tests PASS.

- [ ] **Step 3: Parse the code at the top of `runInstall`**

In `~/keld-signal/internal/agentcli/agentcli.go`, insert at the very start of `runInstall`, before `login := []string{"login"}`:

```go
	// A setup code may carry the host that minted it. Resolve it HERE rather than
	// letting `keld login` do it alone: cfg.apiURL is what puts --api-url on both
	// child commands, and `signal setup` must target the same host as the login or
	// it writes the previous endpoint into hook.json.
	if cfg.code != "" {
		host, bare, err := auth.ParsePairingCode(cfg.code)
		if err != nil {
			return err
		}
		cfg.code = bare
		if host != "" && cfg.apiURL == "" {
			cfg.apiURL = host
		}
	}
```

Add `"github.com/ncx-ai/keld-signal/internal/auth"` to the file's imports.

- [ ] **Step 4: Run the agentcli package**

Run: `cd ~/keld-signal && go test ./internal/agentcli/ -v`
Expected: PASS, all tests including the pre-existing `TestRunInstallSequence`.

- [ ] **Step 5: Run the full Go suite and commit**

```bash
cd ~/keld-signal
go test ./internal/...
git add internal/agentcli/agentcli.go internal/agentcli/agentcli_test.go
git commit -m "feat(install): forward a pairing code's host to login and setup"
```

Expected: the whole suite is green before committing.

---

### Task 5: Atlas — the install one-liner sets `KELD_API_URL`

**Files:**
- Modify: `~/keld-atlas/services/api/app/telemetry_snippets.py:14-48`
- Test: `~/keld-atlas/services/api/tests/test_telemetry_snippets.py`

**Interfaces:**
- Consumes: nothing from Tasks 1-4 (different repo, independent).
- Produces: `context_hook_install_cmd()` and `quick_setup_cmd(source)` both emit `| KELD_API_URL={base} sh`. The module-level constant `_CLI_DEFAULT_API_URL` is removed.

- [ ] **Step 1: Branch keld-atlas**

```bash
cd ~/keld-atlas
git checkout -b feat/installer-host-pairing
```

The repo is currently on `feat/markets-quantity-units` with an untracked `env.example`; leave that file alone.

- [ ] **Step 2: Write the failing test**

Append to `~/keld-atlas/services/api/tests/test_telemetry_snippets.py`:

```python
def test_install_cmd_sets_keld_api_url_on_prod(monkeypatch):
    """Unconditional, INCLUDING prod. The bug this fixes was a prod install losing to
    a stored localhost, so skipping the var when base == prod reintroduces it."""
    monkeypatch.setattr(settings, "otlp_public_url", "https://atlas.keld.co")
    cmd = telemetry_snippets.context_hook_install_cmd()
    assert "| KELD_API_URL=https://atlas.keld.co sh" in cmd


def test_install_cmd_sets_keld_api_url_on_non_prod(monkeypatch):
    monkeypatch.setattr(settings, "otlp_public_url", "http://localhost:8000")
    cmd = telemetry_snippets.context_hook_install_cmd()
    assert "| KELD_API_URL=http://localhost:8000 sh" in cmd


def test_quick_setup_cmd_pairs_install_and_setup(monkeypatch):
    """Both halves of the chained command target the same host: the install step via
    KELD_API_URL, the setup step via --api-url."""
    monkeypatch.setattr(settings, "otlp_public_url", "https://atlas.keld.co")
    cmd = telemetry_snippets.quick_setup_cmd("claude_code")
    assert "| KELD_API_URL=https://atlas.keld.co sh" in cmd
    assert "--api-url https://atlas.keld.co" in cmd
    assert "--tool claude_code" in cmd
```

Match the existing file's import style for `settings` and `telemetry_snippets`; if it imports names directly, adapt these to do the same.

- [ ] **Step 3: Run tests to verify they fail**

```bash
cd ~/keld-atlas && docker compose run --rm --no-deps \
  -e KELD_TEST_DATABASE_URL=postgresql+asyncpg://keld:keld@postgres:5432/keld_test \
  --entrypoint sh api -c "pip install -e .[test] && pytest -q tests/test_telemetry_snippets.py"
```
Expected: the three new tests FAIL — no `KELD_API_URL` in either command.

- [ ] **Step 4: Update both command builders**

In `~/keld-atlas/services/api/app/telemetry_snippets.py`, replace `context_hook_install_cmd` with:

```python
def context_hook_install_cmd() -> str:
    """One-line install of the dependency-free `keld` binary, which provides the context hook
    (run as `keld __hook`). Served per-deploy at {api_base}/install.sh — no Python, no separate
    script or token download. KELD_API_URL pairs the install with THIS deploy: install.sh reads
    it and passes it as --api-url, without which `keld login` silently re-authenticates against
    whatever host a previous install left in ~/.keld/auth.json."""
    base = settings.otlp_public_url.rstrip("/")
    return f"curl -fsSL {base}/install.sh | KELD_API_URL={base} sh"
```

and replace `quick_setup_cmd` with:

```python
def quick_setup_cmd(source: str) -> str:
    """One-line install + `keld signal setup` for a single tool. Installs the dependency-free
    `keld` binary via the published install script (no Python/pip needed), then runs setup; the
    binary lands in ~/.local/bin, which we invoke by full path so the chained command works even
    before that dir is on PATH. No secret is templated in — the CLI device-logs-in and pulls
    endpoint/token from the API itself.

    Both halves are pinned to this deploy, ALWAYS — including prod. Pinning only non-prod
    deploys (what this used to do) leaves a prod install inheriting a stored localhost from an
    earlier dev install, which is the exact failure this pairing work exists to fix."""
    tool = CLI_TOOL_NAMES.get(source, source)
    base = settings.otlp_public_url.rstrip("/")
    return (
        f"curl -fsSL {base}/install.sh | KELD_API_URL={base} sh"
        f" && ~/.local/bin/keld signal setup --tool {tool} --api-url {base}"
    )
```

Delete the now-unused `_CLI_DEFAULT_API_URL` constant and its comment (lines 27-28).

- [ ] **Step 5: Run the snippet tests**

```bash
cd ~/keld-atlas && docker compose run --rm --no-deps \
  -e KELD_TEST_DATABASE_URL=postgresql+asyncpg://keld:keld@postgres:5432/keld_test \
  --entrypoint sh api -c "pip install -e .[test] && pytest -q tests/test_telemetry_snippets.py tests/test_snippets_hooks.py"
```
Expected: PASS. If a pre-existing test asserted the old bare `| sh` form or the conditional
`--api-url`, update it to the new expected string — that assertion is the thing being changed.

- [ ] **Step 6: Commit**

```bash
cd ~/keld-atlas
git add services/api/app/telemetry_snippets.py services/api/tests/test_telemetry_snippets.py
git commit -m "fix(snippets): pin the install one-liner to this deploy with KELD_API_URL"
```

---

### Task 6: Atlas — the setup code carries its host

**Files:**
- Modify: `~/keld-atlas/services/api/app/schemas.py` (`EnrollCodeOut`)
- Modify: `~/keld-atlas/services/api/app/routers/cli.py:131-141` (`enroll_code`)
- Modify: `~/keld-atlas/services/web/hooks/use-enroll-code.ts`
- Modify: `~/keld-atlas/services/web/components/signal/install-panel.tsx`
- Modify: `~/keld-atlas/services/web/components/signal/signal-wizard-install.tsx`
- Test: `~/keld-atlas/services/api/tests/test_cli_enroll_code.py` (create if absent)
- Test: `~/keld-atlas/services/web/components/signal/install-panel.test.tsx`

**Interfaces:**
- Consumes: the format produced by `auth.ParsePairingCode` (Task 1) — `{host}/{CODE}`, scheme omitted for https, kept for http.
- Produces: `EnrollCodeOut.pairing_code: str` alongside the existing `code`. `code` is **kept** so nothing that reads it breaks.

- [ ] **Step 1: Write the failing API test**

Create or append to `~/keld-atlas/services/api/tests/test_cli_enroll_code.py`. Follow the authenticated-client fixture the neighbouring router tests use:

```python
import pytest


@pytest.mark.asyncio
async def test_enroll_code_returns_pairing_code_https(admin_client, monkeypatch):
    """An https deploy renders host-only — the CLI re-adds https://. Keeping the scheme
    would work too but reads badly on a download page."""
    from app.config import settings
    monkeypatch.setattr(settings, "otlp_public_url", "https://atlas.keld.co")
    r = await admin_client.post("/api/cli/enroll-code")
    assert r.status_code == 200
    body = r.json()
    assert body["pairing_code"] == f"atlas.keld.co/{body['code']}"


@pytest.mark.asyncio
async def test_enroll_code_pairing_code_keeps_http_scheme(admin_client, monkeypatch):
    """A dev deploy must keep http:// — the CLI cannot infer it for a non-loopback host."""
    from app.config import settings
    monkeypatch.setattr(settings, "otlp_public_url", "http://localhost:8000")
    r = await admin_client.post("/api/cli/enroll-code")
    body = r.json()
    assert body["pairing_code"] == f"http://localhost:8000/{body['code']}"
```

Adapt `admin_client` to whatever authenticated fixture `tests/conftest.py` exposes for
`web_router` endpoints (they require `get_current_user`).

- [ ] **Step 2: Run to verify it fails**

```bash
cd ~/keld-atlas && docker compose run --rm --no-deps \
  -e KELD_TEST_DATABASE_URL=postgresql+asyncpg://keld:keld@postgres:5432/keld_test \
  --entrypoint sh api -c "pip install -e .[test] && pytest -q tests/test_cli_enroll_code.py"
```
Expected: FAIL — `KeyError: 'pairing_code'`.

- [ ] **Step 3: Add the field and build it**

In `~/keld-atlas/services/api/app/schemas.py`, add to `EnrollCodeOut`:

```python
    # The code prefixed with this deploy's host, e.g. "atlas.keld.co/ABCD-EFGH".
    # The installer parses the host out and pairs with it, so a re-install can move
    # a machine between deploys instead of inheriting its stored one.
    pairing_code: str
```

In `~/keld-atlas/services/api/app/routers/cli.py`, replace the `return EnrollCodeOut(...)` line at the end of `enroll_code` with:

```python
    # Host-qualified so the installer pairs with THIS deploy. https is rendered
    # scheme-less because the CLI re-adds it; http must keep its scheme, since the
    # CLI only infers http for loopback hosts and would otherwise try https.
    base = settings.otlp_public_url.rstrip("/")
    host = base[len("https://"):] if base.startswith("https://") else base
    return EnrollCodeOut(
        code=user_code,
        pairing_code=f"{host}/{user_code}",
        expires_in=settings.cli_device_ttl_s,
    )
```

- [ ] **Step 4: Run the API test**

```bash
cd ~/keld-atlas && docker compose run --rm --no-deps \
  -e KELD_TEST_DATABASE_URL=postgresql+asyncpg://keld:keld@postgres:5432/keld_test \
  --entrypoint sh api -c "pip install -e .[test] && pytest -q tests/test_cli_enroll_code.py tests/test_cli.py"
```
Expected: PASS.

- [ ] **Step 5: Write the failing web test**

In `~/keld-atlas/services/web/components/signal/install-panel.test.tsx`, extend the
`use-enroll-code` mock to return `pairing_code` and assert it is what renders. The existing
mock is at the top of the file; add `pairing_code: "atlas.keld.co/ABCD-EFGH"` beside the
existing `code` value, then add:

```tsx
  it("renders the host-qualified pairing code, not the bare code", () => {
    render(<InstallPanel />);
    expect(screen.getByText("atlas.keld.co/ABCD-EFGH")).toBeInTheDocument();
  });
```

Match the file's existing render helper and import style.

- [ ] **Step 6: Run to verify it fails**

Run: `cd ~/keld-atlas/services/web && pnpm run test -- install-panel`
Expected: FAIL — the bare code renders instead.

- [ ] **Step 7: Render the pairing code**

In `~/keld-atlas/services/web/hooks/use-enroll-code.ts`, add `pairing_code: string` to the
response type. In `install-panel.tsx` and `signal-wizard-install.tsx`, render `pairing_code`
wherever `code` is currently displayed or copied. Leave the `code` field in the type — other
callers may still read it.

- [ ] **Step 8: Run the web tests**

Run: `cd ~/keld-atlas/services/web && pnpm run test`
Expected: PASS. Update `signal-wizard-install.test.tsx`'s mock the same way if it fails on the
missing field.

- [ ] **Step 9: Commit**

```bash
cd ~/keld-atlas
git add services/api/app/schemas.py services/api/app/routers/cli.py \
        services/api/tests/test_cli_enroll_code.py \
        services/web/hooks/use-enroll-code.ts \
        services/web/components/signal/install-panel.tsx \
        services/web/components/signal/install-panel.test.tsx \
        services/web/components/signal/signal-wizard-install.tsx
git commit -m "feat(signal): setup code carries the deploy it was minted by"
```

---

### Task 7: End-to-end verification against prod

The whole point is a behavior no unit test covers: a machine paired to localhost moving to prod.

**Files:** none — verification only.

- [ ] **Step 1: Confirm the starting state**

Run: `/usr/local/keld/keld whoami`
Expected: reports `localhost`, confirming the machine is still mis-paired.

- [ ] **Step 2: Build and install the patched CLI**

```bash
cd ~/keld-signal && go build -o /tmp/keld ./cmd/keld
```

Confirm the binary parses a pairing code without touching the machine:
`/tmp/keld login --help` should still list `--code` and `--api-url`.

- [ ] **Step 3: Verify the parser against a real prod code**

Mint a setup code from the Atlas download page, then run:

```bash
/tmp/keld login --code "atlas.keld.co/<CODE>"
```

Expected: prints `Pairing with https://atlas.keld.co`, then `✓ <principal> · org <org>` with the
**prod** principal — not `admin@acme.test`.

- [ ] **Step 4: Confirm the pairing stuck**

Run: `/tmp/keld whoami`
Expected: `<principal> · org <org> · https://atlas.keld.co · endpoint https://atlas.keld.co`.

Then check `~/.keld/auth.json` holds `"api_url": "https://atlas.keld.co"`.

- [ ] **Step 5: Report**

Report the actual output of steps 3 and 4 verbatim. If the principal is still `admin@acme.test`,
the pairing did not take — stop and investigate rather than declaring success.

---

## Self-Review

**Spec coverage:**

| Spec section | Task |
|---|---|
| Precedence rule — pin move | Task 2 |
| Channel A (`curl \| sh`) — `KELD_API_URL` | Task 5 |
| Channel B (`.pkg`) — pairing code parsing | Tasks 1, 3, 4 |
| Pairing code format + backward compat | Task 1 |
| `Pairing with <host>` in `login.go` | Task 3 |
| `runInstall` sets `cfg.apiURL` for both children | Task 4 |
| Atlas renders the pairing string | Task 6 |
| End-to-end verification | Task 7 |
| Out of scope (429 mislabel, `doctor`, `/install.sh` repo rename) | not planned, by design |

**Deviations from the spec,** both recorded in Global Constraints: no trailing-slash trim (it
creates a host/code ambiguity), and `TestRequireAuthTargetsStoredAPIURL` needs no narrowing
because it is already on the reuse path.

**Type consistency:** `ParsePairingCode(s string) (apiBase, code string, err error)` is defined in
Task 1 and called with that exact signature in Tasks 3 and 4. `EnrollCodeOut.pairing_code` is
added in Task 6 Step 3 and consumed in Step 7. `cfg.apiURL` / `cfg.code` match the existing
`installConfig` fields.
