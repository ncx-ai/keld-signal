#!/usr/bin/env python3
"""Is the block-level failure the VOCABULARY or the UNIT? The retrospective remap said it is
not the vocabulary: merging atv1's 17 verbs into 6/5/4/2 classes moved accuracy 0.350 -> 0.617
while moving the majority constant exactly as far, so LIFT stayed ~0 at every granularity.

The remaining suspect is what the model is being shown. USER TEXT IS 7.0% OF THE INPUT
(23,173 chars against 309,853). The shipped `task_type` facet measures 0.733 on PROMPTS; this
frame packs whole blocks, 93% assistant prose, which is where the paths, SHAs, PR numbers and
test counts live -- the lexical material that drives the measured code.edit over-prediction
(26 predicted against 14 gold).

Three arms over the SAME 60 blocks and the SAME committed labels. Only the input differs.
  U  user turns only            (10 of 60 blocks have none -> stated abstention, not a guess)
  A  assistant turns only       (the control: if A ~= ALL, user text was never being read)
  L  all turns                  (= the 0.350 already measured, re-run here for one-run parity)
"""
import json, os, re, sys, collections
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_atv1 import DOCS
from verb_family_score import pack, LABELS, FRAME          # same packer, same sentence rule

raw=open(LABELS).read(); raw=raw[raw.find("# LIVE LABELS"):]
gold={m.group(1):[x.strip() for x in m.group(2).split(">")]
      for m in re.finditer(r"^(V\d{3})\s+(.+)$", raw, re.M)}
frame={w["id"]:w for w in json.load(open(FRAME))}

from gliner2 import GLiNER2
import torch
dev="cuda" if torch.cuda.is_available() else "cpu"
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1", map_location=dev)
VERB_L=[f"{v}: {DOCS[v]}" for v in DOCS]
print(f"device: {dev}   blocks: {len(gold)}", flush=True)

ARMS={"U user-only":  lambda t:[x for x in t if x[0].upper()=="USER"],
      "A asst-only":  lambda t:[x for x in t if x[0].upper()!="USER"],
      "L all turns":  lambda t:t}
out={}
for name,filt in ARMS.items():
    hit=sethit=0; n=0; abst=0; pd=collections.Counter(); conf=collections.Counter(); rows=[]
    for k in sorted(gold):
        w=frame.get(k)
        if not w: continue
        turns=filt(w["turns"])
        if not turns: abst+=1; continue          # abstain, never guess
        vv=collections.Counter()
        for s in pack(turns):
            vv[ex.classify_text(s,{"verb":{"labels":VERB_L}},
                                include_confidence=True)["verb"]["label"].split(":")[0]]+=1
        if not vv: abst+=1; continue
        top=vv.most_common(1)[0][0]; g=gold[k]
        n+=1; hit+=(top==g[0]); sethit+=(top in g); pd[top]+=1
        if top!=g[0]: conf[f"{g[0]}->{top}"]+=1
        rows.append((k,g[0],top,dict(vv)))
    base=collections.Counter(gold[k][0] for k,_,_,_ in rows).most_common(1)[0][1]/n
    print(f"\n{name:14s} scored={n:2d} abstained={abst:2d}  acc={hit/n:.3f}  "
          f"in-set={sethit/n:.3f}  const={base:.3f}  LIFT={hit/n-base:+.3f}", flush=True)
    print(f"{'':14s} pred dist : {pd.most_common(6)}", flush=True)
    print(f"{'':14s} confusions: {conf.most_common(4)}", flush=True)
    out[name]=rows
json.dump(out, open("/tmp/claude-1000/vf/unit_probe.json","w"), indent=1)
print("\nwrote /tmp/claude-1000/vf/unit_probe.json", flush=True)
