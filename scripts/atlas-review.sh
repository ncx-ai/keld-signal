#!/usr/bin/env bash
# atlas-review.sh — is Signal still sending Atlas what Atlas expects?
#
# Reads the LOCAL Atlas database and compares today against previous days, per
# source and per column Atlas actually prices or joins on, then reconciles
# today's rows against the transcripts on THIS machine (what SHOULD have been
# sent). Run it whenever the capture path changes (tool OTLP on/off, a new
# mirror, a new release) — it is the check that would have caught the
# 2026-09-20 outage (zero rows all day) and the 2026-09-21 per-line prompt
# double-count.
#
# Usage:  scripts/atlas-review.sh [--days N] [--date YYYY-MM-DD] [--tz ZONE]
#   ATLAS_PSQL   how to reach psql (default: docker exec keld-atlas-postgres-1 psql -U keld -d keld)
#   KELD_LOG     daemon log (default ~/.keld/logs/agent.err.log)
#
# Everything here is read-only. Exit code is 1 if any check is FAIL, else 0.
set -euo pipefail

DAYS=9; DATE="$(date +%Y-%m-%d)"; TZ_NAME="${TZ_NAME:-Europe/Amsterdam}"
while [ $# -gt 0 ]; do case "$1" in
  --days) DAYS="$2"; shift 2;; --date) DATE="$2"; shift 2;; --tz) TZ_NAME="$2"; shift 2;;
  -h|--help) sed -n '2,16p' "$0"; exit 0;; *) echo "unknown arg $1" >&2; exit 2;; esac; done
PSQL="${ATLAS_PSQL:-docker exec keld-atlas-postgres-1 psql -U keld -d keld}"
KELD_LOG="${KELD_LOG:-$HOME/.keld/logs/agent.err.log}"
FAILS=0; WARNS=0
q()  { $PSQL -At -F $'\t' -c "$1"; }
tbl(){ column -t -s $'\t'; }
hdr(){ printf '\n\033[1m== %s ==\033[0m\n' "$1"; }
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$1"; }
warn(){ printf '  \033[33mWARN\033[0m %s\n' "$1"; WARNS=$((WARNS+1)); }
fail(){ printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAILS=$((FAILS+1)); }
D="to_char(event_ts at time zone '$TZ_NAME','MM-DD')"
TODAY_MMDD="$(date -j -f %Y-%m-%d "$DATE" +%m-%d 2>/dev/null || date -d "$DATE" +%m-%d)"
SINCE="('$DATE'::date - interval '$DAYS days')"
UNTIL="('$DATE'::date + interval '1 day')"
W="event_ts >= $SINCE and event_ts < $UNTIL"

echo "Atlas review for $DATE (last $DAYS days, tz $TZ_NAME) — $(date '+%H:%M:%S')"

# ---------------------------------------------------------------- A. health
hdr "A. Ingest health"
if grep -q "PAIRED with" "$KELD_LOG" 2>/dev/null; then
  ok "daemon: $(grep 'PAIRED with' "$KELD_LOG" | tail -1 | sed 's/ — the senders.*//' | cut -c1-90)"
else warn "daemon log has no PAIRED line ($KELD_LOG)"; fi
if docker ps --format '{{.Names}}' 2>/dev/null | grep -q keld-atlas-ingest-consumer-1; then
  n=$(docker logs keld-atlas-ingest-consumer-1 --since "${DATE}T00:00:00Z" 2>&1 | grep -ci 'traceback\|exception' || true)
  last=$(docker logs keld-atlas-ingest-consumer-1 --since "${DATE}T00:00:00Z" --timestamps 2>&1 | grep -i 'exception\|error:' | tail -1 | awk '{print $1}')
  if [ "$n" = 0 ]; then ok "atlas ingest consumer: no exceptions today"; else warn "atlas ingest consumer: $n exception lines today (last $last) — rows in that window may be lost"; fi
fi
zero=$(q "select count(*) from tool_events where event_ts >= '$DATE'::date and event_ts < $UNTIL")
if [ "$zero" = 0 ]; then fail "ZERO tool_events rows for $DATE — nothing is arriving"; else ok "$zero tool_events rows for $DATE"; fi

# ---------------------------------------------------------------- B. rows/day
hdr "B. Rows per day × source × event"
(printf 'day\tsource\tevent\trows\n'; q "select $D, source, event_name, count(*) from tool_events where $W group by 1,2,3 order by 1,2,3") | tbl
missing_days=$(q "select string_agg(d::date::text, ' ') from generate_series($SINCE::date, '$DATE'::date - 1, '1 day') d where not exists (select 1 from tool_events where $D = to_char(d,'MM-DD'))")
[ -n "$missing_days" ] && warn "days with NO rows at all: $missing_days (compare against blocks in G — work happened if blocks exist)"

