#!/usr/bin/env bash
# Guards on installers/windows/keld-agent.iss and onboard.cmd.
#
# ⚠️ EVERY ASSERTION HERE IS A BUG THAT ALREADY SHIPPED. `iscc` compiling the
# script proves the files are staged and the syntax parses; it cannot tell you
# that the one [Run] entry is behind a checkbox nobody has to tick, or that it is
# hidden where no human can complete it. Both of those reached main.
#
# Run: bash installers/windows/keld_agent_iss_test.sh
set -euo pipefail
d="$(cd "$(dirname "$0")" && pwd)"
iss="$d/keld-agent.iss"
cmd="$d/onboard.cmd"
fail() { echo "FAIL: $*" >&2; exit 1; }

test -f "$iss" || fail "missing keld-agent.iss"
test -f "$cmd" || fail "missing onboard.cmd"

# ⚠️ UNFOLD THE BACKSLASH CONTINUATIONS FIRST. Inno entries wrap, so the Flags:
# live on the line AFTER the Filename:. Grepping the raw file matches only the
# first half and every flag assertion below passes VACUOUSLY — which is exactly
# what this script did on its first run, reporting a clean bill on a file whose
# flags it had never looked at.
run_block="$(sed -n '/^\[Run\]/,/^\[Code\]/p' "$iss" | sed -e ':a' -e '/\\$/{N;s/\\\n[[:space:]]*//;ba}')"

# 1. ⚠️ REGISTRATION MUST ALWAYS HAPPEN, AND IT NO LONGER LIVES IN [Run].
#    It moved into CurStepChanged/ssPostInstall because `Flags: runhidden` hides
#    a window without stopping Windows allocating a console, and that console
#    appeared on a real install ("it pops a terminal window open twice"). Only
#    keld-wizard-host can pass CREATE_NO_WINDOW, so the call goes through it.
#
#    ⚠️ THE MOVE IMMEDIATELY BROKE THE INVARIANT THIS GUARD EXISTS FOR, which is
#    why it is rewritten rather than deleted. ssPostInstall returns early when
#    the wizard page did not pair, so registering there put the agent behind
#    `Paired` — and on an MDM /SILENT push the page never runs, Paired is always
#    false, and the machine would have installed the files and registered
#    NOTHING, silently. Exactly the failure the original wording describes.
# ⚠️ MATCH ENTRY LINES ONLY. The comments in [Run] name `keld-agent.exe` and
# `runhidden` while explaining why they are the way they are, so an unscoped grep
# matches the PROSE — and deleting the real entry still passed. Found by testing
# this guard against a deliberately broken file rather than trusting it. Still
# needed below, for onboard.cmd.
entries="$(printf '%s\n' "$run_block" | grep '^Filename:' || true)"

# ⚠️ Match the CALL, not its arguments. Keying this on "install --headless"
# made 1b unreachable: dropping the flag also emptied this variable, so the
# missing-registration error fired instead and the flag guard could never
# report. Found by checking that each guard fails for ITS OWN reason.
reg_call="$(sed -n '/procedure CurStepChanged/,/^end;/p' "$iss" \
            | grep -F 'keld-agent.exe' | grep -F 'install' || true)"
[ -n "$reg_call" ] || \
  fail "nothing in ssPostInstall registers the agent; a silent install would register nothing"

# 1a. It must NOT sit inside the `if Paired then` block. Checked structurally:
#     the registration has to appear AFTER that block has closed.
body="$(sed -n '/procedure CurStepChanged/,/^end;/p' "$iss")"
# ⚠️ `|| true` ON EVERY ONE. Under `set -euo pipefail` a command substitution
# whose grep matches nothing kills this script SILENTLY — exit 1, no message, no
# indication which check died. That is strictly worse than a failed assertion,
# because it looks like a crash rather than a finding, and it is what happened
# the first time these were tested against a file with the registration removed.
paired_ln="$(printf '%s\n' "$body" | grep -n 'if Paired then' | head -1 | cut -d: -f1 || true)"
reg_ln="$(printf '%s\n' "$body" | grep -n 'install --headless' | head -1 | cut -d: -f1 || true)"
close_ln="$(printf '%s\n' "$body" | grep -n '^  end;$' | tail -1 | cut -d: -f1 || true)"
if [ -n "$paired_ln" ] && [ -n "$reg_ln" ] && [ -n "$close_ln" ]; then
  [ "$reg_ln" -gt "$close_ln" ] || \
    fail "agent registration is inside the 'if Paired' block - a /SILENT push (page never runs, Paired false) would register nothing"
fi

