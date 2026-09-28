#!/usr/bin/env python3
"""Score the post-fix classifier on the HOLDOUT — the honest number."""
import json, re, collections, sys
R=json.load(open("/tmp/claude-1000/vf/requests.json"))
key={r[0]:(r[1], R[r[1]]["cls"]) for r in json.load(open("/tmp/claude-1000/vf/holdout_key.json"))}
gold={m.group(1):m.group(2).strip() for m in re.finditer(r"^(H\d{3})\s+(\S+)\s*$",
      open("scripts/request-route-holdout-labels.txt").read(), re.M)}
assert len(gold)==80, len(gold)
rows=[(h,gold[h],key[h][1]) for h in sorted(gold)]
n=len(rows); hit=sum(1 for _,g,p in rows if g==p)
CL=sorted({g for _,g,_ in rows}|{p for _,_,p in rows})
print(f"HOLDOUT  n={n}   agreement {hit}/{n} = {hit/n:.3f}")
print(f'{"class":14s} {"gold":>5s} {"pred":>5s} {"hit":>4s} {"prec":>6s} {"rec":>6s} {"F1":>6s}')
P=Rr=F=0
for c in CL:
    g=sum(1 for _,x,_ in rows if x==c); p=sum(1 for _,_,y in rows if y==c)
    h=sum(1 for _,x,y in rows if x==c==y)
    pr=h/p if p else 0.0; rc=h/g if g else 0.0
    f1=2*pr*rc/(pr+rc) if pr+rc else 0.0
    P+=pr; Rr+=rc; F+=f1
    print(f'{c:14s} {g:5d} {p:5d} {h:4d} {pr:6.3f} {rc:6.3f} {f1:6.3f}')
k=len(CL); print(f'\nmacro precision {P/k:.3f}   macro recall {Rr/k:.3f}   macro F1 {F/k:.3f}')
pop=collections.Counter(r["cls"] for r in R.values()); N=sum(pop.values())
prec={c:(sum(1 for h,g,p in rows if p==c==g)/max(1,sum(1 for _,_,p in rows if p==c))) for c in pop}
print(f'  population-weighted expected accuracy: '
      f'{sum(pop[c]/N*prec[c] for c in pop):.3f}')
conf=collections.Counter(f"{g}->{p}" for _,g,p in rows if g!=p)
print(f'\nconfusions ({sum(conf.values())}): {conf.most_common(10)}')
