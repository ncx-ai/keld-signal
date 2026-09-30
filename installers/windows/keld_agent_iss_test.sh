#!/usr/bin/env bash
# Guards on installers/windows/keld-agent.iss.
#
# ⚠️ EVERY ASSERTION HERE IS A BUG THAT ALREADY SHIPPED. `iscc` compiling the
# script proves the files are staged and the syntax parses; it cannot tell you
# that the one [Run] entry is behind a checkbox nobody has to tick, or that it is
# hidden where no human can complete it. Both of those reached main.
#
# Run: bash installers/windows/keld_agent_iss_test.sh
#
# ⚠️ `pipefail` IS DELIBERATELY ABSENT, AND PUTTING IT BACK MAKES THIS SUITE
# FAIL AT RANDOM ON CORRECT INPUT. Every assertion here pipes a variable into
# `grep -q`, which exits on its FIRST match and closes the pipe; the writer then
# takes EPIPE and returns non-zero, and pipefail hands that status to the whole
# pipeline — so `<writer> | grep -q X || fail` FAILS ON A MATCH. It bites only
# when the match is early and the payload outlasts the 64 KB pipe buffer, which
# is what made it intermittent rather than obvious.
#
# Measured 2026-09-29: it took down `installer-guards` in CI on a branch whose
# .iss was correct (run 36610376548) — "printf: write error: Broken pipe"
# followed by a FAIL for a condition the workflow plainly satisfied. Reproduced
# 200/200 with a 400 KB payload matching at byte 0, 0/200 with pipefail off, and
# 0/300 with the same payload matching at the END. `| head -1` and `| tail -1`,
# used throughout this file, have exactly the same shape.
#
# ⚠️ THE FALSE-FAILURE DIRECTION IS NOT THE DANGEROUS ONE. The negative
# assertions read `if <writer> | grep -q X; then fail; fi` — there a match plus
# EPIPE makes the pipeline non-zero, the `if` reads FALSE, and the guard
# silently does NOT fire. A false pass, on exactly the condition it exists to
# catch.
#
# Nothing is lost by dropping it: every assertion's verdict comes from the LAST
# command in its pipeline, and a writer that produced nothing still reaches grep
# as empty input, which fails the assertion the same way.
#
# ⚠️ `set -e` IS STILL ON, so the `|| true` on the command substitutions below
# remains load-bearing — that was never a pipefail matter.
set -eu
d="$(cd "$(dirname "$0")" && pwd)"
iss="$d/keld-agent.iss"
fail() { echo "FAIL: $*" >&2; exit 1; }

test -f "$iss" || fail "missing keld-agent.iss"
# ⚠️ INVERTED 2026-09-29 (web sign-in spec, D10): onboard.cmd is DELETED. It
# prompted for a setup code in a console, and no installer asks anything about
# Keld any more — Signal asks on first open.
[ ! -e "$d/onboard.cmd" ] || fail "onboard.cmd is back; no installer prompts for a setup code"

# ⚠️ UNFOLD THE BACKSLASH CONTINUATIONS FIRST. Inno entries wrap, so the Flags:
# live on the line AFTER the Filename:. Grepping the raw file matches only the
# first half and every flag assertion below passes VACUOUSLY — which is exactly
# what this script did on its first run, reporting a clean bill on a file whose
# flags it had never looked at.
# Portable (BSD and GNU): the `sed ':a;N'` form this used before is GNU-only, so
# the guard could only ever run on the ubuntu CI runner, never on a Mac.
unfold() {
  awk '{ sub(/\r$/, "") }
       { line = $0; if (buf != "") sub(/^[[:space:]]+/, "", line) }
       /\\$/ { sub(/\\$/, "", line); buf = buf line; next }
       { print buf line; buf = "" }' "$@"
}
run_block="$(sed -n '/^\[Run\]/,/^\[Code\]/p' "$iss" | unfold)"

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

# 1a. ⚠️ INVERTED 2026-09-29. Registration used to have to sit OUTSIDE an
#     `if Paired then` block, because the wizard page's pairing gated the tool
#     step and a /SILENT push never paired. The page is gone, so there is no
#     `Paired` at all — and registration must depend on nothing a person did.
#     A reintroduced pairing state in [Code] fails here.
body="$(sed -n '/procedure CurStepChanged/,/^end;/p' "$iss")"
printf '%s\n' "$body" | grep -vE '^[[:space:]]*//' | grep -q 'Paired' && \
  fail "ssPostInstall depends on a pairing state again; registration must be unconditional"

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

