#!/usr/bin/env python3
"""E3 + E4: which UNIT is coherent enough to characterise — a session, a 20-minute block, or
a subagent run?

A unit is worth characterising only if its contents are internally consistent. If a unit is a
grab-bag, any single label for it is a lie and the honest output is a distribution -- which is
fine, but it means the unit is a CONTAINER, not a thing with a kind.

Coherence is measured as the share of a unit's requests falling in its own largest class
(1.0 = every request the same, 1/9 = uniform across the nine classes). Reported against the
POOLED mix as a floor: a unit type whose coherence merely matches the corpus average is
telling you nothing the corpus average did not.
"""
import json, os, re, glob, collections, statistics as st, datetime as dt
HERE=os.path.dirname(os.path.abspath(__file__))
src=open(os.path.join(HERE,"request_route_vocab.py")).read()
ns={}; exec(compile(src[:src.index("files=[]")],"rrv","exec"), ns)
rc=ns["route_class"]
OBS=re.compile(r"\[MESSAGE FROM NON-USER SOURCE\]|<observed_from_primary_session>")
SELF={"13a58628-4f19-4ac9-9423-d669c717f259","d58162e2-bb14-40b5-afd9-2c55e403fe99"}
BIN=300; MAXB=20*60; IDLE=3

def reqs_of(path):
    out={}
    for l in open(path, errors="ignore"):
        try: d=json.loads(l)
        except: continue
        if d.get("type")!="assistant": continue
        rid=d.get("requestId")
        if not rid: continue
        m=d.get("message",{}); u=m.get("usage") or {}
        q=out.setdefault(rid,{"tools":[],"text":"","think":0,"out":0,"ts":d.get("timestamp")})
        q["out"]=max(q["out"], u.get("output_tokens",0) or 0)
        for c in m.get("content",[]):
            t=c.get("type")
            if t=="tool_use": q["tools"].append((c.get("name") or "", c.get("input") or {}))
            elif t=="text":   q["text"]+=(c.get("text","") or "")
            elif t=="thinking": q["think"]+=len(c.get("thinking","") or "")
    return out

# ⚠️ COHERENCE IS CONFOUNDED WITH SIZE and the naive table is misleading: a unit holding one
# request is coherent 1.0 by arithmetic, and corpus B's sessions have a MEDIAN OF 4 requests.
# The control is a size-matched shuffle -- reassign every request to a random unit while
# preserving each unit's size, then recompute. A real unit must beat its own shuffled twin;
# anything that does not is reporting its own smallness.
def shuffled_like(units, allcls, seed=7):
    import random
    rnd=random.Random(seed); pool=list(allcls); rnd.shuffle(pool); out=[]; i=0
    for m in units:
        n=sum(m.values())
        out.append(collections.Counter(pool[i:i+n])); i+=n
    return out

def coherence(mix):
    t=sum(mix.values())
    return max(mix.values())/t if t else 0.0

def survey(root, label):
    fs=glob.glob(os.path.join(root,"**","*.jsonl"), recursive=True)
    agent=[f for f in fs if os.path.basename(f).startswith("agent-")]
    sess=[]
    for f in fs:
        if os.path.basename(f).startswith("agent-") or os.path.basename(f)[:-6] in SELF: continue
        tot=obs=0
        for l in open(f,errors="ignore"):
            tot+=1
            if OBS.search(l): obs+=1
        if tot and obs/tot<=0.20: sess.append(f)
    units={"session":[], "block (20 min)":[], "subagent run":[]}
    pool=collections.Counter()
    for f in sess:
        R=reqs_of(f)
        if not R: continue
        pool.update(rc(q) for q in R.values())
        units["session"].append(collections.Counter(rc(q) for q in R.values()))
        # cut into blocks the way the shipped cutter does
        stamped=[(dt.datetime.fromisoformat(q["ts"].replace("Z","+00:00")).timestamp(), q)
                 for q in R.values() if q.get("ts")]
        if not stamped: continue
        stamped.sort()
        bins=sorted({int(t)//BIN for t,_ in stamped})
        groups=[]; cur=[bins[0]]
        for b in bins[1:]:
            if (b-cur[0])*BIN >= MAXB or (b-cur[-1]) >= IDLE: groups.append(cur); cur=[b]
            else: cur.append(b)
        groups.append(cur)
        for g in groups:
            lo,hi=g[0]*BIN, (g[-1]+1)*BIN
            m=collections.Counter(rc(q) for t,q in stamped if lo<=t<hi)
            if m: units["block (20 min)"].append(m)
    for f in agent:
        R=reqs_of(f)
        if R:
            pool.update(rc(q) for q in R.values())
            units["subagent run"].append(collections.Counter(rc(q) for q in R.values()))
    allcls=list(pool.elements())
    print(f"\n=== {label} ===")
    print(f"  {'unit':16s} {'count':>6s} {'med reqs':>9s} {'coherence':>10s} {'SHUFFLED':>9s} {'REAL GAIN':>10s}")
    for k,v in units.items():
        if not v: continue
        co=[coherence(m) for m in v]; nn=[sum(m.values()) for m in v]
        sh=[coherence(m) for m in shuffled_like(v, allcls)]
        g=st.median(co)-st.median(sh)
        flag="" if g>0.05 else "   <- no better than its own size-matched shuffle"
        print(f"  {k:16s} {len(v):6d} {st.median(nn):9.0f} {st.median(co):10.2f} "
              f"{st.median(sh):9.2f} {g:+10.2f}{flag}")
    # ⚠️ Does coherence SURVIVE AT SIZE? A shuffle controls the arithmetic but not the fact
    # that a 4-request session is a different kind of thing from a 200-request one. If the
    # gain lives only in the small band, the finding is "small units are coherent", which is
    # a tautology dressed as a result.
    print(f"\n  {'unit':16s} {'band':>12s} {'n':>5s} {'coherence':>10s} {'shuffled':>9s} {'gain':>7s}")
    for k,v in units.items():
        if not v: continue
        for lo,hi,nm in ((1,5,"1-5 req"),(6,20,"6-20 req"),(21,10**9,"21+ req")):
            sel=[m for m in v if lo<=sum(m.values())<=hi]
            if len(sel)<8: continue
            co=st.median([coherence(m) for m in sel])
            sh=st.median([coherence(m) for m in shuffled_like(sel, allcls)])
            print(f"  {k:16s} {nm:>12s} {len(sel):5d} {co:10.2f} {sh:9.2f} {co-sh:+7.2f}")
    return units

for var,lab in (("KELD_CORPUS_A","CORPUS A"),("KELD_CORPUS_B","CORPUS B")):
    r=os.environ.get(var,"")
    if r: survey(os.path.expanduser(r), lab)
