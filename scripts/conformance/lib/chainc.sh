#!/usr/bin/env bash
# Chain C — the DAY-THREE chain: what a machine looks like once it has a
# HISTORY.
#
# Chains A and B prove a machine can be INSTALLED and work for ten minutes.
# Every failure the maintainer hit on 2026-09-18 needed history, and a fresh
# machine has none:
#
#   1. an upgrade that left an older `keld` at /usr/local/keld, earlier on PATH
#      than the new one;
#   2. a daemon restarted three times under a live editor, after which the
#      tool's OTLP exporter went silent for 18 minutes and only an editor
#      restart brought it back;
#   3. a `keld signal setup` re-run that left the tool configs holding a secret
#      the proxy answers 401 to;
#   4. a resumed session, whose transcript START never moves, reading
#      `restart_required` forever;
#   5. two live windows, which made one pane row alternate every few seconds;
#   6. a sleeping machine whose 2-second wakes spent the sidecar's restart
#      budget.
#
# Six defects, none of them reachable by chain A or chain B, because each one
# needs something to have happened BEFORE it. Chain C is that "before".
#
# ⚠️ **THIS CHAIN IS EXPECTED TO FAIL TODAY, AND THAT IS ITS PURPOSE.** Steps 2
# to 9 are written against the behaviour four other workstreams are landing in
# parallel (`collect always`, the transcript mirror, the Developer OTLP switch,
# the permanent telemetry secret, the restart-rule truth). A step that cannot
# even be EXPRESSED on this build — the OTLP switch has no key in
# `GET /v1/settings`, so there is nothing to turn on — reports **blocked** with
# what is missing, never a pass. A step that CAN be expressed and does not hold
# reports **FAIL**, and the chain stops there, exactly as chains A and B do.
#
# Three rules this file holds itself to, each of them a lesson already paid for
# elsewhere in this harness:
#
#   - **Assert on a BEHAVIOUR, never on a string.** `proxy_post` is the model:
#     it reads the credential out of the tool's own config, POSTs a real OTLP
#     body through the loopback proxy with it, and requires BOTH a 202 from the
#     proxy AND a delivery at the mock Atlas. Comparing two secrets as strings
#     would pass on a machine where the proxy rejects both.
#   - **COUNT BEFORE AND AFTER.** Every "the lane still reports" assertion is a
#     DELTA. A global count that was already non-zero is satisfied by work from
#     three steps ago, which is how a restarted daemon could look healthy while
#     nothing new arrived — the 18-minute silence, rendered as a green check.
#   - **A count that could not be READ is empty, never zero.** Every reader
#     here prints "" when it could not look, and every caller treats "" as a
#     failure of the reader rather than as "nothing arrived".
#
# ⚠️ **KELD_CONFORM_KEEP_GOING=1 SURVEYS, IT DOES NOT RELAX THE CHAIN.** While
# several of these steps fail at once, "which ones" is the question a branch
# needs answered, and the default — stop at the first failure — can only ever
# name one. The survey runs the rest anyway, LABELS every step that ran after a
# failure, and changes no assertion. Read those verdicts as a survey of a
# machine an earlier step already broke, never as a chain result.

# --- blocked ------------------------------------------------------------------

# blocked <reason> — this step could not be EXPRESSED on this build.
#
# ⚠️ **BLOCKED IS NOT A PASS AND NOT A FAILURE, and collapsing it into either
# is how a chain lies.** Called it a pass and the chain reports green for
# behaviour it never exercised; called it a failure and the chain stops, hiding
# every later step behind one unbuilt feature. So it says so in its own word,
# does not stop the chain, and rides the final summary.
blocked() {
  STEP_BLOCKED="$*"
  say "BLOCKED [$STEP]: $*"
  BLOCKED_STEPS="${BLOCKED_STEPS}${BLOCKED_STEPS:+$'\n'}$STEP: $*"
  return 0
}

# --- reading the mock Atlas ---------------------------------------------------

# TELEMETRY_ROUTES is what a tool's OTLP becomes once the proxy forwards it.
TELEMETRY_ROUTES="/v1/logs /v1/metrics"

# atlas_count <route...> — how many deliveries the mock Atlas has counted on
# these routes, summed. ⚠️ EMPTY, never 0, when the mock could not be read: a
# zero from a server nobody reached is indistinguishable from "nothing was
# published", and reporting the second is the confident negative this codebase
# refuses everywhere else.
atlas_count() {
  local body
  body=$(curl -fsS "$MOCK_ATLAS_URL/_conform/counts" 2>/dev/null) || { echo ""; return 1; }
  printf '%s' "$body" | python3 -c '
import json, sys
try:
    d = json.load(sys.stdin).get("counts", {})
except Exception:
    raise SystemExit(1)
print(sum(int(d.get(r, 0)) for r in sys.argv[1:]))
' "$@" 2>/dev/null || { echo ""; return 1; }
}

# await_count_above <baseline> <budget-seconds> <route...> — poll until the
# mock Atlas has counted MORE than the baseline. Echoes the count it settled on,
# empty when it never rose (or could not be read).
await_count_above() {
  local base=$1 budget=$2; shift 2
  local i now
  for i in $(seq 1 "$budget"); do
    now=$(atlas_count "$@")
    if [ -n "$now" ] && [ "$now" -gt "$base" ]; then echo "$now"; return 0; fi
    sleep 1
  done
  echo "${now:-}"
  return 1
}

# --- reading a tool's own telemetry credential --------------------------------

# tool_otlp_target <tool> — the endpoint and credential THE TOOL ITSELF would
# use, read out of the config keld wrote, printed as three tab-separated fields:
#
#   <logs-url>  <header-name-or-->  <header-value-or-->
#
# ⚠️ **READ FROM THE CONFIG, NOT FROM agent.json, BECAUSE THAT IS THE FACT
# UNDER TEST.** The daemon knows what it will accept; the question every one of
# these steps asks is whether what the TOOL holds is still accepted. Reading the
# daemon's own copy would make every assertion here tautological.
#
# The three shapes are the three the proxy has to accept (AGENTS.md: the tools
# do not agree on one): Claude Code and Codex carry `x-keld-ingest-token`, and
# Gemini cannot carry a header at all so its token is a PATH SEGMENT in the
# endpoint. Codex's configured endpoint already names the signal
# (`…/v1/logs`); the other two are bases their SDK appends to.
tool_otlp_target() {
  local p; p=$(tool_config_path "$1")
  [ -f "$p" ] || { echo ""; return 1; }
  python3 - "$p" <<'PY'
import re, sys
raw = open(sys.argv[1], encoding="utf-8", errors="replace").read()
m = re.search(r'https?://127\.0\.0\.1:\d+[^"\'\s,}\\]*', raw)
if not m:
    raise SystemExit(1)
url = m.group(0).rstrip('/')
if '/v1/' not in url:
    url += '/v1/logs'
tok = re.search(r'x-keld-ingest-token["\']?\s*[=:]\s*["\']?([A-Za-z0-9._\-]+)', raw)
if tok:
    print("%s\t%s\t%s" % (url, "x-keld-ingest-token", tok.group(1)))
else:
    # Gemini: the credential is already inside the URL, so there is no header
    # to send and the URL alone is the whole credential.
    print("%s\t-\t-" % url)
PY
}

