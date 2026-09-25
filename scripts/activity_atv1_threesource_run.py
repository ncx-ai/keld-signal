#!/usr/bin/env python3
"""Score the three-source design on BOTH gold sets."""
import json, os, re, sys, collections, datetime as dt
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
sys.path.insert(0, os.path.join(HERE,"..","sidecar"))
from activity_atv1 import DOCS
from activity_atv1_threesource import file_evidence, rank, snap

SPAN,STRIDE,BUDGET=60,50,1400
FENCE=re.compile(r"```.*?```", re.S)
SENT=re.compile(r"(?<![0-9A-Z])[.!?]+\s+(?=[A-Z\"'(\[])")
CTXS=["general","legal","media","financial","marketing","medical","scientific","sales","support","operations"]
TEAM={"eng":"This work was done by someone on the Engineering team. ",
      "other":"This work was done by someone on the Product team. "}

def text_of(c):
    if isinstance(c,str): return c
    if not isinstance(c,list): return ""
    return "\n".join(b.get("text","") for b in c if isinstance(b,dict) and b.get("type")=="text")

def split_sentences(t):
    out,last=[],0
    for m in SENT.finditer(t): out.append(t[last:m.end()].strip()); last=m.end()
    tail=t[last:].strip()
    if tail: out.append(tail)
    return [s for s in out if s]

def pack(turns):
    subs,buf=[],""
    for role,txt in turns:
        unit=f"{role}: {txt}"
        pieces=[unit] if len(unit)<=BUDGET else []
        if not pieces:
            cur=f"{role}: "
            for s in split_sentences(txt):
                if len(cur)+len(s)+1>BUDGET and cur.strip()!=f"{role}:": pieces.append(cur.strip()); cur=f"{role}: "
                cur+=s+" "
            if cur.strip()!=f"{role}:": pieces.append(cur.strip())
        for p in pieces:
            if len(buf)+len(p)+1>BUDGET and buf: subs.append(buf.strip()); buf=""
            buf+=p+"\n"
    if buf.strip(): subs.append(buf.strip())
    return subs

def load(path, lo=None, hi=None):
    raw=[]
    for line in open(path, errors="ignore"):
        try: o=json.loads(line)
        except Exception: continue
        if o.get("type") not in ("user","assistant") or not o.get("timestamp"): continue
        t=dt.datetime.fromisoformat(o["timestamp"].replace("Z","+00:00"))
        if lo and not (lo<=t<hi): continue
        raw.append(o)
    raw.sort(key=lambda o:o["timestamp"])
    turns=[]
    for o in raw:
        t=re.sub(r"\s+"," ",FENCE.sub(" ",text_of((o.get("message") or {}).get("content")))).strip()
        if not t or t.startswith(("<","Base directory for this skill")): continue
        turns.append(("USER" if o["type"]=="user" else "ASSISTANT", t))
    return raw, turns

def verb_of(g): return g.rsplit(".",1)[0] if g.count(".")>1 else (g if g in ("plan","embed","code.write","code.edit") else g.rsplit(".",1)[0])

from gliner2 import GLiNER2
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1")
VERB=[f"{v}: {DOCS[v]}" for v in DOCS]
CTXL=[f"{c}: work applied to {c} ends" for c in CTXS]+["NONE: no particular professional domain"]

def run(units, gold, team, tag):
    prim=inset=0; recs=[]; mod_added=0
    for k in sorted(gold):
        raw,turns = units[k]
        subs=pack(turns)
        votes=collections.Counter()
        for s in subs:
            v=ex.classify_text(s,{"verb":{"labels":VERB}},include_confidence=True)["verb"]["label"].split(":")[0]
            votes[v]+=1
        big=" ".join(subs)[:1400]
        c=ex.classify_text(TEAM[team]+big,{"ctx":{"labels":CTXL}},include_confidence=True)["ctx"]["label"].split(":")[0]
        c="general" if c=="NONE" else c
        props=file_evidence(raw)
        mod_added += sum(1 for v in props if v not in votes)
        ranked=rank(votes,props,c,len(subs))
        gv=[verb_of(g) for g in gold[k]]
        pv=[i.rsplit(".",1)[0] if i.count(".")>1 else (i if i in ("plan","embed","code.write","code.edit") else i.rsplit(".",1)[0]) for i,_ in ranked]
        if pv:
            prim += (pv[0]==gv[0]); inset += (pv[0] in gv)
            recs.append(sum(1 for g in gv if g in pv)/len(gv))
        if len(gold)<20:
            print(f"  {k}  gold={' > '.join(gold[k])[:44]:44s} pred={' '.join(f'{i}({s:.0%})' for i,s in ranked[:3])}")
    n=len(gold)
    print(f"  {tag:30s} primary {prim}/{n}={prim/n:.3f}  in-set {inset/n:.3f}  recall {sum(recs)/max(len(recs),1):.3f}  (modality added {mod_added} candidates)")

# JOHN
P="/home/dg/Downloads/transcripts-john/session-export-1787252995037/transcript.jsonl"
gold={}
for line in open(os.path.join(HERE,"activity-atv1-john-labels.txt")):
    m=re.match(r"^(J\d\d)\s+(.+)$",line.strip())
    if m: gold[m.group(1)]=[x.strip() for x in m.group(2).split(">")]
allraw,_=load(P)
t0=dt.datetime.fromisoformat(allraw[0]["timestamp"].replace("Z","+00:00"))
tN=dt.datetime.fromisoformat(allraw[-1]["timestamp"].replace("Z","+00:00"))
units={}; start=t0; idx=0
while start<tN:
    end=start+dt.timedelta(minutes=SPAN)
    r,t=load(P,start,end); here,start=start,start+dt.timedelta(minutes=STRIDE)
    if not t: continue
    idx+=1; units[f"J{idx:02d}"]=(r,t)
print("\n=== JOHN 7 (team=other) — three-source")
run(units,gold,"other","three-source")

# ENGINEERING 100 — same three-source pipeline, from the frame's own transcripts
S=os.path.expanduser("~/keld/refseries-context/facets/activity-rerun-sample.ndjson")
egold={}
for line in open(os.path.join(HERE,"activity-atv1-hand-labels.txt")):
    m=re.match(r"^(\d{3})\s+(\S+)",line)
    if m: egold[int(m.group(1))]=[m.group(2)]
recs=[json.loads(l) for l in open(S)][:len(egold)]
eunits={}
for i,r in enumerate(recs,1):
    if not os.path.exists(r["file"]): continue
    lo=dt.datetime.fromisoformat(r["start"]); hi=lo+dt.timedelta(minutes=SPAN)
    raw,turns=load(r["file"],lo,hi)
    if turns: eunits[i]=(raw,turns)
egold={k:v for k,v in egold.items() if k in eunits}
print(f"\n=== ENGINEERING {len(egold)} (team=eng) — three-source")
run(eunits,egold,"eng","three-source")