# 2. After an interactive install, Signal OPENS — that replaced onboard.cmd
#    (AC-10). The entry must be a postinstall action a person sees, and
#    `skipifsilent` so an MDM /SILENT push opens nothing on a screen nobody is at.
#    ⚠️ NEVER runhidden: onboard.cmd once ran as `runhidden nowait` and every
#    Windows machine idled forever behind a window nobody could see.
open_line="$(printf '%s\n' "$entries" | grep -F 'signal open' || true)"
[ -n "$open_line" ] || fail "no [Run] entry opens Signal after install"
printf '%s\n' "$open_line" | grep -qF 'Filename: "{app}\keld.exe"' || \
  fail "the page-open entry does not run the installed keld.exe"
printf '%s\n' "$open_line" | grep -q 'postinstall' || \
  fail "the page-open entry is not a postinstall action"
printf '%s\n' "$open_line" | grep -q 'skipifsilent' || \
  fail "the page-open entry must be 'skipifsilent' or a /SILENT MDM push opens a window at nobody"
printf '%s\n' "$open_line" | grep -q 'runhidden' && \
  fail "the page-open entry is 'runhidden'"
# ⚠️ Ticked by default, unlike the old console fallback: opening Signal IS the
#    finish of an interactive install, not a surprise.
printf '%s\n' "$open_line" | grep -q 'unchecked' && \
  fail "the page-open entry is unchecked - an interactive install would end without Signal ever asking its question"
# Nothing else in [Run] may prompt: every entry there is either the page-open
# step or nothing.
# ($entries spans to [Code], so it includes [UninstallRun]; scope this one to [Run].)
run_only="$(sed -n '/^\[Run\]/,/^\[UninstallRun\]/p' "$iss" | unfold | grep '^Filename:' || true)"
others="$(printf '%s\n' "$run_only" | grep -vF 'signal open' | grep -vF 'Filename: "{app}\Keld Signal.exe"' || true)"
[ -z "$others" ] || fail "[Run] has entries besides opening Signal: $others"

# 2a. The daemon writes agent.json when it starts, and `keld signal open` reads
#     it — so on a first install it would say "not running", and on an upgrade it
#     would open the PREVIOUS daemon's port and secret. ssPostInstall waits for
#     the file to change after registering, bounded.
printf '%s\n' "$body" | grep -q 'WaitForAgent' || \
  fail "ssPostInstall does not wait for the new daemon's agent.json before the finish page opens Signal"
reg_ln="$(printf '%s\n' "$body" | grep -n 'install --headless' | head -1 | cut -d: -f1 || true)"
wait_ln="$(printf '%s\n' "$body" | grep -n 'WaitForAgent' | tail -1 | cut -d: -f1 || true)"
[ -n "$reg_ln" ] && [ -n "$wait_ln" ] && [ "$wait_ln" -gt "$reg_ln" ] || \
  fail "the agent.json wait must come after registration"

# 3. onboard.cmd must not be staged — a Source: for a deleted file is a compile
#    error, and a staged prompt is what D10 removed.
grep -vE '^[[:space:]]*(;|//)' "$iss" | grep -qF 'onboard.cmd' && \
  fail "keld-agent.iss still stages or runs onboard.cmd"

# 3b. PATH must be added WITHOUT asking. A [Tasks] checkbox for it is opt-out, and
#     getting it wrong fails silently: every command this installer tells the user to
#     run ("keld login", "keld signal setup") is then "not recognized", which reads as
#     a broken install rather than an unconfigured one.
#     Unfold continuations first — the [Registry] entry wraps, same trap as [Run].
unfolded="$(unfold "$iss")"
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

# ── Encoding, and the absence of a wizard page ───────────────────────────────

# 5. ⚠️ INNO READS A SCRIPT AS UTF-8 ONLY WHEN IT HAS A BOM. Without one it falls
#    back to the system codepage and every non-ASCII character in a DISPLAYED
#    string becomes mojibake — no error, no warning, nothing in the compile
#    output. Measured on the first real run of the page, which read
#      Connected â€" dg@keld.co Â· Keld
#    where an em-dash and a middot should have been.
bom="$(head -c 3 "$iss" | od -An -tx1 | tr -d ' \n')"
[ "$bom" = "efbbbf" ] || \
  fail "keld-agent.iss has no UTF-8 BOM - every non-ASCII string renders as mojibake"