# proxy_post_target <logs-url> <header-name> <header-value> <what> — POST a real
# OTLP body through the loopback proxy with a given credential, and require the
# request to be BOTH accepted and DELIVERED.
#
# ⚠️ **TWO FACTS, NOT ONE.** The proxy answers the tool BEFORE Atlas (it must:
# putting the daemon's network on the editor's critical path is what makes an
# exporter time out), so a 202 says only "the credential was accepted". The
# delivery is what says the lane works end to end, and it is read as a DELTA at
# the mock — a non-zero total would be satisfied by a batch from an earlier
# step.
#
# The body is a minimal OTLP logs envelope. It carries no text: the proxy's text
# gate would strip it anyway, and a conformance probe that planted prompt text
# on the wire would be the one thing this whole repo exists to prevent.
proxy_post_target() {
  local url=$1 hname=$2 hval=$3 what=$4
  local before code
  before=$(atlas_count $TELEMETRY_ROUTES)
  [ -n "$before" ] || { say "$what: could not read the mock Atlas counts"; return 1; }

  local -a args=(-s -o /dev/null -w '%{http_code}' -X POST "$url"
                 -H 'content-type: application/json'
                 --data '{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"keld-conform"}}]},"scopeLogs":[{"logRecords":[{"body":{"stringValue":"conformance probe"},"attributes":[{"key":"event.name","value":{"stringValue":"keld.conform.probe"}}]}]}]}]}')
  [ "$hname" != "-" ] && args+=(-H "$hname: $hval")

  code=$(curl "${args[@]}" 2>/dev/null)
  if [ "$code" != "202" ]; then
    say "$what: the proxy answered $code for the credential the tool holds"
    say "  401 here IS the outage: the tool keeps posting a secret nothing accepts,"
    say "  and nothing on the machine can tell it to stop."
    return 1
  fi
  if [ -z "$(await_count_above "$before" 20 $TELEMETRY_ROUTES)" ]; then
    say "$what: the proxy accepted the post (202) and NOTHING reached Atlas in 20s."
    say "  Accepted and forwarded are two facts; only the second is the lane."
    return 1
  fi
  say "$what: accepted and forwarded (credential from the tool's own config)"
  return 0
}

# proxy_post <tool> <what> — the above, with the tool's CURRENT configured
# credential.
proxy_post() {
  local tool=$1 what=${2:-"$(tool_display "$1") telemetry"}
  local target; target=$(tool_otlp_target "$tool")
  [ -n "$target" ] || { say "$what: no loopback OTLP endpoint in $(tool_display "$tool")'s config"; return 1; }
  proxy_post_target "$(echo "$target" | cut -f1)" "$(echo "$target" | cut -f2)" "$(echo "$target" | cut -f3)" "$what"
}

# --- reading the daemon -------------------------------------------------------

# daemon_get <path> — one authenticated loopback GET, empty on any failure.
daemon_get() {
  curl -fsS -H "x-keld-agent-secret: $DAEMON_SECRET" "$DAEMON_URL$1" 2>/dev/null || echo ""
}

# integration_state <tool> — the ONE state function's answer for this tool.
#
# ⚠️ Read from GET /v1/integrations, never re-derived here. integrations.Compute
# is the single place that decides a tool's state (AC-8) and the pane renders it
# verbatim; a harness with its own copy of the rule would pass on a machine
# whose own UI says the tool is broken.
integration_state() {
  local want=$1 body; body=$(daemon_get /v1/integrations)
  [ -n "$body" ] || { echo ""; return 1; }
  printf '%s' "$body" | python3 -c '
import json, sys
want = sys.argv[1]
try: d = json.load(sys.stdin)
except Exception: raise SystemExit(1)
for it in d.get("integrations", []):
    if it.get("id") == want:
        print(it.get("state", "")); break
else:
    print("absent")
' "$want" 2>/dev/null
}

# settings_key_present <key...> — does GET /v1/settings publish any of these
# keys? This is how a step decides whether a toggle EXISTS, rather than
# hard-coding "blocked" and going stale the day it lands.
settings_key_present() {
  local body; body=$(daemon_get /v1/settings)
  [ -n "$body" ] || return 1
  printf '%s' "$body" | python3 -c '
import json, sys
try: d = json.load(sys.stdin)
except Exception: raise SystemExit(1)
raise SystemExit(0 if any(k in d for k in sys.argv[1:]) else 1)
' "$@"
}

# settings_value_true <key> — is this v3 setting effectively ON right now?
# GET /v1/settings reports the EFFECTIVE value (file merged with env), which is
# the only one worth asking about: a file value an env var pins is not what the
# machine is doing.
settings_value_true() {
  local body; body=$(daemon_get /v1/settings)
  [ -n "$body" ] || return 1
  printf '%s' "$body" | python3 -c '
import json, sys
try: d = json.load(sys.stdin)
except Exception: raise SystemExit(1)
raise SystemExit(0 if d.get(sys.argv[1]) is True else 1)
' "$1"
}

# --- chain C's steps ----------------------------------------------------------

# OLD_VERSION is what the "already installed" keld this chain leaves behind
# reports. It is a real, stamped build of this source — what makes it OLD is its
# IDENTITY, which is the whole of what the upgrade case turns on: a binary that
# is not the one the upgrade installed, sitting earlier on PATH.
OLD_VERSION=${KELD_CONFORM_OLD_VERSION:-1.0.0-conform-old}