# 1b. Registration must say --headless OUT LOUD. Hiding a window does not take
#     the console away, so keld-agent's TTY probe answered TRUE and it took its
#     INTERACTIVE branch: `keld login` invisibly, then `keld signal setup`
#     blocking forever on a [Y/n] against a stdin no human could reach, wedging
#     the installer until someone killed the child by hand. Running through
#     keld-wizard-host removes the console entirely, which makes the flag's job
#     easier rather than unnecessary - state the intent, do not infer it.
printf '%s\n' "$reg_call" | grep -q -- '--headless' || \
  fail "agent registration omits --headless - install would prompt where nobody can answer and hang"

# 1c. And it must go through RunQuiet, not a bare Exec: that is the only path
#     that passes CREATE_NO_WINDOW.
printf '%s\n' "$reg_call" | grep -q 'RunQuiet' || \
  fail "agent registration does not use RunQuiet - Inno's SW_HIDE leaves the console allocated and it shows"

# 2. Onboarding must be VISIBLE. runhidden here is what made every Windows
#    machine idle forever: an interactive login in a window nobody could see.
onb_line="$(printf '%s\n' "$entries" | grep -F 'onboard.cmd' || true)"
[ -n "$onb_line" ] || fail "no [Run] entry opens onboard.cmd"
printf '%s\n' "$onb_line" | grep -q 'runhidden' && \
  fail "onboard.cmd is 'runhidden' — a human cannot complete a login they cannot see"
printf '%s\n' "$onb_line" | grep -q 'skipifsilent' || \
  fail "onboard.cmd must be 'skipifsilent' or a /SILENT MDM push blocks on a console"

# 3. onboard.cmd must be staged, or iscc fails late and opaquely.
grep -qF 'Source: "onboard.cmd"' "$iss" || fail "onboard.cmd is not staged in [Files]"

# 3b. PATH must be added WITHOUT asking. A [Tasks] checkbox for it is opt-out, and
#     getting it wrong fails silently: every command this installer tells the user to
#     run ("keld login", "keld signal setup") is then "not recognized", which reads as
#     a broken install rather than an unconfigured one.
#     Unfold continuations first — the [Registry] entry wraps, same trap as [Run].
unfolded="$(sed -e ':a' -e '/\\$/{N;s/\\\n[[:space:]]*//;ba}' "$iss")"
# ⚠️ A CODE-ONLY VIEW, because these comments EXPLAIN the very identifiers being
# asserted on. Mutation-testing this script caught two vacuous guards: deleting
# `MB_DEFBUTTON2` from the code still passed, because the comment above it names
# MB_DEFBUTTON2; and commenting OUT the RemoveFromPath call still passed, because a
# commented line still contains the call. Both would have shipped a guard that can
# never fail. Inno comments are `;` outside [Code] and `//` inside it.
code="$(printf '%s\n' "$unfolded" | grep -vE '^[[:space:]]*(;|//)')"

path_line="$(printf '%s\n' "$unfolded" | grep '^Root: HKCU' | grep -F 'Path' || true)"
[ -n "$path_line" ] || fail "no [Registry] entry adds {app} to PATH"
printf '%s\n' "$path_line" | grep -q 'Tasks:' && \
  fail "PATH is behind a [Tasks] checkbox — a user can untick it and every printed command then fails"
grep -q '^Name: "addtopath"' "$iss" && \
  fail "the addtopath task is back; PATH must be unconditional"

# 3c. ...but it must still be GUARDED, or a re-install appends {app} again every run
#     and PATH grows without bound. Unconditional is not the same as unchecked.
printf '%s\n' "$path_line" | grep -q 'Check: NeedsAddPath' || \
  fail "PATH entry lost its NeedsAddPath check — re-installs would append {app} forever"
printf '%s\n' "$code" | grep -q 'function NeedsAddPath' || fail "NeedsAddPath is referenced but not defined"

# 3d. The per-file label must stay hidden. The payload is the frozen sidecar (~15,000
#     torch/transformers files), so Inno's FilenameLabel becomes minutes of unfamiliar
#     deep paths scrolling past — an on-device privacy product must not look like it is
#     rummaging through the machine. The progress bar and status line are untouched.
printf '%s\n' "$code" | grep -q 'WizardForm.FilenameLabel.Visible := False' || \
  fail "the per-file extraction label is not hidden; ~15,000 sidecar paths would scroll past the user"

