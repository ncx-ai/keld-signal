# Pre-auth onboarding — keld-cli (piece 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add non-interactive `keld login --code <CODE>` (redeem a one-time setup code minted by Atlas), and replace the fragile macOS SwiftUI onboarding app with a Terminal script that runs the CLI directly (code-redeem → `signal setup --yes` → `keld-agent install`, with interactive `keld login` as fallback).

**Architecture:** `keld login --code` redeems via a new `POST /v1/cli/enroll {code}` → `{access_token, principal, org}` → `auth.Save` (same end-state as the device flow). The macOS pkg `postinstall` opens `onboard.command` (a Terminal script) instead of the removed `KeldSetup.app`.

**Tech Stack:** Go; the existing `internal/api`, `internal/auth`, `internal/cli` packages; bash for the installer scripts.

**Spec:** `docs/superpowers/specs/2026-07-13-signal-preauth-onboarding-design.md`.

## Global Constraints
- `keld login --code` is a NON-interactive redemption path; interactive `keld login` (device flow) is UNCHANGED and remains the fallback. `--json`, `--api-url`, `--no-browser` unchanged.
- Redemption contract: `POST /v1/cli/enroll {"code": "<CODE>"}` → `200 {access_token, principal, org}` (same shape as `device/poll` success) / `401|410` on invalid|expired|used. The CLI honors `--api-url`.
- Installer still installs BOTH `keld` + `keld-agent`. Onboarding precedes the agent: `keld-agent install` runs LAST.
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Tests: `go test ./...`; `go build ./...`; `gofmt`. Installer scripts: `shellcheck` (if available) + content assertions.

---

## Task 1: `keld login --code` (redeem a setup code)

**Files:** modify `internal/api/client.go`, `internal/auth/device.go`, `internal/cli/login.go`; add tests to `internal/api/client_test.go` (or a new one), `internal/auth/device_test.go`, `internal/cli/login_test.go`.

**Interfaces produced:**
- `func (c *api.Client) Enroll(code string) (map[string]any, error)` — `POST /v1/cli/enroll`.
- `func auth.LoginWithCode(c *api.Client, code string) (*AuthData, error)` — redeem + persist.
- `keld login --code <CODE>`.