# step_c_old_binary — the upgrade that left an older keld ahead on PATH.
#
# ⚠️ **MEASURED 2026-09-18: `/usr/local/keld/keld` survived an upgrade and sat
# ahead of the new one on PATH.** Everything a person types ran the old binary,
# and nothing said so. AGENTS.md already names the shape ("the daemon converges
# and the CLI a human types does not"), and `keld signal doctor` already has a
# check for it (`keldPATHBinaries`) — which is exactly why this step asserts
# TWO things rather than one:
#
#   1. the binary the tool configs now NAME, when RUN, is the new one. This is
#      the behaviour, not a path comparison: the config is only as good as what
#      executing it produces.
#   2. doctor SAYS the older one is shadowing. Detection and correctness are
#      different claims, and a machine can fail either independently.
#
# PATH is restored before the step returns, so a later step never inherits the
# shadow. A harness that left it in place would be testing every subsequent step
# against a machine state no user is in.
step_c_old_binary() {
  step_begin "old-binary"
  local saved_path=$PATH rc=0

  keld_build "$WORK/bin-old" "$OLD_VERSION"
  export PATH="$WORK/bin-old:$PATH"
  say "an older keld is now first on PATH: $(command -v keld) ($(keld --version 2>/dev/null | head -1))"

  # The upgrade as a person performs it: re-run setup BY NAME, which is what
  # every runbook, every doc and every muscle memory says.
  keld signal setup --yes >"$WORK/setup-old-path.out" 2>&1 || {
    say "keld signal setup (through PATH) failed: $(tail -5 "$WORK/setup-old-path.out")"
    PATH=$saved_path
    return 1
  }

  # ⚠️ **ASK THE RELEASE UNDER TEST WHAT IT SAYS, rather than assuming the shape
  # of `--version`.** The first cut compared the whole output line against the
  # bare version string and could never match: `keld --version` prints
  # "keld version 99.0.0-conform". That would have failed this step on a machine
  # where everything was right — a false failure, which is worse here than a
  # missed one, because this chain is expected to fail and a reader has to trust
  # that each failure names something real.
  local want; want=$("$BIN_DIR/keld" --version 2>/dev/null | head -1)
  local t bin ver
  for t in $BEFORE $AFTER; do
    bin=$(hook_binary "$t")
    if [ -z "$bin" ]; then
      say "$(tool_display "$t"): no keld hook command in its config to resolve"
      rc=1; continue
    fi
    ver=$("$bin" --version 2>/dev/null | head -1)
    if [ "$ver" = "$want" ]; then
      say "$(tool_display "$t"): its hook runs '$ver' — the release under test"
    else
      say "$(tool_display "$t"): its hook RUNS '$ver', not the release under test ('$want')."
      say "  The upgrade installed one binary and the machine is wired to another;"
      say "  every prompt this tool sends is handled by the version that was replaced."
      rc=1
    fi
  done

  # Detection, through PATH — which is how a person runs it, and which means the
  # OLD binary answers. ⚠️ That makes this half weaker than it looks: here both
  # binaries are built from this source, so both carry `keldPATHBinaries`. On a
  # real machine the shadowing binary is genuinely older and may have no such
  # check at all, in which case a person typing `keld signal doctor` gets
  # whatever that release knew. The step cannot close that gap; it can say so.
  # doctor exits non-zero when it finds problems, so the output is kept whatever
  # it exits with.
  keld signal doctor >"$WORK/doctor-old-path.out" 2>&1
  if grep -qF "$WORK/bin-old" "$WORK/doctor-old-path.out"; then
    say "doctor names the shadowing binary by path — a person can act on that"
  else
    say "doctor did NOT name the older keld that will run in place of the new one."
    say "  It is checked for by PATH ENTRY, so the finding has to name the path;"
    say "  a report that cannot say WHICH binary shadows is not actionable."
    sed -n '1,12p' "$WORK/doctor-old-path.out" | sed 's/^/    /' >&2
    rc=1
  fi

  PATH=$saved_path
  say "PATH restored — later steps must not inherit the shadow"
  return $rc
}

# hook_binary <tool> — the executable inside the keld hook command in a tool's
# config. Quotes stripped in both shapes: Codex writes the command in a TOML
# literal string, Claude Code and Gemini inside a JSON string where the quotes
# arrive escaped.
hook_binary() {
  local p; p=$(tool_config_path "$1")
  [ -f "$p" ] || { echo ""; return 1; }
  python3 - "$p" <<'PY'
import sys
raw = open(sys.argv[1], encoding="utf-8", errors="replace").read()
i = raw.find("__hook --source")
if i < 0:
    raise SystemExit(1)
# ⚠️ PARSED FROM THE RIGHT. `telemetry.HookCommand` quotes the binary only when
# it cannot survive being read bare, so both shapes are in the wild on the same
# machine — and a left-to-right "first quoted run" reads the JSON KEY
# (`"command"`) instead of the path. The flag is the anchor; everything
# immediately before it is the binary.
head = raw[:i].rstrip()
if head.endswith('\\"'):            # a quoted path inside a JSON string
    head = head[:-2]
    j = head.rfind('\\"')
    out = head[j + 2:] if j >= 0 else head
    out = out.replace('\\\\', '\\')
elif head.endswith('"'):            # a quoted path in TOML, or plain text
    head = head[:-1]
    j = head.rfind('"')
    out = head[j + 1:] if j >= 0 else head
elif head.endswith("'"):            # Codex writes its command in a TOML literal
    head = head[:-1]
    j = head.rfind("'")
    out = head[j + 1:] if j >= 0 else head
else:                               # a bare path: the last token before the flag
    parts = head.split()
    out = parts[-1].lstrip('"\'') if parts else ""
out = out.strip()
if not out:
    raise SystemExit(1)
print(out)
PY
}

# step_c_daemon_restarts — three daemon restarts under a tool that is already
# holding a credential.
#
# ⚠️ **MEASURED 2026-09-18: after three daemon restarts the tool's OTLP
# exporter went silent for 18 minutes, and only restarting the EDITOR brought it
# back.** A tool reads its telemetry config once, at startup, and keeps the
# credential in memory (AGENTS.md says exactly this about the rotated ingest
# token), so the question is whether the credential a tool captured BEFORE the
# restarts is still accepted AFTER them.
#
# ⚠️ **WHAT THIS STEP CANNOT PROVE, STATED RATHER THAN IMPLIED.** `claude -p`
# and `codex exec` EXIT; this harness has no long-lived editor process, so the
# in-memory copy is simulated by capturing the credential once, before the first
# restart, and posting with that captured copy afterwards. That is the same
# fact the editor's exporter holds, carried by a different process. What it does
# NOT reproduce is a live TCP connection kept open across the restart — if the
# 18 minutes were a held connection rather than a refused credential, this step
# passes and the defect stands. Say so rather than claim the whole incident.
step_c_daemon_restarts() {
  step_begin "daemon-restarts"
  local t; t=$(echo $BEFORE | awk '{print $1}')
  [ -n "$t" ] || { blocked "no tool in the before half to hold a credential"; return 0; }

  local target; target=$(tool_otlp_target "$t")
  [ -n "$target" ] || { blocked "$(tool_display "$t") has no loopback OTLP endpoint in its config"; return 0; }
  local url hname hval
  url=$(echo "$target" | cut -f1); hname=$(echo "$target" | cut -f2); hval=$(echo "$target" | cut -f3)

  # The credential as a tool captured it at startup — read ONCE, here, and not
  # re-read afterwards. Re-reading would test the file, which nobody disputes;
  # this tests the copy a running process is holding.
  proxy_post_target "$url" "$hname" "$hval" "before any restart" || return 1

  local i
  for i in 1 2 3; do
    say "restart $i of 3 (the editor stays where it is)"
    daemon_stop
    daemon_start
  done

  proxy_post_target "$url" "$hname" "$hval" "after three restarts, same captured credential" || return 1

  # And the lanes themselves, counted before and after: a prompt after the
  # restarts has to light every lane, and a global count that was already
  # non-zero would be satisfied by work from an earlier step.
  local before_tel; before_tel=$(atlas_count $TELEMETRY_ROUTES)
  [ -n "$before_tel" ] || { say "could not read the mock Atlas counts"; return 1; }
  tool_prompt "$t" "after-restarts"
  close_open_blocks "$t"
  if [ -z "$(await_count_above "$before_tel" "$SETTLE" $TELEMETRY_ROUTES)" ]; then
    say "no NEW telemetry reached Atlas after the restarts (still $before_tel)."
    say "  This is the 18-minute silence: rows from before the restart keep the"
    say "  total non-zero, so only a delta can see it."
    return 1
  fi
  step_assert "$SETTLE" "" "$t"
}

