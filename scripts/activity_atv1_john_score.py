#!/usr/bin/env python3
"""Score GLiNER2 against John's multi-label gold — the only non-engineering ground truth.

Reports BOTH framings so the difference is visible:
  top1 == primary   the mutually-exclusive score the engineering frame used
  top1 in gold set  the multi-label score the repo owner asked for
Also runs the CONTEXT pass, which the engineering corpus could not test at all.
"""
import json, re, os, sys, subprocess, collections

HERE = os.path.dirname(os.path.abspath(__file__))
VIEWS = "/tmp/claude-1000/john-views.txt"
LABELS = os.path.join(HERE, "activity-atv1-john-labels.txt")
sys.path.insert(0, HERE)
from activity_atv1 import RICH, window_text, SENT  # reuse the measured description set

gold = {}
for line in open(LABELS):
    m = re.match(r"^(J\d\d)\s+(.+)$", line.strip())
    if m: gold[m.group(1)] = [x.strip() for x in m.group(2).split(">")]

# rebuild each window's text from the blind views, so the model sees exactly what the labeller saw
wins, cur, key = {}, [], None
for line in open(VIEWS):
    m = re.match(r"^\[(J\d\d)\]", line)
    if m:
        if key: wins[key] = " ".join(cur)
        key, cur = m.group(1), []
        continue
    if key and not line.startswith("="):
        t = line.strip()
        if t.startswith(("USER:", "*")): cur.append(re.sub(r"^(USER:|\*)\s*", "", t))
if key: wins[key] = " ".join(cur)

from gliner2 import GLiNER2
ex = GLiNER2.from_pretrained("fastino/gliner2-large-v1")
VERB = [f"{v}: {RICH[v]}" for v in RICH]
CTXS = ["general","legal","media","financial","marketing","medical","scientific","sales","support","operations"]
CTX = [f"{c}: work applied to {c} ends" for c in CTXS] + ["NONE: no particular professional domain"]

def legal_id(verb, ctx):
    import csv
    return verb, ctx

prim_hit = set_hit = 0
print(f"{'win':5s} {'gold primary':26s} {'predicted verb':18s} {'ctx':12s} ok?")
print("-"*78)
for k in sorted(gold):
    t = window_text({"prompts":[wins.get(k,"")], "prose":[]})
    if not t: print(f"{k:5s} (no text)"); continue
    v = ex.classify_text(t, {"verb":{"labels":VERB}}, include_confidence=True)["verb"]["label"].split(":")[0]
    c = ex.classify_text(t, {"ctx":{"labels":CTX}}, include_confidence=True)["ctx"]["label"].split(":")[0]
    gold_verbs = [g.rsplit(".",1)[0] if g.count(".")>1 else (g if g in ("plan","embed","code.write","code.edit") else g.rsplit(".",1)[0]) for g in gold[k]]
    gold_prim_verb = gold_verbs[0]
    p_ok = (v == gold_prim_verb); s_ok = (v in gold_verbs)
    prim_hit += p_ok; set_hit += s_ok
    mark = "PRIM" if p_ok else ("set" if s_ok else "MISS")
    print(f"{k:5s} {gold[k][0]:26s} {v:18s} {c:12s} {mark}")
n = len(gold)
print()
print(f"  top1 == gold PRIMARY  : {prim_hit}/{n} = {prim_hit/n:.3f}   (mutually-exclusive framing)")
print(f"  top1 IN gold SET      : {set_hit}/{n} = {set_hit/n:.3f}   (multi-label framing)")
print(f"  gold contexts present : {collections.Counter(g.split('.')[-1] for gs in gold.values() for g in gs).most_common()}")
