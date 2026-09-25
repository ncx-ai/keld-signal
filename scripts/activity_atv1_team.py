#!/usr/bin/env python3
"""Does knowing the person's TEAM help pick the verb?

Two mechanisms, deliberately distinguished:
  T1  PRIOR  — state the team in the prompt, offer all 17 verbs (the idiom that fixed
               `work_function`, and that cut false-domain attribution 11 -> 4)
  T2  BRANCH — actually restrict the offered label set by team (what was asked for)

Measured WITHIN each corpus against its own `docs`-wording baseline. The two corpora use
different pipelines (the engineering frame is single-call on a 2000-char cap; John's is
turn-packed sub-window voting), so cross-corpus numbers are NOT comparable — only the delta
from team information is.
"""
import json, re, os, sys, collections, datetime as dt
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_atv1 import DOCS, window_text

# Conservative branch: drop only what is implausible for that team, never the shared middle.
ENG_DROP    = {"image.create","video.create","audio.create","transcribe"}
NONENG_DROP = {"code.write","code.edit","embed"}
PRIOR = {"eng":"The person doing this work is on the Engineering team. ",
         "other":"The person doing this work is not an engineer; they work in product and marketing. "}

def labels_for(team, branch):
    drop = (ENG_DROP if team=="eng" else NONENG_DROP) if branch else set()
    return [f"{v}: {DOCS[v]}" for v in DOCS if v not in drop]

def verb_of(g): return g.rsplit(".",1)[0] if g.count(".")>1 else (g if g in ("plan","embed","code.write","code.edit") else g.rsplit(".",1)[0])

# ---------- corpora ----------
def eng_corpus():
    S=os.path.expanduser("~/keld/refseries-context/facets/activity-rerun-sample.ndjson")
    L=os.path.join(HERE,"activity-atv1-hand-labels.txt")
    gold={}
    for line in open(L):
        m=re.match(r"^(\d{3})\s+(\S+)",line)
        if m: gold[int(m.group(1))]=[m.group(2)]
    recs=[json.loads(l) for l in open(S)][:len(gold)]
    return {i:[window_text(r)] for i,r in enumerate(recs,1)}, gold

def john_corpus():
    import importlib.util
    spec=importlib.util.spec_from_file_location("jc", os.path.join(HERE,"activity_atv1_john_clean.py"))
    src=open(os.path.join(HERE,"activity_atv1_john_clean.py")).read()
    src=src.split("from gliner2 import GLiNER2")[0]          # reuse the packing, not the scoring
    ns={"__name__":"jc"}; exec(compile(src,"jc","exec"), ns)
    return ns["wins"], ns["gold"]

def score(ex, texts, gold, team, branch, tag):
    labs=labels_for(team,branch)
    prim=inset=0; recs=[]
    for k in sorted(gold):
        votes=collections.Counter()
        for t in texts.get(k,[]):
            if not t or len(t)<40: continue
            v=ex.classify_text(t,{"verb":{"labels":labs}},include_confidence=True)["verb"]["label"].split(":")[0]
            votes[v]+=1
        if not votes: continue
        gv=[verb_of(g) for g in gold[k]]
        top=votes.most_common(1)[0][0]
        prim+= (top==gv[0]); inset+= (top in gv)
        recs.append(sum(1 for g in gv if g in votes)/len(gv))
    n=len(gold)
    print(f"  {tag:34s} primary {prim:>3}/{n} = {prim/n:.3f}   in-set {inset/n:.3f}   recall {sum(recs)/max(len(recs),1):.3f}")
    return prim/n

from gliner2 import GLiNER2
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1")

for name, loader, team in (("ENGINEERING 100 (team=eng)", eng_corpus, "eng"),
                           ("JOHN 7 (team=other)",        john_corpus, "other")):
    texts, gold = loader()
    print(f"\n=== {name}")
    score(ex, texts, gold, team, False, "baseline (docs, no team info)")
    tt = {k:[PRIOR[team]+t for t in v] for k,v in texts.items()}
    score(ex, tt,    gold, team, False, "T1 team stated in prompt")
    score(ex, texts, gold, team, True,  "T2 label set BRANCHED by team")
    score(ex, tt,    gold, team, True,  "T1+T2 both")