# 3e. UNINSTALL MUST UNDO WHAT INSTALL DID. Every item here shipped as a leftover:
#     the KeldAgent scheduled task outlived the uninstall pointing at a deleted exe,
#     and the tool configs kept aiming Claude Code / Codex / Gemini at a loopback
#     OTLP port nothing answers any more. Neither surfaced as an error anywhere.
grep -q '^\[UninstallRun\]' "$iss" || fail "no [UninstallRun]; uninstall would leave the scheduled task and tool configs behind"
unrun="$(printf '%s\n' "$unfolded" | sed -n '/^\[UninstallRun\]/,/^\[Code\]/p' | grep '^Filename:' || true)"

# ⚠️ MATCH THE Filename FIELD, NOT THE LINE. The taskkill entry names
# keld-agent.exe inside its /IM arguments, so an unscoped `grep -F keld-agent.exe`
# matches TWO entries — and the ordering test then compared against "2\n3" and died
# with `[: 2\n3: integer expression expected`. That is exactly what happened on this
# guard's first real CI run: it failed on its own bug rather than on the file it
# guards, which is worse than not existing, because it reads as the file being wrong.
tools_line="$(printf '%s\n' "$unrun" | grep -nF 'Filename: "{app}\keld.exe"' || true)"
dereg_line="$(printf '%s\n' "$unrun" | grep -nF 'Filename: "{app}\keld-agent.exe"' || true)"

[ -n "$tools_line" ] || \
  fail "uninstall never restores the tool configs — the tools keep posting to a dead loopback port forever"
printf '%s\n' "$tools_line" | grep -q 'signal uninstall' || \
  fail "the keld.exe uninstall entry does not run 'signal uninstall'"
[ -n "$dereg_line" ] || \
  fail "uninstall never deregisters the agent — the KeldAgent task outlives it, pointing at a deleted exe"
printf '%s\n' "$dereg_line" | grep -q 'Parameters: "uninstall"' || \
  fail "the keld-agent.exe uninstall entry does not run 'uninstall'"

#     ORDER: restoring the tool configs reads the manifest under ~/.keld, so it must
#     come before anything that can remove it.
tools_at="${tools_line%%:*}"
dereg_at="${dereg_line%%:*}"
[ "$tools_at" -lt "$dereg_at" ] || \
  fail "tool-config restore must run before deregistration (it needs the manifest under ~/.keld)"

#     A partially-completed earlier uninstall leaves no binaries, and Inno reports a
#     HARD ERROR when it cannot start a command. Missing binaries must be a no-op.
#     BOTH Keld entries, checked separately — one grep over both would pass on either.
printf '%s\n' "$tools_line" | grep -q 'skipifdoesntexist' || \
  fail "the keld.exe uninstall entry lacks skipifdoesntexist — a re-run after a partial uninstall would error"
printf '%s\n' "$dereg_line" | grep -q 'skipifdoesntexist' || \
  fail "the keld-agent.exe uninstall entry lacks skipifdoesntexist — a re-run after a partial uninstall would error"

# 3f. PATH must be removed on uninstall, and REMOVAL MUST BE SURGICAL. Inno cannot
#     subtract from a shared value declaratively (uninsdeletevalue would delete the
#     user's WHOLE Path), so it is done in [Code] — and the guard is that the code
#     exists and is actually called, since a defined-but-uncalled procedure leaves the
#     stale entry behind exactly as before while looking fixed.
printf '%s\n' "$code" | grep -q 'procedure RemoveFromPath' || fail "PATH is added on install but never removed on uninstall"
printf '%s\n' "$code" | grep -q 'RemoveFromPath(ExpandConstant' || fail "RemoveFromPath is defined but never called"
printf '%s\n' "$code" | grep -q 'procedure CurUninstallStepChanged' || fail "no uninstall-step hook to run the cleanup from"

# 3g. Removing ~/.keld must ASK, and must default to NO. It holds auth.json and
#     hook.json: destroying a login silently is not a cleanup, and an uninstall driven
#     by someone pressing Enter must keep the data.
printf '%s\n' "$code" | grep -q 'DelTree' || fail "no option to remove ~/.keld"
printf '%s\n' "$code" | grep -q 'MB_DEFBUTTON2' || \
  fail "the ~/.keld removal prompt does not default to No — Enter would destroy the user's credentials"
printf '%s\n' "$code" | grep -q "GetEnv('KELD_HOME')" || \
  fail "the ~/.keld prompt ignores KELD_HOME and would offer to delete a directory that is not in use"

# 4. onboard.cmd's own contract: redeem a code, fall back to a browser login, and
#    report from OBSERVED STATE rather than an exit code.
grep -qF 'install --code' "$cmd" || fail "onboard.cmd never redeems a setup code"
grep -qF 'install --login --yes' "$cmd" || fail "onboard.cmd has no browser-login fallback"
grep -qF 'ingest_token'   "$cmd" || fail "onboard.cmd claims success without checking hook.json"

