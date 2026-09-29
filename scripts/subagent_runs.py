#!/usr/bin/env python3
"""Per-SUBAGENT-RUN activity mix. A subagent run is one delegated task: Claude Code writes the
helper's whole conversation to its own `agent-<id>.jsonl`, so a run is already a delimited
unit with a stated goal (its first message, the brief) and a deliverable (its last).

E2 asks whether the BRIEF predicts the MIX. This file computes the mix; the gold needs no
labelling because the deterministic per-request classifier supplies it.

⚠️ FIRST it asks a cheaper question: do runs differ from each other AT ALL? If every run has
the same activity mix there is nothing to predict and E2 is dead before it starts.
"""
import json, os, re, glob, sys, collections, statistics as st
HERE=os.path.dirname(os.path.abspath(__file__))
src=open(os.path.join(HERE,"request_route_vocab.py")).read()
ns={}; exec(compile(src[:src.index("files=[]")],"rrv","exec"), ns)
route_class=ns["route_class"]

def runs_in(root):
    out=[]
    for af in glob.glob(os.path.join(root,"**","agent-*.jsonl"), recursive=True):
        rs=[json.loads(l) for l in open(af,errors="ignore") if l.strip().startswith("{")]
        if not rs: continue
        c=(rs[0].get("message") or {}).get("content")
        brief=c if isinstance(c,str) else " ".join(x.get("text","") for x in (c or []) if isinstance(x,dict))
        reqs={}
        for r in rs:
            if r.get("type")!="assistant": continue
            rid=r.get("requestId")
            if not rid: continue
            m=r.get("message",{}); u=m.get("usage") or {}
            q=reqs.setdefault(rid,{"tools":[],"text":"","think":0,"out":0})
            q["out"]=max(q["out"], u.get("output_tokens",0) or 0)
            for b in m.get("content",[]):
                t=b.get("type")
                if t=="tool_use": q["tools"].append((b.get("name") or "", b.get("input") or {}))
                elif t=="text":   q["text"]+=(b.get("text","") or "")
                elif t=="thinking": q["think"]+=len(b.get("thinking","") or "")
        if not reqs: continue
        mix=collections.Counter(route_class(q) for q in reqs.values())
        tok=collections.Counter()
        for q in reqs.values(): tok[route_class(q)]+=q["out"]
        out.append({"id":os.path.basename(af)[6:-6], "brief":" ".join(brief.split()),
                    "n":len(reqs), "mix":dict(mix), "tok":dict(tok)})
    return out

CL=["retrieve","author_code","operate","author_prose","synthesize","verify","delegate",
    "acknowledge","unclassified"]
for var,label in (("KELD_CORPUS_A","CORPUS A"),("KELD_CORPUS_B","CORPUS B")):
    root=os.environ.get(var,"")
    if not root: continue
    R=runs_in(os.path.expanduser(root))
    print(f"\n=== {label}: {len(R)} subagent runs, "
          f"{sum(r['n'] for r in R):,} requests inside them ===")
    # pooled mix
    pool=collections.Counter()
    for r in R: pool.update(r["mix"])
    T=sum(pool.values())
    print("  pooled mix:", ", ".join(f"{c} {pool[c]/T:.0%}" for c in CL if pool[c]))
    # DOMINANT class per run -- is there variety?
    dom=collections.Counter(max(r["mix"], key=r["mix"].get) for r in R)
    print(f"  dominant class per run: {dict(dom.most_common())}")
    print(f"  majority-class constant = {dom.most_common(1)[0][1]/len(R):.1%}  "
          f"<- anything predicting the dominant class must beat this")
    # how far apart are runs? mean L1 distance between a run's mix and the pooled mix
    def vec(m):
        s=sum(m.values()) or 1
        return [m.get(c,0)/s for c in CL]
    P=vec(pool)
    d=[sum(abs(a-b) for a,b in zip(vec(r["mix"]),P))/2 for r in R]
    print(f"  distance from the pooled average (0=identical, 1=disjoint): "
          f"median {st.median(d):.2f}  p90 {sorted(d)[int(.9*len(d))]:.2f}")
    json.dump(R, open(f"/tmp/claude-1000/runs_{label.split()[-1]}.json","w"))
