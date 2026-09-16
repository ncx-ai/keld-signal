#!/usr/bin/env bash
# The tool table: how to install each integration, how to point it at the mock
# model, how to drive it headless, and where it writes transcripts.
#
# One function per concern, dispatching on the tool id, so adding Codex, Pi and
# Gemini (task A.3's second half) is three cases rather than a second runner.
#
# ⚠️ **Every tool's config dir lives under the isolated HOME.** For Claude Code
# CLAUDE_CONFIG_DIR is set to `$HOME/.claude` deliberately rather than to a
# distinct directory: `tools.ConfigPath` resolves the adapter's target as
# `$HOME/.claude/settings.json` with no knowledge of CLAUDE_CONFIG_DIR, and the
# watcher's claude_code root is `$HOME/.claude/projects`. Pointing the tool
# somewhere else would leave the adapter writing a file the tool never reads —
# a green setup and a silent lane, which is the failure class this whole
# workstream exists to catch.

# tool_supported <tool> — is this tool in the table yet?
tool_supported() {
  case "$1" in
    claude_code|codex) return 0 ;;
    *) return 1 ;;
  esac
}

# tool_all — every tool the table can drive, in a stable order. The seeded
# before/after split shuffles THIS list, so adding a row here is all it takes
# for a tool to appear in both halves over a few runs.
tool_all() { echo "claude_code codex"; }

# tool_display <tool>
tool_display() {
  case "$1" in
    claude_code) echo "Claude Code" ;;
    codex)       echo "Codex" ;;
    *) echo "$1" ;;
  esac
}

# tool_npm_bin <prefix> <name> — where npm put a CLI's entry point.
#
# ⚠️ **npm DOES NOT CREATE A BARE EXECUTABLE ON WINDOWS.** It writes shims:
# <prefix>/<name> (a shell script, POSIX only), <name>.cmd and <name>.ps1, and
# on Windows the bare name is ABSENT. It also puts them at the prefix ROOT, not
# under bin/.
#
# Measured on windows-latest 2026-09-16: the chain installed Claude Code fine,
# reported its version as empty, and then died at the first prompt with
# `claude: No such file or directory` — exit 127 from a path that was never
# going to exist. The version probe had already failed silently one line
# earlier, which is the part worth keeping: a resolver that cannot find the
# binary must fail THERE, not three steps later.
tool_npm_bin() {
  local prefix=$1 name=$2 c
  for c in "$prefix/bin/$name" "$prefix/$name.cmd" "$prefix/bin/$name.cmd" "$prefix/$name"; do
    [ -f "$c" ] && { printf '%s' "$c"; return 0; }
  done
  fail "npm installed $name but left no runnable shim under $prefix (looked for bin/$name, $name.cmd, bin/$name.cmd, $name)"
}

# tool_bin <tool> / tool_version_of <tool> — what `tool_install` resolved.
#
# ⚠️ One global TOOL_BIN was enough while a chain drove one tool. A split chain
# drives several, and the second `tool_install` would have silently pointed
# every later prompt at the last tool resolved — a run that looks like it
# covered two tools and covered one twice.
tool_var() { echo "TOOL_$(echo "$1" | tr 'a-z-' 'A-Z_')_$2"; }
tool_bin() { eval "printf '%s' \"\${$(tool_var "$1" BIN):-}\""; }
tool_version_of() { eval "printf '%s' \"\${$(tool_var "$1" VERSION):-unknown}\""; }
tool_remember() { eval "$(tool_var "$1" BIN)=\$2"; eval "$(tool_var "$1" VERSION)=\$3"; }

# tool_npm_package <tool> — what `npm i -g <pkg>@${VERSION:-latest}` installs.
tool_npm_package() {
  case "$1" in
    claude_code) echo "@anthropic-ai/claude-code" ;;
    codex)       echo "@openai/codex" ;;
    pi)          echo "@earendil-works/pi-coding-agent" ;;
    gemini_cli)  echo "@google/gemini-cli" ;;
  esac
}

