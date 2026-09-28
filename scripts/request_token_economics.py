#!/usr/bin/env python3
"""WHERE THE TOKENS ACTUALLY GO. in:out is 234:1 (john) / 405:1 (owner), so a router that
picks a model by activity class is optimising ~0.3% of the token flow. This asks what the
other 99.7% IS, because that decides whether the class is the wrong lever or merely a
partial one.

The split that matters is CACHE READ vs CACHE CREATION vs fresh input: a cache-read token is
already cheap, so a raw in:out ratio overstates the prize. This separates them.
"""
import json, os, re, glob, collections, statistics as st

OBS=re.compile(r"\[MESSAGE FROM NON-USER SOURCE\]|<observed_from_primary_session>")
SELF={"13a58628-4f19-4ac9-9423-d669c717f259","d58162e2-bb14-40b5-afd9-2c55e403fe99"}
src=open("scripts/request_route_vocab.py").read()
ns={}; exec(compile(src[:src.index("files=[]")],"x","exec"), ns)

def scan(root, label):
    reqs={}; seen=set()
    for f in glob.glob(os.path.join(root,"**","*.jsonl"), recursive=True):
        if os.path.basename(f)[:-6] in SELF: continue
        tot=obs=0
        for line in open(f, errors="ignore"):
            tot+=1
            if OBS.search(line): obs+=1
        if not tot or obs/tot>0.20: continue
        for line in open(f, errors="ignore"):
            try: d=json.loads(line)
            except: continue
            if d.get("type")!="assistant": continue
            rid=d.get("requestId"); u=d.get("uuid")
            if not rid or u in seen: continue
            seen.add(u)
            m=d.get("message",{}); g=m.get("usage") or {}
            r=reqs.setdefault(rid,{"tools":[],"text":"","think":0,"out":0,
                                   "fresh":0,"cw":0,"cr":0})
            r["out"]  = max(r["out"],  g.get("output_tokens",0) or 0)
            r["fresh"]= max(r["fresh"],g.get("input_tokens",0) or 0)
            r["cw"]   = max(r["cw"],   g.get("cache_creation_input_tokens",0) or 0)
            r["cr"]   = max(r["cr"],   g.get("cache_read_input_tokens",0) or 0)
            for c in m.get("content",[]):
                t=c.get("type")
                if t=="tool_use": r["tools"].append((c.get("name") or "", c.get("input") or {}))
                elif t=="text":   r["text"]+= (c.get("text","") or "")
                elif t=="thinking": r["think"]+=len(c.get("thinking","") or "")
    for r in reqs.values(): r["inp"]=r["fresh"]+r["cw"]+r["cr"]; r["cls"]=ns["route_class"](r)
    F=sum(r["fresh"] for r in reqs.values()); W=sum(r["cw"] for r in reqs.values())
    Rd=sum(r["cr"] for r in reqs.values());   O=sum(r["out"] for r in reqs.values())
    T=F+W+Rd
    print(f"\n=== {label} — {len(reqs):,} requests ===")
    print(f"  fresh input   {F:15,d}  {F/T:6.2%} of input")
    print(f"  cache WRITE   {W:15,d}  {W/T:6.2%}   (priced above fresh)")
    print(f"  cache READ    {Rd:15,d}  {Rd/T:6.2%}   (priced ~10x below fresh)")
    print(f"  output        {O:15,d}")
    print(f"  raw in:out {T/O:.0f}:1   but NON-CACHE-READ in:out {(F+W)/O:.1f}:1")
    # a rough cost model: read 0.1, fresh 1.0, write 1.25, output 5.0 (Anthropic-shaped ratios)
    cost=lambda f,w,r,o: f*1.0 + w*1.25 + r*0.1 + o*5.0
    tot=cost(F,W,Rd,O)
    print(f"  COST SHARE (read 0.1 / fresh 1.0 / write 1.25 / output 5.0):")
    for nm,v in (("fresh",F*1.0),("cache write",W*1.25),("cache read",Rd*0.1),("output",O*5.0)):
        print(f"      {nm:12s} {v/tot:6.1%}")
    print(f"  per class — share of total modelled cost:")
    cc=collections.Counter()
    for r in reqs.values(): cc[r["cls"]]+=cost(r["fresh"],r["cw"],r["cr"],r["out"])
    for c,v in cc.most_common(): print(f"      {c:14s} {v/tot:6.1%}")
    return reqs

scan(os.path.expanduser("~/keld/john-projects/projects"), "JOHN")
scan(os.path.expanduser("~/.claude/projects"),             "OWNER")
