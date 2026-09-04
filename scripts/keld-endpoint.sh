#!/bin/sh
# Point a Keld agent at an Atlas — local, dev or prod — and see where it points now.
#
#   scripts/keld-endpoint.sh                      where everything on this machine points
#   scripts/keld-endpoint.sh save local           save the CURRENT hook.json as the "local" profile
#   scripts/keld-endpoint.sh use local            switch the daemon AND the AI tools, then restart
#   scripts/keld-endpoint.sh use local --daemon-only
#   scripts/keld-endpoint.sh list
#   KELD_HOME=~/.keld-smoke scripts/keld-endpoint.sh use dev
#
# ⚠️ **AN ENVIRONMENT LIVES IN TWO PLACES, AND MOVING ONE IS WORSE THAN MOVING NEITHER.**
#
#   1. $KELD_HOME/hook.json — the daemon: enrichments, blocks, client-events, org settings,
#      and the telemetry it proxies. This script writes it.
#   2. every AI tool's own config — `keld signal setup` writes an OTLP endpoint and an ingest
#      token straight into ~/.claude/settings.json (and Codex's, and Gemini's), plus the
#      SessionStart hook's KELD_CTX_ENDPOINT/KELD_CTX_TOKEN. Those are baked at setup time and
#      know nothing about hook.json.
#
# Move only the first and a single session's blocks land in dev while its telemetry and tool
# context keep going to local — one session split across two environments, joined by nothing,
# with every individual piece looking healthy. So `use` re-runs `keld signal setup` against the
# new endpoint unless you pass --daemon-only.
#
# ⚠️ **A PROFILE IS AN ENDPOINT AND ITS TOKEN, NOT A URL.** `ingest_token` is issued by the
# Atlas that will accept it, so a prod token is meaningless against local. Rewriting only the
# URL gives an agent posting to the right host and being rejected by it, which looks exactly
# like a broken agent.
#
# ⚠️ **hook.json IS READ ONCE, AT DAEMON START.** `awaitConfig` polls only while a machine is
# unconfigured; once it has an endpoint and a token, that pair is fixed for the life of the
# process. Editing the file changes nothing until the agent restarts, which is why `use` does.
set -eu

KELD_HOME_DIR="${KELD_HOME:-$HOME/.keld}"
PROFILES="$KELD_HOME_DIR/endpoints"
HOOK="$KELD_HOME_DIR/hook.json"

die() { echo "keld-endpoint: $*" >&2; exit 1; }

# Endpoints are stored in the BASE shape (scheme://host:port) because that is what the daemon
# derives every destination from — it cuts at "/v1/" if it finds one and appends if it does not
# (enrichEndpoint / signalBlocksEndpoint / settingsEndpoint / logsEndpoint). Both shapes work,
# which is precisely how two homes on one machine drift into disagreeing about which is "the"
# endpoint.
normalise() {
  python3 - "$1" <<'PY'
import sys
u = sys.argv[1]
i = u.find("/v1/")
print((u[:i] if i >= 0 else u).rstrip("/"))
PY
}

# ⚠️ The TOOL half, printed first because it is the half nobody thinks to check. A daemon and a
# tool pointing at different Atlases is not visibly broken anywhere else.
show_tools() {
  python3 - <<'PY'
import json, os, re

def note(label, value):
    print(f"  {label:<28} {value}")

print("AI tools (written by `keld signal setup`)")
p = os.path.expanduser("~/.claude/settings.json")
if os.path.exists(p):
    try:
        d = json.load(open(p))
    except Exception:
        d = {}
    env = d.get("env", {}) or {}
    note("claude_code · telemetry", env.get("OTEL_EXPORTER_OTLP_ENDPOINT", "(not configured)"))
    blob = json.dumps(d.get("hooks", {}))
    m = re.search(r"KELD_CTX_ENDPOINT=(\S+)", blob)
    note("claude_code · tool-context", m.group(1) if m else "(no keld hook)")
else:
    note("claude_code", "(no ~/.claude/settings.json)")
q = os.path.expanduser("~/.codex/config.toml")
if os.path.exists(q):
    text = open(q, errors="replace").read()
    # Only inside the keld-managed markers: a Codex config holds unrelated URLs (MCP servers,
    # providers) and the first one in the file is not the one this is about.
    block = re.search(r"# >>> keld.*?(?=\n# <<<|\Z)", text, re.S)
    m = re.search(r'endpoint = "([^"]+)"', block.group(0)) if block else None
    note("codex · telemetry", m.group(1) if m else "(no keld-managed block)")
q = os.path.expanduser("~/.gemini/settings.json")
if os.path.exists(q):
    try:
        t = (json.load(open(q)).get("telemetry") or {})
    except Exception:
        t = {}
    ep = t.get("otlpEndpoint") or ""
    # ⚠️ Gemini carries the token in the URL as ?token=, so this must never be printed whole.
    note("gemini · telemetry", ep.split("?")[0] if ep else "(not configured)")
print()
PY
}