# tool_install <tool> — resolve the binary.
#
# On a developer machine the tool is already installed and is used as found, so
# a local run tests THIS machine's version; KELD_CONFORM_INSTALL=1 (what the
# container and VM legs set) installs it at @latest into an isolated npm prefix
# instead. Either way the resolved path and version are printed, because "which
# version was this proven against" is the question the matrix answers.
tool_install() {
  local tool=$1
  case "$tool" in
    claude_code)
      if [ "${KELD_CONFORM_INSTALL:-0}" = "1" ]; then
        local prefix="$WORK/npm"
        mkdir -p "$prefix"
        say "npm i -g $(tool_npm_package "$tool")@${TOOL_VERSION:-latest} into $prefix"
        npm config set prefix "$prefix" >/dev/null 2>&1
        npm i -g "$(tool_npm_package "$tool")@${TOOL_VERSION:-latest}" >"$WORK/npm-install.log" 2>&1 \
          || fail "npm install failed: $(tail -20 "$WORK/npm-install.log")"
        TOOL_BIN=$(tool_npm_bin "$prefix" claude)
      else
        TOOL_BIN=${KELD_CONFORM_CLAUDE_BIN:-}
        if [ -z "$TOOL_BIN" ]; then
          # PATH was prefixed with $WORK by isolate_init and HOME was moved, so
          # look in the usual places explicitly rather than trusting `command -v`.
          for c in "$REAL_HOME/.local/bin/claude" "/usr/local/bin/claude" "/opt/homebrew/bin/claude"; do
            [ -x "$c" ] && { TOOL_BIN=$c; break; }
          done
        fi
        [ -n "$TOOL_BIN" ] && [ -x "$TOOL_BIN" ] \
          || fail "claude not found (set KELD_CONFORM_CLAUDE_BIN, or KELD_CONFORM_INSTALL=1 to npm-install it)"
      fi
      TOOL_VERSION_SEEN=$("$TOOL_BIN" --version 2>/dev/null | head -1)
      [ -n "$TOOL_VERSION_SEEN" ] || fail "$TOOL_BIN produced no --version output; it is not runnable here"
      tool_remember "$tool" "$TOOL_BIN" "$TOOL_VERSION_SEEN"
      say "$(tool_display "$tool") = $TOOL_BIN ($TOOL_VERSION_SEEN)"
      ;;
    codex)
      if [ "${KELD_CONFORM_INSTALL:-0}" = "1" ]; then
        local prefix="$WORK/npm"
        mkdir -p "$prefix"
        say "npm i -g $(tool_npm_package "$tool")@${TOOL_VERSION:-latest} into $prefix"
        npm config set prefix "$prefix" >/dev/null 2>&1
        npm i -g "$(tool_npm_package "$tool")@${TOOL_VERSION:-latest}" >"$WORK/npm-install-codex.log" 2>&1 \
          || fail "npm install failed: $(tail -20 "$WORK/npm-install-codex.log")"
        TOOL_BIN=$(tool_npm_bin "$prefix" codex)
      else
        TOOL_BIN=${KELD_CONFORM_CODEX_BIN:-}
        if [ -z "$TOOL_BIN" ]; then
          for c in "$REAL_HOME/.local/bin/codex" "/opt/homebrew/bin/codex" "/usr/local/bin/codex"; do
            [ -x "$c" ] && { TOOL_BIN=$c; break; }
          done
        fi
        [ -n "$TOOL_BIN" ] && [ -x "$TOOL_BIN" ] \
          || fail "codex not found (set KELD_CONFORM_CODEX_BIN, or KELD_CONFORM_INSTALL=1 to npm-install it)"
      fi
      # ⚠️ PROBE THE VERSION IN A THROWAWAY CODEX_HOME. `codex --version`
      # CREATES its home directory (installation_id, four sqlite files), and
      # `tools.Detect` reports Codex installed by exactly that directory. So
      # asking the binary its version was enough to make an after-half tool look
      # installed before the chain had installed it: the detector's first tick
      # configured it from NO config file, wrote a keld-only config.toml, and
      # `tool_materialize` then found a file already there and left the mock
      # provider out — after which codex dialled api.openai.com and the step
      # failed on a 401 from the real API.
      TOOL_VERSION_SEEN=$(CODEX_HOME="$WORK/version-probe-codex" "$TOOL_BIN" --version 2>/dev/null | head -1)
      tool_remember "$tool" "$TOOL_BIN" "$TOOL_VERSION_SEEN"
      say "$(tool_display "$tool") = $TOOL_BIN ($TOOL_VERSION_SEEN)"
      ;;
    *) fail "tool_install: $tool is not in the table yet" ;;
  esac
}

