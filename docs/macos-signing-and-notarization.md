# macOS signing & notarization — state of play

**Last updated:** 2026-08-11 (notarization resolved; see §3)

Read this before touching macOS installer signing. It records what was built, what
is verified, and how the notarization stall resolved.

**One-line summary:** signing works and is proven in CI, and **notarization now
works too** — verdicts land in ~25 seconds, stapled inline by the release build
(measured on v0.20.0 and v0.21.0). The decoupling built to tolerate an unbounded
queue is retained deliberately: it costs nothing when verdicts are fast and is the
reason a future stall cannot block a release.

---

## 1. What was built

Six merges, all on `main`, in dependency order:

| commit | what |
|---|---|
| `7454f2d` | Import Developer ID certs in CI; sign **every** Mach-O in the payload |
| `959c75c` | Stop rejecting Atlas built-in passes as custom (keld-atlas#62) — unrelated, found en route |
| `228d325` | Decouple notarization from the release build |
| `c52084e` | Ship the macOS pkg **without** the ML sidecar |
| `8ce0650` | Sign the standalone sidecar tarball |
| `6c4b9e8` | Async notarization stapling (`.github/workflows/staple.yml`) |

### Signing (works, verified)

Signing could never have worked before `7454f2d`: the workflow passed
`APPLE_DEVELOPER_ID_*` to `codesign`/`productsign`, but those are identity **names**
resolved against the runner's keychain search list, and nothing ever imported a
cert. `productbuild --sign` would have failed outright; `codesign` was swallowed by
a `|| true`.

Now: a throwaway keychain is created per job, both p12s imported, and the identity
names are **derived from the keychain** rather than trusted from a secret (a typo
in a hand-typed name fails at `productsign` with an opaque error). The keychain is
removed with `if: always()`.

Two certs are required and are not interchangeable — see §2.

`installers/macos/sign-macho.sh` is the shared sweep, used by both `build-pkg.sh`
and the tarball step. It finds Mach-O **by content** (`file`), never by extension,
and signs **deepest-path-first** so nested code is sealed before its loaders.

Proven by run `31034270833`: 103 nested binaries signed, chain resolving
leaf → Developer ID CA (G2) → Apple Root CA, `pkgutil` reporting
*"signed by a developer certificate issued by Apple for distribution."*

### The pkg no longer contains the sidecar

Apple's notary service scans every file in a submission, and the frozen sidecar is
~15k files / ~190MB of torch. Payload is now 4 files (`keld`, `keld-agent`,
`onboard.command`, `VERSION`); signing dropped from ~103 Mach-O to 2.

`onboard.command` fetches the sidecar into **`~/.local/bin`** — already a
well-known `sidecarBinPath()` dir on darwin (`daemon.go`), and user-writable, so no
sudo prompt. It fetches **before** `keld-agent install`, because that command starts
the daemon. Pinned to the pkg's release via the staged `VERSION` file, falling back
to the latest-release API for dry-run builds. Apple-Silicon-only. Non-fatal on
failure: telemetry still works, enrichment jobs spool, re-running retries.

⚠️ **This did not speed up notarization** — see §3. It remains worthwhile on its own
merits (2 signings not 103, smaller download), but it was not the fix it was framed
as.

### Notarization is decoupled, and stapling is async

`build-pkg.sh` submits, waits only `KELD_NOTARY_TIMEOUT` (default **15m**), then
ships. `Invalid`/`Rejected` still **fails** the build (a broken payload is not fixed
by waiting); only a timeout is tolerated. `KELD_NOTARY_REQUIRED=1` restores
fail-on-timeout.

`.github/workflows/staple.yml` then staples after the fact: it downloads a release's
`.pkg` assets, skips already-stapled ones, resolves each submission from Apple's
history **by filename**, waits as long as Apple actually takes, staples, re-uploads.
Still-pending is not a failure; the next scheduled run retries.

**Why async matters:** an inline timeout couples "how long the build waits" to
"whether we ever staple." Too short and we never staple — and the build goes green
either way, so it would silently become permanent.

---

## 2. Credentials & assets

Two certs, **not interchangeable** — `productbuild` rejects an Application cert and
`codesign` rejects an Installer cert:

- **Developer ID Application** → the Mach-O binaries
- **Developer ID Installer** → the `.pkg` itself

Both issued 2026-08-05, expiring **2031-08-06**, under the **G2 Sub-CA** (pick G2:
the previous Sub-CA expires 2027-02-01). Team `Keld Inc (FZBUSSZPTD)`.

Both p12s were originally **leaf-only** and were rebuilt to bundle the **G2
intermediate**. Without it a clean CI runner cannot build a chain to a trusted root
(it works on a dev Mac only because the intermediate is already in the system
keychain). Rebuilt files live at `~/Downloads/keld-signing/*.p12` (0600).

Repo secrets (all set):

```
APPLE_CERT_APP_P12        APPLE_NOTARY_KEY        (the .p8 CONTENTS)
APPLE_CERT_INSTALLER_P12  APPLE_NOTARY_KEY_ID
APPLE_CERT_PASSWORD       APPLE_NOTARY_ISSUER
```

`APPLE_DEVELOPER_ID_APP` / `_INSTALLER` are **optional overrides only** — normally
the names are derived from the imported keychain.

Note `notarytool --key` wants a **path**, but a CI secret naturally holds the key
**contents**; `build-pkg.sh` and `staple.yml` both accept either.

---

## 3. The notarization stall — RESOLVED 2026-08-06

Notarization now returns verdicts in **~25 seconds**, and the release build staples
inline. Measured:

| release | submitted | outcome | elapsed |
|---|---|---|---|
| v0.19.0 | 2026-08-06 12:23Z | `still pending after 15m` → shipped unstapled | **>15m** |
| v0.20.0 | 2026-08-06 21:28Z | `notarized + stapled` | **23s** |
| v0.21.0 | 2026-08-11 00:54Z | `notarized + stapled` | **24s** |

The transition happened between v0.19.0 and v0.20.0, and was an **account-side
change, not a repo change** — nothing in this repository touched signing or
notarization between those two builds. That is consistent with the leading
hypothesis recorded below (an unaccepted App Store Connect agreement gating account
services while still allowing certs to issue and the API to authenticate), though
the exact remedy was applied outside CI and is not evidenced here.

**Do not read v0.19.0's timeout as flakiness.** It predates the fix. Every
submission after the transition has been fast and consistent.

### What this means for the pipeline

The decoupling (submit, wait `KELD_NOTARY_TIMEOUT`, ship regardless; sweep later via
`staple.yml`) is **kept on purpose**. It costs nothing now that verdicts are fast —
the release staples inline and the sweep is a ~13s no-op — and it means a future
Apple-side stall degrades to "ships unstapled, still validates online" instead of
blocking releases. `KELD_NOTARY_REQUIRED=1` remains available to make a timeout fatal;
that is now a defensible default rather than a trap, but it is deliberately still
opt-in, since a green release should not depend on Apple's queue latency.

`staple.yml` runs daily (was 6-hourly, which swept an already-stapled release four
times a day forever).

### Historical: the stall as it was diagnosed

The following was the state as of 2026-08-06 00:13Z, when zero notarizations had
ever completed for this team. Retained because the reasoning is what narrowed the
cause to the account, and because it documents what to check if it recurs.

| submitted | payload | status as of 2026-08-06 00:13Z |
|---|---|---|
| 2026-08-05 18:41Z | ~15k files, ~190MB | `In Progress` — **5h32m** |
| 2026-08-05 23:18Z | 4 files | `In Progress` — **55m** |

Total submissions at that point: **2**. Verdicts: **0**.

### Ruled out

- **Payload size.** Two submissions three orders of magnitude apart behave
  identically. This was my leading hypothesis and it is wrong.
- **An error we're not seeing.** The logs endpoint returns **404** — Apple produces
  a log only when it reaches a verdict, so a 404 proves no verdict, not a hidden
  failure. Also: single submission (no retry storm), CI runner polled continuously
  for 4h+, and Apple's status feed reported *Developer ID Notary Service* healthy
  throughout.
- **Our credentials.** Auth succeeds; Apple accepted and registered both uploads.
- **Cancelling.** Not possible. `notarytool` has no cancel verb and `DELETE` on a
  submission returns 404 (the id is valid — `GET` works — so the method simply is
  not implemented). Submissions sit until Apple resolves them; they are inert and
  do not block future work.

### Leading hypothesis (this is what it turned out to be — account-side)

**The account is not fully provisioned for notarization.** Certs were issued
2026-08-05 16:03Z, only ~2.6h before the first-ever submission. This fits every
observation: no error (nothing failed), no log (no verdict), service healthy for
everyone else, and total indifference to what we submit.

The most common concrete cause is **an unaccepted agreement in App Store Connect**.
A pending Apple Developer Program License Agreement gates account services while
still allowing certificates to issue and the API to authenticate — exactly what we
see.

**➡️ If notarization ever stalls again, check this first** (it is what resolved it
in 2026-08-06):

1. developer.apple.com → **Account** — look for banners about agreements or
   enrollment still finalizing.
2. App Store Connect → **Business / Agreements** — anything "Pending", especially
   requiring the Account Holder.

If something is unaccepted, accepting it typically releases queued submissions
without resubmitting.

Secondary, only if the above is clean: **head-of-line blocking** (the slim
submission queued behind the wedged fat one). It cannot explain why the fat one
stuck in the first place, so it is at best a side effect. Testable by whether they
resolve in submission order.

### Checking status without macOS

`notarytool` is macOS-only, but the Notary API is REST and reachable from anywhere
with the App Store Connect key. Mint an **ES256 JWT** (`kid` = key id, `iss` = issuer
id, `aud` = `appstoreconnect-v1`, 10-min expiry; the JWS needs the raw `r||s` pair,
so convert from the DER signature `cryptography` emits) and call:

```
GET https://appstoreconnect.apple.com/notary/v2/submissions          # history
GET https://appstoreconnect.apple.com/notary/v2/submissions/{id}     # status
GET https://appstoreconnect.apple.com/notary/v2/submissions/{id}/logs # 404 until a verdict
```

This is how everything in §3 was established while the CI job was still blocked.

---

## 4. Open follow-ups

- **`KELD_NOTARY_TIMEOUT` still defaults to 15m.** With the async stapler in place
  the inline wait is only a fast-path optimization, and on current evidence it never
  hits. Dropping it to ~2m would make builds finish sooner at no cost — worth doing
  once we know whether Apple ever responds for this account.
- **`staple.yml` has never run against a real release.** Both existing submissions
  are dry-run builds with no release behind them, so nothing picks them up
  automatically. The first real release exercises that path.
- **The sidecar tarball is signed but not notarized** — deliberate. It is fetched by
  `curl` (both `onboard.command` and `install.sh`), and curl sets no
  `com.apple.quarantine` bit, so Gatekeeper never evaluates it and a ticket would
  buy nothing.
- **Windows installers still ship unsigned.** `installers.yml` has a placeholder:
  `signtool sign step (gated) would run here`. Same class of gap as the one just
  closed for macOS.
- **Two files were already unformatted at HEAD** — `internal/agent/enrich/types.go`
  and `pipeline_custom_test.go`, from concurrent enrichment-gating work. Left alone
  deliberately; `gofmt -l` flags them.
- **The `gh` token lost its `workflow` scope twice mid-session**, most plausibly from
  a concurrent session re-authenticating against the shared `~/.config/gh/hosts.yml`.
  A fine-grained PAT in the environment would be immune. Symptom: pushes touching
  `.github/workflows/` are rejected; fix is `gh auth refresh -h github.com -s workflow`.

---

## 5. Gotchas worth not rediscovering

- **Gatekeeper validates ONLINE.** An unstapled but notarized pkg passes on any
  online machine; stapling only adds *offline* validation. This is what makes
  shipping before the ticket lands safe.
- **`curl | sh` bypasses Gatekeeper entirely** — no quarantine bit, and
  `sudo installer -pkg` performs no Gatekeeper check. `scripts/install.sh` is an
  unsigned distribution channel that already works. Signing the pkg buys the
  double-click-from-a-browser experience, and nothing else.
- **Notarization ≠ approval.** It is an automated malware scan that issues a
  revocable ticket. Apple does not review the software.
- **`openssl verify` fails on Developer ID leaves with `error 34: unhandled critical
  extension`** — that is openssl not understanding Apple's proprietary critical EKU,
  not a broken chain. Use `-ignore_critical` to check the chain properly.
- **Every Mach-O must be signed, not just entrypoints.** Notarization rejects the
  whole submission over a single unsigned nested `.dylib`.
