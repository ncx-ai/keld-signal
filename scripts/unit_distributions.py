#!/usr/bin/env python3
"""Do unit DISTRIBUTIONS carry information? (Not: can a unit be labelled — that is settled and
was the wrong question.)

A distribution is useful when units DIFFER from each other in a way that is not noise. Two
things to establish, in order:

  1. DISTINCTIVENESS -- is a unit's mix further from the corpus average than a size-matched
     shuffle of the same requests? If not, reporting it tells a reader nothing the corpus
     average did not already say.
  2. SHAPE -- do the distributions fall into recognisable recurring profiles, or is every
     unit its own snowflake? A handful of profiles is a product; 300 snowflakes is a table.

⚠️ Size is the confound throughout: a small unit is far from any average by arithmetic. Every
number here is against a size-matched shuffle, never a raw baseline.
"""
import json, os, re, glob, collections, statistics as st, datetime as dt, random
HERE=os.path.dirname(os.path.abspath(__file__))
src=open(os.path.join(HERE,"request_route_vocab.py")).read()
ns={}; exec(compile(src[:src.index("files=[]")],"rrv","exec"), ns)
rc=ns["route_class"]
OBS=re.compile(r"\[MESSAGE FROM NON-USER SOURCE\]|<observed_from_primary_session>")
SELF={"13a58628-4f19-4ac9-9423-d669c717f259","d58162e2-bb14-40b5-afd9-2c55e403fe99"}
CL=["retrieve","author_code","operate","author_prose","synthesize","verify","delegate","acknowledge","unclassified"]
BIN=300; MAXB=20*60; IDLE=3

def reqs_of(p):
    out={}
    for l in open(p,errors="ignore"):
        try: d=json.loads(l)
        except: continue
        if d.get("type")!="assistant": continue
        rid=d.get("requestId")
        if not rid: continue
        m=d.get("message",{}); u=m.get("usage") or {}
        q=out.setdefault(rid,{"tools":[],"text":"","think":0,"out":0,"ts":d.get("timestamp")})
        q["out"]=max(q["out"],u.get("output_tokens",0) or 0)
        for c in m.get("content",[]):
            t=c.get("type")
            if t=="tool_use": q["tools"].append((c.get("name") or "",c.get("input") or {}))
            elif t=="text": q["text"]+=(c.get("text","") or "")
            elif t=="thinking": q["think"]+=len(c.get("thinking","") or "")
    return out

def vec(m):
    t=sum(m.values()) or 1
    return [m.get(c,0)/t for c in CL]
def l1(a,b): return sum(abs(x-y) for x,y in zip(a,b))/2