# tool_env <tool> — export the per-tool environment: isolated config dir, the
# mock model, and every switch that would otherwise reach the network.
tool_env() {
  case "$1" in
    claude_code)
      export CLAUDE_CONFIG_DIR="$ISO_HOME/.claude"
      export ANTHROPIC_BASE_URL="$MOCK_LLM_URL"
      export ANTHROPIC_API_KEY="sk-ant-mock"
      export DISABLE_AUTOUPDATER=1
      export DISABLE_ERROR_REPORTING=1
      # ⚠️ DISABLE_TELEMETRY is deliberately NOT set: it would switch off the
      # exporter whose arrival is checkpoint 4. The mock Atlas is the only
      # destination the settings file names, so nothing leaves the machine.
      #
      # The directory is NOT pre-created here. `tools.Detect` reports Claude
      # Code installed by the existence of ~/.claude, so creating it would hide
      # the real state of a machine where the tool is installed but has never
      # run — which is exactly the case the detector (WS-C1) exists for.
      # `tool_materialize` is what creates it, at the point in the chain where
      # this tool is "installed".
      ;;
    codex)
      # CodexAdapter.ConfigPath resolves $HOME/.codex/config.toml and knows
      # nothing about CODEX_HOME, while watch.DiscoverRoots and
      # watch.AnalyzeRoots read CODEX_HOME. Pointing the two at the same
      # directory is what keeps the adapter writing the file the tool reads.
      export CODEX_HOME="$ISO_HOME/.codex"
      export MOCK_API_KEY=sk-mock
      export CODEX_DISABLE_UPDATE_CHECK=1
      ;;
  esac
}

# tool_materialize <tool> — make this tool look INSTALLED to `tools.Detect` and
# to the detector, at the point in the chain where it is installed.
#
# ⚠️ On a CI VM this is `npm i -g <pkg>` plus the tool's own first run. On a
# developer machine the binary is already installed system-wide and cannot be
# installed later, so what the after-half actually does is make the tool's
# CONFIG DIRECTORY appear — which is the fact both `tools.Detect` and the
# detector key on, and therefore the fact the chain is testing. The difference
# is stated in the run's output rather than papered over.
tool_materialize() {
  case "$1" in
    claude_code)
      mkdir -p "$ISO_HOME/.claude"
      ;;
    codex)
      # Codex needs a provider before it can run at all, so its config.toml is
      # written here — the mock provider only. Everything keld writes is
      # appended by the adapter as its own delimited block, so this file is
      # also the realistic case: a config that already has the user's content
      # in it.
      mkdir -p "$CODEX_HOME"
      # ⚠️ Keyed on the PROVIDER, not on the file. A config.toml can already
      # exist with only keld's block in it (the detector writes one for a tool
      # it finds unconfigured), and returning early on the file's existence
      # left codex with no provider at all — which sends it to the real
      # api.openai.com. The block is PREPENDED because these are bare top-level
      # keys: appended after keld's `[otel]` table they would be read as part
      # of it.
      if [ -f "$CODEX_HOME/config.toml" ] && grep -q '^model_provider' "$CODEX_HOME/config.toml"; then
        return 0
      fi
      local existing=""
      [ -f "$CODEX_HOME/config.toml" ] && existing=$(cat "$CODEX_HOME/config.toml")
      cat > "$CODEX_HOME/config.toml" <<TOML
