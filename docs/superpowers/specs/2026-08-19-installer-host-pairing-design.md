# Installer host pairing — design

**Date:** 2026-08-19
**Status:** approved, not yet implemented
**Repos:** `keld-signal` (this one, most of the change) + `keld-atlas` (two call sites)

## Problem

Re-installing Keld Signal cannot move a machine from one Keld deploy to another. The install
runs, the agent restarts, the browser login completes — and the machine stays paired to
whatever host it was paired to before.

Observed on a real machine on 2026-08-19: a reinstall intended to target `https://atlas.keld.co`
left `~/.keld/auth.json` on `http://localhost:3000` as `admin@acme.test`. All of `auth.json`,
`hook.json`, `manifest.json` and `agent.json` were rewritten at the install timestamp, and the
agent restarted onto the same second — so every step ran. The ingest token in `hook.json` was
rewritten but still hashed to a token minted 13 days earlier on the local dev database, proving
the prod host was never contacted. The user's telemetry continued flowing to their local compose
stack while they waited for it to appear in prod.

The symptom is silent. Nothing errors, `keld signal doctor` reports "No problems found", and the
only way to see it is `keld whoami`.

## Root cause

`internal/auth/device.go:120`, inside `RequireAuthReport`:

```go
// A stored token is only valid at the server it was minted on (its APIURL).
if existing != nil && existing.APIURL != "" && paths.APIBaseOverride() == "" {
    paths.SetAPIBaseOverride(existing.APIURL)
}
```

This runs **before** the `if !(force && !noLogin)` check, so it applies to a forced `keld login`
too. `paths.APIBase()` resolves override → `KELD_API_URL` → `DefaultAPIURL`, and the pin has
already set the override — so `DefaultAPIURL` (`https://atlas.keld.co`) is unreachable on any
machine that has ever logged in anywhere. The device-flow browser login is served by the *stored*
host, which authorizes its own user and writes its own URL straight back.

The pin exists for a real reason, named in its own comment: without it `keld signal setup` sends a
locally-minted token to `atlas.keld.co` and gets a 401. The fix must preserve that.

Two delivery channels then fail to supply the host that would have overridden the pin:

- **`curl | sh`** — `scripts/install.sh:119-125` already reads `KELD_API_URL` and passes it as
  `--api-url`, and its comment already describes this exact bug. But nothing ever sets the
  variable: the copy-paste line Atlas generates is a bare `curl … | sh`.
- **`.pkg`** — `installers/macos/onboard.command` calls `keld-agent install` with no host flag at
  all. A notarized pkg is deliberately identical bytes for every deploy, so it has nothing to read
  its origin from.

## Design

### The precedence rule

One rule, applied everywhere the API base is resolved:

```
--api-url  >  host from setup code  >  KELD_API_URL  >  stored auth.json (reuse only)  >  DefaultAPIURL
```

The only change to existing semantics is the parenthesis on the fourth term. The pin moves below
the `force` check in `RequireAuthReport`, so it governs the *reuse* path — returning stored
credentials, and every subsequent command that runs against them — but no longer captures an
explicit `keld login`. A forced login with no other signal targets `DefaultAPIURL`, which is what
a user re-running the installer means.

### Channel A — `curl | sh`

No change to `scripts/install.sh` and no change to how it is served. It already consumes
`KELD_API_URL` from its environment; the generated command simply sets it:

```
curl -fsSL {base}/install.sh | KELD_API_URL={base} sh
```

`install.sh:125` turns that into `--api-url {base}` for the `keld-agent install` it runs, which
forwards it to both `keld login` and `keld signal setup`.

The variable must be set **unconditionally**, including when `{base}` is prod. The failure this
fixes was a *prod* install losing to a stored localhost, so the existing
"only pass `--api-url` when this deploy differs from prod" logic in `quick_setup_cmd` would
reintroduce it. That logic also currently appends `--api-url` only to the trailing
`keld signal setup`, leaving the install step inside the pipe unpaired; setting the environment
variable covers both.

### Channel B — `.pkg`, via the setup code

The setup code carries its own origin:

```
atlas.keld.co/ABCD-EFGH
```

`onboard.command` needs no change — it already forwards `$CODE` to `keld-agent install`, which
derives the host from it.

**Parsing** (`ParsePairingCode`, the single parser):

1. Trim surrounding whitespace; strip a single trailing `/`.
2. Split on the **last** `/`. No `/` present means no host part.
3. Uppercase the code segment only. The server's alphabet is
   `ABCDEFGHJKLMNPQRSTUVWXYZ23456789` (uppercase, with `I`/`O`/`0`/`1` excluded as ambiguous),
   and `POST /v1/cli/enroll` looks the code up as a raw Redis key — case-sensitive, no
   normalization — so a lowercase-typed code fails today. Uppercasing client-side is a strict
   improvement and cannot collide, since no lowercase code is ever minted. The host part is left
   as typed.
4. Resolve the host: a segment containing `://` is used as-is; otherwise prepend `https://`,
   except a segment whose hostname (ignoring any `:port`) is `localhost` or `127.0.0.1`, which
   gets `http://`.
5. Empty code segment after a host (`atlas.keld.co/`) is an error, reported as an invalid
   setup code.

