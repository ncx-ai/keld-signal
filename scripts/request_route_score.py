#!/usr/bin/env python3
"""Score the DETERMINISTIC per-request classifier against 120 blind hand labels."""
import json, re, collections
key={k[0]:(k[1],k[2]) for k in json.load(open("/tmp/claude-1000/vf/req_key.json"))}
gold={m.group(1):m.group(2).strip()
      for m in re.finditer(r"^(R\d{3})\s+(\S+)\s*$", open("scripts/request-route-hand-labels.txt").read(), re.M)}
assert len(gold)==120, len(gold)
CLASSES=sorted({v for v in gold.values()} | {c for _,c in key.values()})

rows=[(r, gold[r], key[r][1]) for r in sorted(gold)]
hit=sum(1 for _,g,p in rows if g==p); n=len(rows)
print(f"n={n}   overall agreement with the hand labels: {hit}/{n} = {hit/n:.3f}")
print("⚠️ the sample is stratified 15/class, so this is a MACRO figure, not population accuracy.\n")
print(f'{"class":14s} {"gold":>5s} {"pred":>5s} {"hit":>4s} {"prec":>6s} {"rec":>6s} {"F1":>6s}')
P=R=F=0
for c in CLASSES:
    g=sum(1 for _,x,_ in rows if x==c); p=sum(1 for _,_,y in rows if y==c)
    h=sum(1 for _,x,y in rows if x==c==y)
    pr=h/p if p else 0.0; rc=h/g if g else 0.0
    f1=2*pr*rc/(pr+rc) if pr+rc else 0.0
    P+=pr; R+=rc; F+=f1
    print(f'{c:14s} {g:5d} {p:5d} {h:4d} {pr:6.3f} {rc:6.3f} {f1:6.3f}')
k=len(CLASSES)
print(f'\nmacro precision {P/k:.3f}   macro recall {R/k:.3f}   macro F1 {F/k:.3f}')
conf=collections.Counter(f"{g}->{p}" for _,g,p in rows if g!=p)
print(f'\nconfusions ({sum(conf.values())} errors):')
for c,v in conf.most_common(12): print(f'   {c:34s} {v}')
