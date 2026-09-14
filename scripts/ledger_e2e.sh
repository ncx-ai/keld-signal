#!/bin/bash
# ledger_e2e.sh <atlas-url> — the plan's T42, against a REAL Atlas dev instance.
#
# Generates a day of work, runs an ISOLATED v3 daemon with Send to Atlas ON
# against the given Atlas, and proves the whole chain from both ends:
#   generated corpus -> watcher -> worktree sidecar -> block cutter -> ledger
#   -> POST /v1/signal/blocks -> Atlas's own database holds the same block ids,
#   and the org's project values arrive on the settings poll.
#
# ⚠️ ISOLATION IS THE WHOLE POINT OF EVERY EXPORT BELOW.
#   - HOME and KELD_HOME both point at a temp dir: KELD_WATCH_ROOTS ADDS a root,
#     it does not replace the defaults, and the first run of the local-only
#     e2e measured the developer's real transcripts by accident.
#   - KELD_SIDECAR_BIN points at the WORKTREE sidecar: the installed frozen
#     binary is an older build with no per-block tokens.
#   - The ingest token is read from ~/.keld/endpoints/local.json, the file
#     `keld login` writes for the local dev Atlas. Nothing else in ~/.keld is
#     read or written.
#
# Read-back is through Postgres, not the /v1/blocks/* routes: those are
# admin-gated behind a user session and the daemon's ingest token cannot reach
# them — verified 2026-09-05. A dev e2e reading its own database is honest; a
# script that pasted a session cookie would not be.
#
# Usage: scripts/ledger_e2e.sh http://localhost:8000
set -u
ATLAS=${1:-http://localhost:8000}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
REAL_HOME=${REAL_HOME:-$HOME}
WORK=${WORK:-$(mktemp -d /tmp/keld-e2e.XXXXXX)}
HOME_DIR=$WORK/home; SIM=$WORK/sim; WS=$WORK/workspaces
mkdir -p "$HOME_DIR/state"
pass=0; fail=0
ok()   { pass=$((pass+1)); echo "  ok   $1"; }
bad()  { fail=$((fail+1)); echo "  FAIL $1"; }
PG=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -i postgres | head -1)
psqlq() { docker exec "$PG" psql -U keld -d keld -Atc "$1" 2>/dev/null; }

echo "== 0. preconditions"
TOK=$(python3 -c "import json;print(json.load(open('$REAL_HOME/.keld/endpoints/local.json'))['ingest_token'])" 2>/dev/null)
[ -n "$TOK" ] && ok "ingest token for the local Atlas found" || { bad "no ~/.keld/endpoints/local.json — run: keld login against $ATLAS"; exit 1; }
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "x-keld-ingest-token: $TOK" "$ATLAS/v1/enrichment-settings")
[ "$code" = "200" ] && ok "Atlas at $ATLAS accepts the token" || { bad "Atlas answered $code to the settings poll"; exit 1; }
[ -n "$PG" ] && ok "postgres container: $PG" || bad "no postgres container; read-back will be skipped"

echo "== 1. generate a day of work (real git checkouts, so repo resolves)"
python3 "$ROOT/scripts/blockgen/blockgen.py" --out "$SIM" --workspaces "$WS" --sessions 4 --days 1 --seed 42 >/dev/null && ok "generated" || { bad "generator failed"; exit 1; }
SESSIONS=$(find "$SIM" -name '*.jsonl' -exec basename {} .jsonl \; | sort)

echo "== 2. build"
( cd "$ROOT" && go build -o "$WORK/keld-agent" ./cmd/keld-agent ) && ok "daemon built" || { bad "build failed"; exit 1; }
printf '#!/bin/sh\nexec %s/bin/python %s/sidecar/serve.py "$@"\n' "$REAL_HOME/.keld/sidecar-venv" "$ROOT" > "$WORK/sidecar"; chmod +x "$WORK/sidecar"

echo "== 3. run the daemon, Send to Atlas ON, against $ATLAS"
cat > "$HOME_DIR/hook.json" <<JSON
{"ingest_token":"$TOK","endpoint":"$ATLAS"}
JSON
export HOME="$HOME_DIR" KELD_HOME="$HOME_DIR"
export KELD_ATLAS=1 KELD_BLOCKS=1 KELD_BLOCKS_INTERVAL=15s KELD_BLOCKS_BACKFILL=1
export KELD_WATCH_ROOTS="claude_code:$SIM" KELD_WATCH_POLL=2s KELD_WATCH_BACKFILL=1
export KELD_TELEMETRY_PORT=14997 KELD_SIDECAR_BIN="$WORK/sidecar" KELD_SETTINGS_POLL=10s
"$WORK/keld-agent" run > "$WORK/daemon.log" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null; sleep 1; pkill -P $PID 2>/dev/null; true' EXIT
for i in $(seq 1 60); do [ -f "$HOME_DIR/agent.json" ] && break; sleep 1; done
[ -f "$HOME_DIR/agent.json" ] && ok "daemon up" || { bad "no agent.json"; tail -20 "$WORK/daemon.log"; exit 1; }
PORT=$(python3 -c "import json;print(json.load(open('$HOME_DIR/agent.json'))['port'])")
SECRET=$(python3 -c "import json;print(json.load(open('$HOME_DIR/agent.json'))['secret'])")
ledger() { curl -s -H "x-keld-agent-secret: $SECRET" "http://127.0.0.1:$PORT/v1/ledger"; }
projects() { curl -s -H "x-keld-agent-secret: $SECRET" "http://127.0.0.1:$PORT/v1/projects"; }