# 6. ⚠️ INVERTED 2026-09-29: there is NO "Set up Keld" page. It signed the person
#    in (setup code or an embedded device flow) and asked which tools to
#    configure, all before the install — the exact questions AC-10 removed from
#    every installer. A custom page, a setup-code field or a sign-in run coming
#    back fails here.
printf '%s\n' "$code" | grep -q 'CreateCustomPage' && \
  fail "the installer has a custom wizard page again; no installer asks anything about Keld"
printf '%s\n' "$code" | grep -qE "'login|login --|--code|whoami|device_code" && \
  fail "the installer signs in again; Signal asks on first open"
printf '%s\n' "$code" | grep -q 'signal setup' && \
  fail "the installer configures tools again; the daemon's auto-setup does that"
grep -q 'Flags: dontcopy' "$iss" && \
  fail "a dontcopy payload is back - it only existed to drive the removed page before install"

# 7. The helper stays INSTALLED: RunQuiet registers the agent through it with no
#    console, and the KeldAgent task starts the daemon through `--spawn`. CI must
#    stage it too.
grep -q 'Source: "keld-wizard-host.exe";.*DestDir' "$iss" || \
  fail "keld-wizard-host.exe is not installed to {app}"

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

# 9b/9c/10a/10b. ⚠️ INVERTED 2026-09-30 (merge of main into the web sign-in
#     branch). Main's versions pinned onboard.cmd's `unchecked` flag and the
#     wizard page's approval panel (ShowApproval, AfterLogin/EvApprovalURL,
#     DrainPanel's browser fallback, WebPanel visibility). That page and
#     onboard.cmd are deleted on this branch (web sign-in spec AC-10, D10), so
#     the guards become "none of it may come back".
for ident in ShowApproval DrainPanel AfterLogin EvApprovalURL WebPanel StartPanel NeedsConsoleOnboarding; do
  if printf '%s\n' "$code" | grep -q "$ident"; then
    fail "the removed wizard page is back in [Code] ($ident) - no installer asks anything about Keld"
  fi
done

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
# ⚠️ BOUND THIS AT THE NEXT STEP HEADER, NOT AT A NAMED LATER STEP. This read
#    `,/name: Restore HuggingFace/`, and the nearest such step AFTER the app
#    build is 567 lines further down — so $app_step was 34 KB of unrelated
#    workflow and the assertion below passed if ANY step in that span said
#    --no-bundle. It also fed the pipefail race described at the top of this
#    file: 34 KB to write with the match at byte 269. Measured 2026-09-29.
app_step="$(awk '/^      - name: Build the desktop app \(Windows\)/{f=1; print; next}
                 f && /^      - name:/{exit} f' "$wf")"
[ -n "$app_step" ] || fail "cannot isolate the Windows app build step - this guard would pass vacuously"
# ⚠️ STRIP THE # COMMENTS, OR THIS READS THE PROSE THAT EXPLAINS THE FLAG. The
#    step carries two comment lines naming `--no-bundle` while justifying it, so
#    the assertion passed with the flag deleted from the actual npx command.
#    Verified by mutation 2026-09-29 — it was vacuous from the day it was
#    written. Fifth occurrence of this exact defect in this file; see the note
#    on it in 10e.
app_cmd="$(printf '%s\n' "$app_step" | grep -v '^[[:space:]]*#' || true)"
printf '%s\n' "$app_cmd" | grep -q -- '--no-bundle' || \
  fail "the Windows app build does not use --no-bundle - it would produce a second installer"

# 10d. ⚠️ THE APP MUST OPEN WHEN THE INSTALLER FINISHES, AND THAT ENTRY'S FLAGS
#      PULL IN OPPOSITE DIRECTIONS FROM EVERY OTHER [Run] LINE IN THIS FILE.
#      The deleted onboard.cmd entry had to be `unchecked`, because a ticked
#      postinstall entry opened a blank console. The reasoning inverts here: this
#      one opens the application window, which is the thing the person was
#      waiting for. Copying `unchecked` across as a house style is the specific
#      mistake this guard catches.
# ⚠️ SCOPED TO [Run]. The [Icons] shortcut (10f) shares this Filename and
#    carries `Check: AppPresent`, so an unscoped match let the Check assertion
#    below pass off the shortcut's line. Found by it passing against a merged
#    .iss whose launch entry had no Check at all.
open_line="$(printf '%s\n' "$run_only" | grep -F 'Filename: "{app}\Keld Signal.exe"' || true)"
[ -n "$open_line" ] || \
  fail "nothing opens the desktop app when the installer finishes"