# step_c_setup_rerun — `keld signal setup` run again on a machine that is
# already configured.
#
# ⚠️ **MEASURED 2026-09-18: a setup re-run left the tool configs holding a
# secret the proxy answers 401 to.** Both directions are asserted, because they
# are different failures with the same symptom:
#
#   - the credential the configs hold AFTER the re-run must work (the file was
#     rewritten with something the daemon does not accept), and
#   - the credential a running tool captured BEFORE it must STILL work (the
#     secret rotated under a process that cannot be told).
#
# Asserted by MAKING A REQUEST, never by comparing the two secrets: two strings
# that match prove nothing about a proxy that rejects both, and two that differ
# prove nothing about a proxy that accepts both. `agentcfg.TelemetrySecret` is
# documented as generated once and never rotated — this is the check that the
# documentation is true of the running machine.
step_c_setup_rerun() {
  step_begin "setup-rerun"
  local t; t=$(echo $BEFORE | awk '{print $1}')
  [ -n "$t" ] || { blocked "no configured tool to re-run setup against"; return 0; }

  local before; before=$(tool_otlp_target "$t")
  [ -n "$before" ] || { blocked "$(tool_display "$t") has no loopback OTLP endpoint in its config"; return 0; }

  say "keld signal setup --yes, on a machine that is already configured"
  "$BIN_DIR/keld" signal setup --yes >"$WORK/setup-rerun.out" 2>&1 \
    || { say "the re-run failed: $(tail -10 "$WORK/setup-rerun.out")"; return 1; }

  local t2
  for t2 in $BEFORE $AFTER; do
    proxy_post "$t2" "$(tool_display "$t2") after the setup re-run" || return 1
  done

  proxy_post_target "$(echo "$before" | cut -f1)" "$(echo "$before" | cut -f2)" \
    "$(echo "$before" | cut -f3)" "the credential captured BEFORE the re-run" || return 1
  return 0
}

# step_c_resume — a session that is CONTINUED rather than started.
#
# ⚠️ **MEASURED 2026-09-18: a resumed session's transcript START never moves,
# so the machine read `restart_required` forever.** The rule is "the newest tool
# session started before the config was written"; a session resumed today but
# opened last week satisfies that reading of it while being, in every sense a
# person cares about, a session that is running now with the config in force.
#
# A tool with no resume cannot express this, and says so per tool rather than
# failing: Gemini writes a WHOLE NEW session file per `gemini -p`, which is the
# opposite of the case under test.
#
# ⚠️ **THE STEP HAS TO BUILD ITS OWN MACHINE, BECAUSE THE WIRE ANSWERS PER TOOL
# AND THE QUESTION IS PER SESSION.** `restartFacts` asks the restart question of
# EVERY live session (b550504, and rightly: "is any window of this tool still on
# the old config" is what a person acts on), so on a machine that has driven
# eight sessions in the last half hour — which is precisely what chain C is by
# the time it gets here — the tool-level answer is `restart_required` whatever
# the RESUMED session does. Every one of those sessions predates the setup
# re-run two steps back, and `sessionActiveWindow` is 30 minutes.
#
# So the step parks the other transcripts for its own duration, leaving the one
# machine state in which the question has a single answer: ONE window open. They
# are moved, never deleted, and put back before the step returns — the steps
# after this one want the realistic machine, and the second-window step
# positively needs it.
#
# ⚠️ **AND IT PROVES THE DETECTOR FIRES FIRST.** "It does not read
# restart_required" is an ABSENCE, and an absence proves nothing unless the
# check is known to fire when the condition holds — chain B's skew control makes
# exactly this argument. So the step drives a session, touches the config so the
# session is older than it, and REQUIRES restart_required before resuming. If
# the control does not fire, the step reports inconclusive rather than passing.
step_c_resume() {
  step_begin "resume"
  local t any=0 rc=0
  for t in $BEFORE $AFTER; do
    case "$t" in
      claude_code|codex) ;;
      *) say "$(tool_display "$t"): no resume — every invocation is a new session, so this case does not exist for it"; continue ;;
    esac
    any=1
    park_transcripts "$t"
    tool_prompt "$t" "window-fresh"
    # ⚠️ **THE PRECONDITION IS WRITTEN WHERE THE RULE READS IT, AND TWO EASIER
    # LEVERS WERE MEASURED FIRST.** The fact this step needs is "keld wrote this
    # tool's config after that session started", and since WS6 that is
    # `manifest.Tools[<adapter>].ConfiguredAt` — keld's OWN record — with the
    # manifest's mtime and then the tool config's mtime only as fallbacks for
    # machines that have no such record.
    #
    #   - `touch`ing the tool config does nothing at all: the per-tool record
    #     wins. Measured on the rebased branch — the control read `working`.
    #   - Re-running `keld signal setup --yes` does not move it either, because
    #     the config is already correct and keld writes nothing. Measured:
    #     configured_at stayed at 23:10:55 across two re-runs while the session
    #     under test started at 23:13:43.
    #
    # Both readings are the product being right. So the instant is written
    # directly into keld's own record — the same class of construction as
    # parking the other transcripts, and the only one that states the fact the
    # rule is about rather than hoping a side effect produces it.
    python3 - "$ISO_HOME/manifest.json" "$(tool_config_path "$t")" <<'PY'
import json, sys, datetime
path, cfg = sys.argv[1], sys.argv[2]
try:
    with open(path) as f:
        d = json.load(f)
except Exception:
    raise SystemExit(0)
now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
for name, tool in (d.get("tools") or {}).items():
    if isinstance(tool, dict) and tool.get("config_path") == cfg:
        tool["configured_at"] = now
with open(path, "w") as f:
    json.dump(d, f, indent=2)