# ── The wizard page ──────────────────────────────────────────────────────────

# 5. ⚠️ INNO READS A SCRIPT AS UTF-8 ONLY WHEN IT HAS A BOM. Without one it falls
#    back to the system codepage and every non-ASCII character in a DISPLAYED
#    string becomes mojibake — no error, no warning, nothing in the compile
#    output. Measured on the first real run of the page, which read
#      Connected â€" dg@keld.co Â· Keld
#    where an em-dash and a middot should have been.
bom="$(head -c 3 "$iss" | od -An -tx1 | tr -d ' \n')"
[ "$bom" = "efbbbf" ] || \
  fail "keld-agent.iss has no UTF-8 BOM - every non-ASCII string renders as mojibake"

# 6. The page runs BEFORE the payload is installed, so it drives copies extracted
#    to {tmp}. Without a dontcopy entry it drives paths that do not exist, and
#    every step fails to start.
grep -q 'Source: "keld.exe";.*Flags: dontcopy' "$iss" || \
  fail "keld.exe is not staged dontcopy - the wizard page would have nothing to drive"
grep -q 'Source: "keld-wizard-host.exe";.*Flags: dontcopy' "$iss" || \
  fail "keld-wizard-host.exe is not staged dontcopy - the page could not run anything"
grep -q 'ExtractTemporaryFile' "$iss" || \
  fail "no ExtractTemporaryFile - a dontcopy file is not on disk until it is extracted"

# 7. The helper is required by the page AND installed, so CI must stage it too.
grep -q 'Source: "keld-wizard-host.exe";.*DestDir' "$iss" || \
  fail "keld-wizard-host.exe is not installed to {app}"

# 8. ⚠️ Without --bin-path every tool hook pins {tmp}\keld.exe, a path that stops
#    existing when the wizard closes. The config looks right; the hook never runs.
code_block="$(sed -n '/^\[Code\]/,$p' "$iss")"
printf '%s\n' "$code_block" | grep -q -- '--bin-path' || \
  fail "ssPostInstall omits --bin-path - every tool hook would pin a temp path"

# 9. The console fallback must no longer fire on the success path, and must still
#    exist for /SILENT and for a [Code] failure.
printf '%s\n' "$onb_line" | grep -q 'Check:' || \
  fail "onboard.cmd is unconditional - a console would open after a successful wizard"

# 10. ⚠️ A `Source:` the BUILD never produces is a compile error, and the two
#     build paths are NOT the same path. installers.yml stages keld.exe /
#     keld-agent.exe / keld-wizard-host.exe from the GoReleaser archive on a
#     release, and rebuilds them natively on a workflow_dispatch DRY RUN. The
#     helper was added to the release path and to .goreleaser.yaml but not to the
#     dry-run branch, so the only way to exercise this workflow without cutting a
#     release failed on a Copy-Item the release path would have satisfied —
#     i.e. the rehearsal broke while the performance worked, which is the worst
#     ordering there is. Guard #7 above proves the .iss wants the binary; this
#     proves both halves of CI actually produce it.
wf="$d/../../.github/workflows/installers.yml"
test -f "$wf" || fail "cannot find installers.yml - this guard would pass vacuously"
stage_step="$(sed -n '/name: Stage keld\/keld-agent binaries/,/^      - name: /p' "$wf")"
printf '%s\n' "$stage_step" | grep -q 'cmd/keld-wizard-host' || \
  fail "installers.yml never builds cmd/keld-wizard-host on the dry-run path - a workflow_dispatch run cannot package Windows"
grep -q 'keld-wizard-host' "$d/../../.goreleaser.yaml" || \
  fail ".goreleaser.yaml does not build keld-wizard-host - the RELEASE path would stage a binary the archive lacks"
# The archive it rides must be the one the workflow unzips (keld_windows_amd64.zip),
# so the id has to appear in an archive's `ids:` list, not merely under `builds:`.
# ⚠️ AND A WINDOWS-ONLY BINARY IN A SHARED ARCHIVE NEEDS
#    `allow_different_binary_count`. Without it GoReleaser refuses to build at
#    all — "archive has different count of binaries for each platform" — because
#    the Windows archive holds three binaries and the others hold two.
#    ⚠️ THIS GUARD USED TO REQUIRE ONLY THE FIRST HALF, so it enforced exactly
#    the thing that breaks the release. Nothing caught it: `goreleaser check`
#    lints the CONFIG (valid), the installer dry run uses `go build` natively,
#    and only a REAL release runs GoReleaser — so it first fired on the v3.1.0
#    tag and took the release down at its first step.
awk '/^archives:/{a=1} a' "$d/../../.goreleaser.yaml" | grep -q 'allow_different_binary_count' || \
  fail "the shared archive lacks allow_different_binary_count - GoReleaser refuses a windows-only binary beside cross-platform ones"