def units_of(root):
    fs=glob.glob(os.path.join(root,"**","*.jsonl"), recursive=True)
    U={"session":[], "block":[], "run":[]}
    for f in fs:
        base=os.path.basename(f)
        if base.startswith("agent-"):
            R=reqs_of(f)
            if R: U["run"].append(collections.Counter(rc(q) for q in R.values()))
            continue
        if base[:-6] in SELF: continue
        tot=obs=0
        for l in open(f,errors="ignore"):
            tot+=1
            if OBS.search(l): obs+=1
        if not tot or obs/tot>0.20: continue
        R=reqs_of(f)
        if not R: continue
        U["session"].append(collections.Counter(rc(q) for q in R.values()))
        st_=[(dt.datetime.fromisoformat(q["ts"].replace("Z","+00:00")).timestamp(),q)
             for q in R.values() if q.get("ts")]
        if not st_: continue
        st_.sort(); bins=sorted({int(t)//BIN for t,_ in st_})
        gs=[]; cur=[bins[0]]
        for b in bins[1:]:
            if (b-cur[0])*BIN>=MAXB or (b-cur[-1])>=IDLE: gs.append(cur); cur=[b]
            else: cur.append(b)
        gs.append(cur)
        for g in gs:
            lo,hi=g[0]*BIN,(g[-1]+1)*BIN
            m=collections.Counter(rc(q) for t,q in st_ if lo<=t<hi)
            if m: U["block"].append(m)
    return U

def shuffled(units, pool, seed=7):
    rnd=random.Random(seed); p=list(pool); rnd.shuffle(p); o=[]; i=0
    for m in units:
        n=sum(m.values()); o.append(collections.Counter(p[i:i+n])); i+=n
    return o

# ---------------------------------------------------------------------------
# SHAPE: do the distributions fall into recurring PROFILES, or is every unit its own
# snowflake? A handful of profiles is a product; 300 snowflakes is a table nobody reads.
# k-means on the 9-dim mix, k chosen by where the shuffled control stops improving.
def kmeans(X, k, iters=60, seed=3):
    rnd=random.Random(seed); C=[list(x) for x in rnd.sample(X,k)]
    for _ in range(iters):
        A=[min(range(k), key=lambda j: sum((a-b)**2 for a,b in zip(x,C[j]))) for x in X]
        for j in range(k):
            pts=[X[i] for i,a in enumerate(A) if a==j]
            if pts: C[j]=[sum(p[d] for p in pts)/len(pts) for d in range(len(X[0]))]
    A=[min(range(k), key=lambda j: sum((a-b)**2 for a,b in zip(x,C[j]))) for x in X]
    inertia=sum(sum((a-b)**2 for a,b in zip(X[i],C[A[i]])) for i in range(len(X)))
    return C,A,inertia

def profile_report(units, pool, label, kmax=6):
    X=[vec(m) for m in units if sum(m.values())>=6]     # drop units too small to have a shape
    if len(X)<40: return
    allc=list(pool.elements())
    S=[vec(m) for m in shuffled([m for m in units if sum(m.values())>=6], allc)]
    base=sum(sum((a-b)**2 for a,b in zip(x, [sum(y[d] for y in X)/len(X) for d in range(9)])) for x in X)
    print(f"\n  --- {label}: {len(X)} units with >=6 requests ---")
    print(f"  {'k':>2s} {'real var explained':>19s} {'shuffled':>9s} {'GAIN':>7s}")
    best=None
    for k in range(2, kmax+1):
        _,_,ir = kmeans(X,k); _,_,ish = kmeans(S,k)
        bs=sum(sum((a-b)**2 for a,b in zip(x,[sum(y[d] for y in S)/len(S) for d in range(9)])) for x in S)
        r=1-ir/base; s=1-ish/bs
        print(f"  {k:2d} {r:19.2f} {s:9.2f} {r-s:+7.2f}")
        if best is None or (r-s)>best[1]: best=(k, r-s)
    k=best[0]
    C,A,_=kmeans(X,k)
    print(f"  profiles at k={k} (the widest gap over shuffled):")
    for j in range(k):
        n=sum(1 for a in A if a==j)
        top=sorted(zip(CL,C[j]), key=lambda kv:-kv[1])[:3]
        print(f"     {n:4d} units ({n/len(X):4.0%})  " + "  ".join(f"{c} {v:.0%}" for c,v in top))

for var,lab in (("KELD_CORPUS_A","CORPUS A"),("KELD_CORPUS_B","CORPUS B")):
    root=os.environ.get(var,"")
    if not root: continue
    U=units_of(os.path.expanduser(root))
    pool=collections.Counter()
    for v in U.values():
        for m in v: pool.update(m)
    P=vec(pool); allc=list(pool.elements())
    print(f"\n=== {lab} — DISTINCTIVENESS: how far is a unit's mix from the corpus average? ===")
    print(f"  {'unit':10s} {'n':>5s} {'med req':>8s} {'real dist':>10s} {'shuffled':>9s} {'GAIN':>7s}")
    for k,v in U.items():
        if not v: continue
        real=st.median([l1(vec(m),P) for m in v])
        sh  =st.median([l1(vec(m),P) for m in shuffled(v,allc)])
        nn=st.median([sum(m.values()) for m in v])
        print(f"  {k:10s} {len(v):5d} {nn:8.0f} {real:10.2f} {sh:9.2f} {real-sh:+7.2f}")
    # and by size band, since small units are far from any average by arithmetic
    print(f"\n  {'unit':10s} {'band':>10s} {'n':>5s} {'real':>7s} {'shuf':>7s} {'GAIN':>7s}")
    for k,v in U.items():
        for lo,hi,nm in ((1,5,"1-5"),(6,20,"6-20"),(21,10**9,"21+")):
            sel=[m for m in v if lo<=sum(m.values())<=hi]
            if len(sel)<8: continue
            r=st.median([l1(vec(m),P) for m in sel]); s=st.median([l1(vec(m),P) for m in shuffled(sel,allc)])
            print(f"  {k:10s} {nm:>10s} {len(sel):5d} {r:7.2f} {s:7.2f} {r-s:+7.2f}")
    for k,v in U.items():
        if v: profile_report(v, pool, f"{lab} / {k}")

