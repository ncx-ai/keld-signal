#!/usr/bin/env python3
"""Can we classify the general TYPE OF WORK — marketing, sales, engineering, legal...?

This is the question on its own, not as half of an activity type. It is answerable because two
corpora with DIFFERENT known domains exist:
    engineering  — 100 windows of one developer's software work
    product/mktg — 7 windows of building and revising a customer deck

Ground truth is at the CORPUS level, so no per-window labelling is needed. The test is
DISCRIMINATION: if both corpora produce the same distribution, it does not work, and that
comparison is its own control.

`atv1`'s context list has no `engineering` value, so this uses a business-function vocabulary
matching the question as asked.
"""
import json, os, re, sys, collections, datetime as dt
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)

DOMAINS = {
 "engineering": "building software: writing, debugging, testing or deploying code and infrastructure",
 "product":     "product management: specs, requirements, roadmaps, milestones, deciding what to build",
 "marketing":   "marketing and content: campaigns, brand, positioning, copy, launch material",
 "sales":       "sales and customer deals: proposals, pitches, pre-sales solution design, customer presentations",
 "finance":     "finance: budgets, costs, pricing, revenue, accounting, spend analysis",
 "legal":       "legal, contracts, compliance and risk",
 "support":     "customer support: troubleshooting a customer's problem, answering tickets",
 "operations":  "internal operations and process: logistics, scheduling, administration",
 "general":     "none of these in particular; general or mixed work",
}
LABELS=[f"{k}: {v}" for k,v in DOMAINS.items()]

SPAN,STRIDE,BUDGET=60,50,1400
FENCE=re.compile(r"```.*?```", re.S)
SENT=re.compile(r"(?<![0-9A-Z])[.!?]+\s+(?=[A-Z\"'(\[])")

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
    return turns

from gliner2 import GLiNER2
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1")

def run(name, windows, expect):
    per_window=collections.Counter(); per_sub=collections.Counter()
    for turns in windows:
        subs=pack(turns)
        v=collections.Counter()
        for s in subs:
            d=ex.classify_text(s,{"domain":{"labels":LABELS}},include_confidence=True)["domain"]["label"].split(":")[0]
            v[d]+=1; per_sub[d]+=1
        if v: per_window[v.most_common(1)[0][0]]+=1
    n=sum(per_window.values()) or 1
    print(f"\n=== {name}   ({n} windows)   expected: {expect}")
    print("  by WINDOW (majority of its sub-windows):")
    for d,c in per_window.most_common(): print(f"     {d:12s} {c:3d}  {c/n:5.0%}")
    ns=sum(per_sub.values()) or 1
    print("  by SUB-WINDOW:", ", ".join(f"{d} {c/ns:.0%}" for d,c in per_sub.most_common(4)))
    return per_window

# corpus A
P=os.environ.get("KELD_CORPUS_A_SESSION", "")  # set to the corpus-A session export
allt=load(P)
raw=[o for o in open(P, errors="ignore")]
jw=[]
import itertools
ts=[]
for line in open(P, errors="ignore"):
    try: o=json.loads(line)
    except Exception: continue
    if o.get("type") in ("user","assistant") and o.get("timestamp"):
        ts.append(dt.datetime.fromisoformat(o["timestamp"].replace("Z","+00:00")))
ts.sort(); start=ts[0]
while start<ts[-1]:
    t=load(P,start,start+dt.timedelta(minutes=SPAN)); start+=dt.timedelta(minutes=STRIDE)
    if t: jw.append(t)

# engineering
S=os.path.expanduser("~/keld/refseries-context/facets/activity-rerun-sample.ndjson")
ew=[]
for line in open(S):
    r=json.loads(line)
    if len(ew)>=40: break
    if not os.path.exists(r["file"]): continue
    lo=dt.datetime.fromisoformat(r["start"]); t=load(r["file"],lo,lo+dt.timedelta(minutes=SPAN))
    if t: ew.append(t)

run("ENGINEERING (your transcripts)", ew, "engineering / product")
run("CORPUS A (customer deck session)",   jw, "sales / marketing / product")