model = "mock-1"
model_provider = "mock"

[model_providers.mock]
name = "Mock"
base_url = "$MOCK_LLM_URL/v1"
wire_api = "responses"
env_key = "MOCK_API_KEY"
TOML
      [ -n "$existing" ] && printf '\n%s\n' "$existing" >> "$CODEX_HOME/config.toml"
      ;;
  esac
}

# tool_transcript_root <tool> — where the checkpoint looks for transcripts.
tool_transcript_root() {
  case "$1" in
    claude_code) echo "$ISO_HOME/.claude/projects" ;;
    # Codex nests: sessions/<yyyy>/<mm>/<dd>/rollout-<ts>-<session>.jsonl.
    codex)       echo "$ISO_HOME/.codex/sessions" ;;
  esac
}

# tool_prompt <tool> <label> — one headless prompt.
#
# The prompt is OURS ("reply with one word"), which is what makes the resulting
# transcript the only one a failing CI job may upload.
tool_prompt() {
  local tool=$1 label=$2 out="$WORK/prompt-$2-$1.out"
  local bin
  bin=$(tool_bin "$tool")
  [ -n "$bin" ] || fail "tool_prompt: $tool was never resolved by tool_install"
  case "$tool" in
    claude_code)
      say "prompt [$label]: claude -p"
      ( cd "$WORK" && "$bin" -p "reply with one word" \
          --output-format json --model "${CONFORM_MODEL:-claude-sonnet-4-6}" \
          < /dev/null > "$out" 2>&1 )
      local rc=$?
      [ $rc -eq 0 ] || fail "claude -p exited $rc: $(tail -5 "$out")"
      grep -q '"is_error":false' "$out" \
        || fail "claude -p reported an error: $(tail -5 "$out")"
      ;;
    codex)
      # ⚠️ **`--dangerously-bypass-hook-trust` IS REQUIRED HERE, AND A REAL USER
      # CANNOT USE IT.** Verified on Codex 0.153.4: with the flag all three keld
      # hooks fire; without it NONE do and Codex prints nothing at all — no
      # warning, no prompt, no `hooks.state` entry. So an unattended chain can
      # only exercise the hook lane by bypassing the very approval step that
      # `approval_required` exists to represent (AC-9). What this run proves is
      # therefore that the hooks are correctly registered and that keld reads
      # their payload; it does NOT prove a person's first Codex session
      # captures anything, because on that machine the hooks are silently inert
      # until /hooks is run. The line below says so on every run.
      say "codex: passing --dangerously-bypass-hook-trust; a REAL user cannot," \
          "and without it Codex fires no hooks and says nothing (0.153.4)."
      say "prompt [$label]: codex exec"
      # ⚠️ NOT `env -i`. The hook keld registers is `keld __hook --source codex`,
      # and it resolves the daemon's address through KELD_HOME — which an empty
      # environment drops. Measured: with `env -i HOME=… PATH=… CODEX_HOME=…`
      # all three hooks fired, the tool wrote its rollout, telemetry forwarded,
      # and ZERO pointers reached the daemon, because the hook looked for
      # agent.json under $HOME/.keld while KELD_HOME points at $HOME itself. The
      # ambient environment is already the isolated one; the two provider
      # variables below are unset so a developer's own key cannot be picked up.
      ( cd "$WORK" \
          && unset OPENAI_API_KEY OPENAI_BASE_URL CODEX_API_KEY \
          && "$bin" exec --skip-git-repo-check --dangerously-bypass-hook-trust \
          "reply with one word" < /dev/null > "$out" 2>&1 )
      local rc=$?
      [ $rc -eq 0 ] || fail "codex exec exited $rc: $(tail -10 "$out")"
      grep -q "MOCK OK" "$out" || fail "codex exec did not reach the mock model: $(tail -10 "$out")"
      ;;
    *) fail "tool_prompt: $tool is not in the table yet" ;;
  esac
}

