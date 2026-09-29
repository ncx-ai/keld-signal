#!/usr/bin/env python3
"""FREE-FORM route: don't tell GLiNER2 what to choose from.

Motivation, measured: label WORDING is worth 0.378 on `code.*` and the whole difference between
0/7 and 3/7 on corpus A's set. A closed label set makes the answer a function of our prose. This
asks GLiNER2 to EXTRACT what the work is, in the text's own words, then maps that into the atv1
space by embedding — so our wording stops being the bottleneck.

Two stages, run sequentially so only one model is resident at a time:
  1. GLiNER2 `extract_entities` with open-ended labels (no atv1 vocabulary in the prompt)
  2. Qwen3-Embedding-0.6B (already on disk) — the repo's own last-token/L2 scheme from
     analysis/textembed.py — against the 68 atv1 descriptions PLUS a NULL doc, so a span
     attributes only by BEATING "nothing", which is /attribute's measured discipline.
"""
import csv, json, os, re, sys, collections, datetime as dt, pickle
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_atv1 import DOCS

CSVP=os.path.expanduser("~/Downloads/keld-activity-types-v1.csv")
EMB=os.path.expanduser("~/.keld/models/qwen3-embedding-0.6b")
STAGE1="/tmp/claude-1000/freeform_spans.json"

# open-ended: these name the QUESTION, never the answer set
OPEN = {
  "activity": "the kind of work being performed, as a short verb phrase",
  "artifact": "the thing being produced, changed or examined",
}

def atv1_docs():
    out={}
    for r in csv.DictReader(open(CSVP)):
        v,c=r["verb"],r["context"]
        base=DOCS.get(v, v.replace("."," "))
        out[r["activity_type_id"]] = base if c in ("none","general") else f"{base}, applied to {c} work"
    return out

# ---------------- stage 1 ----------------
if sys.argv[1:2]==["extract"]:
    import importlib.util
    spec=importlib.util.spec_from_file_location("tsr", os.path.join(HERE,"activity_atv1_threesource_run.py"))
    # reuse the packing helpers without running its scoring
    src=open(os.path.join(HERE,"activity_atv1_threesource_run.py")).read().split("from gliner2 import GLiNER2")[0]
    ns={"__name__":"tsr","__file__":os.path.join(HERE,"activity_atv1_threesource_run.py")}
    exec(compile(src,"tsr","exec"), ns)
    pack, load, SPAN, STRIDE = ns["pack"], ns["load"], ns["SPAN"], ns["STRIDE"]

    from gliner2 import GLiNER2
    ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1")
    out={}

    P=os.environ.get("KELD_CORPUS_A_SESSION", "")  # set to the corpus-A session export
    allraw,_=load(P)
    t0=dt.datetime.fromisoformat(allraw[0]["timestamp"].replace("Z","+00:00"))
    tN=dt.datetime.fromisoformat(allraw[-1]["timestamp"].replace("Z","+00:00"))
    start,idx=t0,0
    while start<tN:
        end=start+dt.timedelta(minutes=SPAN)
        raw,turns=load(P,start,end); start+=dt.timedelta(minutes=STRIDE)
        if not turns: continue
        idx+=1; spans=[]
        for s in pack(turns):
            r=ex.extract_entities(s, OPEN)
            for vs in (r.get("entities") or {}).values(): spans+=list(vs)
        out[f"J{idx:02d}"]=spans
        print(f"  J{idx:02d}: {len(spans)} spans  e.g. {spans[:6]}", flush=True)
    json.dump(out, open(STAGE1,"w"), indent=1)
    print(f"wrote {STAGE1}")
    sys.exit()

# ---------------- stage 2 ----------------
import torch
from transformers import AutoModel, AutoTokenizer
tok=AutoTokenizer.from_pretrained(EMB, padding_side="left")
model=AutoModel.from_pretrained(EMB, dtype=torch.float32); model.eval()
def encode(texts, bs=16):
    out=[]
    for i in range(0,len(texts),bs):
        b=tok(texts[i:i+bs], padding=True, truncation=True, max_length=256, return_tensors="pt")
        with torch.no_grad():
            h=model(**b).last_hidden_state[:,-1].float()
            h=torch.nn.functional.normalize(h,p=2,dim=1)
        out+= [r for r in h]
    return out

DESC=atv1_docs()
ids=list(DESC); NULL="no particular activity; nothing in particular is being done"
vecs=encode([DESC[i] for i in ids]+[NULL])
idv, nullv = vecs[:-1], vecs[-1]

spans_by_win=json.load(open(STAGE1))
gold={}
for line in open(os.path.join(HERE,"activity-atv1-corpusA-labels.txt")):
    m=re.match(r"^(J\d\d)\s+(.+)$", line.strip())
    if m: gold[m.group(1)]=[x.strip() for x in m.group(2).split(">")]

def verb_of(g): return g.rsplit(".",1)[0] if g.count(".")>1 else (g if g in ("plan","embed","code.write","code.edit") else g.rsplit(".",1)[0])

prim=inset=0; recs=[]; beat=tot=0
print(f"\n{'win':5s} {'gold primary':28s} free-form -> atv1 (top 3)")
print("-"*110)
for k in sorted(gold):
    sp=[s for s in spans_by_win.get(k,[]) if len(s)>2]
    if not sp: print(f"{k:5s} (no spans)"); continue
    sv=encode(sp)
    votes=collections.Counter()
    for v in sv:
        sims=torch.stack([torch.dot(v,d) for d in idv])
        ns=float(torch.dot(v,nullv)); tot+=1
        best=int(torch.argmax(sims))
        if float(sims[best])>ns: votes[ids[best]]+=1; beat+=1
    if not votes: print(f"{k:5s} all spans lost to NULL"); continue
    n=sum(votes.values())
    ranked=votes.most_common(3)
    gv=[verb_of(g) for g in gold[k]]
    pv=[i.rsplit(".",1)[0] if i.count(".")>1 else (i if i in ("plan","embed","code.write","code.edit") else i.rsplit(".",1)[0]) for i,_ in votes.most_common()]
    prim+= (pv[0]==gv[0]); inset+= (pv[0] in gv); recs.append(sum(1 for g in gv if g in pv)/len(gv))
    print(f"{k:5s} {gold[k][0]:28s} "+" ".join(f"{i}({c/n:.0%})" for i,c in ranked))
N=len(gold)
print(f"\n  primary {prim}/{N}={prim/N:.3f}   in-set {inset/N:.3f}   recall {sum(recs)/max(len(recs),1):.3f}")
print(f"  spans beating NULL: {beat}/{tot} = {beat/max(tot,1):.2f}")
print(f"  baseline to beat (three-source, closed labels): primary 0.429  recall 0.548")