# A short fingerprint of the token, so two agents can be compared without a secret reaching a
# terminal, a screenshot or a paste into a ticket.
describe() {
  python3 - "$1" <<'PY'
import hashlib, json, os, sys
p = sys.argv[1]
if not os.path.exists(p):
    print("  (no hook.json — not configured)"); raise SystemExit
try:
    d = json.load(open(p))
except Exception:
    print("  (hook.json is unreadable or not JSON)"); raise SystemExit
tok = d.get("ingest_token") or ""
fp = hashlib.sha256(tok.encode()).hexdigest()[:8] if tok else "none"
print(f"  endpoint : {d.get('endpoint') or '(unset)'}")
print(f"  token    : {fp}   (sha256 prefix — never the token itself)")
PY
}

write_hook() {   # write_hook <endpoint> <token>
  python3 - "$HOOK" "$1" "$2" <<'PY'
import json, os, sys
path, endpoint, token = sys.argv[1], sys.argv[2], sys.argv[3]
os.makedirs(os.path.dirname(path), exist_ok=True)
json.dump({"endpoint": endpoint, "ingest_token": token}, open(path, "w"), indent=2)
open(path, "a").write("\n")
os.chmod(path, 0o600)
PY
}

save_profile() { # save_profile <path> <endpoint> <token>
  python3 - "$1" "$2" "$3" <<'PY'
import json, os, sys
path, endpoint, token = sys.argv[1], sys.argv[2], sys.argv[3]
json.dump({"endpoint": endpoint, "ingest_token": token}, open(path, "w"), indent=2)
open(path, "a").write("\n")
os.chmod(path, 0o600)
PY
}

read_field() {   # read_field <file> <field>
  python3 - "$1" "$2" <<'PY'
import json, sys
try:
    print(json.load(open(sys.argv[1])).get(sys.argv[2]) or "")
except Exception:
    print("")
PY
}

restart_agent() {
  if command -v keld >/dev/null 2>&1; then
    echo "restarting the agent…"
    keld signal restart || die "restart failed — start it yourself, the config is already written"
  else
    echo "keld is not on PATH, so the config is written but nothing was restarted. Use:"
    echo "  launchctl kickstart -k gui/\$(id -u)/co.keld.agent      # macOS"
    echo "  systemctl --user restart keld-agent.service             # Linux"
  fi
}

# The tool half. `keld signal setup` is the only thing that may write an AI tool's config — it
# owns the managed blocks, the conflict handling and the per-tool shapes (Claude's OTEL env,
# Gemini's ?token= URL, the SessionStart hook), and re-implementing any of that in shell would
# make this a second writer of files another program believes it owns.
#
# --no-login: reuse stored credentials for that Atlas and fail loudly when there are none,
# rather than opening a browser from inside a config-switching script.
setup_tools() {
  ep="$1"
  if ! command -v keld >/dev/null 2>&1; then
    echo
    echo "keld is not on PATH, so the AI TOOLS WERE NOT REPOINTED. Until you run:"
    echo "  keld signal setup --api-url $ep -y"
    echo "their telemetry and tool-context still go to the previous environment."
    return 0
  fi
  echo "repointing the AI tools at $ep…"
  if keld signal setup --api-url "$ep" -y --no-login; then
    echo "tools repointed. Restart any open AI tool session — these configs are read at start."
  else
    echo
    echo "⚠️  The daemon moved to $ep but THE TOOLS DID NOT: their telemetry and tool-context"
    echo "    still go to the previous environment. Most likely there are no stored"
    echo "    credentials for $ep yet:"
    echo "      keld login --api-url $ep && keld signal setup --api-url $ep -y"
  fi
}