awk '/^archives:/{a=1} a' "$d/../../.goreleaser.yaml" | grep -q 'keld-wizard-host' || \
  fail "keld-wizard-host is built but not listed in any archive's ids - it would never reach the release asset"

# 9a. ⚠️ THE RESTART MANAGER MUST STAY OFF, AND SOMETHING MUST STOP THE AGENT
#     INSTEAD. Inno defaults to CloseApplications=yes (a modal listing processes
#     to close, which reads as an error on every upgrade) and
#     RestartApplications=yes — which RELAUNCHES the console-subsystem daemon
#     from a GUI installer, giving it a fresh console window, outside the
#     scheduled task and without --hide-console.
grep -q '^CloseApplications=no'   "$iss" || \
  fail "CloseApplications is not disabled - every upgrade shows a Restart Manager modal that reads as an error"
grep -q '^RestartApplications=no' "$iss" || \
  fail "RestartApplications is not disabled - Inno relaunches keld-agent.exe itself, with a console window and outside the task"
grep -q 'function PrepareToInstall' "$iss" || \
  fail "nothing stops the running agent before files are replaced; with CloseApplications=no the upgrade would fail on locked binaries"
prep="$(sed -n '/function PrepareToInstall/,/^end;/p' "$iss")"
printf '%s\n' "$prep" | grep -q 'taskkill' || fail "PrepareToInstall does not stop the agent processes"

# 9a-2. ⚠️ AN UPGRADE MUST PICK UP THE SIGNED UNINSTALLER. Inno keeps an existing
#       unins000.exe — it only writes one when it is missing or older, and both
#       come from the same Inno version. Measured: v11 replaced every binary in
#       {app} and left unins000.exe untouched and NotSigned, while CI had proved
#       the stub inside keld-setup.exe WAS signed. So every machine that upgraded
#       keeps an uninstaller SAC will refuse.
grep -q 'procedure RemoveStaleUninstaller' "$iss" || \
  fail "nothing removes the stale uninstaller; an upgraded machine keeps the unsigned one and cannot uninstall"
stale="$(sed -n '/procedure RemoveStaleUninstaller/,/^end;/p' "$iss")"
# ⚠️ The .dat claims the slot. Deleting it would orphan the log and strand every
#    file recorded in it, so the procedure must read it and must NOT delete it.
printf '%s\n' "$stale" | grep -q 'unins000.dat' || \
  fail "RemoveStaleUninstaller ignores unins000.dat - without that check the slot may not be ours to reuse"
printf '%s\n' "$stale" | grep -q "DeleteFile(Dat)" && \
  fail "RemoveStaleUninstaller deletes the uninstall LOG - that orphans every file it records"
# And the outcome must be verified, since reusing the slot is an assumption.
grep -q '{uninstallexe}' "$iss" || \
  fail "nothing confirms an uninstaller exists after install; a wrong slot guess would be silent until someone uninstalls"
# Both helpers are console programs launched from a GUI installer.
[ "$(printf '%s\n' "$prep" | grep -c 'SW_HIDE')" -ge 2 ] || \
  fail "PrepareToInstall runs schtasks/taskkill without SW_HIDE - each pops a console window"

# 9b. ⚠️ THE CONSOLE FALLBACK MUST NOT AUTO-RUN. `postinstall` entries are TICKED
#     BY DEFAULT, so on any install that did not end paired, closing the
#     installer launched onboard.cmd and left a blank console sitting on the
#     desktop waiting for input — on a product whose whole Windows story is that
#     no terminal ever appears. `unchecked` keeps the fallback reachable while
#     making it a deliberate choice.
printf '%s\n' "$onb_line" | grep -q 'unchecked' || \
  fail "onboard.cmd is a ticked-by-default postinstall action - it will open a console at every unpaired install"