Splitting on the last `/` is unambiguous because the code itself contains no `/` — it is
`XXXX-XXXX`. It handles a scheme (`https://atlas.keld.co/ABCD-EFGH`) and a path-bearing deploy
(`https://example.com/keld/ABCD-EFGH`) correctly without special cases.

**Backward compatibility:** a bare `ABCD-EFGH` yields an empty host, falls through to
`DefaultAPIURL`, and behaves exactly as today. Codes minted by an Atlas that predates this change
keep working.

### Data flow

```
Atlas download page
  renders  atlas.keld.co/ABCD-EFGH
      │
      ▼
onboard.command  ── $CODE ──▶  keld-agent install --code <pairing>
                                     │
                                     ├─ ParsePairingCode → cfg.apiURL = https://atlas.keld.co
                                     │
                                     ├─▶ keld login --api-url <host> --code ABCD-EFGH
                                     │       └─ LoginWithCode → persistToken(result, c.BaseURL)
                                     │              └─ auth.json.api_url = https://atlas.keld.co
                                     │
                                     ├─▶ keld signal setup --api-url <host>
                                     │       └─ hook.json / manifest.json endpoint
                                     │
                                     └─▶ installService()  (agent restart)
```

`runInstall` must set `cfg.apiURL` from the parsed code rather than letting `login` parse it
alone. `cfg.apiURL` is what appends `--api-url` to **both** child commands; if only `login`
learned the host, `signal setup` would re-resolve independently and write the old endpoint into
`hook.json`. That divergence is the most likely regression in this change and is covered by a
dedicated test.

### Error handling

- Malformed pairing code (empty code segment): fail before any network call, with a message
  naming the expected shape. No partial install.
- Host unreachable / enroll rejects the code: unchanged from today — `keld-agent install` reports
  the failure and `install.sh` prints its existing re-run hint. The agent is still installed;
  `keld login --api-url <host>` remains the manual recovery.
- A host-bearing code plus an explicit `--api-url` that disagree: the flag wins, silently. It is
  the higher-precedence, more explicit signal, and it is how dev targets a local stack with a
  code minted elsewhere.

### Security note

The pairing string is host-bearing input pasted into a terminal, so a malicious string could aim
a fresh install at an attacker-controlled host. This does not widen the existing surface — anyone
who can hand you a pairing string can equally hand you a `curl … | sh` line — but the CLI prints
`Pairing with <host>` before authenticating, so the target is visible in the terminal transcript
and in the login screen the browser opens. Human-readable format is chosen over an opaque encoded
blob for this reason.

## Files

**keld-signal**

| File | Change |
|---|---|
| `internal/auth/code.go` | New. `ParsePairingCode(s) (apiBase, code string, err error)` — the only parser. |
| `internal/auth/device.go` | Move the stored-`APIURL` pin below the `force` check in `RequireAuthReport`. |
| `internal/cli/login.go` | Parse `--code`; set the API base override from its host when `--api-url` was not given; send the bare code to `LoginWithCode`. Print `Pairing with <host>` before authenticating — here rather than in `runInstall`, so a direct `keld login --code` shows it too. |
| `internal/agentcli/agentcli.go` | `runInstall` parses the code and sets `cfg.apiURL`, so both child commands receive `--api-url`. |

Unchanged: `scripts/install.sh`, `installers/macos/onboard.command`, `installers/macos/scripts/postinstall`.

**keld-atlas**

| File | Change |
|---|---|
| `services/api/app/telemetry_snippets.py` | `context_hook_install_cmd()` and `quick_setup_cmd()` emit `\| KELD_API_URL={base} sh`, unconditionally. |
| `services/web/app/cli/signal/page.tsx` | Render the pairing string `{host}/{code}` instead of the bare code. |
| Download page (pkg setup code) | Same pairing string. |

Unchanged: `services/api/app/main.py`, `services/web/next.config.ts` — `/install.sh` keeps
redirecting to GitHub. Serving the script from Atlas is out of scope until there is a storage
solution.

## Testing

- `ParsePairingCode` table test: bare code; bare host; explicit `https://`; explicit `http://`;
  `localhost:8000`; `127.0.0.1`; path-bearing host; trailing slash; lowercase code; empty code
  segment; surrounding whitespace.
- `runInstall` argv test: a host-bearing code puts `--api-url <host>` on **both** the `login` and
  the `signal setup` child commands.
- `RequireAuthReport`: narrow `TestRequireAuthTargetsStoredAPIURL` to the reuse path; add a test
  that a forced login with no flag and no env targets `DefaultAPIURL` despite a stored host.
  `TestRequireAuthFlagOverrideBeatsStoredAPIURL` should pass unchanged.
- Atlas snippet tests: `KELD_API_URL` present for a prod base *and* a non-prod base.
- End-to-end on a machine already paired to localhost: redeem a prod pairing code, then assert
  `keld whoami` reports `atlas.keld.co` and `hook.json`'s endpoint matches.

## Out of scope

- The hook logging an HTTP 429 from the daemon as `daemon unreachable` (`forward:` path). Real,
  independent of pairing, own change.
- `keld signal doctor` reporting "No problems found" while the spool is backing up and publishes
  are failing. Same.
- `/install.sh` is served by the `next.config.ts` rewrite pointing at `ncx-ai/keld-cli`, which
  resolves only because GitHub redirects the renamed repo to `keld-signal`; the API's own
  `main.py` route is dead on `atlas.keld.co`. Worth cleaning up, but it is not this change.