cmd="${1:-status}"

case "$cmd" in
  status)
    show_tools
    for h in "$HOME/.keld" "$HOME/.keld-smoke"; do
      [ -d "$h" ] || continue
      echo "$h"
      describe "$h/hook.json"
      if [ -d "$h/endpoints" ]; then
        cur=$(read_field "$h/hook.json" endpoint)
        for f in "$h"/endpoints/*.json; do
          [ -e "$f" ] || continue
          [ "$(read_field "$f" endpoint)" = "$cur" ] && echo "  matches  : $(basename "${f%.json}")"
        done
      fi
      echo
    done
    ;;

  list)
    [ -d "$PROFILES" ] || die "no profiles saved yet in $PROFILES (see: $0 save <name>)"
    for f in "$PROFILES"/*.json; do
      [ -e "$f" ] || die "no profiles saved yet in $PROFILES"
      echo "$(basename "${f%.json}")	$(read_field "$f" endpoint)"
    done
    ;;

  save)
    name="${2:-}"
    [ -n "$name" ] || die "usage: $0 save <name>"
    [ -f "$HOOK" ] || die "$HOOK does not exist — run 'keld login && keld signal setup' first"
    ep=$(normalise "$(read_field "$HOOK" endpoint)")
    tok=$(read_field "$HOOK" ingest_token)
    [ -n "$ep" ] && [ -n "$tok" ] || die "$HOOK has no endpoint/token to save"
    mkdir -p "$PROFILES"
    save_profile "$PROFILES/$name.json" "$ep" "$tok"
    echo "saved '$name' -> $ep   ($PROFILES/$name.json)"
    ;;

  use)
    name="${2:-}"
    [ -n "$name" ] || die "usage: $0 use <name> [--daemon-only]   (see: $0 list)"
    src="$PROFILES/$name.json"
    [ -f "$src" ] || die "no profile '$name' in $PROFILES — save one first with: $0 save $name"
    ep=$(read_field "$src" endpoint)
    tok=$(read_field "$src" ingest_token)
    [ -n "$ep" ] && [ -n "$tok" ] || die "profile '$name' is missing an endpoint or a token"
    write_hook "$ep" "$tok"
    echo "$KELD_HOME_DIR now points at $ep  (profile: $name)"
    restart_agent
    if [ "${3:-}" = "--daemon-only" ]; then
      echo
      echo "--daemon-only: the AI tools still post telemetry and tool-context wherever"
      echo "'keld signal setup' last pointed them, so this session's data is split across"
      echo "two environments. Deliberate here — just know that is what it is."
    else
      setup_tools "$ep"
    fi
    ;;

  set)
    url="${2:-}"
    [ -n "$url" ] || die "usage: $0 set <url> [token]   (token defaults to the one already there)"
    ep=$(normalise "$url")
    tok="${3:-$(read_field "$HOOK" ingest_token)}"
    [ -n "$tok" ] || die "no token: pass one, or run 'keld login && keld signal setup' against $ep"
    old=$(read_field "$HOOK" endpoint)
    if [ -n "$old" ] && [ "$(normalise "$old")" != "$ep" ] && [ -z "${3:-}" ]; then
      echo "⚠️  keeping the token issued by $old while pointing at $ep."
      echo "    Tokens are issued per Atlas — if $ep rejects it, run 'keld login && keld signal setup'."
    fi
    write_hook "$ep" "$tok"
    echo "$KELD_HOME_DIR now points at $ep"
    restart_agent
    setup_tools "$ep"
    ;;

  -h|--help|help)
    sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'
    ;;

  *)
    die "unknown command '$cmd' (try: status, list, save, use, set, help)"
    ;;
esac