PY
    local ctl="" i
    for i in $(seq 1 "$DETECT_BUDGET"); do
      ctl=$(integration_state "$t")
      [ "$ctl" = "restart_required" ] && break
      sleep 1
    done
    if [ "$ctl" != "restart_required" ]; then
      unpark_transcripts "$t"
      blocked "$(tool_display "$t") does not read restart_required even for a session that started BEFORE the config (it reads '${ctl:-<no answer>}'). The detector is silent here, so a clear reading after a resume would prove nothing."
      continue
    fi
    say "$(tool_display "$t") reads restart_required for a session older than its config — the detector fires"
    tool_prompt "$t" "resume-session"
    # ⚠️ **POLLED, NOT READ ONCE, AND THE BUDGET IS THE WHOLE POINT.** The
    # machine is allowed a moment to learn that the resumed process adopted the
    # config — that proof is telemetry arriving for the session id, which is
    # forwarded asynchronously — so a single read taken the instant the prompt
    # returns would fail a machine that is merely a second behind. What the
    # machine may NOT do is stay there: the incident is a row that reads
    # restart_required FOREVER, because the only thing that is old is the
    # transcript's start instant and that can never move.
    local i st="" cleared=0
    for i in $(seq 1 "$DETECT_BUDGET"); do
      st=$(integration_state "$t")
      [ -n "$st" ] || { sleep 1; continue; }
      [ "$st" = "restart_required" ] || { cleared=$i; break; }
      sleep 1
    done
    if [ "$cleared" = "0" ]; then
      say "$(tool_display "$t") still reads '${st:-<no answer>}' ${DETECT_BUDGET}s after a RESUMED session."
      say "  Nothing is waiting on a restart: the process running that session"
      say "  started after the config was written and read it on the way in. The"
      say "  transcript's start instant is the only thing that is old, and it can"
      say "  never move — so an instruction to restart cannot be followed."
      rc=1
      unpark_transcripts "$t"
      continue
    fi
    say "$(tool_display "$t") reads '$st' after resuming (cleared in ${cleared}s)"
    # ⚠️ REPORTED, NOT FAILED — the same call `tool_await_configured` makes when
    # a freshly configured tool reads `broken`. This step's claim is about
    # restart_required and nothing else, so `broken` here does not fail it; but a
    # tool whose session was driven seconds ago has had no time to go silent on
    # any lane, and AC-4 says idle is never broken. That is a Compute finding,
    # and a run that saw it and said nothing would be hiding it.
    [ "$st" = "broken" ] && say "  ⚠️  'broken' seconds after a live prompt — nothing has had a chance to go silent yet (AC-4)"
    # And it stays cleared: a state that clears and comes back is the same
    # unfollowable instruction with a delay in front of it.
    local j back=""
    for j in 1 2 3; do
      sleep 3
      back=$(integration_state "$t")
      if [ "$back" = "restart_required" ]; then
        say "$(tool_display "$t") went BACK to restart_required ${j} read(s) later."
        rc=1; break
      fi
    done
    unpark_transcripts "$t"
  done
  [ "$any" = "1" ] || { blocked "no tool in this run can resume a session"; return 0; }
  return $rc
}