printf '%s\n' "$open_line" | grep -q 'postinstall' || \
  fail "the app launch is not a postinstall action - it would run mid-install instead of on the Finished page"
# ⚠️ NEGATIVE ASSERTIONS GO IN AN `if`, NOT `grep -q X && fail`. Under the
#    `set -e` at the top of this file, the `&&` form EXITS 1 WHEN THE GREP DOES
#    NOT MATCH — i.e. the script dies silently on exactly the passing case, and
#    every guard after it never runs.
if printf '%s\n' "$open_line" | grep -q 'unchecked'; then
  fail "the app launch is unchecked - it is meant to be ticked by default"
fi
# ⚠️ runhidden would hide the window the entry exists to open. It is the reflex
#    fix everywhere else in this file, because those children are
#    CONSOLE-subsystem; this one is GUI-subsystem and gets no console at all.
if printf '%s\n' "$open_line" | grep -q 'runhidden'; then
  fail "the app launch says runhidden - it would hide the window it exists to open"
fi
# ⚠️ The app is optional (see 10c), and Inno reports a HARD ERROR when it cannot
#    start a [Run] command. A build without the app must install cleanly.
printf '%s\n' "$open_line" | grep -q 'skipifdoesntexist' || \
  fail "the app launch would fail the install on a build where the Rust step did not produce the app"
printf '%s\n' "$open_line" | grep -q 'skipifsilent' || \
  fail "the app launch is not skipifsilent - an MDM push would throw a window at whoever is at the console"
# ⚠️ EXACTLY ONE THING OPENS SIGNAL ON THE FINISHED PAGE (merge 2026-09-30). The
#    app launch and the `keld signal open` fallback must carry MUTUALLY EXCLUSIVE
#    Checks — the app when it is on disk, the browser page when it is not — or a
#    person gets two "Open Keld Signal" checkboxes and two windows.
printf '%s\n' "$open_line" | grep -qE 'Check:[[:space:]]*AppPresent([^A-Za-z0-9_]|$)' || \
  fail "the app launch has no Check: AppPresent - with the fallback also ticked, two things would open"
fallback_line="$(printf '%s\n' "$run_only" | grep -F 'signal open' || true)"
printf '%s\n' "$fallback_line" | grep -qE 'Check:[[:space:]]*not[[:space:]]+AppPresent([^A-Za-z0-9_]|$)' || \
  fail "the keld signal open fallback is not gated on 'not AppPresent' - it would open a browser beside the app"

# 10e. ⚠️ LAUNCHING THE APP AT THE END OF AN INSTALL BREAKS THE *NEXT* ONE UNLESS
#      SOMETHING STOPS IT FIRST. Windows will not delete a running exe, so the
#      copy step stops on a modal — "DeleteFile failed; code 5. Access is
#      denied." — measured on the first install after 10d's entry was added.
#      Nothing else covers it: `CloseApplications=no` is set deliberately (guard
#      9a), so Inno never offers to close anything.
#
#      Same on the way out: the app outlives its own window (closing hides it
#      behind a tray icon), so an uninstall on a machine where anyone ever
#      opened it could not remove the app's own exe.
#
#      ⚠️ AND IT MUST BE `/F`. The app's CloseRequested handler calls
#      prevent_close, so the WM_CLOSE a graceful taskkill posts HIDES the window
#      and leaves the process alive holding the file — the guard would pass and
#      the install would still fail.
prep="$(sed -n '/^function PrepareToInstall/,/^end;/p' "$iss" || true)"
[ -n "$prep" ] || fail "cannot find PrepareToInstall - this guard would pass vacuously"
# ⚠️ STRIP THE // COMMENTS BEFORE MATCHING, OR THIS GUARD READS ITS OWN PROSE.
#    The comment above the Exec explains why `/F` is load-bearing, so it contains
#    both "taskkill" and "/F" — and an unfiltered `grep taskkill` returns it
#    alongside the code. Verified: with the comment left in, deleting `/F` from
#    the actual Exec still passed. That is four times this suite has matched a
#    comment restating the code instead of the code.
prep_kill="$(printf '%s\n' "$prep" | grep -v '^[[:space:]]*//' \
  | awk '/,[[:space:]]*$/ { buf = buf $0; next } { print buf $0; buf = "" }' \
  | grep 'taskkill' || true)"
