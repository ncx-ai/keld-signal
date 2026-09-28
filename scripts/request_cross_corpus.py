#!/usr/bin/env python3
"""CROSS-PERSON CHECK. Same rules, a different person's transcripts, no labels.

Everything measured so far -- the 8-class vocabulary, the nine mechanism fixes, the resolver,
the 120 fitted labels and the 80 holdout -- comes from ONE person's transcripts. The rules
could be encoding one engineer's shell habits rather than the shape of agentic work.

The residual is a COVERAGE metric and needs no hand labels, so this runs on a second corpus
directly. Bar, pre-registered before the number exists (see the results doc):
    <= 5%   tool-shaped; proceed
    5-10%   person-shaped at the margins; one more resolver round
    > 10%   the rules encode one person's habits; a different project

⚠️ SESSIONS BELONGING TO THIS INVESTIGATION ARE EXCLUDED. They are unrepresentatively
Bash-and-Python-heavy and the rules were written while inside them, so including them would
be scoring the classifier on its own training session.
"""
import json, os, re, glob, sys, collections, statistics as st
HERE=os.path.dirname(os.path.abspath(__file__))
src=open(os.path.join(HERE,"request_route_vocab.py")).read()
ns={}; exec(compile(src[:src.index("files=[]")], "rrv", "exec"), ns)
route_class, split_heredocs = ns["route_class"], ns["split_heredocs"]

OBS=re.compile(r"\[MESSAGE FROM NON-USER SOURCE\]|<observed_from_primary_session>")
SELF={"13a58628-4f19-4ac9-9423-d669c717f259","d58162e2-bb14-40b5-afd9-2c55e403fe99"}

def collect(root, label):
    files=[]; skipped_self=0
    for f in glob.glob(os.path.join(root,"**","*.jsonl"), recursive=True):
        if os.path.basename(f).replace(".jsonl","") in SELF: skipped_self+=1; continue
        tot=obs=0
        for line in open(f, errors="ignore"):
            tot+=1
            if OBS.search(line): obs+=1
        if tot and obs/tot<=0.20: files.append(f)
    reqs={}; seen=set()
    for f in files:
        for line in open(f, errors="ignore"):
            try: d=json.loads(line)
            except: continue
            if d.get("type")!="assistant": continue
            rid=d.get("requestId"); u=d.get("uuid")
            if not rid or u in seen: continue
            seen.add(u)
            m=d.get("message",{}); usg=m.get("usage") or {}
            r=reqs.setdefault(rid,{"tools":[],"text":"","think":0,"out":0,"inp":0,
                                   "side":bool(d.get("isSidechain"))})
            r["out"]=max(r["out"], usg.get("output_tokens",0) or 0)
            r["inp"]=max(r["inp"], (usg.get("cache_read_input_tokens",0) or 0)
                                  +(usg.get("cache_creation_input_tokens",0) or 0)
                                  +(usg.get("input_tokens",0) or 0))
            for c in m.get("content",[]):
                t=c.get("type")
                if t=="tool_use": r["tools"].append((c.get("name") or "", c.get("input") or {}))
                elif t=="text":   r["text"]+= (c.get("text","") or "")
                elif t=="thinking": r["think"]+=len(c.get("thinking","") or "")
    for r in reqs.values(): r["cls"]=route_class(r)
    print(f"{label}: {len(files)} transcripts kept ({skipped_self} self-excluded), "
          f"{len(reqs):,} requests, {sum(1 for r in reqs.values() if r['side'])/max(1,len(reqs)):.1%} subagent")
    return reqs

A=collect(os.path.expanduser("~/keld/john-projects/projects"), "JOHN  ")
B=collect(os.path.expanduser("~/.claude/projects"),             "OWNER ")
CL=["retrieve","author_code","operate","author_prose","synthesize","verify","delegate",
    "acknowledge","unclassified"]
ca=collections.Counter(r["cls"] for r in A.values()); na=sum(ca.values())
cb=collections.Counter(r["cls"] for r in B.values()); nb=sum(cb.values())
print(f"\n{'class':14s} {'JOHN':>9s} {'OWNER':>9s} {'delta':>8s}")
for c in CL:
    print(f"{c:14s} {ca[c]/na:9.1%} {cb[c]/nb:9.1%} {cb[c]/nb-ca[c]/na:+8.1%}")
print(f"\nRULE COVERAGE   john {1-ca['unclassified']/na:.1%}   owner {1-cb['unclassified']/nb:.1%}")
print(f"RESIDUAL        john {ca['unclassified']/na:.1%}   owner {cb['unclassified']/nb:.1%}")
ia=[r["inp"] for r in A.values()]; ob=[r["out"] for r in A.values()]
ib=[r["inp"] for r in B.values()]; ou=[r["out"] for r in B.values()]
print(f"\nin:out ratio    john {sum(ia)/max(1,sum(ob)):.0f}:1   owner {sum(ib)/max(1,sum(ou)):.0f}:1")
print(f"median in/out   john {st.median(ia):,.0f}/{st.median(ob):.0f}   "
      f"owner {st.median(ib):,.0f}/{st.median(ou):.0f}")
rem=[r for r in B.values() if r["cls"]=="unclassified"]
heads=collections.Counter()
for r in rem:
    b=[i for n,i in r["tools"] if n.split("__")[-1]=="Bash"]
    if not b: heads["(tool) "+(r["tools"][0][0].split("__")[-1] if r["tools"] else "none")]+=1; continue
    skel,_=split_heredocs(b[0].get("command","") or "")
    cc,prog,_=ns["canonical"](skel); heads[prog or "?"]+=1
print(f"\nOWNER residual heads ({len(rem)} requests, {len(heads)} distinct):")
for t,n in heads.most_common(15): print(f"   {t:24s} {n:4d}  {n/max(1,len(rem)):5.1%}")
