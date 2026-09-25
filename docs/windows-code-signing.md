# Windows code signing

**Status:** live since 2026-09-25. Windows binaries and the installer are signed
with **Azure Artifact Signing** (formerly Trusted Signing) as `CN=Keld Inc`.

The procurement history — including the claim that this service was unavailable
to us, which was wrong — is in
`docs/superpowers/plans/2026-09-15-windows-code-signing-procurement.md`. That
file is closed; this one is what to keep current.

## Why any of this exists

Windows 11 ships **Smart App Control** on by default on clean installs, and it
refuses unsigned native code. Measured on a real Windows 11 machine on
2026-09-15 against the *released* product:

```
C:\Users\<user>\AppData\Local\Programs\keld\keld.exe
Status : NotSigned
run    : BLOCKED — An Application Control policy has blocked this file
```

⚠️ **It is not a steady failure, which is why it went unnoticed for weeks.** The
same file ran at 2:05pm and was blocked at 2:44pm with nothing changed: unsigned
code is admitted or refused on a per-file reputation guess that moves over time.
Every release is a fresh set of files with no history, so shipping unsigned was a
lottery drawn per release rather than a known-broken state anyone could see.

## The configuration

Not secrets — these live in `.github/workflows/installers.yml`:

| | |
| --- | --- |
| Account endpoint | `https://eus.codesigning.azure.net/` |
| Signing account | `keld` |
| Certificate profile | `keld-public-trust` |
| Publisher subject | `CN=Keld Inc, O=Keld Inc, L=Maitland, S=Florida, C=US` |
| Azure tenant | `5f69b875-7a32-484c-bd79-15a89baf8a48` |
| CI service principal | `keld-github-actions` (`a195beda-96fd-4d5b-a8b5-1841110130da`) |
| Role | Artifact Signing Certificate Profile Signer, scoped to `keld` |

Secrets on `ncx-ai/keld-signal`: `AZURE_TENANT_ID`, `AZURE_CLIENT_ID`,
`AZURE_CLIENT_SECRET`, `AZURE_SUBSCRIPTION_ID`.

**There is no PFX and there never will be.** Artifact Signing keeps the key in
the service and lends it at signing time; the CA/Browser Forum's June-2023 rule
requires exactly that. Any instruction mentioning a `.pfx` file predates this.

## How the build signs

⚠️ **TWO PASSES, AND THE ORDER IS LOAD-BEARING.** Smart App Control evaluates a
binary **as it loads**, not once at install time. A signed `keld-setup.exe` that
installs an unsigned `keld.exe` buys nothing: the install succeeds and the
product is refused the moment it starts — which is the failure measured above,
on a payload binary extracted to a temp directory, not on the installer.

1. **The payload, before `iscc`.** `installers/windows/sign-payload.ps1
   -CatalogOut` enumerates the staged payload and writes a catalog;
   `Azure/artifact-signing-action@v2` signs exactly that list; the same script
   re-reads the catalog with `-VerifyCatalog` and fails the build on any gap.
2. **`keld-setup.exe`, after `iscc`.** Same action, `files:` input.

⚠️ **The catalog is what keeps vendors' signatures on vendors' files.** The
action can sweep a folder itself, and doing that would re-sign every PE binary in
the payload — replacing third-party attestations with ours, which we have no
standing to make and cannot recreate. Measured on the real CI payload
(run 35021526816): **16,498 files, 188 PE binaries, 78 already vendor-signed,
110 needing ours.** An earlier local measurement said 42 and was low by 2.6x;
size any signing quota off 110 per release, not 42.

⚠️ **Verification reads the CATALOG, never a fresh scan.** A rescan after signing
finds every file `Valid` — including the ones just signed — so it could never
report a gap. A signer that exits 0 having silently skipped a file is the failure
this whole arrangement exists to prevent.

**Timestamping is not optional.** Artifact Signing certificates are short-lived
and rotated by the service, so an untimestamped signature stops validating when
the certificate that produced it expires — weeks after shipping, not years. Both
steps set `timestamp-rfc3161: http://timestamp.acs.microsoft.com`.

