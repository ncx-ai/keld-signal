#!/usr/bin/env python3
"""E2, narrowed: does a subagent run's BRIEF predict whether the run will WRITE CODE?

⚠️ THE OBVIOUS TARGET IS DEAD. "Predict the run's dominant activity" has a majority constant
of 86% (corpus A) / 89% (corpus B) -- almost every delegated run is retrieval-dominant, so
there is nothing to beat. "Will it write any code?" is balanced (54% / 60%) and is the
routing-actionable question anyway: it decides whether the run needs a code-capable model.

DESIGN: fit on CORPUS A, test on CORPUS B -- cross-person by construction, so there is no
holdout to contaminate and no way to tune on the test set. The gold needs no labelling: it is
the deterministic per-request classifier's own output for that run.

⚠️ The fitted terms are suspect on their face. The strongest negative terms on corpus A are
`re-reviewing`, `verdicts`, `out-of-scope`, `critical/important` -- the vocabulary of ONE
subagent-driven-development skill, not of work. If the rule is really learning that skill's
phrasing it will collapse on corpus B, and that collapse is the result worth having.

A SHUFFLED-LABEL control runs alongside: if it does not collapse, the number is void.
"""
import json, re, random, collections, math
A=json.load(open("/tmp/claude-1000/runs_A.json"))
B=json.load(open("/tmp/claude-1000/runs_B.json"))
TOK=re.compile(r"[a-z][a-z._/-]{2,}")
def y(r): return 1 if r["mix"].get("author_code",0)>0 else 0

def fit(train, min_df=12):
    pos=[set(TOK.findall(r["brief"].lower())) for r in train if y(r)]
    neg=[set(TOK.findall(r["brief"].lower())) for r in train if not y(r)]
    cp=collections.Counter(); cn=collections.Counter()
    for d in pos: cp.update(d)
    for d in neg: cn.update(d)
    w={}
    for t in set(cp)|set(cn):
        a,b=cp[t],cn[t]
        if a+b < min_df: continue
        w[t]=math.log(((a+0.5)/(len(pos)+1))/((b+0.5)/(len(neg)+1)))
    return w

def score(brief, w):
    return sum(w.get(t,0.0) for t in set(TOK.findall(brief.lower())))

def evaluate(model, data, thr, name):
    tp=fp=tn=fn=0
    for r in data:
        p = 1 if score(r["brief"], model) > thr else 0
        g = y(r)
        tp += p==1 and g==1; fp += p==1 and g==0
        tn += p==0 and g==0; fn += p==0 and g==1
    n=len(data); acc=(tp+tn)/n
    const=max(sum(y(r) for r in data), n-sum(y(r) for r in data))/n
    prec=tp/max(1,tp+fp); rec=tp/max(1,tp+fn)
    f1=2*prec*rec/max(1e-9,prec+rec)
    print(f"  {name:32s} acc {acc:.3f}  (constant {const:.3f}, LIFT {acc-const:+.3f})  "
          f"P {prec:.2f} R {rec:.2f} F1 {f1:.2f}")
    return acc-const

W=fit(A)
# threshold chosen on the TRAINING corpus only
best=max(((evaluate.__doc__,t) for t in [0]), default=None)
ths=sorted({round(score(r["brief"],W),1) for r in A})
bt, ba = 0.0, -9
for t in ths:
    a=sum(1 for r in A if (score(r["brief"],W)>t)==bool(y(r)))/len(A)
    if a>ba: ba, bt = a, t
print(f"fitted on CORPUS A: {len(W)} terms, threshold {bt:.1f} chosen on A\n")
print("IN-SAMPLE (corpus A — fitted here, reported for reference only):")
evaluate(W, A, bt, "lexical rule")
print("\nOUT-OF-SAMPLE (corpus B — never seen):")
l=evaluate(W, B, bt, "lexical rule")
evaluate({"implement":1.0}, B, 0.5, "one word: 'implement'")
print("\nSHUFFLED-LABEL CONTROL (corpus A labels permuted, refit, tested on B):")
random.seed(11)
sh=[dict(r) for r in A]; ys=[y(r) for r in A]; random.shuffle(ys)
for r,v in zip(sh,ys): r["mix"]={"author_code":1} if v else {"retrieve":1}
evaluate(fit(sh), B, bt, "shuffled rule")
