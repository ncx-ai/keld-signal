#!/usr/bin/env python3
"""John's session, WINDOWED: cut each 60-min window into 400-char sub-windows, classify each,
rank verbs by share. This is the design the single-call arm structurally cannot express, because
a 60-min window does not fit GLiNER2's 512 positions.

Scored against MULTI-LABEL gold, in the framing actually asked for: does the ranked prediction
recover the gold set, and in roughly the right order?
"""
import re, sys, os, collections, json
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_atv1 import RICH

VIEWS="/tmp/claude-1000/john-views.txt"
LABELS=os.path.join(HERE,"activity-atv1-john-labels.txt")
SENT=re.compile(r"(?<=[.!?])\s+")

gold={}
for line in open(LABELS):
    m=re.match(r"^(J\d\d)\s+(.+)$", line.strip())
    if m: gold[m.group(1)]=[x.strip() for x in m.group(2).split(">")]

wins,cur,key={}, [], None
for line in open(VIEWS):
    m=re.match(r"^\[(J\d\d)\]", line)
    if m:
        if key: wins[key]=" ".join(cur)
        key,cur=m.group(1),[]; continue
    if key and not line.startswith("="):
        t=line.strip()
        if t.startswith(("USER:","*")): cur.append(re.sub(r"^(USER:|\*)\s*","",t))
if key: wins[key]=" ".join(cur)

def subwindows(text, size=400):
    out,buf=[],""
    for s in SENT.split(text):
        if len(buf)+len(s)+1>size and buf: out.append(buf.strip()); buf=""
        buf+=" "+s
    if buf.strip(): out.append(buf.strip())
    return out

def verb_of(gid):
    return gid.rsplit(".",1)[0] if gid.count(".")>1 else (gid if gid in ("plan","embed","code.write","code.edit") else gid.rsplit(".",1)[0])

from gliner2 import GLiNER2
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1")
VERB=[f"{v}: {RICH[v]}" for v in RICH]

tot_top1_in_set=tot_prim=0; recalls=[]
print(f"{'win':5s} {'n':3s} {'gold (prominence order)':50s} predicted ranked (share)")
print("-"*130)
for k in sorted(gold):
    subs=subwindows(wins.get(k,""))
    votes=collections.Counter()
    for s in subs:
        if len(s)<40: continue
        v=ex.classify_text(s,{"verb":{"labels":VERB}},include_confidence=True)["verb"]["label"].split(":")[0]
        votes[v]+=1
    n=sum(votes.values()) or 1
    ranked=[(v,c/n) for v,c in votes.most_common()]
    gverbs=[verb_of(g) for g in gold[k]]
    top1=ranked[0][0] if ranked else None
    tot_prim += (top1==gverbs[0]); tot_top1_in_set += (top1 in gverbs)
    found=sum(1 for g in gverbs if g in votes); recalls.append(found/len(gverbs))
    print(f"{k:5s} {len(subs):3d} {' > '.join(gold[k])[:50]:50s} "
          + " ".join(f"{v}({p:.0%})" for v,p in ranked[:4]))
N=len(gold)
print()
print(f"  top1 == gold PRIMARY          : {tot_prim}/{N} = {tot_prim/N:.3f}")
print(f"  top1 IN gold set              : {tot_top1_in_set}/{N} = {tot_top1_in_set/N:.3f}")
print(f"  mean gold-set RECALL in ranked: {sum(recalls)/N:.3f}   <- 'did it propose all that apply'")