**Absent secrets produce an unsigned build, not a failed one** (forks, PRs),
mirroring the Apple path in the same workflow. The run then carries a
`::warning title=Unsigned Windows installer`. Whether that should instead be a
hard gate on a release, as macOS treats notarization, is an open decision — see
below.

Invariants are pinned by `installers/windows/keld_agent_iss_test.sh` (guards
11–13): signing order relative to `iscc`, catalog-not-folder-sweep, verification
present, timestamping on both steps.

## ⚠️ Known gap: the uninstaller is NOT signed

Inno extracts `unins000.exe` onto the target machine at install time, so neither
pass above reaches it — the stub exists only during the compile. Inno's
`SignedUninstaller=yes` is the only thing that can sign it, and it needs a
**command line**, which a GitHub Action cannot be.

**Consequence, stated rather than discovered: on a Smart App Control machine
installing works and uninstalling is refused.** Better than before (both were
refused), short of complete.

The fix is a command-line signer, which Azure does publish: the
`Microsoft.Trusted.Signing.Client` NuGet package (1.0.95 at time of writing)
carries a signtool dlib used as

```
signtool sign /v /fd SHA256 /tr http://timestamp.acs.microsoft.com /td SHA256 \
  /dlib <pkg>\bin\x64\Azure.CodeSigning.Dlib.dll /dmdfile <metadata.json> $f
```

with the same four `AZURE_*` credentials. Setting `KELD_SIGN_COMMAND` to that
re-enables the gated block already present in `installers/windows/keld-agent.iss`
with no other change — which is why that seam is kept rather than deleted. It is
deliberately not wired yet: **an unproven `SignTool` HALTS the compile** (measured
— `SignedUninstaller=yes` with no working tool stops `iscc` outright), and that is
the entire Windows release, so it wants its own dry run first.

## Verifying a build

`workflow_dispatch` with an empty `release_tag` is a dry run that still receives
secrets and uploads `keld-setup.exe` as an artifact without touching a release.
Use that loop rather than cutting releases.

```bash
gh workflow run installers.yml --ref <branch>
```

The run log prints the signer subject, so "signed by whom" is answerable without
downloading anything. On a Windows 11 machine with Smart App Control **in
enforcement mode**:

```powershell
Get-AuthenticodeSignature .\keld-setup.exe | Format-List Status, SignerCertificate
```

Expect `Valid` and `CN=Keld Inc`. **Then install and run the full flow** — the
acceptance test is that `keld.exe` runs after extraction, because that is the
exact thing that failed.

⚠️ **Expect a SmartScreen download warning on the first signed release anyway.**
Signing does not grant instant reputation; Microsoft removed that even for EV
certificates in 2024. What signing buys is that reputation now accrues to a
stable `Keld Inc` identity across releases instead of restarting per file. A
SmartScreen prompt is not evidence that signing is broken — check the signature
status instead.

Also: **SAC honours RSA certificates, not ECC.** Artifact Signing issues RSA, so
this is satisfied; don't "improve" it later.

## Operational notes

- ⚠️ **The client secret expires in 24 months** (from 2026-09-25) and will break
  a release silently when it does. Either calendar it or remove it — see OIDC
  below.
- ⚠️ **Identity validation expires and must be renewed.** Microsoft warns from 60
  days out. If it lapses, certificate rotation stops and signing stops with it.
- **The Azure subscription must stay pay-as-you-go.** Artifact Signing rejects
  free, trial and sponsored subscriptions.
- **Windows ARM runners are not supported** by the action. The Windows leg is
  `windows-latest` (amd64), which is all we ship.
- Scope is Windows only. macOS has its own Apple Developer ID certificate and is
  unaffected; Linux `install.sh` is out of scope.

### Open decision: move to OIDC

Federated credentials remove the client secret entirely, deleting the only
recurring failure mode above. Because this workflow fires from `workflow_call`,
`release` **and** `workflow_dispatch`, the clean shape is one federated credential
scoped to a GitHub **Environment** rather than juggling branch and tag subjects.
Not urgent; it is strictly a reduction in what can break.
