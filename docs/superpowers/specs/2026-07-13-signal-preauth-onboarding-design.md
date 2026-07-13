# Design: pre-authenticated installer onboarding via a one-time setup code

**Date:** 2026-07-13
**Status:** design (brainstorm decisions locked), pending review
**Repos:** keld-cli (CLI `login --code` + macOS installer onboarding) + keld-atlas
(`services/api` mint+redeem endpoints, `services/web` download-page code UI)

## Problem

Installer onboarding today is fragile and interactive. On macOS a separate SwiftUI
app (`installers/macos/KeldSetup`) is `open`ed by the pkg `postinstall` to run
`keld login --json` / `keld signal setup --json`; a colleague's install showed **no
app at all** (likely Gatekeeper blocking the unsigned/un-notarized app, or the
cross-session root→GUI `open` failing — all swallowed by `|| true`). On Windows there
is **no** in-installer onboarding at all. We want onboarding to (a) be robust, (b)
drive the already-installed CLI directly, and (c) skip the interactive browser login
by **pre-authenticating** off the downloader's existing Atlas identity.

## Decisions (locked via brainstorm)

- **macOS onboarding is a Terminal running the CLI** (the SwiftUI app is removed).
- **Pre-auth via a one-time "setup code"** shown on the authenticated download page;
  the user pastes it during onboarding. (Chosen over a companion file / bootstrapper.)
- **Identity = downloader self-setup**: the code is bound to the downloader's
  principal + org, single-use, short-lived.
- **Go straight to pre-auth** (no interim interactive-only step); interactive
  `keld login` (device flow) remains the **fallback** when there's no code.
- The installer still installs **both `keld` and `keld-agent`** (unchanged).
- Onboarding **precedes** starting the bg agent: `keld-agent install` runs last.

## The setup code = a pre-approved device grant (reuse, don't reinvent)

`keld login` already uses an OAuth2 **device flow**: `POST /v1/cli/device/start` →
`{device_code, user_code, verification_url, interval, expires_in}`; the user approves
in the browser (authed web) via `POST /api/cli/device/approve`; `POST /v1/cli/device/poll
{device_code}` → `202` pending / `200 {access_token, principal, org}` → `auth.Save`.

The setup code is simply a device grant that is **pre-approved at mint time** (because
the person minting it is already authenticated in the web app), redeemed by the
**user-visible code** instead of the device_code. Minimal new surface:

- **Mint** (Atlas, authed web): `POST /api/cli/enroll-code` (session cookie →
  `get_current_user` → org + principal). Creates an **already-approved** grant bound
  to that principal+org, single-use, TTL ~15 min, and returns
  `{ code: "<XXXX-XXXX>", expires_in }`. The `code` is the redemption secret shown on
  the download page.
- **Redeem** (Atlas, public): `POST /v1/cli/enroll { code }` → validate (exists,
  approved, unexpired, **unused**) → mark used (single-use) → return
  `{ access_token, principal, org }` (identical shape to a successful device/poll).
  Rate-limited; 401/410 on invalid/expired/used.

Code format: reuse the device `user_code` style — human-pasteable, dash-grouped, high
entropy (≳40 bits) so single-use + 15-min TTL makes guessing infeasible. Admin-revocable.

## keld-cli changes

- **`api.Client.Enroll(code string)`** → `POST /v1/cli/enroll {code}`; returns the
  `{access_token, principal, org}` map (mirrors `DevicePoll`'s decode). 401/410 →
  a typed "invalid or expired setup code" error.
- **`keld login --code <CODE>`**: a non-interactive redemption path in `login.go`.
  When `--code` is set: skip the device flow entirely — call `Enroll`, build
  `auth.AuthData{AccessToken, Principal, Org, APIURL: c.BaseURL}`, `auth.Save`, print
  the same `Logged in as <principal> (org: <org>)`. Honors `--api-url`. On failure,
  returns a clear error (so the onboarding script can fall back to interactive login).
  Interactive `keld login` (device flow) is unchanged and is the fallback.
- **`keld signal setup --yes`** already exists — no change. (The onboarding runs
  login-via-code, then setup, then agent install.)

## macOS installer changes (this repo)

- **Remove the SwiftUI app**: delete `installers/macos/KeldSetup/` (Swift sources +
  `Package.swift`), `installers/macos/build-app.sh`, `installers/macos/Info.plist`;
  drop the `build-app.sh` call + the `KeldSetup.app` codesign block from
  `build-pkg.sh`; the mac pkg build no longer needs the Swift toolchain.
- **Add `installers/macos/onboard.command`** (shipped in the payload at
  `/usr/local/keld/onboard.command`): an interactive Terminal script that
  1. prints a short header,
  2. prompts: *"Paste your setup code from the Keld download page (or press Enter to
     log in with a browser):"*,
  3. if a code is entered → `keld login --code "$CODE"`; **fallback** (empty/failed) →
     `keld login` (interactive device flow),
  4. then `keld signal setup --yes`,
  5. then `keld-agent install` (register + start the agent — last),
  6. prints a success/next-steps message; on any step failure, prints how to re-run
     (`/usr/local/keld/onboard.command`) and that it's idempotent.
