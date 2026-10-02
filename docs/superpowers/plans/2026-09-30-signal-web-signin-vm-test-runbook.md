# Signal web sign-in — install test runbook for a throwaway machine (action A6)

Hand this file to a Claude session **running on the test machine** (Claude Desktop → Code tab, with
**Computer use** turned on in Settings → General). It covers what no automated test can: the real
double-click installer, what opens when it finishes, the first-open choice, and a real browser returning
to Signal. Spec: `docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html` (AC-10, AC-12, §8 #5–6).

⚠️ **Only on a disposable machine** (an Azure VM restored from a clean snapshot, or a macOS VM).
Never on a machine whose Signal someone uses: the installer replaces it.

## Before you start

- keld-atlas PR #310 is deployed to **dev** (`https://atlas-dev.keld.co/cli/signal/authorize` loads a
  page, not a 404). Prod does not have it yet, so this test points Signal at dev.
- The tester can sign in to atlas-dev (it is behind Cloudflare Access; use a team account).
- The installers come from the dry-run build of keld-signal PR #58 (run 36719519251):
  - Windows: `gh run download 36719519251 -R ncx-ai/keld-signal -n installers-windows-latest-amd64`
    → `keld-setup.exe` (signed).
  - macOS: `gh run download 36719519251 -R ncx-ai/keld-signal -n installers-macos-14-arm64` → the `.pkg`.
  If `gh` is not signed in on the machine, the lead downloads them and copies them over.
- Point Signal at dev. With an installer built from #58 after `keld signal env` landed (2026-10-02),
  run `keld signal env dev` right after installing: it saves the choice in `agent-config.json` and
  restarts Signal, and `keld signal status` then says "Atlas: dev … not production". With the older
  installer from run 36719519251, set the variables **before** installing, for the signed-in user:
  - Windows (PowerShell): `setx KELD_API_URL https://atlas-dev.keld.co` and
    `setx KELD_ATLAS_WEB_URL https://atlas-dev.keld.co`, then sign out and back in so new processes see them.
  - macOS: `launchctl setenv KELD_API_URL https://atlas-dev.keld.co` and
    `launchctl setenv KELD_ATLAS_WEB_URL https://atlas-dev.keld.co`.
- Take a screenshot at every step marked 📸 and keep them for the report.

## Run 1 — double-click install (a person installing)

| # | Do | Pass when |
|---|---|---|
| 1.1 | Double-click the installer and click through with the defaults. 📸 each page. | No page asks for a setup code, a sign-in or which tools to use. |
| 1.2 | Finish. 📸 the last page. | Exactly **one** "open Signal" option is shown and ticked. Windows: it opens the Keld Signal desktop app. No console window stays open. |
| 1.3 | Let it open Signal. 📸 | The first-open screen: **Sign in with Atlas** and **Use without an account**. |
| 1.4 | Click **Sign in with Atlas**. 📸 the browser. | The default browser opens `…/cli/signal/authorize?…` on atlas-dev. The Signal window says to finish in the browser and shows the link. |
| 1.5 | Sign in if asked, then 📸 the Continue screen. | It shows the tester's email and org and one **Continue** button. |
| 1.6 | Click **Continue**. 📸 the tab, then 📸 Signal. | The tab says **"Signed in. Close this tab."** Within about 5 s Signal shows "Signed in as <email> · <org>". |
| 1.7 | Check the files: Windows `%USERPROFILE%\.keld\hook.json`, macOS `~/.keld/hook.json`. Then run `keld signal doctor`. | `hook.json` has an `endpoint` on atlas-dev and a non-empty `ingest_token`. Doctor reports no problems (or only ones unrelated to pairing). |

## Run 2 — local only (restore the clean snapshot first)

| # | Do | Pass when |
|---|---|---|
| 2.1 | Install by double-click, open Signal, click **Use without an account**. 📸 | The normal Signal page appears. A "restart to apply" bar may show; note it, it is a known polish item. |
| 2.2 | Close and reopen Signal; reboot once and reopen. | The first-open screen does not come back. `~/.keld/agent-config.json` has `"send_to_atlas": false`. |
| 2.3 | Settings → **Sign in with Atlas**, finish in the browser. | Pairs as in 1.4–1.7. |

## Run 3 — silent install, as IT would do it (restore the clean snapshot first)

Get a setup code from atlas-dev: Integrations → Signal → setup code (`atlas-dev.keld.co/XXXX-XXXX`).

| # | Do | Pass when |
|---|---|---|
| 3.1 | Windows: `Start-Process .\keld-setup.exe -ArgumentList '/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART' -Wait`. macOS: `sudo installer -pkg <pkg> -target /`. | Exit code 0. **No window of any kind opens.** macOS: the analysis engine download starts in the background (`~/.local/bin/keld-agent-sidecar` appears within a few minutes). |
| 3.2 | As the signed-in user (not admin/root): `keld-agent install --code <CODE>`. | `hook.json` has an ingest token; nothing opened. |
| 3.3 | Open Signal. | Signed in; the first-open screen never appears. |

## Run 4 — browsers (after Run 1, same machine)

Change the default browser and repeat 1.4–1.6 each time. Windows: Edge, Chrome, Firefox. macOS: Safari,
Chrome, Firefox. **Pass** when every browser lands on "Signed in. Close this tab." with no block or warning
about opening `http://127.0.0.1`. 📸 any warning.

## Run 5 — Linux browser opener (only if a Linux desktop VM is available)

Install with `install.sh` (no `--code`), open Signal, click Sign in. **Pass** when a browser opens. If
nothing opens, the link shown on the page must still complete sign-in (spec §8 #5).

## Report

For every numbered step: **pass / fail / blocked**, one line of what happened, and the screenshot name.
Add the installer file names and hashes (`Get-FileHash` / `shasum -a 256`), the OS version, and the
browser versions. The lead records the result in the spec's actions chapter (A6) with the date.
