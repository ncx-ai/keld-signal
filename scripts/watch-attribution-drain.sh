#!/bin/sh
# Watch a running keld-agent's attribution queue drain, once a second-ish.
#
#   scripts/watch-attribution-drain.sh            # the installed agent (~/.keld)
#   KELD_HOME=~/.keld-smoke scripts/watch-attribution-drain.sh
#
# One line per tick:
#
#   01:36:41 waiting=  0 running=False done= 11 collected= 11 hb_kills=0 quarantined=0 | batch~5.4s last_block=2.5s
#
#   waiting/running  the sidecar's queue right now — `running=False` with `waiting=0`
#                    is an idle queue, which is either "all done" or "nothing arrived".
#   done/collected   blocks encoded, and blocks the daemon has picked up. These track
#                    each other; `done` running ahead means results are waiting for the
#                    next 45 s sweep.
#   hb_kills         children killed for going silent (no batch for 60 s). Not slow —
#                    silent. A number here that climbs is the thing to chase.
#   quarantined      blocks retired after four genuine failures.
#   batch~/last_block  mean per-batch and last whole-block encode time.
#
# ⚠️ The sidecar's port CHANGES ON EVERY DAEMON START, so it is re-read from
# agent.json on every tick rather than once at launch — a restart mid-watch
# otherwise leaves this loop polling a port that no longer exists and printing
# "not answering" about a sidecar that is perfectly healthy.
PORT_OF() {
  python3 -c "import json,os;print(json.load(open(os.path.join(os.path.expanduser(os.environ.get('KELD_HOME','~/.keld')),'agent.json')))['sidecar_port'])" 2>/dev/null
}
echo "sidecar :$(PORT_OF)   (ctrl-C to stop)"
LAST=""
while true; do
  P=$(PORT_OF)
  if [ -n "$P" ] && [ "$P" != "$LAST" ]; then
    [ -n "$LAST" ] && echo "-- daemon restarted: sidecar now :$P (was :$LAST)"
    LAST="$P"
  fi
  BODY=$(curl -s --max-time 4 "http://127.0.0.1:$P/metrics")
  if [ -z "$BODY" ]; then
    echo "$(date +%H:%M:%S)  sidecar :$P not answering"
  else
    # A parse failure here is a metrics-shape change, not a dead sidecar — say which.
    printf '%s' "$BODY" | python3 -c "
import json,sys,time
d=json.load(sys.stdin); a=d.get('attribution') or {}; c=a.get('counts',{}); e=d.get('embed',{})
print(f\"{time.strftime('%H:%M:%S')}  waiting={a.get('waiting','-'):>3}  running={str(a.get('running','-')):<5}\"
      f\"  done={c.get('completed',0):>3}  collected={c.get('collected',0):>3}\"
      f\"  hb_kills={c.get('heartbeat_kills',0)}  quarantined={c.get('quarantined',0)}\"
      f\"  | batch~{e.get('mean_batch_ms',0)/1000:.1f}s  last_block={e.get('last_encode_ms',0)/1000:.1f}s\")
" || echo "$(date +%H:%M:%S)  answered, but /metrics did not parse — shape changed?"
  fi
  sleep 5
done