- **`postinstall`**: keep the CLI symlinks; **remove** the headless service
  pre-registration and the app-launch; instead open the onboarding Terminal in the
  logged-in user's GUI session: `launchctl asuser $uid sudo -u $user open
  /usr/local/keld/onboard.command`. (Agent registration now happens inside the
  onboarding script, after login+setup.)
- A pkg-installed `.command` under `/usr/local/keld` isn't Gatekeeper-quarantined like
  the downloaded `.app` was, and a failure is a visible Terminal, not an invisible app.

## keld-atlas changes

- **`services/api`**: `POST /api/cli/enroll-code` (authed) mints the pre-approved
  grant + returns the code; `POST /v1/cli/enroll` (public) redeems it. Reuse the
  existing device-grant storage/token issuance (add a "pre-approved at creation" path
  + redeem-by-user-code + single-use consumption). Rate-limit redeem.
- **`services/web`** (customer app :3000 — where installer downloads + the existing
  `keld login` approval page live): on the download surface, call `enroll-code` and
  show the **setup code** + its expiry, with a "regenerate" control. Copy: "Paste this
  in the installer's setup window."

## Fallback & compatibility

- No code / expired / installer obtained out-of-band → `onboard.command` falls back to
  interactive `keld login` (device flow) → setup → agent install. Nothing regresses.
- `keld login --code` is additive; interactive `keld login`, `--json`, `--api-url`,
  `--no-browser` all unchanged. Atlas endpoints are additive (device flow untouched).

## Security

- Code: single-use, ~15-min TTL, bound to (org, principal) at mint, redeemed over
  HTTPS, redeem endpoint rate-limited, admin-revocable. Grants exactly one
  machine-enrollment for that user's org. Not embedded in the (signed) installer.

## Testing

- **keld-cli**: `api.Client.Enroll` against an httptest stub (success + 401/410);
  `keld login --code` writes the expected `auth.json` (mirror `device_test.go`
  patterns) and errors cleanly on a bad code; `shellcheck` `onboard.command` + assert
  the pkg payload contains it. macOS GUI/Terminal + pkg = manual/CI verification
  (no Swift/macOS in the Linux dev env — stated honestly).
- **keld-atlas api**: mint returns a code bound to the caller's org+principal;
  redeem returns the token for a valid code, 410 on used/expired, single-use (second
  redeem fails), cross-org isolation; rate-limit smoke.
- **keld-atlas web**: the download page renders the code + expiry + regenerate
  (vitest), calling the mint endpoint.

## Decomposition (→ per-repo plans, sequence)

1. **keld-cli**: `api.Client.Enroll` + `keld login --code` (+ tests); remove the
   SwiftUI app + `onboard.command` + `postinstall` rewrite; docs. (Testable now
   against an httptest stub.)
2. **keld-atlas `services/api`**: `enroll-code` mint + `/v1/cli/enroll` redeem
   (reuse device grant) + tests.
3. **keld-atlas `services/web`**: download-page setup-code UI + tests.
4. **End-to-end**: wire live (local Atlas + a real code + `keld login --code` +
   `signal setup`), verify enrollment writes auth.json/hook.json and the agent starts.

## Risks / notes

- The redeem endpoint is public + returns a session token → treat like the device
  flow's poll: rate-limit, single-use, short TTL, no user enumeration in errors.
- Downloader-self-setup means an installer handed to a colleague would (if they reuse
  the downloader's code) enroll under the downloader's identity — acceptable for the
  self-service model; fleet/org join-keys are a documented future addition.
