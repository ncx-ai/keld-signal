#!/bin/sh
# Point a Keld agent at an Atlas — local, dev or prod — and see where it points now.
#
#   scripts/keld-endpoint.sh                      where every agent on this machine points
#   scripts/keld-endpoint.sh save local           save the CURRENT hook.json as the "local" profile
#   scripts/keld-endpoint.sh use local            switch to it, then restart the agent
#   scripts/keld-endpoint.sh list                 the saved profiles
#   KELD_HOME=~/.keld-smoke scripts/keld-endpoint.sh use dev
#
# ⚠️ **A PROFILE IS AN ENDPOINT AND ITS TOKEN, NOT A URL.** `ingest_token` in hook.json is
# issued by the Atlas that will accept it, so a prod token is meaningless against local and
# vice versa. Rewriting only the URL — the obvious version of this script — gives you an agent
# that posts to the right host and is rejected by it, which looks exactly like a broken agent.
# So a profile is the whole hook.json, saved per environment, and switching swaps both fields.
#
# ⚠️ **hook.json IS READ ONCE, AT DAEMON START.** `awaitConfig` polls only while a machine is
# unconfigured; once it has an endpoint and a token, that pair is fixed for the life of the
# process. Editing the file changes nothing until the agent restarts, which is why `use`
# restarts it for you.
set -eu

KELD_HOME_DIR="${KELD_HOME:-$HOME/.keld}"
PROFILES="$KELD_HOME_DIR/endpoints"
HOOK="$KELD_HOME_DIR/hook.json"

die() { echo "keld-endpoint: $*" >&2; exit 1; }

# Endpoints are stored in the BASE shape (scheme://host:port) because that is what the daemon
# derives every destination from — it cuts at "/v1/" if it finds one and appends if it does not
# (see enrichEndpoint / signalBlocksEndpoint / settingsEndpoint). Both shapes work, which is
# precisely why two machines drift into disagreeing about which one is "the" endpoint.
normalise() {
  python3 - "$1" <<'PY'
import sys
u = sys.argv[1]
i = u.find("/v1/")
print((u[:i] if i >= 0 else u).rstrip("/"))
PY
}

# A short fingerprint of the token, so two agents can be compared without a secret reaching a
# terminal, a screenshot or a log.
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
    echo "keld is not on PATH, so the config is written but nothing was restarted."
    echo "Restart it with whichever applies:"
    echo "  keld signal restart"
    echo "  launchctl kickstart -k gui/\$(id -u)/co.keld.agent      # macOS"
    echo "  systemctl --user restart keld-agent.service             # Linux"
  fi
}

cmd="${1:-status}"

case "$cmd" in
  status)
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
    python3 - "$PROFILES/$name.json" "$ep" "$tok" <<'PY'
import json, os, sys
path, endpoint, token = sys.argv[1], sys.argv[2], sys.argv[3]
json.dump({"endpoint": endpoint, "ingest_token": token}, open(path, "w"), indent=2)
open(path, "a").write("\n")
os.chmod(path, 0o600)
PY
    echo "saved '$name' -> $ep   ($PROFILES/$name.json)"
    ;;

  use)
    name="${2:-}"
    [ -n "$name" ] || die "usage: $0 use <name>   (see: $0 list)"
    src="$PROFILES/$name.json"
    [ -f "$src" ] || die "no profile '$name' in $PROFILES — save one first with: $0 save $name"
    ep=$(read_field "$src" endpoint)
    tok=$(read_field "$src" ingest_token)
    [ -n "$ep" ] && [ -n "$tok" ] || die "profile '$name' is missing an endpoint or a token"
    write_hook "$ep" "$tok"
    echo "$KELD_HOME_DIR now points at $ep  (profile: $name)"
    restart_agent
    ;;

  set)
    url="${2:-}"
    [ -n "$url" ] || die "usage: $0 set <url> [token]   (token defaults to the one already there)"
    ep=$(normalise "$url")
    tok="${3:-$(read_field "$HOOK" ingest_token)}"
    [ -n "$tok" ] || die "no token: pass one, or run 'keld login && keld signal setup' against $ep"
    # ⚠️ Keeping the existing token across a HOST change is the failure this script exists to
    # prevent, so it is called out rather than silently allowed.
    old=$(read_field "$HOOK" endpoint)
    if [ -n "$old" ] && [ "$(normalise "$old")" != "$ep" ] && [ -z "${3:-}" ]; then
      echo "⚠️  keeping the token issued by $old while pointing at $ep."
      echo "    Tokens are issued per Atlas — if $ep rejects it, run 'keld login && keld signal setup'."
    fi
    write_hook "$ep" "$tok"
    echo "$KELD_HOME_DIR now points at $ep"
    restart_agent
    ;;

  -h|--help|help)
    sed -n '2,28p' "$0" | sed 's/^# \{0,1\}//'
    ;;

  *)
    die "unknown command '$cmd' (try: status, list, save, use, set, help)"
    ;;
esac