echo "== 4. wait for blocks to be cut AND received by Atlas (≤ 4 min)"
for i in $(seq 1 120); do
  N=$(ledger | python3 -c "import json,sys
d=json.load(sys.stdin); print(sum(1 for b in d['blocks'] if b['cells'].get('received',{}).get('status')=='ok'))" 2>/dev/null)
  [ "${N:-0}" -gt 0 ] && break; sleep 2
done
ledger > "$WORK/ledger.json"
python3 - "$WORK/ledger.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1])); bl=d['blocks']
rec=[b for b in bl if b['cells'].get('received',{}).get('status')=='ok']
sent=[b for b in bl if b['cells'].get('sent',{}).get('status')=='ok']
meas=[b for b in bl if b['cells'].get('measured',{}).get('status')=='ok' and (b['cells']['measured'].get('tokens') or {}).get('input',0)>0]
print(f"  blocks={len(bl)} sent_ok={len(sent)} received_ok={len(rec)} measured_with_tokens={len(meas)}")
h={x['key']:(x['status'],x.get('detail','')) for x in d['health']}
print("  health:", h)
open(sys.argv[1]+'.ids','w').write("\n".join(f"{b['key']['session']} {b['key']['start']}" for b in rec))
PY
NREC=$(grep -c . "$WORK/ledger.json.ids")
[ "$NREC" -gt 0 ] && ok "$NREC block(s) cut, measured, sent and RECEIVED (Atlas answered)" || bad "no block reached received=ok";

echo "== 5. read the same blocks back from Atlas's database"
if [ -n "$PG" ] && [ "$NREC" -gt 0 ]; then
  miss=0
  while read -r sess start; do
    n=$(psqlq "select count(*) from blocks where session_id='$sess' and extract(epoch from start_ts)::bigint=$start")
    [ "${n:-0}" -ge 1 ] || { miss=$((miss+1)); echo "     missing in Atlas: $sess@$start"; }
  done < "$WORK/ledger.json.ids"
  [ "$miss" -eq 0 ] && ok "every received block exists in Atlas's blocks table (session, start)" || bad "$miss block(s) the ledger says Atlas received are not in its database"
else
  echo "  skip read-back"
fi

echo "== 6. the org's projects arrived on the poll, and attribution behaved"
projects > "$WORK/projects.json"
python3 - "$WORK/projects.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
ps=d.get('projects',[]); sug=d.get('suggestions',[]); cov=d.get('coverage',{})
atlas=[p for p in ps if (p.get('origin') or p.get('project',{}).get('origin'))=='atlas'] if ps else []
print(f"  projects={len(ps)} from_atlas={len(atlas)} suggestions={len(sug)} coverage={cov}")
for s in sug[:6]: print("   suggestion:", s.get('kind'), s.get('value'), s.get('blocks'), 'blocks')
PY
NP=$(python3 -c "import json;print(len(json.load(open('$WORK/projects.json')).get('projects',[])))")
[ "$NP" -gt 0 ] && ok "org projects present ($NP)" || bad "no org projects — the settings poll did not populate the vocabulary"
NS=$(python3 -c "import json;print(len(json.load(open('$WORK/projects.json')).get('suggestions',[])))")
[ "$NS" -gt 0 ] && ok "suggestions exist for unattributed work ($NS) — expected: the org's values carry no repo tags yet" || bad "no suggestions"
grep -q 'repo' <(python3 -c "import json;print([s.get('kind') for s in json.load(open('$WORK/projects.json')).get('suggestions',[])])") && ok "a suggestion is keyed by REPOSITORY (the real checkout resolved)" || bad "no repo-keyed suggestion; workspace fallback only"

echo "== 7. pairing a second host via /v1/config"
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST -H "x-keld-agent-secret: $SECRET" -H 'content-type: application/json' -d '{"code":"not a code"}' "http://127.0.0.1:$PORT/v1/config")
case "$code" in
  400) ok "/v1/config refuses a malformed code with 400" ;;
  404) echo "  pending: /v1/config not mounted yet (lane B2)" ;;
  *)   bad "/v1/config answered $code to a malformed code" ;;
esac

echo "== result: $pass ok, $fail failed  (work dir: $WORK)"
[ "$fail" -eq 0 ]
