#!/usr/bin/env python3
"""Build the verb/family labelling frame from the colleague's real Claude Code transcripts.

REFRAME (2026-09-28, repo owner): the investigation targets VERB and FAMILY only. Context/domain
is deferred — it could not be validated by any available route (synthesis: generators disagree
2/3; public chat data: the work is not in it).

⚠️ 146 of 218 files are EXCLUDED as observer sessions — an agent's meta-transcripts, every turn
`[MESSAGE FROM NON-USER SOURCE]` / `<observed_from_primary_session>`. They are not human work
and would be a fifth of the corpus. Rule: drop a file if >20% of its turns match that marker.

⚠️ SAMPLING IS DELIBERATELY STRATIFIED, NOT RANDOM. `keld-website` is oversampled because it is
the only source of `language`-family work (Notion docs, publishing, review); a proportional
sample would be ~85% `code`/`agentic` and could not measure the family axis at all. This is
recorded so the distribution is never read as the population's.
"""
import json, os, re, glob, random, collections, datetime as dt

D=os.path.expanduser("~/keld/john-projects/projects")
OUT="/tmp/claude-1000/vf"
SPAN,STRIDE=60,50
BUDGET=1400
FENCE=re.compile(r"```.*?```", re.S)
OBS=re.compile(r"MESSAGE FROM NON-USER SOURCE|observed_from_primary_session")
SENT=re.compile(r"(?<![0-9A-Z])[.!?]+\s+(?=[A-Z\"'(\[])")

# how many windows to draw from each project group
QUOTA=[("-Users-johnluther-keld-keld-website", 20),   # language family lives here
       ("-Users-johnluther-keld",              30),
       ("*",                                   10)]

def text_of(c):
    if isinstance(c,str): return c
    if not isinstance(c,list): return ""
    return "\n".join(b.get("text","") for b in c if isinstance(b,dict) and b.get("type")=="text")

def bound(t,n):
    t=" ".join(t.split())
    if len(t)<=n: return t
    cut=t[:n]; i=max(cut.rfind(". "),cut.rfind("! "),cut.rfind("? "))
    return (cut[:i+1] if i>n*0.4 else cut)+f" [... {len(t)-n} chars omitted]"

def load(f):
    turns=[]; obs=0; n=0
    for line in open(f, errors="ignore"):
        try: o=json.loads(line)
        except Exception: continue
        if o.get("type") not in ("user","assistant") or not o.get("timestamp"): continue
        raw=text_of((o.get("message") or {}).get("content")) or ""
        n+=1
        if OBS.search(raw): obs+=1
        t=re.sub(r"\s+"," ",FENCE.sub(" ",raw)).strip()
        if not t or t.startswith(("<","Base directory","Caveat:")): continue
        turns.append((dt.datetime.fromisoformat(o["timestamp"].replace("Z","+00:00")),
                      "USER" if o["type"]=="user" else "ASSISTANT", t))
    if n and obs/n>0.2: return None
    turns.sort(key=lambda x:x[0])
    return turns

def windows_of(f, turns):
    out=[]; start=turns[0][0]; tN=turns[-1][0]
    while start<tN:
        sl=[x for x in turns if start<=x[0]<start+dt.timedelta(minutes=SPAN)]
        here,start=start,start+dt.timedelta(minutes=STRIDE)
        if not sl: continue
        prompts=[bound(t,700) for r,t in ((x[1],x[2]) for x in sl) if r=="USER"]
        prose=[bound(t,400) for r,t in ((x[1],x[2]) for x in sl) if r=="ASSISTANT"]
        if not prompts and not prose: continue
        out.append({"file":f,"proj":os.path.basename(os.path.dirname(f)),
                    "start":here.isoformat(),"prompts":prompts,"prose":prose,
                    "turns":[(x[1],x[2]) for x in sl]})
    return out

pools=collections.defaultdict(list)
for f in sorted(glob.glob(os.path.join(D,"*","*.jsonl"))):
    turns=load(f)
    if not turns: continue
    proj=os.path.basename(os.path.dirname(f))
    for w in windows_of(f,turns):
        if sum(len(p) for p in w["prompts"]+w["prose"])<600: continue
        pools[proj].append(w)

random.seed(0); picked=[]
used=set()
for key,q in QUOTA:
    if key=="*":
        pool=[w for p,ws in pools.items() if p not in used for w in ws]
    else:
        pool=pools.get(key,[]); used.add(key)
    random.shuffle(pool)
    picked += pool[:q]
random.shuffle(picked)
for i,w in enumerate(picked,1): w["id"]=f"V{i:03d}"

os.makedirs(OUT, exist_ok=True)
json.dump(picked, open(f"{OUT}/frame.json","w"), indent=1)
print(f"windows available per project:")
for p,ws in sorted(pools.items(), key=lambda kv:-len(kv[1])): print(f"   {p[:52]:52s} {len(ws)}")
print(f"\npicked {len(picked)} windows")
print("  by project:", collections.Counter(w['proj'][-28:] for w in picked).most_common())

with open(f"{OUT}/blind_views.txt","w") as fh:
    for w in picked:
        fh.write("="*100+f"\n[{w['id']}]  prompts={len(w['prompts'])} assistant_turns={len(w['prose'])}\n"+"="*100+"\n\n")
        for p in w["prompts"]: fh.write(f"USER: {p}\n\n")
        if w["prose"]:
            fh.write("--- assistant prose ---\n")
            for p in w["prose"]: fh.write(f"* {p}\n")
        fh.write("\n")
print(f"wrote {OUT}/blind_views.txt ({os.path.getsize(OUT+'/blind_views.txt')} bytes)")
