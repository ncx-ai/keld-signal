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
    claude_code) return 0 ;;
    *) return 1 ;;
  esac
}

# tool_display <tool>
tool_display() {
  case "$1" in
    claude_code) echo "Claude Code" ;;
    *) echo "$1" ;;
  esac
}

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
        TOOL_BIN="$prefix/bin/claude"
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
      # run — which is exactly the case the detector (WS-C1) exists for. The
      # chain's before-half prompt is what creates it, as a person would.
      ;;
  esac
}

# tool_transcript_root <tool> — where the checkpoint looks for transcripts.
tool_transcript_root() {
  case "$1" in
    claude_code) echo "$ISO_HOME/.claude/projects" ;;
  esac
}

# tool_prompt <tool> <label> — one headless prompt.
#
# The prompt is OURS ("reply with one word"), which is what makes the resulting
# transcript the only one a failing CI job may upload.
tool_prompt() {
  local tool=$1 label=$2 out="$WORK/prompt-$2.out"
  case "$tool" in
    claude_code)
      say "prompt [$label]: claude -p"
      ( cd "$WORK" && "$TOOL_BIN" -p "reply with one word" \
          --output-format json --model "${CONFORM_MODEL:-claude-sonnet-4-6}" \
          < /dev/null > "$out" 2>&1 )
      local rc=$?
      [ $rc -eq 0 ] || fail "claude -p exited $rc: $(tail -5 "$out")"
      grep -q '"is_error":false' "$out" \
        || fail "claude -p reported an error: $(tail -5 "$out")"
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
    codex)       echo "store_rows,publish" ;;
    *)           echo "" ;;
  esac
}
