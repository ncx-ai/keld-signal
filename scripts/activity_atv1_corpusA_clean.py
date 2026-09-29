#!/usr/bin/env python3
"""corpus A's session, sub-windows cut at LOGICAL boundaries — turn first, sentence only if a turn
overflows, never mid-clause and never a truncation marker.

The prior run violated AGENTS.md's "never cut text mid-sentence" convention three ways:
  - it fed the human-readable views, whose `[... N chars omitted]` markers leaked into 36% of
    sub-windows as if they were text
  - its splitter `(?<=[.!?])\\s+` treated list enumerators `1.` `2.` as sentence ends, shredding
    the architect's numbered procedure into fragments
  - it stripped role markers, running user prompts into assistant prose with no boundary
This reads the TRANSCRIPT directly, keeps whole turns, and preserves who spoke.
"""
import json, re, os, sys, collections, datetime as dt
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_atv1 import RICH, DOCS

P=os.environ.get("KELD_CORPUS_A_SESSION", "")  # set to the corpus-A session export
LABELS=os.path.join(HERE,"activity-atv1-corpusA-labels.txt")
SPAN,STRIDE=60,50
BUDGET=1400                     # chars; well inside 512 deberta positions
FENCE=re.compile(r"```.*?```", re.S)
# a sentence end: . ! ? NOT preceded by a digit (list enumerator) or single capital (initial),
# and followed by whitespace + something that starts a clause.
SENT=re.compile(r"(?<![0-9A-Z])[.!?]+\s+(?=[A-Z\"'(\[])")

def text_of(c):
    if isinstance(c,str): return c
    if not isinstance(c,list): return ""
    return "\n".join(b.get("text","") for b in c if isinstance(b,dict) and b.get("type")=="text")

def split_sentences(t):
    out,last=[],0
    for m in SENT.finditer(t):
        out.append(t[last:m.end()].strip()); last=m.end()
    tail=t[last:].strip()
    if tail: out.append(tail)
    return [s for s in out if s]

def pack(turns, budget=BUDGET):
    """Whole turns into sub-windows; a turn longer than budget splits at sentence ends only."""
    subs, buf = [], ""
    for role, txt in turns:
        unit=f"{role}: {txt}"
        if len(unit) <= budget:
            pieces=[unit]
        else:
            pieces, cur = [], f"{role}: "
            for s in split_sentences(txt):
                if len(cur)+len(s)+1 > budget and cur.strip() != f"{role}:":
                    pieces.append(cur.strip()); cur=f"{role}: "
                cur += s+" "
            if cur.strip() != f"{role}:": pieces.append(cur.strip())
        for p in pieces:
            if len(buf)+len(p)+1 > budget and buf:
                subs.append(buf.strip()); buf=""
            buf += p+"\n"
    if buf.strip(): subs.append(buf.strip())
    return subs

gold={}
for line in open(LABELS):
    m=re.match(r"^(J\d\d)\s+(.+)$", line.strip())
    if m: gold[m.group(1)]=[x.strip() for x in m.group(2).split(">")]

turns=[]
for line in open(P, errors="ignore"):
    try: o=json.loads(line)
    except Exception: continue
    if o.get("type") not in ("user","assistant") or not o.get("timestamp"): continue
    t=text_of((o.get("message") or {}).get("content"))
    t=re.sub(r"\s+"," ",FENCE.sub(" ",t)).strip()
    if not t or t.startswith(("<","Base directory for this skill")): continue
    turns.append((dt.datetime.fromisoformat(o["timestamp"].replace("Z","+00:00")),
                  "USER" if o["type"]=="user" else "ASSISTANT", t))
turns.sort(key=lambda x:x[0])

wins={}; start=turns[0][0]; idx=0
while start < turns[-1][0]:
    end=start+dt.timedelta(minutes=SPAN)
    sl=[(r,t) for ts,r,t in turns if start<=ts<end]
    here,start=start,start+dt.timedelta(minutes=STRIDE)
    if not sl: continue
    idx+=1; wins[f"J{idx:02d}"]=pack(sl)

print("=== sub-window hygiene ===")
allsubs=[s for v in wins.values() for s in v]
print(f"  sub-windows: {len(allsubs)}   with truncation markers: {sum('chars omitted' in s for s in allsubs)}")
print(f"  shortest {min(len(s) for s in allsubs)}  longest {max(len(s) for s in allsubs)}")
print(f"  example: {allsubs[0][:170]!r}\n")

from gliner2 import GLiNER2
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1")
import os as _os
STY = DOCS if _os.environ.get("WORDING")=="docs" else RICH
VERB=[f"{v}: {STY[v]}" for v in STY]
def verb_of(g): return g.rsplit(".",1)[0] if g.count(".")>1 else (g if g in ("plan","embed","code.write","code.edit") else g.rsplit(".",1)[0])

prim=inset=0; recs=[]
print(f"{'win':5s} {'n':3s} {'gold primary':26s} predicted ranked")
print("-"*110)
for k in sorted(gold):
    votes=collections.Counter()
    for s in wins.get(k,[]):
        v=ex.classify_text(s,{"verb":{"labels":VERB}},include_confidence=True)["verb"]["label"].split(":")[0]
        votes[v]+=1
    n=sum(votes.values()) or 1
    gv=[verb_of(g) for g in gold[k]]
    top=votes.most_common(1)[0][0] if votes else None
    prim += (top==gv[0]); inset += (top in gv)
    recs.append(sum(1 for g in gv if g in votes)/len(gv))
    print(f"{k:5s} {len(wins.get(k,[])):3d} {gold[k][0]:26s} "+" ".join(f"{v}({c/n:.0%})" for v,c in votes.most_common(4)))
N=len(gold)
print()
print(f"  top1 == gold PRIMARY : {prim}/{N} = {prim/N:.3f}   (was 0.000 on malformed input)")
print(f"  top1 IN gold set     : {inset}/{N} = {inset/N:.3f}   (was 0.143)")
print(f"  mean gold-set recall : {sum(recs)/N:.3f}   (was 0.190)")