# 9c. ⚠️ THE APPROVAL PANEL BELONGS TO THE SIGN-IN RUN ONLY, AND WITHOUT THAT
#     SCOPE IT COMES BACK OVER THE NEXT STEP. DrainRun evaluates its "show the
#     panel" condition for EVERY event of EVERY run. EvApprovalURL is cleared
#     only when a sign-in STARTS, and HideApproval resets ApprovalShown to False
#     — so after a successful sign-in both halves were true again and the first
#     `tool` event of the NEXT run launched a second WebView2 re-navigating to
#     the sign-in page, behind the tool checkboxes. Reported as the checklist
#     drawn "on top of the old sign in page"; the page underneath was live.
show_cond="$(printf '%s\n' "$code" | grep -n 'ShowApproval(EvApprovalURL)' -B4 || true)"
printf '%s\n' "$show_cond" | grep -q 'Mode = RunSignIn' || \
  fail "the approval panel is shown without checking Mode - it will relaunch over the tools step"
# And the URL must be retired once used: a spent device code cannot be approved,
# so any later reader (the browser fallback, a retry) would send someone nowhere.
after_login="$(sed -n '/^procedure AfterLogin/,/^end;/p' "$iss" || true)"
[ -n "$after_login" ] || fail "cannot find procedure AfterLogin - this guard would pass vacuously"
printf '%s\n' "$after_login" | grep -qF "EvApprovalURL :=" || \
  fail "AfterLogin does not clear EvApprovalURL - a spent approval URL stays live for later readers"

# 10a. ⚠️ ONLY A KNOWN FAILURE MAY FALL BACK TO A BROWSER. DrainPanel used to end
#      in an unconditional else, so ANY panel status the script did not
#      recognise tore down a working embed and launched a browser. Adding one
#      diagnostic event to the helper was enough to trigger it: the sign-in form
#      rendered, the next event arrived, and a browser window replaced it.
#      The helper and this script ship together but are edited separately, so an
#      unrecognised status means "newer helper", never "the embed failed".
drain="$(sed -n '/^procedure DrainPanel/,/^end;/p' "$iss")"
printf '%s\n' "$drain" | grep -q "Status <> 'no_runtime'" || \
  fail "DrainPanel falls back to a browser on ANY unrecognised status - one new diagnostic event would eject a working embed"
printf '%s\n' "$drain" | grep -q 'ShellExec' || \
  fail "DrainPanel no longer has a browser fallback at all - the no-WebView2 case would leave a blank rectangle"

# 10b. ⚠️ THE WEB PANEL MUST BE VISIBLE BEFORE THE HELPER EMBEDS INTO IT.
#      StartPanel hands WebPanel.Handle to the helper, which creates a WebView2
#      controller as a child of that window. A controller created under a HIDDEN
#      parent NEVER STARTS RENDERING, and showing the parent afterwards does not
#      notify it — so the page loads, its JavaScript runs (proved by
#      atlas.keld.co bytes in the WebView2 code cache) and nothing is painted.
#      Shipped in 31cafa0 and reported as "this used to work".
approval="$(sed -n '/^procedure ShowApproval/,/^end;/p' "$iss")"
printf '%s\n' "$approval" | grep -q 'WebPanel.Visible := True' || \
  fail "ShowApproval does not make WebPanel visible - a WebView2 embedded into a hidden window renders nothing, ever"
# and the order matters: visible FIRST, then hand the handle over.
vis_ln="$(printf '%s\n' "$approval" | grep -n 'WebPanel.Visible := True' | head -1 | cut -d: -f1)"
start_ln="$(printf '%s\n' "$approval" | grep -n 'StartPanel(' | head -1 | cut -d: -f1)"
if [ -n "$vis_ln" ] && [ -n "$start_ln" ] && [ "$vis_ln" -gt "$start_ln" ]; then
  fail "WebPanel is shown AFTER StartPanel - the controller is still created under a hidden window"
fi
printf '%s\n' "$approval" | grep -q 'WebPanel.Visible := False' && \
  fail "ShowApproval still hides WebPanel; that is the line that made the sign-in page render nothing"

# 10c. ⚠️ THE DESKTOP APP SHIPS BESIDE keld.exe, AND ITS ABSENCE MUST NOT BREAK
#      THE BUILD. `keld signal open` prefers the app over a browser tab and finds
#      it by looking next to the running keld.exe — so it installs into {app}
#      rather than its own directory, giving one install dir and nothing to keep
#      in step. But it is a CONVENIENCE: without it `signal open` opens a
#      browser, which is what every release before this did. A Rust build failing
#      on the runner must not take the whole Windows installer down, and a
#      missing `Source:` is otherwise a compile error.
app_line="$(printf '%s\n' "$unfolded" | grep -F 'Source: "Keld Signal.exe"' || true)"
[ -n "$app_line" ] || \
  fail "the desktop app is not shipped; keld signal open would always fall back to a browser"
