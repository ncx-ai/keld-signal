# Signal web sign-in — implementation plan

Spec (signed off 2026-09-29): `docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html`
(published: https://claude.ai/artifact/KnriC1Jogi1VD7DMiW7Xqe). All ten decisions D1–D10 approved as
recommended. The acceptance criteria AC-1…AC-12 in that page are the contract; nothing here overrides them.

## Shape of the work

Three repos, six parallel builders in phase 1, one integration + end-to-end phase, one grading phase.
The orchestrating session (the "lead") writes no feature code. It owns the contracts below, merges,
runs every suite, reviews every diff against the ACs, and sends work back.

| Agent | Repo · worktree · branch | Owns (files it may change) | ACs |
|---|---|---|---|
| `atlas-api` | keld-atlas · `.claude/worktrees/wsi-api` · `feat/signal-web-signin-api` | `services/api/**` | AC-2, AC-3 (OAuth half), AC-4, AC-7 (py), AC-9 (Atlas half) |
| `atlas-web` | keld-atlas · `.claude/worktrees/wsi-web` · `feat/signal-web-signin-web` | `services/web/**`, `.github/workflows/promote-prod.yml` | AC-3, AC-7 (ts), D4 gate |
| `signal-daemon` | keld-signal · `.claude/worktrees/wsi-daemon` · `feat/web-signin-daemon` | `internal/agent/ingress/**`, `internal/auth/**`, `internal/api/**`, `internal/paths/**`, `internal/agent/daemon/**` (route mounting only), `internal/conform/mockatlas/**` | AC-1, AC-5, AC-6, AC-8 |
| `signal-ui` | keld-signal · `.claude/worktrees/wsi-ui` · `feat/web-signin-ui` | `internal/agent/ui/**`, `ui/e2e/**`, `.github/workflows/ci.yml` | AC-5 (page half), AC-9 (Signal half), AC-12 |
| `installers` | keld-signal · `.claude/worktrees/wsi-installers` · `feat/web-signin-installers` | `installers/**`, `scripts/install.sh`, `scripts/install.ps1`, `.github/workflows/installers.yml`, `docs/install.md`, `docs/architecture/packaging-and-installers.md`, the installer bullets of `AGENTS.md` | AC-10, D10 |
| `canary` | keld-atlas-canary · `../_worktrees/canary-signal-signin` · `feat/signal-signin` | the whole repo | AC-11 |

Integration branches: keld-signal `feat/web-signin`, keld-atlas `feat/signal-web-signin` (both off
`main`). Nothing is pushed until the lead has graded the result.

## Frozen contracts

Every agent builds to these. A change to one goes through the lead, never agent-to-agent.

### C1 — Atlas `POST /api/cli/authorize/validate` and `POST /api/cli/authorize` (session-gated, `/api/*`)

Body (both): `{"redirect_uri": str, "state": str, "code_challenge": str, "code_challenge_method": "S256"}`

- `redirect_uri` is valid iff it matches `^http://127\.0\.0\.1:([0-9]{4,5})/auth/callback$` **exactly**
  (no surrounding whitespace, no control characters, no query, no fragment, no user info) and the port
  is 1024–65535. One helper, `valid_loopback_redirect(uri) -> bool`, in `services/api/app/cli_redirect.py`.
- `state` matches `^[A-Za-z0-9_-]{43,128}$`. `code_challenge` matches `^[A-Za-z0-9_-]{43}$`. Method is `S256`.
- `/validate` → `200 {"valid": true}` or `400 {"detail": "invalid_redirect_uri" | "invalid_state" | "invalid_challenge"}`. Mints nothing.
- `/authorize` → same 400s; on success mints an **already-approved, single-use grant bound to the
  challenge**, TTL `settings.cli_browser_grant_ttl_s = 120`, org = the session user's org, and answers
  `200 {"redirect_to": "<redirect_uri>?pairing_code=<urlenc pairing_code>&state=<urlenc state>"}`.
  `pairing_code` is built exactly as `enroll_code` builds it today (`host/CODE`).
- Unauthenticated → 401 (existing dependency).
- Minting goes through `cd.mint_approved_grant(r, user_id, org_id, ttl_s, challenge=None)`, extracted
  from `enroll_code`; `enroll_code` becomes its other caller. Grant JSON gains `"challenge"` only when set.
- Log one structured line per mint: `cli.grant_minted user_id=… org_id=… kind=browser|setup_code`.

### C2 — Atlas `POST /v1/cli/enroll`

Body `{"code": str, "code_verifier": str | null}`. Claim (GETDEL) first, exactly as today. If the claimed
grant has a `challenge`, require `base64url_nopad(sha256(code_verifier)) == challenge`; otherwise answer
**410 with today's detail string** — the grant is already consumed. Grants without a challenge behave
exactly as today whether or not a verifier is sent. Log `cli.redeem outcome=ok|bad_verifier|expired kind=…`.

### C3 — Atlas `next` handling

`_safe_next` (Python) and one new `safeNext` (TypeScript, `services/web/lib/safe-next.ts`, replacing the
copies in `app/login/page.tsx` and `components/auth/oauth-buttons.tsx`) implement the rule in
`services/api/tests/fixtures/safe_next_cases.json` and are both tested against **that file** (already
committed on both Atlas branches). New OAuth signups keep their destination: the callback redirects to
`/signup/finish?next=<_safe_next(next)>` and the finish page navigates to `safeNext(next)` when done.

### C4 — Atlas page `GET /cli/signal/authorize?redirect_uri&state&code_challenge&code_challenge_method`

Behind the login gate (not in `PUBLIC_PREFIXES`). On load: call `/validate`; invalid → a fixed error
"This sign-in link is not valid." and **no Continue button**. Valid → "Continue as <email>" with the org
name and one **Continue** button (plus "Cancel", which only says Signal will stay signed out). Continue
calls `/authorize` and sets `window.location.href` to the returned `redirect_to` **only**. It never builds a
URL from its own query string.

### C5 — Signal daemon routes

- `POST /v1/auth/start` (page secret) → `200 {"authorize_url": str, "opened": bool}`; `409
  {"error":"send_to_atlas_is_off"}` when Send to Atlas is off (same as `/v1/config`). Makes 32 random
  bytes of state and a 32-byte verifier (base64url, no padding), S256 challenge; stores
  `{state → verifier, api_base, expires_at}` in memory, max 4 pending (oldest evicted), 10 min expiry.
  `authorize_url = AtlasWebBase() + "/cli/signal/authorize?" + redirect_uri=http://127.0.0.1:<own port>/auth/callback&state&code_challenge&code_challenge_method=S256`.
  Opens the browser with the existing opener (`internal/auth` `openURL`, exported) unless
  `KELD_AUTH_NO_BROWSER=1`.
- `GET /auth/callback?pairing_code&state` (**no secret**; the state is the credential). Delete the pending
  entry on first sight whatever happens next. Refuse (fixed HTML, nothing written, no Atlas call) on
  unknown/expired/used state, on a pairing-code host that differs from the stored `api_base`, or when
  Send to Atlas is off. Otherwise call the shared `pair(base, code, verifier)` — the one function
  `POST /v1/config` also uses — which enrolls with the verifier, fetches onboarding, writes `auth.json`
  and `hook.json`. Atlas 410 → fixed "That code expired" page. Every response: `text/html`, fixed body
  (nothing from the URL echoed), `Cache-Control: no-store`, `Referrer-Policy: no-referrer`,
  `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`.
- `GET /v1/auth/state` (page secret) → `{"paired": bool, "principal": str|null, "org": str|null,
  "first_run": bool, "pending": bool, "last_error": null|"not_started_here"|"expired"|"atlas_mismatch"|"atlas_off"|"atlas_error"|"save_failed"}` (`save_failed`: Atlas finished, hook.json could not be written here).
  `first_run` = not paired AND `send_to_atlas` absent from agent-config.json AND `KELD_ATLAS` unset.
- `paths.AtlasWebBase()` = `KELD_ATLAS_WEB_URL`, else `APIBase()`.
- `auth.LoginWithCode` / the API client send `code_verifier` when given one.
- Mock Atlas (`internal/conform/mockatlas`): `GET /cli/signal/authorize` applies the C1 rules and 302s to
  `redirect_to` with a mock pairing code bound to the challenge; `/v1/cli/enroll` enforces C2.

### C6 — Signal page

- First open (`first_run` true): the choice screen from the spec wireframe — **Sign in with Atlas** /
  **Use locally only**, "You can sign in later from Settings." Local only = `PUT /v1/settings
  {"send_to_atlas": false}` through the existing route. Never shown when `first_run` is false.
- Sign in anywhere (first-run, Settings, the not-signed-in bar): `POST /v1/auth/start`; show "Finish
  signing in in your browser" with the `authorize_url` as a visible link fallback; poll `/v1/auth/state`
  every 1 s until paired (show signed-in within 5 s of the callback) or `last_error` (show its message +
  Try again). From local-only mode, Settings' Sign in first sets `send_to_atlas: true`.
- The paste-a-setup-code box stays in Settings.

## Rules for every agent

1. TDD: failing test first, then code. Commit on your own branch only; small commits; never push; never
   merge other branches unless the lead tells you to.
2. Never touch the real `~/.keld`, never run `keld-agent install`, never restart the user's services.
   Any daemon you run uses `KELD_HOME=$(mktemp -d)` and `KELD_TELEMETRY_PORT` in 14400–14499 (pick one).
3. Never modify the local dev database or the running Atlas stack. Atlas tests run against a **separate
   test database** — read `services/api/tests/conftest.py` first and report how you ran them.
4. Stay inside your "Owns" column. If you need something outside it, stop and report it.
5. Report at the end: branch, commits, every test command you ran with its real pass/fail output, which
   ACs you believe are met with `path:line` evidence, and anything you could not verify.

## Phase 2 — integration and end-to-end (lead)

1. Merge both Atlas branches into `feat/signal-web-signin`. Check it out in the main keld-atlas checkout,
   so the running stack on localhost:3000/8000 serves it (hot reload). Rebuild the api image if
   `pyproject.toml` changed; run pending migrations; confirm the seeded logins still work.
2. Merge the three Signal branches into `feat/web-signin`. Run `go test ./...`, the UI node tests, the
   installer guards, and the Playwright suite against the mock Atlas.
3. Dispatch `e2e-local`: a Playwright suite in `ui/e2e/local-atlas/` that runs **only** when
   `KELD_E2E_ATLAS_WEB=http://localhost:3000` is set, driving a real throwaway daemon, a real browser and
   the real local Atlas. Minimum cases: happy path (email login); signed-out → login → back to the same
   authorize URL; hostile `redirect_uri` shows the error and mints nothing; forged return with unknown
   state writes nothing; replayed return refused; code without verifier refused (direct API call); wrong
   Atlas host refused; Send to Atlas off refused; first-run local-only choice persists across a daemon
   restart; Settings sign-in from local-only mode; `/login?next=/\evil.com` stays on Atlas; setup-code
   paste still pairs (AC-8 regression).
4. Run the canary spec against localhost:3000.

## Phase 3 — grading (lead)

A reviewer agent grades the merged diff against AC-1…AC-12 with the discovery skill's `REVIEW.md`, file
not URL. The lead re-runs every suite, fixes nothing silently (sends it back to the owning agent), then
fills section 9 of the spec, flips its chip to Reviewed and republishes.