[ -n "$prep_kill" ] || fail "PrepareToInstall does not run taskkill - a running agent locks its own files"
printf '%s\n' "$prep_kill" | grep -qF 'Keld Signal.exe' || \
  fail "PrepareToInstall does not stop the desktop app - the install it launches would block the next install's file copy"
printf '%s\n' "$prep_kill" | grep -q '/F' || \
  fail "PrepareToInstall's taskkill is not /F - the app intercepts WM_CLOSE and would stay alive holding its exe"
unkill="$(printf '%s\n' "$unfolded" | grep -F 'RunOnceId: "killstragglers"' || true)"
[ -n "$unkill" ] || fail "the uninstall straggler kill is gone - a live process would block file removal"
printf '%s\n' "$unkill" | grep -qF 'Keld Signal.exe' || \
  fail "uninstall does not stop the desktop app - it could not delete the app's own exe"
printf '%s\n' "$unkill" | grep -q '/F' || \
  fail "the uninstall taskkill is not /F - the app intercepts WM_CLOSE and would stay alive holding its exe"

# 10f. ⚠️ THE APP NEEDS A START MENU ENTRY, OR IT HAS NO ENTRY POINT AT ALL ONCE
#      THE INSTALLER CLOSES. DisableProgramGroupPage=yes and, until this guard,
#      no [Icons] section: after the Finished page the app could only be started
#      from a terminal (`keld signal open`) or by finding the exe under
#      {localappdata}. 10d's launch entry opens it exactly once, which HIDES that
#      gap rather than closing it — which is why this guard is separate from 10d
#      and must not be folded into it.
# Anchored on a leading `Name:`, which is what distinguishes an [Icons] entry
# from 10d's [Run] entry — they share a Filename and nothing else.
icon_line="$(printf '%s\n' "$unfolded" | grep -F 'Filename: "{app}\Keld Signal.exe"' | grep '^Name:' || true)"
[ -n "$icon_line" ] || \
  fail "no Start Menu shortcut for the desktop app - it is unreachable once the installer closes"
# ⚠️ [Icons] HAS NO `skipifdoesntexist`. The app is optional (see 10c/10d), and an
#    icon whose target is missing is created anyway, pointing at nothing — so the
#    optionality has to be a Check, and the Check has to be one that runs AFTER
#    [Files] or it answers False on every fresh install.
printf '%s\n' "$icon_line" | grep -q 'Check:' || \
  fail "the Start Menu shortcut has no Check - a build without the app would ship a shortcut to a missing file"
chk="$(printf '%s\n' "$icon_line" | sed -n 's/.*Check:[[:space:]]*\([A-Za-z_][A-Za-z0-9_]*\).*/\1/p')"
[ -n "$chk" ] || fail "cannot read the Start Menu shortcut's Check function name"
# $code is already comment-stripped at the top of this file, which matters here
# for the reason spelled out in 10e: the prose above AppPresent names FileExists
# while explaining it, so an unstripped search would find the comment and pass
# with the function gutted.
# ⚠️ THE NAME NEEDS A BOUNDARY. `grep "^function $chk"` matches any function
#    whose name merely STARTS with it, so renaming AppPresent to AppPresentGone
#    — exactly what a careless refactor does, leaving the [Icons] Check dangling
#    and iscc failing — passed this assertion. Caught by the mutation test.
printf '%s\n' "$code" | grep -qE "^function ${chk}[^A-Za-z0-9_]" || \
  fail "the Start Menu shortcut's Check ($chk) is not defined in [Code] - iscc would fail"
printf '%s\n' "$code" | sed -n "/^function $chk/,/^end;/p" | grep -q 'FileExists' || \
  fail "$chk does not test for the app on disk - the shortcut's optionality is not actually guarded"

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

echo "PASS: windows installer asks nothing, registers unconditionally, opens Signal after an interactive install (skipifsilent, never runhidden), reads as UTF-8, adds PATH without asking, hides the file firehose, uninstalls cleanly, ships the wizard helper on both CI paths, signs the payload before iscc and the installer after without trampling vendor signatures, and claims success from observed state"
