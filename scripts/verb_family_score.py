#!/usr/bin/env python3
"""Score VERB (17) and FAMILY (7) against the 60 blind labels on real colleague transcripts.

FAMILY is scored as the PUBLISHED ANSWER, which is a different question from the one already
refuted. The hierarchical arm (family -> verb cascade) failed at 0.350 because a wrong family
pick is UNRECOVERABLE when the verb pass can only choose inside it. Nothing about that says a
7-way family label is a bad OUTPUT — and the domain probe showed coarser, better-separated
vocabularies classify markedly better.

Two derivations of family are measured, because they can disagree:
  F-direct   ask the model for the family in one 7-way call
  F-derived  ask for the verb, map the answer to its family via the CSV
"""
import csv, json, os, re, sys, collections
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_atv1 import DOCS

CSVP=os.path.expanduser("~/Downloads/keld-activity-types-v1.csv")
FRAME="/tmp/claude-1000/vf/frame.json"
LABELS=os.path.join(HERE,"verb-family-hand-labels.txt")
BUDGET=1400
SENT=re.compile(r"(?<![0-9A-Z])[.!?]+\s+(?=[A-Z\"'(\[])")

FAM_OF={}
for r in csv.DictReader(open(CSVP)): FAM_OF[r["verb"]]=r["family"]

FAM_DESC={
 "language":      "producing or revising written material: documents, decks, pages, specs, reports, emails",
 "understanding":  "making sense of finished work: checking it for errors, categorising it, or pulling facts out of it",
 "agentic":        "investigating, deciding an approach, or talking a question through",
 "media":          "producing images, video or audio",
 "code":           "writing or changing software code",
 "infra":          "turning content into vectors",
 "fallback":       "none of these",
}

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

# The labels file carries TWO blocks: a superseded set assigned from truncated views, and the
# live set below the LIVE marker. Read ONLY past the marker -- relying on "later wins" would
# silently score the wrong set the moment anyone reorders the file.
_raw=open(LABELS).read()
_cut=_raw.find("# LIVE LABELS")
if _cut < 0: sys.exit("labels file has no LIVE block")
gold={}
for line in _raw[_cut:].splitlines():
    m=re.match(r"^(V\d{3})\s+(.+)$", line.strip())
    if m: gold[m.group(1)]=[x.strip() for x in m.group(2).split(">")]
print(f"gold: {len(gold)} blocks from the LIVE block")
frame={w["id"]:w for w in json.load(open(FRAME))}

from gliner2 import GLiNER2
import torch
_dev="cuda" if torch.cuda.is_available() else "cpu"
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1", map_location=_dev)
print(f"device: {_dev}")
VERB_L=[f"{v}: {DOCS[v]}" for v in DOCS]
FAM_L=[f"{f}: {d}" for f,d in FAM_DESC.items()]

vp=vs_=0; vrec=[]; fdp=0; fvp=0; conf=collections.Counter(); fconf=collections.Counter()
rows=[]
for k in sorted(gold):
    w=frame.get(k)
    if not w: continue
    subs=pack(w["turns"])
    vv=collections.Counter(); ff=collections.Counter()
    for s in subs:
        vv[ex.classify_text(s,{"verb":{"labels":VERB_L}},include_confidence=True)["verb"]["label"].split(":")[0]]+=1
        ff[ex.classify_text(s,{"family":{"labels":FAM_L}},include_confidence=True)["family"]["label"].split(":")[0]]+=1
    if not vv: continue
    gv=gold[k]; gf=FAM_OF[gv[0]]
    top=vv.most_common(1)[0][0]
    vp += (top==gv[0]); vs_ += (top in gv)
    vrec.append(sum(1 for g in gv if g in vv)/len(gv))
    if top!=gv[0]: conf[f"{gv[0]}->{top}"]+=1
    ftop=ff.most_common(1)[0][0]
    fdp += (ftop==gf)
    fvp += (FAM_OF.get(top)==gf)
    if ftop!=gf: fconf[f"{gf}->{ftop}"]+=1
    rows.append((k,gv[0],top,gf,ftop))
n=len(rows)
print(f"\nscored {n} blocks\n")
print(f"  VERB      top1 == gold primary : {vp}/{n} = {vp/n:.3f}")
print(f"            top1 in gold set     : {vs_/n:.3f}")
print(f"            gold-set recall      : {sum(vrec)/n:.3f}")
print()
print(f"  FAMILY    F-direct  (7-way call): {fdp}/{n} = {fdp/n:.3f}")
print(f"            F-derived (via verb)  : {fvp}/{n} = {fvp/n:.3f}")
print()
base_v=collections.Counter(g for _,g,_,_,_ in rows).most_common(1)[0][1]/n
base_f=collections.Counter(f for _,_,_,f,_ in rows).most_common(1)[0][1]/n
print(f"  majority constants: verb {base_v:.3f}   family {base_f:.3f}")
print(f"  LIFT: verb {vp/n-base_v:+.3f}   family(direct) {fdp/n-base_f:+.3f}   family(derived) {fvp/n-base_f:+.3f}")
print(f"\n  top verb confusions  : {conf.most_common(5)}")
print(f"  top family confusions: {fconf.most_common(5)}")
json.dump(rows, open("/tmp/claude-1000/vf/scored.json","w"), indent=1)