printf '%s\n' "$app_line" | grep -q 'DestDir: "{app}"' || \
  fail "the desktop app is not installed beside keld.exe - signal open looks there first and would not find it"
printf '%s\n' "$app_line" | grep -q 'skipifsourcedoesntexist' || \
  fail "the desktop app is a required Source - a failed Rust build would fail the whole installer compile"
grep -q 'name: Build the desktop app (Windows)' "$wf" || \
  fail "nothing builds the desktop app on the Windows leg; the Source above would never exist"
# ⚠️ --bundles app is a macOS FORMAT; on Windows it would emit nsis/msi, i.e. a
#    second installer beside keld-setup.exe. We want the bare executable.
app_step="$(sed -n '/name: Build the desktop app (Windows)/,/name: Restore HuggingFace/p' "$wf")"
printf '%s\n' "$app_step" | grep -q -- '--no-bundle' || \
  fail "the Windows app build does not use --no-bundle - it would produce a second installer"

# 11. ⚠️ THE PAYLOAD IS SIGNED BEFORE iscc AND THE INSTALLER AFTER, AND THAT
#     ORDER IS THE WHOLE POINT. Smart App Control evaluates a binary as it
#     LOADS, so an installer signed over an unsigned payload installs fine and
#     is refused the moment keld.exe starts — the exact failure measured on a
#     real machine. Reordered, every step still "passes" and the product is
#     dead on the machines this exists for, which is why it is pinned by LINE
#     ORDER rather than by presence.
ln_payload="$(grep -n 'name: Sign the Windows payload'   "$wf" | cut -d: -f1)"
ln_iscc="$(   grep -n 'name: Package Windows installer'  "$wf" | cut -d: -f1)"
ln_setup="$(  grep -n 'name: Sign the Windows installer' "$wf" | cut -d: -f1)"
for v in ln_payload ln_iscc ln_setup; do
  [ -n "${!v}" ] || fail "installers.yml has no step for $v - the Windows signing chain is incomplete"
done
[ "$ln_payload" -lt "$ln_iscc" ] || \
  fail "the payload is signed AFTER iscc - the installer would carry unsigned binaries and SAC refuses them at load"
[ "$ln_setup" -gt "$ln_iscc" ] || \
  fail "keld-setup.exe is signed BEFORE iscc builds it - that step can only be signing a stale or absent file"

# 12. Vendor signatures must not be swept away: the action is handed an explicit
#     catalog, never a recursive folder sweep. 78 of the payload's 188 PE
#     binaries arrive signed by their own vendors, and re-signing replaces an
#     attestation we cannot recreate with one we have no standing to make.
grep -q 'files-catalog:' "$wf" || \
  fail "installers.yml does not hand the signing action a catalog"
grep -q 'files-folder-recurse:' "$wf" && \
  fail "installers.yml sweeps a folder recursively - that re-signs vendor-signed binaries; use the catalog"
grep -q 'CatalogOut' "$wf" || fail "nothing generates the signing catalog"
grep -q 'VerifyCatalog' "$wf" || \
  fail "nothing verifies the catalog after signing - a signer that exits 0 having skipped a file would ship"

# 13. Timestamping. Artifact Signing certificates are short-lived and rotated by
#     the service, so an untimestamped signature stops validating within weeks of
#     shipping. Both signing steps must carry it.
[ "$(grep -c 'timestamp-rfc3161:' "$wf")" -eq 2 ] || \
  fail "expected both signing steps to set timestamp-rfc3161; short-lived certs make this mandatory, not optional"

# 13a. ⚠️ THE UNINSTALLER MUST BE SIGNED DURING THE COMPILE — nothing before or
#      after can reach it. Inno extracts unins000.exe on the target machine at
#      install time, so an unsigned one is refused on exactly the machines
#      installing was refused on, leaving people unable to remove the product.
#      Only Inno can sign it, and only via a COMMAND LINE, which is why this one
#      path uses the signtool dlib rather than the signing Action.
grep -q 'SignedUninstaller=yes' "$iss" || \
  fail "SignedUninstaller is gone - the uninstaller would ship unsigned and be blocked on SAC machines"
grep -q 'name: Prepare the uninstaller signer' "$wf" || \
  fail "nothing builds the uninstaller signing command; SignedUninstaller would have no SignTool and iscc HALTS"
prep_sign="$(sed -n '/name: Prepare the uninstaller signer/,/name: Package Windows installer/p' "$wf")"
printf '%s\n' "$prep_sign" | grep -q 'Azure.CodeSigning.Dlib.dll' || \
  fail "the uninstaller signer does not use the Trusted Signing dlib"
