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

# 1. Registration must ALWAYS happen. Behind `postinstall` it is a tickbox the
#    user can clear, and `skipifsilent` skips it outright — an MDM /SILENT push
#    would install the files and register nothing, silently.
# ⚠️ MATCH ENTRY LINES ONLY. The comments in [Run] name `keld-agent.exe` and
# `runhidden` while explaining why they are the way they are, so an unscoped grep
# matches the PROSE — and deleting the real entry still passed. Found by testing
# this guard against a deliberately broken file rather than trusting it.
entries="$(printf '%s\n' "$run_block" | grep '^Filename:' || true)"
reg_line="$(printf '%s\n' "$entries" | grep -F 'keld-agent.exe' || true)"
[ -n "$reg_line" ] || fail "no [Run] entry registers the agent; a silent install would register nothing"
printf '%s\n' "$reg_line" | grep -q 'postinstall' && \
  fail "agent registration is behind 'postinstall' — a user can untick it and a /SILENT push skips it"
printf '%s\n' "$reg_line" | grep -q 'skipifsilent' && \
  fail "agent registration is 'skipifsilent' — MDM pushes would register nothing"

# 1b. Registration must say --headless OUT LOUD. `runhidden` hides the window but
#     leaves the child a real console, so stdout is a terminal and keld-agent's TTY
#     probe answers TRUE here — it ran `keld login` invisibly and then blocked
#     forever on `keld signal setup`'s [Y/n], wedging the installer until someone
#     killed the process by hand. Inferring "no human" from the absence of a
#     terminal does not work on Windows; the intent has to be stated.
printf '%s\n' "$reg_line" | grep -q -- '--headless' || \
  fail "agent registration omits --headless — install would prompt inside a hidden console and hang"

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
grep -qF 'install --yes'  "$cmd" || fail "onboard.cmd has no browser-login fallback"
grep -qF 'ingest_token'   "$cmd" || fail "onboard.cmd claims success without checking hook.json"

echo "PASS: windows installer registers unconditionally, onboards visibly, adds PATH without asking, hides the file firehose, uninstalls cleanly, and claims success from observed state"
