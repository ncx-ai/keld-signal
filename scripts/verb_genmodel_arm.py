#!/usr/bin/env python3
"""⚠️ THIS ARM WAS WRITTEN AND NEVER RAN. Committed as a record of a designed but
unexecuted experiment, not as a result -- there is no number from it anywhere.

Why it did not run: the system `llama-cpp` package ships `libggml-base.so` and `libggml.so`
but NO backend (`libggml-cpu.so` / `libggml-cuda.so`), so `llama-server` cannot load any
model at all ("no backends are loaded"). The transformers fallback -- downloading
Qwen3-1.7B -- stalled at 1.9 GB of ~3.4 GB. By then the per-request reframe had made the
block-level question this arm asks the wrong one, so it was not restarted.

It remains the right shape for the question if it is ever asked again, INCLUDING its
shuffled-label control, and its two deliberate easings are stated in the docstring below.

Original docstring follows.
"""
"""Does a small GENERATIVE model clear GLiNER2's measured ceiling on block prose?

Fourteen GLiNER2 measurements (6 vocabularies x granularity, 3 input variants, 2 family
derivations) all land in lift +0.000..+0.117. That bounds a bi-encoder trained on
GPT-4o-annotated news/law/wiki/pubmed/arxiv with ZERO developer text. It says nothing about
a decoder that can actually read the block.

CONTROLLED: same 60 blocks, same committed labels, same 17-verb vocabulary, same DOCS
descriptions GLiNER2 was given. Only the model and the framing change.

TWO DIFFERENCES ARE DELIBERATE AND BOTH ARE STATED RATHER THAN HIDDEN:
  1. WHOLE BLOCK IN ONE CALL. Max block is 30,076 chars (~7.5k tokens) against a 32k window,
     so nothing is cut -- which also removes sub-window voting, and with it the 25% of blocks
     that tied at the top and the 8 of 21 hits that won by a single vote.
  2. RANKED OUTPUT, matching the gold's own shape. GLiNER2 emits one label per call and the
     ranking was reconstructed from votes; this model is asked the question the labels answer.
Both make this arm EASIER, so a null result is decisive and a win is not yet attributable to
the model class alone. A matched sub-window arm is the follow-up if it wins.

Also run: SHUFFLED-LABEL CONTROL. Descriptions are permuted across verb ids. If accuracy does
not collapse, the arm is reading register rather than content and the number is void.
"""
import json, os, re, sys, collections, urllib.request, random
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_atv1 import DOCS

URL="http://127.0.0.1:8099/v1/chat/completions"
VERBS=list(DOCS)
LABELS=os.path.join(HERE,"verb-family-hand-labels.txt")
raw=open(LABELS).read(); raw=raw[raw.find("# LIVE LABELS"):]
gold={m.group(1):[x.strip() for x in m.group(2).split(">")]
      for m in re.finditer(r"^(V\d{3})\s+(.+)$", raw, re.M)}
frame={w["id"]:w for w in json.load(open("/tmp/claude-1000/vf/frame.json"))}

SCHEMA={"type":"object","properties":{
    "activities":{"type":"array","minItems":1,"maxItems":4,
                  "items":{"type":"string","enum":VERBS}}},
    "required":["activities"],"additionalProperties":False}

def ask(block_text, docs):
    menu="\n".join(f"- {v}: {docs[v]}" for v in VERBS)
    sysmsg=("You classify a block of work from a developer's AI-assistant transcript.\n"
            "Judge by the ARTIFACT produced and the INTENT, not the mechanism. Writing a "
            "script to publish a document is publishing, not coding, if the document is the "
            "point; writing the publisher itself is coding.\n\n"
            f"Choose from these activities ONLY:\n{menu}\n\n"
            "Return the activities that genuinely occupy part of this block, MOST PROMINENT "
            "FIRST. Two or three is normal. Do not pad the list.")
    body={"messages":[{"role":"system","content":sysmsg},
                      {"role":"user","content":block_text}],
          "temperature":0.0,"max_tokens":120,
          "response_format":{"type":"json_schema",
                             "json_schema":{"name":"acts","strict":True,"schema":SCHEMA}}}
    req=urllib.request.Request(URL, json.dumps(body).encode(),
                               {"Content-Type":"application/json"})
    with urllib.request.urlopen(req, timeout=600) as r:
        out=json.loads(r.read())["choices"][0]["message"]["content"]
    return json.loads(out)["activities"]

def run(name, docs):
    hit=sset=0; rec=[]; pred1=collections.Counter(); conf=collections.Counter(); rows=[]
    for k in sorted(gold):
        w=frame[k]
        txt="\n\n".join(f"{r}: {t}" for r,t in w["turns"])
        try: acts=ask(txt, docs)
        except Exception as e:
            print(f"  {k} FAILED {type(e).__name__}: {e}", flush=True); continue
        g=gold[k]; top=acts[0]
        hit+=(top==g[0]); sset+=(top in g); pred1[top]+=1
        rec.append(sum(1 for x in g if x in acts)/len(g))
        if top!=g[0]: conf[f"{g[0]}->{top}"]+=1
        rows.append((k,g,acts))
    n=len(rows)
    base=collections.Counter(gold[k][0] for k,_,_ in rows).most_common(1)[0][1]/n
    print(f"\n{name}: n={n}")
    print(f"  top1 == gold primary : {hit}/{n} = {hit/n:.3f}   (const {base:.3f}, LIFT {hit/n-base:+.3f})")
    print(f"  top1 in gold set     : {sset/n:.3f}")
    print(f"  gold-set recall      : {sum(rec)/n:.3f}")
    print(f"  pred dist            : {pred1.most_common(8)}")
    print(f"  confusions           : {conf.most_common(5)}", flush=True)
    return rows

real=run("REAL descriptions", DOCS)
json.dump(real, open("/tmp/claude-1000/vf/gen_real.json","w"), indent=1)

vals=list(DOCS.values()); random.seed(7); random.shuffle(vals)
SHUF=dict(zip(VERBS, vals))
shuf=run("SHUFFLED-LABEL CONTROL", SHUF)
json.dump(shuf, open("/tmp/claude-1000/vf/gen_shuf.json","w"), indent=1)
print("\ndone", flush=True)
