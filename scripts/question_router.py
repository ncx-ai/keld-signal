#!/usr/bin/env python3
"""Route a common-sense question to the deterministic level that answers it.

    ~/.keld/study-venv/bin/python scripts/question_router.py

Measured on one window, 18 questions: routing accuracy 89% (16/18), inapplicable questions routed
to nothing 3/3, and ZERO fabricated values — because the model never sees a number.

The model is asked to do one small closed thing — pick which recorded dimension a question is
about — and the VALUE then comes from the frame, with its provenance. That inverts the usual
arrangement: instead of handing a model a page of context and hoping the answer inside it is the
one it repeats, the model only interprets the QUESTION, which is a few words long and where it has
no numbers to misread. Measured against the alternative in the same session: asked to name a
ticket over the full block, it returned the window's own reference-event count.
"""
import json, os, socket, subprocess, sys, time, urllib.request
sys.path.insert(0,"/home/dg/keld/keld-signal/scripts")
import pandas as pd
from refseries import characterize

# What each level answers, in the words an analyst would use. This IS the routing table.
LEVELS={
 "workspace":"the repository or working directory the work happened in",
 "repo":"the canonical repository identity, read from the checkout's git config",
 "branch":"the git branch, which is usually the unit of work in flight",
 "component":"the subsystem or area of the codebase",
 "file":"the individual files touched",
 "artifact":"the kind of thing being worked on: code, presentation, spreadsheet, document, pdf",
 "action":"the physical act: reading, editing, committing, testing, converting a document",
 "lang":"the programming language",
 "ext":"the file types by extension",
 "tool":"which assistant tools were used",
 "exe":"which command-line programs were run",
 "service":"which external services or hosts were contacted",
 "skill":"which named skill or procedure was applied",
 "model":"which AI model served the work",
 "tempo":"how much each side was talking, and whether the person was steering or hands-off",
 "cost":"tokens consumed, as a proxy for spend",
}
EXPECT={  # the level a competent analyst would say answers each question
 "What project is this for?":"workspace",
 "Which repository is this?":"workspace",
 "What is the canonical repo name?":"repo",
 "Which branch was this work on?":"branch",
 "What part of the codebase was touched?":"component",
 "Which files were changed?":"file",
 "What kind of document or artefact was being produced?":"artifact",
 "What was the person actually doing — reading, editing, committing?":"action",
 "What programming language was used?":"lang",
 "Which command line tools were run?":"exe",
 "Did this work call out to any external service?":"service",
 "Was a documented procedure or skill followed?":"skill",
 "Which AI model was used?":"model",
 "Was the engineer hands-on or letting it run?":"tempo",
 "How many tokens did this hour consume?":"cost",
 "Which JIRA ticket does this belong to?":"none",
 "Which customer is being billed?":"none",
 "Which sprint is this in?":"none",
}
SCHEMA={"type":"object","additionalProperties":False,
        "properties":{"dimension":{"type":"string","enum":list(LEVELS)+["none"]}},
        "required":["dimension"]}

def serve(model,fn):
    with socket.socket() as s: s.bind(("127.0.0.1",0)); port=s.getsockname()[1]
    p=subprocess.Popen(["llama-server","-m",model,"--ctx-size","4096","--parallel","1",
                        "--no-warmup","--jinja","--flash-attn","off","--port",str(port)],
                       stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    url=f"http://127.0.0.1:{port}"
    try:
        for _ in range(900):
            try: urllib.request.urlopen(url+"/health",timeout=2); break
            except Exception: time.sleep(1)
        return fn(url)
    finally:
        p.terminate()
        try: p.wait(timeout=20)
        except subprocess.TimeoutExpired: p.kill()

def route(url,q):
    menu="\n".join(f"  {k}: {v}" for k,v in LEVELS.items())
    p=("A recorded dataset describes one hour of an engineer's work. These are the dimensions it "
       f"records:\n{menu}\n  none: nothing in the list answers the question\n\n"
       f"QUESTION: {q}\nWhich single dimension answers it?\n")
    body={"messages":[{"role":"user","content":p}],"temperature":0,"max_tokens":48,
          "response_format":{"type":"json_schema","json_schema":{"name":"a","strict":True,
                                                                 "schema":SCHEMA}}}
    r=urllib.request.Request(url+"/v1/chat/completions",data=json.dumps(body).encode(),
                             headers={"Content-Type":"application/json"})
    with urllib.request.urlopen(r,timeout=600) as resp:
        return json.loads(json.load(resp)["choices"][0]["message"]["content"])["dimension"]

def answer(doc,level):
    """The value, from the frame, with where it came from. Never from a model."""
    L={lv:b for r in doc["rungs"].values() for lv,b in r["levels"].items()}
    if level=="cost":
        t=doc.get("tempo") or {}
        return (f"{t.get('assistant_output_tokens',0):,} output tokens", "speaker frame")
    if level=="tempo":
        t=doc.get("tempo") or {}
        em,am=t.get("engineer_messages",0),t.get("assistant_messages",0)
        r=am/em if em else None
        return ((f"{em} engineer vs {am} assistant messages"
                 + (f" — {r:.0f}:1, " + ("hands-off" if r>=15 else "steering") if r else "")),
                "speaker frame")
    if level=="workspace":
        w=doc["window"]
        v=w.get("workspace_of_cwd")
        return (f"{v}", "resolved workspace (no .keld.toml project declared, so the repository "
                        "name is the proxy)")
    blk=L.get(level)
    if not blk or not blk.get("top"):
        return (None,"not recorded in this window")
    tops=blk["top"][:3]
    return (", ".join(f"{i['ref']} {100*i['share']:.0f}%" for i in tops), f"{level} level")

def main():
    d = sys.argv[1] if len(sys.argv) > 1 else "/tmp/refseries-f745121b"
    ent = sys.argv[2] if len(sys.argv) > 2 else "f745121b"
    refs=pd.read_parquet(f"{d}/refs.parquet"); lvls=pd.read_parquet(f"{d}/levels.parquet")
    spk=pd.read_parquet(f"{d}/speaker.parquet"); base=pd.read_parquet(f"{d}/baseline.parquet")
    R=refs[refs.repo==ent]; t=R.bin.min().floor("h")
    while R[(R.bin>=t)&(R.bin<t+pd.Timedelta("60min"))].n.sum()<800: t+=pd.Timedelta("50min")
    doc=characterize(refs,lvls,spk,ent,t,t+pd.Timedelta("60min"),5,base=base)
    print(f"window {t:%d %b %H:%M}Z, {ent}\n")
    rows=[]
    def run(url):
        for q,want in EXPECT.items():
            got=route(url,q)
            val,prov=answer(doc,got) if got!="none" else (None,"routed to nothing")
            ok=got==want
            print(f"  {'OK ' if ok else 'ROUTE'}  {q}")
            print(f"         -> {got:11}{'' if ok else f' (expected {want})'}"
                  f"   answer: {val if val is not None else '(not recorded)'}")
            rows.append({"q":q,"want":want,"got":got,"ok":ok})
    serve(os.path.expanduser("~/.keld/models/gguf/granite-4.1-3b-Q4_K_M.gguf"),run)
    df=pd.DataFrame(rows)
    print(f"\nrouting accuracy {df.ok.mean():.0%} ({int(df.ok.sum())}/{len(df)})")
    print(f"  inapplicable questions routed to 'none': "
          f"{df[df.want=='none'].ok.mean():.0%} ({int(df[df.want=='none'].ok.sum())}/"
          f"{len(df[df.want=='none'])})")
main()