- [ ] **Step 1: Write failing tests.**
  - `internal/api` (mirror any existing client test; use httptest): `Enroll` posts `{"code":"..."}` to `/v1/cli/enroll` and returns the decoded map on 200; on 401/410 returns an error whose message mentions an invalid/expired code.
  ```go
  func TestEnrollSuccess(t *testing.T) {
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          if r.URL.Path != "/v1/cli/enroll" { t.Fatalf("path %s", r.URL.Path) }
          var body map[string]string; json.NewDecoder(r.Body).Decode(&body)
          if body["code"] != "AB12-CD34" { t.Fatalf("code %q", body["code"]) }
          w.Header().Set("content-type","application/json")
          w.Write([]byte(`{"access_token":"tok","principal":"dg@keld.co","org":"Acme"}`))
      }))
      defer srv.Close()
      res, err := api.NewClient(srv.URL, "").Enroll("AB12-CD34")
      if err != nil { t.Fatal(err) }
      if res["access_token"] != "tok" || res["org"] != "Acme" { t.Fatalf("res %v", res) }
  }
  func TestEnrollExpired(t *testing.T) {
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }))
      defer srv.Close()
      if _, err := api.NewClient(srv.URL, "").Enroll("x"); err == nil { t.Fatal("want error on 410") }
  }
  ```
  - `internal/auth` (mirror device_test.go's httptest + KELD_HOME tmp): `LoginWithCode` against a stub `/v1/cli/enroll` writes `auth.json` with the returned principal/org/access_token + the client BaseURL as api_url.
  - `internal/cli` (mirror login_test.go): `keld login --code AB12-CD34 --api-url <stub>` exits 0 and persists auth; a stub returning 410 makes it exit non-zero with a clear message.

- [ ] **Step 2: Run → fail** (`go test ./internal/api/... ./internal/auth/... ./internal/cli/...`).

- [ ] **Step 3: Implement.**
  - `api/client.go` — add (mirrors `DevicePoll`):
  ```go
  // Enroll calls POST /v1/cli/enroll to redeem a one-time setup code, returning
  // the same {access_token, principal, org} payload as a successful device poll.
  func (c *Client) Enroll(code string) (map[string]any, error) {
      body, _ := json.Marshal(map[string]string{"code": code})
      resp, err := c.post("/v1/cli/enroll", body)
      if err != nil { return nil, err }
      defer resp.Body.Close()
      if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusGone {
          return nil, errs.New("invalid or expired setup code")
      }
      if err := checkStatus(resp); err != nil { return nil, err }
      var result map[string]any
      if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
          return nil, errs.New("Atlas returned invalid JSON: %v", err)
      }
      return result, nil
  }
  ```
  - `auth/device.go` — factor the token-persist out of `Login` into a shared helper, then add `LoginWithCode`:
  ```go
  // persistToken validates a device/enroll result map and saves it as AuthData.
  func persistToken(result map[string]any, apiURL string) (*AuthData, error) {
      str := func(k string) (string, bool) { s, ok := result[k].(string); return s, ok }
      at, ok1 := str("access_token"); pr, ok2 := str("principal"); org, ok3 := str("org")
      if !ok1 || !ok2 || !ok3 { return nil, errs.New("Atlas returned an unexpected response") }
      a := AuthData{AccessToken: at, Principal: pr, Org: org, APIURL: apiURL}
      if err := Save(a); err != nil { return nil, err }
      return &a, nil
  }

  // LoginWithCode redeems a one-time setup code (non-interactive; no browser) and
  // persists the resulting credentials.
  func LoginWithCode(c *api.Client, code string) (*AuthData, error) {
      result, err := c.Enroll(code)
      if err != nil { return nil, err }
      return persistToken(result, c.BaseURL)
  }
  ```
  Refactor `Login`'s success block to call `persistToken(result, c.BaseURL)` (behavior-preserving — same fields).
  - `cli/login.go` — add the flag + the code path (place BEFORE the device-flow branches):
  ```go
  cmd.Flags().String("code", "", "Redeem a one-time setup code (non-interactive; skips the browser login).")
  // ... in RunE, after resolving apiURL/paths.SetAPIBaseOverride and reading jsonOut:
  code, _ := cmd.Flags().GetString("code")
  if code != "" {
      a, err := auth.LoginWithCode(api.NewClient(paths.APIBase(), ""), code)
      if err != nil {
          if jsonOut { emitEvent(errorEvent{Event: "error", Message: err.Error()}); return errs.ErrSilentExit }
          return err
      }
      if jsonOut { emitEvent(authorizedEvent{Event: "authorized", Principal: a.Principal, Org: a.Org}) } else {
          console.Print(fmt.Sprintf("Logged in as %s (org: %s)", a.Principal, a.Org))
      }
      return nil
  }
  ```

- [ ] **Step 4: Run → pass** (the three packages) + `go build ./...` + `go test ./...` (no regressions — the `Login` refactor must keep device_test green) + `gofmt -l internal/` empty.

- [ ] **Step 5: Commit** `feat(cli): keld login --code redeems a one-time setup code (non-interactive enroll)`.

---

## Task 2: macOS Terminal onboarding (remove SwiftUI app)

**Files:** delete `installers/macos/KeldSetup/` (recursively), `installers/macos/build-app.sh`, `installers/macos/Info.plist`; modify `installers/macos/build-pkg.sh`, `installers/macos/scripts/postinstall`, `AGENTS.md`; create `installers/macos/onboard.command`, `installers/macos/onboard_command_test.sh` (or a Go/bash content test).

- [ ] **Step 1: Write the failing check.** Create `installers/macos/onboard_command_test.sh` (a bash assertion script, run with `bash`): asserts `onboard.command` exists, is executable-friendly (has a shebang), and contains — in order — the fallback-aware `keld login --code`, an interactive `keld login` fallback, `keld signal setup --yes`, and `keld-agent install`; and asserts `build-pkg.sh` no longer references `build-app.sh`/`KeldSetup`, and `postinstall` no longer references `KeldSetup.app` but does `open` `onboard.command`. Run it → fails (script/edits absent).
  ```bash
  #!/usr/bin/env bash
  set -euo pipefail
  d="$(cd "$(dirname "$0")" && pwd)"
  cmd="$d/onboard.command"
  test -f "$cmd" || { echo "missing onboard.command"; exit 1; }
  head -1 "$cmd" | grep -q '^#!' || { echo "no shebang"; exit 1; }
  grep -q 'keld login --code' "$cmd" || { echo "no code redeem"; exit 1; }
  grep -q 'signal setup --yes' "$cmd" || { echo "no setup --yes"; exit 1; }
  grep -q 'keld-agent install' "$cmd" || { echo "no agent install"; exit 1; }
  grep -q 'KeldSetup' "$d/build-pkg.sh" && { echo "build-pkg still refs KeldSetup"; exit 1; } || true
  grep -q 'KeldSetup.app' "$d/scripts/postinstall" && { echo "postinstall still refs app"; exit 1; } || true
  grep -q 'onboard.command' "$d/scripts/postinstall" || { echo "postinstall does not open onboard.command"; exit 1; }
  echo "onboard checks passed"
  ```

- [ ] **Step 2: Run → fail.**

- [ ] **Step 3: Implement.**
  - `rm -rf installers/macos/KeldSetup installers/macos/build-app.sh installers/macos/Info.plist`.
  - `installers/macos/onboard.command` (payload → `/usr/local/keld/onboard.command`):
  ```bash
  #!/bin/bash
  # Keld setup — runs after install. Redeems your one-time setup code (from the Keld
  # download page) for a non-interactive login, configures your AI tools, then starts
  # the background agent. Safe to re-run.
  set -uo pipefail
  KELD="/usr/local/bin/keld"; AGENT="/usr/local/bin/keld-agent"
  echo; echo "==== Set up Keld ===="; echo
  printf "Paste your setup code from the Keld download page (or press Enter to log in with a browser): "
  read -r CODE
  if [ -n "$CODE" ]; then
    "$KELD" login --code "$CODE" || { echo "Setup code didn't work; falling back to browser login…"; "$KELD" login || exit 1; }
  else
    "$KELD" login || exit 1
  fi
  "$KELD" signal setup --yes || exit 1
  "$AGENT" install || exit 1
  echo; echo "Keld is set up and running. You can close this window."; echo
  echo "(Re-run anytime: /usr/local/keld/onboard.command)"
  ```
  Make it executable in the payload (the build stages it; `chmod +x`).
  - `build-pkg.sh`: remove the `bash "$ROOT/build-app.sh" "$STAGE"` line and the entire `if [ -d "$STAGE/KeldSetup.app" ]; then codesign … ; fi` block. (Ensure `onboard.command` ships executable — either it's committed executable and copied by the caller staging the payload, or add a `chmod +x "$STAGE/onboard.command"` after staging; confirm how the payload STAGE is assembled and that onboard.command lands in it — if the CI stages specific files, add onboard.command to that list.)
  - `postinstall`: remove the `keld-agent install` pre-registration line and the `if [ -d "$PREFIX/KeldSetup.app" ]; …open…KeldSetup.app…` block; add:
  ```bash
  # Onboarding runs in the user's GUI session and registers/starts the agent itself
  # (after login + setup), so the agent isn't started before the user is set up.
  if [ -f "$PREFIX/onboard.command" ]; then
    chmod +x "$PREFIX/onboard.command" || true
    launchctl asuser "$uid" sudo -u "$user" open "$PREFIX/onboard.command" || true
  fi
  ```
  (Keep the CLI symlinks. Note: `keld-agent install` is no longer in postinstall — it's in onboard.command. If a fully-headless service registration is still wanted as a safety net, that's a separate decision; per the spec, onboarding precedes the agent, so we do NOT pre-register.)
  - `AGENTS.md`: update the "macOS onboarding UI" gotcha to describe the Terminal `onboard.command` flow (code redeem → setup → agent install; interactive login fallback; no SwiftUI app).
  - Confirm CI `installers.yml` doesn't separately reference `build-app.sh`/KeldSetup (per inspection it only calls `build-pkg.sh`); if it does, update it.

- [ ] **Step 4: Run the checks** — `bash installers/macos/onboard_command_test.sh` passes; `shellcheck installers/macos/onboard.command installers/macos/scripts/postinstall` (if shellcheck present) clean or only benign warnings; `grep -r KeldSetup installers/ .github/` returns nothing (app fully removed). `go build ./...` still OK (no Go touched).

- [ ] **Step 5: Commit** `feat(installer): macOS Terminal onboarding (onboard.command); remove SwiftUI KeldSetup app`.

---

## Self-Review
- Spec coverage: `keld login --code` + `api.Enroll` + persist (T1) ✓; SwiftUI removal + `onboard.command` (code→setup→agent, browser fallback) + postinstall rewrite + build-pkg cleanup + docs (T2) ✓. Onboarding-precedes-agent (agent install last, in the script) ✓. Installs both binaries (unchanged) ✓.
- Placeholders: none — Enroll/LoginWithCode/login-code + the onboard.command + postinstall edits are concrete; tests concrete. The one flagged verification (how the pkg STAGE assembles the payload → ensure onboard.command lands + is executable) directs the implementer to confirm against build-pkg.sh/CI, not skip.
- Type consistency: `Enroll` map shape ↔ `persistToken` extraction ↔ `AuthData` fields; `LoginWithCode` used by `login --code`; the `Login` refactor reuses `persistToken` (device flow unchanged behaviorally). Redeem contract `/v1/cli/enroll {code}` matches the Atlas piece (piece 2).
- Fallback: onboard.command falls back to interactive `keld login` on empty/failed code; interactive login path in login.go untouched.
