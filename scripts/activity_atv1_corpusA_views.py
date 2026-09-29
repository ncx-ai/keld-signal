#!/usr/bin/env python3
"""Blind views for corpus A's product/marketing session, on the SAME 60/50 grid as the
engineering frame so the two gold sets pool.

Blind by the same rules attempt four used: user prompts + assistant prose only, fenced code
elided, every tool_use / tool_result / thinking block dropped. No tool name, no file path, no
action, no count reaches the labeller.
"""
import json, os, re, sys, datetime as dt

SPAN, STRIDE = 60, 50
P = os.environ.get("KELD_CORPUS_A_SESSION", "")  # set to the corpus-A session export
FENCE = re.compile(r"```.*?```", re.S)

def text_of(content):
    if isinstance(content, str): return content
    if not isinstance(content, list): return ""
    out = []
    for b in content:
        if isinstance(b, dict) and b.get("type") == "text":
            out.append(b.get("text", ""))
    return "\n".join(out)

def bound(t, n):
    t = re.sub(r"\s+", " ", t).strip()
    if len(t) <= n: return t
    cut = t[:n]
    i = max(cut.rfind(". "), cut.rfind("! "), cut.rfind("? "))
    return (cut[:i+1] if i > n*0.4 else cut) + f" [... {len(t)-n} chars omitted]"

turns = []
for line in open(P, errors="ignore"):
    try: o = json.loads(line)
    except Exception: continue
    if o.get("type") not in ("user","assistant") or not o.get("timestamp"): continue
    turns.append(o)
turns.sort(key=lambda o: o["timestamp"])
t0 = dt.datetime.fromisoformat(turns[0]["timestamp"].replace("Z","+00:00"))
tN = dt.datetime.fromisoformat(turns[-1]["timestamp"].replace("Z","+00:00"))

start, idx = t0, 0
while start < tN:
    end = start + dt.timedelta(minutes=SPAN)
    sl = [o for o in turns
          if start <= dt.datetime.fromisoformat(o["timestamp"].replace("Z","+00:00")) < end]
    here, start = start, start + dt.timedelta(minutes=STRIDE)
    if not sl: continue
    idx += 1
    prompts, prose = [], []
    for o in sl:
        t = text_of((o.get("message") or {}).get("content"))
        if not t.strip(): continue
        if o.get("type") == "user":
            if not t.lstrip().startswith(("<", "Base directory for this skill")):
                prompts.append(bound(t, 700))
        else:
            p = bound(FENCE.sub(" ", t), 400)
            if p: prose.append(p)
    print("="*100)
    print(f"[J{idx:02d}] corpusA#{here:%Y%m%dT%H%M}   prompts={len(prompts)} assistant_turns={len(prose)}")
    print("="*100)
    print()
    for p in prompts: print(f"USER: {p}\n")
    if prose:
        print("--- assistant prose ---")
        for p in prose: print(f"* {p}")
    print()