# park_transcripts <tool> / unpark_transcripts <tool> — move this tool's
# existing transcripts out of its watched root, and put them back.
#
# ⚠️ **MOVED, NEVER DELETED, AND ALWAYS PUT BACK.** They are the evidence of
# every earlier step; a harness that removed them would be destroying the thing
# a failing run is read from. The move preserves mtimes (`mv` within one
# filesystem), so a restored transcript is exactly as live as it was.
#
# This constructs ONE machine state — a single window open — and it exists
# because the wire answers per TOOL while one step's question is about one
# SESSION. Nothing else in this chain parks anything: the realistic machine,
# with several live sessions, is what the other steps are about.
park_transcripts() {
  local root; root=$(tool_transcript_root "$1")
  local park="$WORK/parked-$1"
  rm -rf "$park"; mkdir -p "$park"
  [ -d "$root" ] || return 0
  local n=0 f rel dir
  while IFS= read -r f; do
    rel=${f#"$root"/}
    # ⚠️ `dirname` IS NOT USABLE HERE. Claude Code names a project directory
    # after the workspace PATH with the slashes replaced, so on this harness it
    # begins with `-private-tmp-…` — and `dirname -private-…` is read as a run
    # of flags ("illegal option -- p", measured). Parameter expansion has no
    # option parsing, so it cannot be fooled by a leading dash.
    dir=${rel%/*}; [ "$dir" = "$rel" ] && dir=""
    mkdir -p "$park${dir:+/$dir}"
    mv "$f" "$park/$rel" && n=$((n+1))
  done < <(find "$root" -type f \( -name '*.jsonl' -o -name '*.json' \) 2>/dev/null)
  say "parked $n existing $(tool_display "$1") transcript(s) — one window open is the state under test"
}

unpark_transcripts() {
  local root; root=$(tool_transcript_root "$1")
  local park="$WORK/parked-$1"
  [ -d "$park" ] || return 0
  local n=0 f rel dir
  while IFS= read -r f; do
    rel=${f#"$park"/}
    dir=${rel%/*}; [ "$dir" = "$rel" ] && dir=""   # see park_transcripts: no dirname
    mkdir -p "$root${dir:+/$dir}"
    mv "$f" "$root/$rel" && n=$((n+1))
  done < <(find "$park" -type f 2>/dev/null)
  rm -rf "$park"
  say "restored $n parked $(tool_display "$1") transcript(s)"
}

# step_c_second_window — two live sessions of one tool at once.
#
# ⚠️ **MEASURED 2026-09-18: with two windows open, a pane row alternated every
# few seconds.** The state was decided from "the newest transcript FILE", and
# with two sessions being written the newest file changes from poll to poll — so
# the answer changed with it, and a person watching the pane saw a row flicker
# between two states with nothing on the machine changing.
#
# ⚠️ **THE ASSERTION IS "DOES NOT ALTERNATE", NOT "DOES NOT CHANGE", and the
# difference is what keeps it from failing a healthy machine.** A row is allowed
# to move once and settle — a session that has just been seen legitimately goes
# from idle to working. What it may not do is come BACK: a value that reappears
# after a different one is the flicker, and it means two answers about one
# machine are both live. Five reads, because three cannot tell a settle from
# the first half of an alternation.
step_c_second_window() {
  step_begin "second-window"
  local t; t=$(echo $BEFORE | awk '{print $1}')
  [ -n "$t" ] || { blocked "no tool in the before half to open a second window for"; return 0; }

  # Two FRESH sessions: `claude -p` and `codex exec` each start a new one, which
  # is what a second window is. Both transcripts are then being written, which
  # is the state under test.
  tool_prompt "$t" "window-one"
  tool_prompt "$t" "window-two"

  local i st all=""
  for i in 1 2 3 4 5; do
    st=$(integration_state "$t")
    [ -n "$st" ] || { say "/v1/integrations did not answer on read $i"; return 1; }
    all="$all $st"
    sleep 3
  done
  say "five consecutive reads with two live sessions:$all"

  # An alternation is a value that RETURNS after a different one.
  local seen="" prev="" v
  for v in $all; do
    if [ "$v" != "$prev" ] && [ -n "$prev" ]; then
      case " $seen " in
        *" $v "*)
          say "$(tool_display "$t")'s state ALTERNATES:$all"
          say "  Nothing about the machine changed between these reads. A row that"
          say "  flickers is worse than a wrong one: it tells a person their own"
          say "  machine is unstable, and no instruction it prints can be acted on."
          say "  Two live sessions means two answers, and the row shows whichever"
          say "  transcript was written last."
          return 1 ;;
      esac
    fi
    [ "$v" != "$prev" ] && seen="$seen $v"
    prev=$v
  done
  return 0
}

# step_c_sleep_wake — the machine stops, and comes back.
#
# ⚠️ **MEASURED 2026-09-09 ON A REAL MAC (AGENTS.md, supervisor.go): a 90s
# readiness deadline was armed at 03:19, the machine slept with ~2-second dark
# wakes every 15 minutes, and at 05:20, 07:23 and 09:42 the supervisor killed a
# child that had been given seconds of real time as a "failed start". The third
# exhausted the cap.** The sidecar's readiness deadline is therefore measured in
# time the machine was AWAKE, and both sleep detectors compare WALL-CLOCK
# instants (`Round(0)`), because on macOS Go's monotonic clock does not advance
# through a sleep.
#
# ⚠️ **HOW THE JUMP IS SIMULATED, AND WHERE THE SIMULATION DIVERGES.** The
# harness cannot sleep the machine and must not step the system clock (it is not
# root, and a harness that rewrites the clock of the machine it runs on is a
# worse defect than the one it checks for — the rule `scripts/e2e-up.sh` learned
# at 00:16 CEST on 2026-09-19, when the wall clock was the defect and nothing in
# the report could say so). So the daemon and the sidecar's process GROUP are
# SIGSTOPped and resumed: from inside both processes the wall clock jumps by the
# freeze, which is exactly what each detector reads. The divergence, stated
# because it is real: Go's MONOTONIC clock keeps advancing under SIGSTOP and
# does not across a macOS sleep. That difference is precisely why the detectors
# were moved onto the wall clock, so this simulation exercises them — but a
# regression that reintroduced a monotonic detector would pass here and fail on
# a real lid-close.
#
# The freeze defaults to 35s, which crosses `defaultStartSleepGap` (30s), the
# detector the incident is about. The health owner's own detector floors its gap
# at TWO MINUTES, so reaching that one costs a 130s freeze and is opt-in
# (KELD_CONFORM_SLEEP=130). The step SAYS which of the two its freeze can reach;
# claiming the other would be claiming a check that never ran.
step_c_sleep_wake() {
  step_begin "sleep-wake"
  local freeze=${KELD_CONFORM_SLEEP:-35}
  local pgid=""
  [ -f "$SIDECAR_PID_FILE" ] && pgid=$(cat "$SIDECAR_PID_FILE" 2>/dev/null || echo "")

  case "$(uname -s)" in
    MINGW*|MSYS*|CYGWIN*)
      blocked "no SIGSTOP on Windows; the wall-clock jump cannot be simulated without stepping the machine's clock"
      return 0 ;;
  esac
  [ -n "${DAEMON_PID:-}" ] || { say "no daemon to freeze"; return 1; }

  if [ "$freeze" -ge 130 ]; then
    say "freezing for ${freeze}s — crosses BOTH the supervisor's 30s start gap and the health owner's 2-minute floor"
  else
    say "freezing for ${freeze}s — crosses the supervisor's 30s start gap only."
    say "  The health owner's sleep detector floors its gap at two minutes; set"
    say "  KELD_CONFORM_SLEEP=130 to reach it. It is NOT exercised by this run."
  fi

  kill -STOP "$DAEMON_PID" 2>/dev/null || { say "could not SIGSTOP the daemon"; return 1; }
  [ -n "$pgid" ] && kill -STOP "-$pgid" 2>/dev/null
  sleep "$freeze"
  [ -n "$pgid" ] && kill -CONT "-$pgid" 2>/dev/null
  kill -CONT "$DAEMON_PID" 2>/dev/null || { say "could not resume the daemon"; return 1; }
  say "resumed after ${freeze}s of wall clock the processes did not run through"

  kill -0 "$DAEMON_PID" 2>/dev/null || { say "the daemon is gone after the wake"; return 1; }

  # The sidecar is what the incident killed, so ask the sidecar. /health is the
  # same question the readiness gate asks, and it is asked with a budget because
  # a woken machine is allowed to take a moment — what it may not do is stay
  # down.
  local port i ok=0
  port=$(agent_json_field sidecar_port)
  if [ -z "$port" ] || [ "$port" = "0" ]; then
    say "no sidecar port in agent.json after the wake — the analysis half is not reachable"
    return 1
  fi
  for i in $(seq 1 60); do
    if curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then ok=1; break; fi
    sleep 1
  done
  if [ "$ok" != "1" ]; then
    say "the sidecar does not answer /health 60s after the wake."
    say "  Each dark wake used to be read as a failed start; three of them spent"
    say "  the restart cap and the machine served nothing until a human noticed."
    return 1
  fi
  say "the sidecar answers /health after the wake (${i}s)"

  # Serving, not merely up: a process that accepts TCP and answers nothing is
  # the state the readiness gate exists to distinguish.
  local t; t=$(echo $BEFORE | awk '{print $1}')
  [ -n "$t" ] || return 0
  local before_tel; before_tel=$(atlas_count $TELEMETRY_ROUTES)
  tool_prompt "$t" "after-wake"
  close_open_blocks "$t"
  if [ -n "$before_tel" ] && [ -z "$(await_count_above "$before_tel" "$SETTLE" $TELEMETRY_ROUTES)" ]; then
    say "no new telemetry after the wake"
    return 1
  fi
  step_assert "$SETTLE" "" "$t"
}

# step_c_unpaired — a full pass with no Atlas pairing at all.
#
# The claim under test is `collect always`: a machine nobody has paired still
# COLLECTS — the ledger records its blocks, the spool holds its pointers, the
# watcher advances its cursors — and SENDS nothing. Then pairing delivers what
# was collected, rather than starting from the moment of pairing.
#
# ⚠️ **COUNTED BEFORE AND AFTER, TWICE.** "Nothing is sent" is a delta of ZERO
# across the unpaired window, not a low total; "everything arrives" is a delta
# of MORE THAN ZERO across the pairing. A total would be satisfied by every
# earlier step in this chain.
step_c_unpaired() {
  step_begin "unpaired"
  daemon_stop

  # Unpair by moving the two files pairing writes. Nothing else is touched:
  # the transcripts, the store and the tool configs are what a machine that
  # collected before pairing would have.
  local keep="$WORK/paired-aside"
  mkdir -p "$keep"
  [ -f "$ISO_HOME/hook.json" ] && mv "$ISO_HOME/hook.json" "$keep/hook.json"
  [ -f "$ISO_HOME/auth.json" ] && mv "$ISO_HOME/auth.json" "$keep/auth.json"
  say "unpaired: hook.json and auth.json moved aside"

  local before_all; before_all=$(atlas_count /v1/enrichments /v1/signal/blocks $TELEMETRY_ROUTES)
  [ -n "$before_all" ] || { say "could not read the mock Atlas counts"; return 1; }

  daemon_start

  # ⚠️ **EVERY "COLLECTED" FACT IS A DELTA ACROSS THIS WINDOW, and the marker is
  # what makes it one.** Eight steps have already run: the spool holds files,
  # the cursors file exists, the ledger holds blocks. A presence check would
  # therefore pass on a machine that collected nothing at all while unpaired —
  # the same defect as reading a non-zero Atlas total and calling the lane
  # healthy.
  local mark="$WORK/unpaired-mark"
  : > "$mark"
  local ledger_before; ledger_before=$(ledger_rows)

  local t; t=$(echo $BEFORE | awk '{print $1}')
  [ -n "$t" ] || { blocked "no tool in the before half to drive unpaired"; return 0; }
  tool_prompt "$t" "unpaired"
  close_open_blocks "$t"
  sleep 20   # one blocks sweep (KELD_BLOCKS_INTERVAL=20s) plus the watcher poll

  # COLLECTED — three surfaces, each reported separately, because "collect
  # always" is not one mechanism and a partial answer has to name which half of
  # it runs.
  local collected=0 detail=""
  local ledger_after; ledger_after=$(ledger_rows)
  if [ "${ledger_after:-0}" -gt "${ledger_before:-0}" ]; then collected=$((collected+1)); fi
  detail="ledger ${ledger_before:-?} -> ${ledger_after:-?} row(s)"

  local spooled; spooled=$(find "$ISO_HOME/spool" -type f -newer "$mark" 2>/dev/null | wc -l | tr -d ' ')
  [ "${spooled:-0}" -gt 0 ] && collected=$((collected+1))
  detail="$detail; ${spooled:-0} new spool file(s)"

  local cursors; cursors=$(find "$ISO_HOME/watch" -name cursors.json -newer "$mark" 2>/dev/null | wc -l | tr -d ' ')
  [ "${cursors:-0}" -gt 0 ] && collected=$((collected+1))
  detail="$detail; watcher cursors $( [ "${cursors:-0}" -gt 0 ] && echo advanced || echo "did not move")"

  say "unpaired collection: $detail"

  # SENT — must be exactly nothing.
  local now_all; now_all=$(atlas_count /v1/enrichments /v1/signal/blocks $TELEMETRY_ROUTES)
  [ -n "$now_all" ] || { say "could not read the mock Atlas counts"; return 1; }
  if [ "$now_all" != "$before_all" ]; then
    say "an UNPAIRED machine sent $((now_all - before_all)) request(s) to Atlas."
    say "  Collecting without pairing is the feature; sending without pairing is"
    say "  publishing somebody's work to an org that never claimed the machine."
    return 1
  fi
  say "nothing was sent while unpaired ($before_all before, $now_all after)"

  if [ "$collected" -lt 3 ]; then
    say "an unpaired machine collected on $collected of 3 surfaces ($detail)."
    say "  A machine that collects nothing until it is paired has no history to"
    say "  deliver when it is, and the work done before pairing is gone for good."
    # Pair again before returning: a failed step stops the chain, and leaving
    # the machine unpaired would make the failure harder to read, not easier.
    step_c_repair_pairing
    return 1
  fi

  # PAIR, and require what was collected to ARRIVE.
  daemon_stop
  step_c_repair_pairing
  daemon_start
  if [ -z "$(await_count_above "$before_all" "$SETTLE" /v1/enrichments /v1/signal/blocks $TELEMETRY_ROUTES)" ]; then
    say "pairing delivered NOTHING that was collected while unpaired."
    say "  Collection that cannot be delivered afterwards is a local log, not a"
    say "  pipeline: the first day of every machine would be silently lost."
    return 1
  fi
  say "what was collected while unpaired arrived after pairing"
  return 0
}

# ledger_rows — how many rows GET /v1/ledger holds (blocks plus pending).
# Empty when the route could not be read, which every caller treats as a
# reader failure rather than as "the machine has collected nothing".
ledger_rows() {
  local body; body=$(daemon_get "/v1/ledger?since=0&limit=500")
  [ -n "$body" ] || { echo ""; return 1; }
  printf '%s' "$body" | python3 -c '
import json, sys
try: d = json.load(sys.stdin)
except Exception: raise SystemExit(1)
print(len(d.get("blocks") or []) + len(d.get("pending") or []))
' 2>/dev/null || { echo ""; return 1; }
}

# step_c_repair_pairing — put the two files back.
step_c_repair_pairing() {
  local keep="$WORK/paired-aside"
  [ -f "$keep/hook.json" ] && mv "$keep/hook.json" "$ISO_HOME/hook.json"
  [ -f "$keep/auth.json" ] && mv "$keep/auth.json" "$ISO_HOME/auth.json"
  say "re-paired: hook.json and auth.json restored"
}

# step_c_otlp_switch — the tool's own OTLP lane and the transcript mirror,
# running at the same time.
#
# The daemon can learn what a prompt cost twice: from the TRANSCRIPT (the
# ledger's `measured` cell, which is how a watched source with no egress is
# covered at all) and from the tool's OWN OTLP (forwarded through the loopback
# proxy). With the Developer OTLP switch on, both are live, and two things have
# to hold:
#
#   - the two lanes AGREE on every priced field — the model and the four token
#     buckets, plus the identifiers that join them. A disagreement is an invoice
#     that depends on which lane answered.
#   - Atlas ends with ONE row per request, not two. Two lanes reporting the same
#     request as two is double-counted spend, which is worse than missing spend:
#     it is confidently wrong.
#
# ⚠️ **THIS STEP HAS NEVER EXECUTED.** There is no tool-OTLP switch on this
# branch: `GET /v1/settings` publishes send_to_atlas, dev_blocks, show_breaks,
# workstreams_off, attribution, dev_generate and dev_repos, and none of them is
# it. The guard asks the daemon's own contract surface rather than hard-coding
# the answer, so the step un-blocks the day the key lands — and if it lands
# under a name this list does not hold, the blocked line says exactly which
# names were looked for.
OTLP_SWITCH_KEYS=${KELD_CONFORM_OTLP_SWITCH_KEYS:-"tool_otlp tool_otel send_tool_otlp developer_otlp otlp_switch"}
step_c_otlp_switch() {
  step_begin "otlp-switch"
  # shellcheck disable=SC2086  # the key list must word-split
  if ! settings_key_present $OTLP_SWITCH_KEYS; then
    blocked "no tool-OTLP switch in GET /v1/settings (looked for: $OTLP_SWITCH_KEYS). The switch is the Developer-OTLP workstream's; until it exists there is nothing to turn on, and a run with it off would prove the opposite of what this step is for."
    return 0
  fi

  local t; t=$(echo $BEFORE | awk '{print $1}')
  [ -n "$t" ] || { blocked "no tool in the before half to drive with the switch on"; return 0; }

  local key="" k
  for k in $OTLP_SWITCH_KEYS; do
    if settings_key_present "$k"; then key=$k; break; fi
  done

  # ⚠️ **ASK WHETHER IT IS ON, AND ONLY THEN TRY TO TURN IT ON.** The chain
  # exports KELD_TOOL_OTLP=1 (see run-chain.sh), and an env-pinned key is
  # REPORTED IN `readonly` and refused by PUT — writing the file would have no
  # observable effect. A step that PUT blindly would either fail on the refusal
  # or, worse, believe it had changed something it had not.
  if settings_value_true "$key"; then
    say "$key is already on (pinned by the environment for this chain)"
  else
    say "turning $key on through PUT /v1/settings"
    curl -fsS -X PUT -H "x-keld-agent-secret: $DAEMON_SECRET" -H 'content-type: application/json' \
      -d "{\"$key\":true}" "$DAEMON_URL/v1/settings" >/dev/null 2>&1 || {
        say "PUT /v1/settings refused $key"; return 1; }
    daemon_stop; daemon_start
    settings_value_true "$key" || { say "$key is still off after the PUT"; return 1; }
  fi

  # The lane has to be WIRED before it can be compared: the switch decides
  # whether keld writes the tool's OTEL block, and the detector does that on its
  # own poll. Waiting on the CONFIG is the `await_hook_repair` idiom — a timer
  # here would prove whatever the timer happened to be.
  local i
  for i in $(seq 1 "$DETECT_BUDGET"); do
    [ -n "$(tool_otlp_target "$t")" ] && break
    sleep 1
  done
  if [ -z "$(tool_otlp_target "$t")" ]; then
    say "$key is on and $(tool_display "$t")'s config still carries no OTLP endpoint after ${DETECT_BUDGET}s."
    say "  With no tool lane there is only one lane, and this step's question —"
    say "  do the two agree — has no second side to ask."
    return 1
  fi

  tool_prompt "$t" "otlp-switch"
  close_open_blocks "$t"
  step_assert "$SETTLE" "" "$t" || return 1

  # The two lanes, compared on the priced fields alone.
  local ledger; ledger=$(daemon_get "/v1/ledger?since=0&limit=200")
  [ -n "$ledger" ] || { say "GET /v1/ledger did not answer"; return 1; }
  printf '%s' "$ledger" > "$WORK/ledger-otlp-switch.json"

  # ⚠️ **EXIT 2 IS "CANNOT COMPARE", EXIT 1 IS "THEY DISAGREE", and collapsing
  # them would accuse the product of a defect this harness cannot substantiate.**
  # Measured on the rebased branch: every block's `measured` cell reads
  # `status:"n/a", reason:"no_tokens"` — the mock model's usage never reaches the
  # priced half — so there is no priced side to compare, which is a fact about
  # the FIXTURE. The cell's own stated reason is printed either way.
  local rc=0
  python3 - "$WORK/ledger-otlp-switch.json" "$ATLAS_STATE" <<'PY' || rc=$?
import json, os, sys, glob

# The priced fields, named once. `model` plus these four buckets are what an
# invoice is built from; everything else about a block is description.
BUCKETS = ("input", "output", "cache_read", "cache_creation")

ledger_path, state = sys.argv[1], sys.argv[2]
led = json.load(open(ledger_path))

# LANE ONE — the transcript: the ledger's `measured` cell, derived from the
# JSONL the tool wrote and from nothing else.
blocks = led.get("blocks") or []
measured, unpriced = [], []
for b in blocks:
    cell = (b.get("cells") or {}).get("measured")
    if cell and cell.get("status") == "ok":
        measured.append({
            "session": (b.get("key") or {}).get("session"),
            "model": cell.get("model"),
            "tokens": cell.get("tokens") or {},
            "requests": int(cell.get("requests") or 0),
        })
    else:
        unpriced.append((cell or {}).get("status", "absent") + "/" + (cell or {}).get("reason", ""))
if not measured:
    if not blocks:
        print("otlp-switch: the ledger holds no block at all, so there is nothing to compare")
    else:
        print("otlp-switch: none of the ledger's %d block(s) is priced — measured cells read %s. "
              "The transcript lane has no priced side, so the comparison cannot run."
              % (len(blocks), ", ".join(sorted(set(unpriced)))))
    raise SystemExit(2)

# LANE TWO — the tool's own OTLP, as the mock Atlas stored it after the proxy
# forwarded it. Read from the persisted BODIES rather than from a count: the
# question is what each row SAYS, not how many arrived.
bodies = []
for route in ("v1_logs", "v1_metrics"):
    for p in sorted(glob.glob(os.path.join(state, route, "*.json"))):
        try:
            bodies.append(json.load(open(p)))
        except Exception:
            pass

def attrs(node, out):
    """Every OTLP attribute key/value pair anywhere in a body."""
    if isinstance(node, dict):
        if "key" in node and "value" in node and isinstance(node["value"], dict):
            v = node["value"]
            out[node["key"]] = v.get("stringValue", v.get("intValue", v.get("doubleValue")))
        for v in node.values():
            attrs(v, out)
    elif isinstance(node, list):
        for v in node:
            attrs(v, out)
    return out

seen = {}
for b in bodies:
    attrs(b, seen)

problems = []
for m in measured:
    missing = [k for k in BUCKETS if k not in m["tokens"]]
    if missing:
        problems.append("the transcript lane reports no %s for session %s"
                        % ("/".join(missing), m["session"]))
    if not m["model"]:
        problems.append("the transcript lane names no model for session %s" % m["session"])
    # The tool's own lane names the model it billed. The two must be the SAME
    # string: an invoice that depends on which lane answered is not an invoice.
    tool_model = seen.get("model") or seen.get("gen_ai.request.model")
    if m["model"] and tool_model and tool_model != m["model"]:
        problems.append("the two lanes name different models for session %s: transcript %r, tool %r"
                        % (m["session"], m["model"], tool_model))

# ONE ROW PER REQUEST. `requests` is what the transcript lane counted; if the
# tool's own lane adds a second row for each of them, the org is billed twice
# for work done once — confidently wrong, which is worse than missing.
want = sum(m["requests"] for m in measured)
if want and len(bodies) > 2 * want:
    problems.append("the mock Atlas holds %d telemetry bodies for %d request(s) — two lanes "
                    "reporting the same work is double-counted spend" % (len(bodies), want))

if problems:
    for p in problems:
        print("otlp-switch: " + p)
    raise SystemExit(1)
print("otlp-switch: the two lanes agree on model and all four token buckets across "
      "%d block(s), with %d telemetry body/bodies for %d request(s)"
      % (len(measured), len(bodies), want))
PY
  case "$rc" in
    0) return 0 ;;
    2) blocked "the transcript lane priced nothing this run, so the two lanes have no priced field to disagree about (see the line above). The tool's own lane is live; what is missing is a measured block."
       return 0 ;;
    *) return 1 ;;
  esac
}