# tool_not_expected <tool> — checkpoints this tool is not yet REQUIRED to meet,
# as a comma-separated list for `keld-conform check --not-expected`.
#
# Empty for Claude Code: all five lanes are built. Codex's store_rows and
# publish go here until WS-D's reader and WS-B's capture land — and an unmet
# one then reads "n/a", never "pass".
tool_not_expected() {
  case "$1" in
    claude_code) echo "" ;;
    # Codex's reader (WS-D) and its capture (WS-B) both landed, and
    # WorkstreamsEligible("codex") is true, so all five lanes are built and all
    # five are REQUIRED. KELD_CONFORM_CODEX_NOT_EXPECTED is the lever for a
    # machine where one is known-broken, so a narrowing is always visible in
    # the command line rather than hidden in this table.
    codex)       echo "${KELD_CONFORM_CODEX_NOT_EXPECTED:-}" ;;
    *)           echo "" ;;
  esac
}

# tool_config_path <tool> — the file keld's adapter writes, which an upgrade
# must leave byte-identical (chain B).
tool_config_path() {
  case "$1" in
    claude_code) echo "$ISO_HOME/.claude/settings.json" ;;
    codex)       echo "$ISO_HOME/.codex/config.toml" ;;
  esac
}

# tool_await_configured <tool> <seconds> — wait for the daemon's DETECTOR to
# configure a tool that appeared after Signal was installed (AC-3).
#
# ⚠️ Read from `GET /v1/integrations`, not from the config file. The file
# appearing says a write happened; the route says the ONE state function
# (integrations.Compute) agrees the tool is configured, which is the fact the
# pane, doctor and this harness are all supposed to share. Checking the file
# instead would let the harness pass on a machine whose own UI says the tool is
# broken.
tool_await_configured() {
  local tool=$1 budget=${2:-60} i state configured
  for i in $(seq 1 "$budget"); do
    local body
    body=$(curl -fsS -H "x-keld-agent-secret: $DAEMON_SECRET" "$DAEMON_URL/v1/integrations" 2>/dev/null || echo "")
    if [ -n "$body" ]; then
      configured=$(printf '%s' "$body" | python3 -c "
import json,sys
want=sys.argv[1]
try: d=json.load(sys.stdin)
except Exception: print('?'); raise SystemExit
for it in d.get('integrations',[]):
    if it.get('id')==want:
        print('%s %s' % (it.get('configured'), it.get('state'))); break
else: print('absent -')
" "$tool")
      case "$configured" in
        "True "*|"true "*)
          state=${configured#* }
          say "detector configured $(tool_display "$tool") after ${i}s (state: $state)"
          case "$state" in
            broken)
              # Not a harness condition — the tool WAS configured, which is what
              # this step asserts — but a tool the daemon configured one second
              # ago has no silent expected lane to be broken on, and the state
              # the pane will show a person is wrong. Reported loudly rather
              # than failed, because the rule lives in integrations.Compute.
              say "⚠️  $(tool_display "$tool") reads 'broken' immediately after the detector"
              say "    configured it. Nothing has had a chance to go silent yet — this is"
              say "    a Compute finding (AC-4: idle is never broken), not a chain failure." ;;
          esac
          return 0 ;;
      esac
    fi
    sleep 1
  done
  say "the detector did not configure $(tool_display "$tool") within ${budget}s"
  say "  last seen: ${configured:-no answer from $DAEMON_URL/v1/integrations}"
  return 1
}