# ---------------------------------------------------------------- C. population
hdr "C. Column population on PRICED events (% non-null) — the columns Atlas prices/joins on"
echo "   claude_code api_request · codex sse_event · gemini_cli api_response"
(printf 'day\tsource\trows\tmodel\ttokens\tcost\tsession\trequest\tprompt\tprincipal\tduration\tappver\tdedup_uniq\n'
q "select $D, source, count(*),
 round(100.0*count(model)/count(*)), round(100.0*count(*) filter (where input_tokens>0 or output_tokens>0 or cache_read_tokens>0)/count(*)),
 round(100.0*count(*) filter (where cost_usd>0)/count(*)), round(100.0*count(session_id)/count(*)), round(100.0*count(request_id)/count(*)),
 round(100.0*count(prompt_id)/count(*)), round(100.0*count(principal)/count(*)), round(100.0*count(duration_ms)/count(*)), round(100.0*count(app_version)/count(*)),
 round(100.0*count(distinct dedup_key)/count(*))
 from tool_events where $W and ((source in ('claude_code','cowork') and event_name='api_request') or (source='codex' and event_name='sse_event') or (source='gemini_cli' and event_name='api_response'))
 group by 1,2 order by 2,1") | tbl
# verdicts for today
while IFS=$'\t' read -r src n model tok cost sess req dd; do
  [ -z "$src" ] && continue
  [ "$model" -lt 100 ] && fail "$src today: model on $model% of priced rows"
  [ "$tok" -lt 95 ] && fail "$src today: tokens on $tok% of priced rows"
  [ "$cost" -lt 95 ] && fail "$src today: cost on $cost% — pricing gap (model_price?)"
  [ "$sess" -lt 100 ] && fail "$src today: session_id on $sess%"
  [ "$req" -lt 95 ] && warn "$src today: request_id on $req% (dedup falls back)"
  [ "$dd" -lt 100 ] && fail "$src today: dedup_key not unique ($dd%) — rows are colliding"
  ok "$src today: $n priced rows, model $model% tokens $tok% cost $cost% session $sess% request $req%"
done < <(q "select source, count(*), round(100.0*count(model)/count(*)), round(100.0*count(*) filter (where input_tokens>0 or output_tokens>0 or cache_read_tokens>0)/count(*)),
 round(100.0*count(*) filter (where cost_usd>0)/count(*)), round(100.0*count(session_id)/count(*)), round(100.0*count(request_id)/count(*)), round(100.0*count(distinct dedup_key)/count(*))
 from tool_events where event_ts >= '$DATE'::date and event_ts < $UNTIL and ((source in ('claude_code','cowork') and event_name='api_request') or (source='codex' and event_name='sse_event') or (source='gemini_cli' and event_name='api_response')) group by 1")

# ---------------------------------------------------------------- D. reconcile vs disk (today only)
hdr "D. Reconciliation against transcripts on THIS machine ($DATE)"
first_start=$(grep "listening on 127.0.0.1" "$KELD_LOG" 2>/dev/null | grep "$(echo "$DATE" | tr - /)" | head -1 | awk '{print $2}')
echo "   first daemon start today: ${first_start:-unknown} ($TZ_NAME) — lines before it are not the mirror's to send"
# Codex: advancing token_count records per rollout == rows in Atlas
cdir="$HOME/.codex/sessions/$(echo "$DATE" | tr - /)"
if ls "$cdir"/rollout-*.jsonl >/dev/null 2>&1; then
  exp=0; for f in "$cdir"/rollout-*.jsonl; do
    n=$(grep '"token_count"' "$f" | python3 -c '
import sys,json; last=None; n=0
for l in sys.stdin:
    d=json.loads(l); info=(d.get("payload",d).get("info") or {})
    if not info.get("last_token_usage"): continue
    t=(info.get("total_token_usage") or {}).get("total_tokens",0)
    if t!=0 and t==last: continue
    if t!=0: last=t
    n+=1
print(n)'); exp=$((exp+n)); done
  act=$(q "select count(*) from tool_events where source='codex' and event_name='sse_event' and event_ts >= '$DATE'::date and event_ts < $UNTIL")
  if [ "$exp" = "$act" ]; then ok "codex: $act rows == $exp advancing token_count records across $(ls "$cdir"/rollout-*.jsonl | wc -l | tr -d ' ') rollouts (sub-agents group under the parent session_id)"
  elif [ "$act" -lt "$exp" ]; then warn "codex: $act rows in Atlas vs $exp expected on disk (missing $((exp-act)); records before the first daemon start today are expected losses)"
  else fail "codex: $act rows in Atlas vs $exp expected — MORE than on disk, duplicates"; fi
else echo "   codex: no rollouts on disk for $DATE"; fi
# Claude Code: distinct requestIds with usage after first start, per 5-min bucket vs Atlas
q "select to_char(date_trunc('minute', event_ts) - (extract(minute from event_ts)::int % 5) * interval '1 minute','HH24:MI'), count(*) from tool_events where source='claude_code' and event_name='api_request' and event_ts >= '$DATE'::date and event_ts < $UNTIL group by 1 order by 1" > /tmp/atlas_review_buckets.txt
python3 - "$DATE" "$first_start" "$TZ_NAME" <<'PY'
import sys,json,glob,os,datetime,collections,zoneinfo
date,start,tzn=sys.argv[1],sys.argv[2],sys.argv[3]
tz=zoneinfo.ZoneInfo(tzn); day=datetime.date.fromisoformat(date)
s=datetime.datetime.combine(day, datetime.time.fromisoformat(start), tz).astimezone(datetime.timezone.utc) if start else None
lo=datetime.datetime.combine(day, datetime.time(), tz).astimezone(datetime.timezone.utc); hi=lo+datetime.timedelta(days=1)
exp=collections.Counter(); seen=set(); prompts=collections.Counter(); files=0
for f in glob.glob(os.path.expanduser('~/.claude/projects/*/*.jsonl')):
    if os.path.basename(f).startswith('agent-'): continue
    if datetime.datetime.fromtimestamp(os.path.getmtime(f), datetime.timezone.utc) < lo: continue
    files+=1
    for line in open(f, errors='replace'):
        try: d=json.loads(line)
        except: continue
        ts=d.get('timestamp')
        if not ts: continue
        at=datetime.datetime.fromisoformat(ts.replace('Z','+00:00'))
        if at<lo or at>=hi or (s and at<s): continue
        if d.get('type')=='assistant' and d.get('requestId') and (d.get('message') or {}).get('usage') and d['requestId'] not in seen:
            seen.add(d['requestId']); exp[at.strftime('%H:%M')[:3]+f"{(at.minute//5)*5:02d}"]+=1
        if d.get('type')=='user' and d.get('promptId') and not d.get('isSidechain') and not d.get('isMeta'):
            c=(d.get('message') or {}).get('content')
            if isinstance(c,list) and any(isinstance(b,dict) and b.get('type')=='tool_result' for b in c): continue
            prompts[d['promptId']]+=1
atlas={l.split('\t')[0]:int(l.split('\t')[1]) for l in open('/tmp/atlas_review_buckets.txt') if '\t' in l}
E=sum(v for b,v in exp.items()); A=sum(v for b,v in atlas.items() if (not s) or b>=s.strftime('%H:%M'))
holes=[(b,e,atlas.get(b,0)) for b,e in sorted(exp.items()) if atlas.get(b,0)<e]
print(f"   claude_code: {A} api_request rows in Atlas vs {E} distinct requestIds with usage on disk after first start ({files} live transcripts)")
if holes:
    print("   buckets (UTC) where Atlas has FEWER than the transcript:")
    for b,e,a in holes: print(f"     {b}  expected {e:4}  atlas {a:4}  diff {a-e:5}")
    tail=holes[-1][0]
    print("   (the last bucket is usually the live tail — 5s poll + batch flush)")
print(f"   claude_code: {len(prompts)} genuine promptIds on disk after first start; {sum(prompts.values())} user lines carry them")
PY

# ---------------------------------------------------------------- E. duplicates
hdr "E. Duplicate usage rows ($DATE)"
dup=$(q "select count(*) from (select attributes->>'prompt.id' from tool_events where source='claude_code' and event_name='user_prompt' and event_ts >= '$DATE'::date and event_ts < $UNTIL group by 1 having count(*)>1) x")
tot=$(q "select count(*) from tool_events where source='claude_code' and event_name='user_prompt' and event_ts >= '$DATE'::date and event_ts < $UNTIL")
dist=$(q "select count(distinct attributes->>'prompt.id') from tool_events where source='claude_code' and event_name='user_prompt' and event_ts >= '$DATE'::date and event_ts < $UNTIL")
if [ "$dup" = 0 ]; then ok "claude_code user_prompt: $tot rows, $dist distinct prompt.ids"; else fail "claude_code user_prompt: $tot rows for $dist distinct prompt.ids — $dup prompts counted more than once (Atlas 'prompts' KPI over-counts)"; fi
dupr=$(q "select count(*) from (select request_id from tool_events where event_name in ('api_request','sse_event','api_response') and request_id is not null and event_ts >= '$DATE'::date and event_ts < $UNTIL group by source, request_id having count(*)>1) x")
if [ "$dupr" = 0 ]; then ok "priced rows: no request_id appears twice"; else fail "priced rows: $dupr request_ids appear more than once — spend is double-counted"; fi

# ---------------------------------------------------------------- F. cost sanity
hdr "F. Cost sanity — \$ per million tokens by model (should be stable day to day)"
(printf 'day\tsource\tmodel\tcalls\tavg_tok/call\tusd\tusd_per_Mtok\n'
q "select $D, source, model, count(*), round(avg(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens)), round(sum(cost_usd)::numeric,2),
 round((1e6*sum(cost_usd)/nullif(sum(input_tokens+output_tokens+cache_read_tokens+cache_creation_tokens),0))::numeric,3)
 from tool_events where $W and event_name in ('api_request','sse_event','api_response') and (input_tokens>0 or output_tokens>0 or cache_read_tokens>0)
 group by 1,2,3 order by 3,1") | tbl
unpriced=$(q "select string_agg(distinct model, ', ') from tool_events where event_ts >= '$DATE'::date and event_ts < $UNTIL and event_name in ('api_request','sse_event','api_response') and (input_tokens>0 or output_tokens>0) and cost_usd=0")
[ -n "$unpriced" ] && fail "UNPRICED models today (tokens but \$0): $unpriced — add to model_price"

# ---------------------------------------------------------------- G. blocks + enrichments
hdr "G. Blocks and enrichments per day"
(printf 'day\tsource\tblocks\twith_dims\twith_effort\tends\n'
q "select to_char(start_ts at time zone '$TZ_NAME','MM-DD'), source_id, count(*), count(*) filter (where dimensions::text<>'{}'), count(*) filter (where effort::text<>'{}'), string_agg(distinct end_reason, ',')
 from blocks where start_ts >= $SINCE and start_ts < $UNTIL group by 1,2 order by 1,2") | tbl
(printf 'day\tsource\tenrichments\tjoined_to_usage\tstatus\n'
q "select to_char(ts at time zone '$TZ_NAME','MM-DD'), source_id, count(*), count(*) filter (where exists (select 1 from tool_events te where te.prompt_id=e.corr_id and te.org_id=e.org_id)), string_agg(distinct pipeline_status, ',')
 from enrichments e where ts >= $SINCE and ts < $UNTIL group by 1,2 order by 1,2") | tbl
echo "   (codex enrichments never join: Atlas stores prompt_id=NULL for codex by design)"

# ---------------------------------------------------------------- H. attribute keys lost/gained
hdr "H. Attribute keys on priced events: today vs the most recent earlier day with rows"
for src in claude_code codex; do
  ev=$([ "$src" = codex ] && echo sse_event || echo api_request)
  ref=$(q "select max($D) from tool_events where source='$src' and event_name='$ev' and $W and $D < '$TODAY_MMDD'")
  [ -z "$ref" ] && { echo "   $src: no earlier day in window to compare"; continue; }
  lost=$(q "with k as (select $D d, jsonb_object_keys(attributes) k from tool_events where source='$src' and event_name='$ev' and $W and (input_tokens>0 or output_tokens>0))
    select string_agg(k, ' ') from (select k from k group by k having count(*) filter (where d='$ref')>0 and count(*) filter (where d='$TODAY_MMDD')=0 order by k) x")
  gained=$(q "with k as (select $D d, jsonb_object_keys(attributes) k from tool_events where source='$src' and event_name='$ev' and $W and (input_tokens>0 or output_tokens>0))
    select string_agg(k, ' ') from (select k from k group by k having count(*) filter (where d='$ref')=0 and count(*) filter (where d='$TODAY_MMDD')>0 order by k) x")
  echo "   $src ($ev) vs $ref:"; echo "     lost:   ${lost:--}"; echo "     gained: ${gained:--}"
done
echo "   Expected losses with tool OTLP off: identity (user.*, organization.id → 'principal' comes from the ingest token),"
echo "   duration_ms/ttft_ms (not in transcripts), terminal.type, query_source, event.sequence (deliberate: request_id is the key)."

# ---------------------------------------------------------------- verdict
hdr "Verdict"
echo "  $FAILS FAIL · $WARNS WARN"
[ "$FAILS" = 0 ]