printf '%s\n' "$prep_sign" | grep -q 'timestamp.acs.microsoft.com' || \
  fail "the uninstaller signature is not timestamped - short-lived certs make it invalid within weeks"
# ⚠️ `$f` is INNO's placeholder for the file being signed; without it signtool
# gets no file and every signature fails, which HALTS the compile.
printf '%s\n' "$prep_sign" | grep -q 'dmdf' || \
  fail "no /dmdf metadata file - the dlib cannot resolve the account or certificate profile"
# And the compile must PROVE it happened rather than trust the directive.
pkg_step="$(sed -n '/name: Package Windows installer/,/name: Sign the Windows installer/p' "$wf")"
# ⚠️ The check must key on the ARTIFACT (uninst.e32), not on a log phrase. Inno
#    never prints "Signing uninstaller"; it runs the tool and names the file. The
#    first version guessed the wording and failed a build whose uninstaller had
#    been signed correctly — a false negative on the one thing being verified.
printf '%s\n' "$pkg_step" | grep -qF 'uninst\.e32' || \
  fail "the build does not verify the uninstaller stub (uninst.e32) was signed"
printf '%s\n' "$pkg_step" | grep -q 'Skeldsign' || \
  fail "iscc is not given the keldsign SignTool; SignedUninstaller would halt the compile"
# ⚠️ THE SIGNTOOL DLIB AUTHENTICATES FROM THE ENVIRONMENT, unlike the Action,
#    which takes the same credentials as inputs. Without these the dlib walks
#    DefaultAzureCredential all the way to InteractiveBrowserCredential and
#    BLOCKS waiting for a browser that cannot exist on a runner - measured at two
#    hours with no output before the run was cancelled.
printf '%s\n' "$pkg_step" | grep -q 'AZURE_CLIENT_SECRET' || \
  fail "the iscc step has no AZURE_* credentials; the uninstaller signer would hang on interactive auth"
printf '%s\n' "$pkg_step" | grep -q 'timeout-minutes' || \
  fail "the iscc step has no timeout; a blocking signing call would burn the whole job"
# ⚠️ MATCH THE LIST ENTRY, NOT THE PROSE. The comment beside this setting names
#    InteractiveBrowserCredential while explaining why it is excluded, so an
#    unscoped grep matched the explanation and passed with the setting deleted —
#    the same way guard #1 once matched the [Run] comments. Found by checking
#    that it fails.
printf '%s\n' "$prep_sign" | grep -qF "'InteractiveBrowserCredential'," || \
  fail "the signing metadata does not exclude InteractiveBrowserCredential - a missing credential hangs instead of failing"

# 13b. ⚠️ THE ACTION'S `files:` INPUT REQUIRES AN ABSOLUTE PATH AND REFUSES A
#      RELATIVE ONE ("The file path '...' is not rooted." — measured, run
#      36144191695, which signed all 111 payload binaries and then failed on the
#      installer). `files-catalog` is the opposite: its path may be relative and
#      its ENTRIES are relative to it. The two inputs disagree, so copying the
#      shape from one to the other is exactly the mistake that shipped.
awk '/files:/ && !/files-catalog:/ && !/files-folder/' "$wf" | grep -q 'github.workspace' || \
  fail "the signing action's files: input is not rooted at github.workspace - the action refuses a relative path"

# 14. ⚠️ A RELEASE MAY NOT SHIP UNSIGNED. Degrading to a warning is correct for
#     a fork or a dry run — those are MEANT to produce non-distributable output
#     — and wrong for a release, where it means a rotated-out client secret
#     silently ships an installer Smart App Control refuses, discovered by a
#     customer rather than by CI. Same hard gate macOS makes for notarization.
sign_step="$(sed -n '/name: Enumerate Windows binaries/,/name: Sign the Windows payload/p' "$wf")"
printf '%s\n' "$sign_step" | grep -q 'IS_RELEASE' || \
  fail "the Windows signing gate does not consult IS_RELEASE - an unsigned RELEASE would build and upload"
printf '%s\n' "$sign_step" | grep -qi 'throw .*UNSIGNED release' || \
  fail "an unsigned release is not refused - macOS hard-gates notarization and Windows must match"

echo "PASS: windows installer registers unconditionally, onboards in the wizard, keeps the console fallback gated, reads as UTF-8, adds PATH without asking, hides the file firehose, uninstalls cleanly, ships the wizard helper on both CI paths, signs the payload before iscc and the installer after without trampling vendor signatures, and claims success from observed state"
