#!/usr/bin/env python3
"""DERIVE routing classes from the deterministic reference levels, instead of hand-designing them.

Pre-registration: ~/keld/refseries-context/facets/ROUTING-CLASS-PREREGISTRATION.md, written before
any clustering ran. Results: ~/keld/refseries-context/facets/ROUTING-CLASS-RESULTS.md.

## Why this is a different question from the one just refuted

`activity_type` was refuted deterministically (0.218 against a 0.538 constant, lift -0.321,
`transform` predicted 36 times and right zero times). The diagnosis was the LABEL BINDING, not
tuning and not coverage: `action` records which physical act touched a file, while `Activities`
divides on what the change MEANT. So this script does not test a hand-designed vocabulary against
the levels. It asks what the levels can actually separate, and then whether those partitions
correspond to different routing targets.

The existence proof that a coarser observable vocabulary works: collapsed to "was this hour
authoring or not", the same action level scored 0.756 against the same 0.538 baseline (+0.218).
Six-way failed; two-way worked. The open question is the natural granularity, which is what a
sweep over k answers.

## The prior refutation that sets the bar

Semantic clustering was already refuted AS THE ATTRIBUTION MECHANISM (2026-08-22 handoff): five
thresholds swept, no partition ever had both mass and coherence — 0.04 gave 137 clusters with 107
singletons; 0.15 gave a largest cluster holding 38% of unrelated work. Root cause: 160 of 200
windows carried a genuinely distinct topic. THAT clustered free-text topics. This clusters
low-dimensional, highly repetitive level vectors, which is a different object and should partition
far better — but the failure shape is identical, which is why rules 1 and 2 exist and why every
constant below is fixed in this file before a number exists.

## Everything decided in advance, and why each number is what it is

Read this as the pre-registration made executable. `git log` proves it preceded the measurement.

  SPAN / STRIDE 60 / 50 min   Established earlier in this series: stride must not divide span
                              (median distance from a real transition to a window edge 22 -> 12
                              min). Not re-tuned here.
  MIN_ACTION_EVENTS 5         == window.MIN_EVIDENCE, itself derived (ceil(log .05 / log .5)): the
                              first n at which a unanimous window is distinguishable from a coin
                              flip. A proportion vector over 1-4 events is noise, so such windows
                              are EXCLUDED and counted, never silently kept.
  K_RANGE 2..12               Two is the granularity that already survived; twelve is past the
                              point where rule 1's singleton clause must bite.
  GROUP_EQUAL_WEIGHT          Each of the six FEATURE GROUPS gets equal total weight (each column
                              z-scored, then divided by sqrt(group size)). Without it action-mix's
                              ~22 columns would outvote volume's 1 by 22:1 and the sweep would be
                              measuring the shape of the vocabulary rather than of the work.
  SPLIT_BY = session          The held-out split is BY TRANSCRIPT, not by window, because stride
                              (50) < span (60): adjacent windows share 10 minutes of events, so a
                              per-window split would put overlapping near-duplicates on both sides
                              and inflate stability by leakage.
  MAX_CLUSTER_SHARE 0.50      Rule 1. The catch-all that killed the last attempt.
  NEAR_SINGLETON_SHARE 0.01   Rule 1 says "singleton or near-singleton"; this is the reading fixed
  MAX_SINGLETON_SHARE 0.20    here: a cluster holding under 1% of windows is near-singleton, and
                              over 20% of windows in such clusters fails.
  STABILITY_FLOOR 0.70        Rule 2, verbatim.
  ETA2_MATERIAL 0.10          Rule 3 says clusters must differ "materially" on >= 2 routing axes.
                              This is the reading fixed here: eta^2 (between-cluster share of
                              variance) >= 0.10, i.e. between the conventional medium (0.06) and
                              large (0.14) effect. MIN_AXES 2, verbatim.

## Deterministic features only

Per the pre-registration: action mix, artifact mix, lang, breadth, volume, verification. No text,
no model, no prose-derived entities — the `term` level is dropped in the worker for exactly that
reason (it regex-extracts names from message text even with spaCy disabled).

ONE measure sits outside the fitted matrix on purpose: `interactivity` (user turns per window) is
reported as a routing AXIS but is NOT a feature, because the pre-registered feature list does not
contain it. Adding it to the fit after the fact would be the tuning this study is trying not to
acquire; leaving it out of the axes would drop one of the five axes rule 3 is scored on. A turn
COUNT is not text.

## Stated limitation, up front

The routing axes are functions of the same features the clustering fits, so "clusters separate on
axis X" is NOT independent evidence — it is a check that the partition is more than
one-dimensional. It cannot be more than that, and rule 3 is written as a floor, not a discovery.
Separately, "routable to model X" has no ground truth in this corpus, so nothing here can show that
routing on a class saves money or improves answers.
"""
import argparse
import collections
import concurrent.futures
import glob
import hashlib
import json
import math
import os
import sys
from datetime import UTC, datetime, timedelta

import numpy as np
from scipy.cluster.hierarchy import fcluster, linkage
from scipy.cluster.vq import kmeans2
from scipy.optimize import linear_sum_assignment

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.levels import events_for_turns                      # noqa: E402
from app.analysis.reconcile import reconcile                          # noqa: E402
from app.analysis.transcript import iter_turns                        # noqa: E402
from app.analysis.vocab import (ARTIFACT_DIR, ARTIFACT_EXT,           # noqa: E402
                                ARTIFACT_SKILL, EXE_ACTION, TOOL_ACTION)
from app.analysis.window import MIN_EVIDENCE                          # noqa: E402

SPAN, STRIDE = 60, 50
SEED = 0
COMPONENT_DEPTH = 2
MIN_ACTION_EVENTS = MIN_EVIDENCE                 # == 5, derived; see window.MIN_EVIDENCE
K_RANGE = list(range(2, 13))
RESTARTS = 20
MAX_CLUSTER_SHARE = 0.50
NEAR_SINGLETON_SHARE = 0.01
MAX_SINGLETON_SHARE = 0.20
STABILITY_FLOOR = 0.70
ETA2_MATERIAL = 0.10
MIN_AXES = 2
N_LANG_ONEHOT = 6
N_EXAMPLES = 3

# The closed action vocabulary, read out of vocab.py rather than retyped, plus the four acts
# `action_for` only ever produces from a VERB (they appear in neither table's values).
ACTIONS = sorted({a for a in TOOL_ACTION.values() if a} | set(EXE_ACTION) |
                 {"commit", "sync with remote", "test", "build", "install"})
ARTIFACTS = sorted(set(ARTIFACT_EXT) | set(ARTIFACT_DIR) | set(ARTIFACT_SKILL.values()) |
                   {"code"})
AUTHORING = ("create", "edit", "transform", "publish", "convert a document")
INSPECTING = ("read", "search", "fetch")
VERIFYING = ("test", "build", "run code")


def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


# --------------------------------------------------------------------------- frame

def _one(job):
    """One transcript -> (sha, path, kept rows, pending paths, first/last turn epoch, user-turn
    times). Parsed ONCE and sliced later (the pattern established in f63d524).

    `term` rows are dropped here: they are regex-extracted from message TEXT, which this study
    excludes by construction. spaCy is disabled (`nlp=None`) for the same reason and because
    nothing downstream reads terms.

    THE SESSION FIELD IS OVERWRITTEN WITH A UNIQUE FILE ID, and that is load-bearing. Windows are
    keyed on (transcript, start), and a reconciled row's only handle back to its transcript is
    `base[1]`, which `levels.events_for_turns` sets to `os.path.basename(path)[:8]`. In this
    corpus that prefix is NOT unique: subagent transcripts are named `agent-<hash>.jsonl`, so
    `agent-a6`, `agent-ad`, `agent-ac` … collide, and 445 of 500 files share a prefix with another
    file. Keeping the prefix silently merged those files' events into one pseudo-transcript
    (measured: the frame came out at 550 windows against a true 1,022, a 46% loss) — exactly the
    plausible-wrong-number failure this study keeps hitting. The id is per-file and only used as a
    grouping key; `reconcile` keys its declared-path index on (root, repo), never on session, so
    cross-transcript reattribution is unaffected.
    """
    path, root, fid = job
    with open(path, "rb") as fh:
        sha = hashlib.sha256(fh.read()).hexdigest()
    turns = [o for o in iter_turns(path) if o.get("timestamp")]
    if not turns:
        return sha, path, [], [], None, None, []
    rows, pending, _n = events_for_turns(turns, path, root, (), None)
    keep = [r[:1] + (fid,) + r[2:] for r in rows if r[5] == "ref" and r[6] != "term"]
    pending = [((b[0], fid) + b[2:], rel, fi, rk) for b, rel, fi, rk in pending]
    users = [_epoch(o["timestamp"]).timestamp() for o in turns if o.get("type") == "user"]
    t0, tN = _epoch(turns[0]["timestamp"]), _epoch(turns[-1]["timestamp"])
    return sha, path, keep, pending, t0, tN, users


def features_of(counts, distinct, n_user):
    """A window's raw, human-readable feature record. Proportions and counts only."""
    act = counts["action"]
    art = counts["artifact"]
    lang = counts["lang"]
    n_act = sum(act.values())
    n_art = sum(art.values())
    n_lang = sum(lang.values())
    top_lang, top_lang_n = (lang.most_common(1) or [(None, 0)])[0]
    return {
        "n_actions": n_act,
        "n_artifact_events": n_art,
        "volume": counts["_total"],
        "action_mix": {a: round(act.get(a, 0) / n_act, 4) for a in ACTIONS if act.get(a)},
        "artifact_mix": {a: round(art.get(a, 0) / n_art, 4)
                         for a in ARTIFACTS if n_art and art.get(a)},
        "lang_top": top_lang,
        "lang_top_share": round(top_lang_n / n_lang, 4) if n_lang else 0.0,
        # "whether one dominates at all" — the package's own definition of dominance, so this
        # study and production mean the same thing by the word.
        "lang_dominates": bool(n_lang >= MIN_EVIDENCE and top_lang_n / n_lang >= 0.5),
        "n_distinct_lang": len(lang),
        "distinct_files": distinct["file"],
        "distinct_dirs": distinct["dir"],
        "distinct_exes": distinct["exe"],
        "verification": bool(sum(act.get(v, 0) for v in VERIFYING)),
        "verify_share": round(sum(act.get(v, 0) for v in VERIFYING) / n_act, 4) if n_act else 0.0,
        "authoring_share": round(sum(act.get(a, 0) for a in AUTHORING) / n_act, 4)
                           if n_act else 0.0,
        "inspecting_events": sum(act.get(a, 0) for a in INSPECTING),
        "n_user_turns": n_user,
        "top_actions": [f"{a}:{n}" for a, n in act.most_common(5)],
        "top_artifacts": [f"{a}:{n}" for a, n in art.most_common(3)],
    }


def frame(roots, out):
    files = sorted(f for r in roots
                   for f in glob.glob(os.path.join(r, "**", "*.jsonl"), recursive=True))
    if not files:
        sys.exit("no transcripts under " + ", ".join(roots))
    jobs = [(p, os.path.dirname(os.path.dirname(p)), f"t{i:04d}") for i, p in enumerate(files)]
    workers = max(1, min(int((os.cpu_count() or 4) * 0.8), len(jobs)))
    print(f"parsing {len(jobs)} transcripts on {workers} worker(s)")
    with concurrent.futures.ProcessPoolExecutor(max_workers=workers) as ex:
        results = list(ex.map(_one, jobs))

    rows, pending, spans, users, names = [], [], {}, {}, {}
    seen, n_dup, n_empty = set(), 0, 0
    for (sha, path, r, pnd, t0, tN, us), (_p, _r, fid) in zip(results, jobs):
        if sha in seen:
            n_dup += 1
            continue
        seen.add(sha)
        if t0 is None:
            n_empty += 1
            continue
        rows += r
        pending += pnd
        spans[fid] = (t0, tN)
        users[fid] = us
        names[fid] = os.path.basename(path)[:8]
    recon, stats = reconcile(pending, COMPONENT_DEPTH)
    rows += recon
    print(f"transcripts={len(spans)} duplicates={n_dup} empty={n_empty} rows={len(rows)}")
    if stats:
        print("  reconciled: " + ", ".join(f"{k}={v}" for k, v in sorted(stats.items())))

    by_sess = collections.defaultdict(list)
    for r in rows:
        by_sess[r[1]].append(r)

    n_win = 0
    with open(out, "w") as fh:
        for sess, (t0, tN) in sorted(spans.items()):
            srows = sorted(by_sess.get(sess, []), key=lambda r: r[0])
            times = [r[0] for r in srows]
            uts = sorted(users.get(sess, []))
            start = t0
            while start < tN or start == t0:
                end = start + timedelta(minutes=SPAN)
                a, b = start.timestamp(), end.timestamp()
                lo = np.searchsorted(times, a, "left")
                hi = np.searchsorted(times, b, "left")
                sl = srows[lo:hi]
                nu = int(np.searchsorted(uts, b, "left") - np.searchsorted(uts, a, "left"))
                start += timedelta(minutes=STRIDE)
                if not sl:
                    continue
                counts = collections.defaultdict(collections.Counter)
                distinct = collections.defaultdict(set)
                total = n_side = 0
                for r in sl:
                    counts[r[6]][r[7]] += r[8]
                    distinct[r[6]].add(r[7])
                    total += r[8]
                    n_side += 1 if r[4] else 0
                counts["_total"] = total
                repo = (counts["workspace"].most_common(1) or [(None, 0)])[0][0]
                rec = features_of(counts,
                                  {k: len(distinct[k]) for k in ("file", "dir", "exe")}, nu)
                rec["wid"] = f"{sess}-{a:.0f}"
                rec["label"] = (f"{names[sess]}-"
                                f"{datetime.fromtimestamp(a, UTC):%Y%m%dT%H%M}")
                rec["session"] = sess
                rec["repo"] = repo
                # NOT features and never fitted. Two facts already recorded on every transcript
                # line, carried along so the derived partition can be checked against what the
                # store ALREADY knows — the failure the 2026-08-22 handoff named ("digest-only
                # answers reconstruct `component` and `branch`, columns we already compute").
                rec["sidechain_share"] = round(n_side / len(sl), 4)
                rec["agent_file"] = names[sess].startswith("agent-")
                n_win += 1
                fh.write(json.dumps(rec) + "\n")
    print(f"windows={n_win} -> {out}")


# --------------------------------------------------------------------------- matrix

def matrix(recs, langs):
    """Feature records -> (X, column names, group of each column). Raw, unscaled."""
    cols, groups = [], []
    cols += [f"action:{a}" for a in ACTIONS];        groups += ["action"] * len(ACTIONS)
    cols += [f"artifact:{a}" for a in ARTIFACTS];    groups += ["artifact"] * len(ARTIFACTS)
    lang_cols = list(langs) + ["other", "none"]
    cols += [f"lang:{l}" for l in lang_cols] + ["lang:dominates"]
    groups += ["lang"] * (len(lang_cols) + 1)
    cols += ["breadth:files", "breadth:dirs", "breadth:exes"]; groups += ["breadth"] * 3
    cols += ["volume:events"];                       groups += ["volume"]
    cols += ["verify:present"];                      groups += ["verify"]
    X = np.zeros((len(recs), len(cols)))
    for i, r in enumerate(recs):
        j = 0
        for a in ACTIONS:
            X[i, j] = r["action_mix"].get(a, 0.0); j += 1
        for a in ARTIFACTS:
            X[i, j] = r["artifact_mix"].get(a, 0.0); j += 1
        top = r["lang_top"]
        which = top if top in langs else ("none" if top is None else "other")
        for l in lang_cols:
            X[i, j] = 1.0 if l == which else 0.0; j += 1
        X[i, j] = 1.0 if r["lang_dominates"] else 0.0; j += 1
        X[i, j] = math.log1p(r["distinct_files"]); j += 1
        X[i, j] = math.log1p(r["distinct_dirs"]); j += 1
        X[i, j] = math.log1p(r["distinct_exes"]); j += 1
        X[i, j] = math.log1p(r["volume"]); j += 1
        X[i, j] = 1.0 if r["verification"] else 0.0; j += 1
    return X, cols, groups


def scaler(X, groups):
    """z-score per column, then equal total weight per GROUP (see the header: without this the
    22-column action group outvotes the 1-column volume group 22:1)."""
    mu = X.mean(0)
    sd = X.std(0)
    sd[sd == 0] = 1.0
    size = collections.Counter(groups)
    w = np.array([1.0 / math.sqrt(size[g]) for g in groups])
    return mu, sd, w


def apply_scaler(X, s):
    mu, sd, w = s
    return (X - mu) / sd * w


def fit(Z, k, restarts=RESTARTS, seed=SEED):
    """k-means, best of `restarts` seeded k-means++ inits by within-cluster sum of squares."""
    best = None
    for s in range(restarts):
        cent, lab = kmeans2(Z, k, minit="++", seed=seed + s, missing="raise")
        wss = float(((Z - cent[lab]) ** 2).sum())
        if best is None or wss < best[0]:
            best = (wss, cent, lab)
    return best[1], best[2], best[0]


def assign(Z, cent):
    d = ((Z[:, None, :] - cent[None, :, :]) ** 2).sum(-1)
    return d.argmin(1)


def silhouette(Z, lab):
    k = lab.max() + 1
    if k < 2:
        return float("nan")
    D = np.sqrt(np.maximum(((Z[:, None, :] - Z[None, :, :]) ** 2).sum(-1), 0.0))
    out = np.zeros(len(Z))
    for i in range(len(Z)):
        own = lab == lab[i]
        n_own = own.sum()
        if n_own <= 1:
            out[i] = 0.0
            continue
        a = D[i, own].sum() / (n_own - 1)
        b = min(D[i, lab == c].mean() for c in range(k) if c != lab[i] and (lab == c).any())
        out[i] = (b - a) / max(a, b)
    return float(out.mean())


def agreement(la, lb, k):
    """Best-match label agreement between two labelings of the SAME points (rule 2's metric),
    via optimal assignment on the contingency table. Also the Adjusted Rand Index, which needs no
    matching, as a second opinion."""
    C = np.zeros((k, k))
    for x, y in zip(la, lb):
        C[x, y] += 1
    r, c = linear_sum_assignment(-C)
    agree = C[r, c].sum() / len(la)
    n = len(la)
    si = sum(v * (v - 1) / 2 for v in C.sum(1))
    sj = sum(v * (v - 1) / 2 for v in C.sum(0))
    sij = sum(v * (v - 1) / 2 for v in C.ravel())
    exp = si * sj / (n * (n - 1) / 2)
    mx = (si + sj) / 2
    ari = (sij - exp) / (mx - exp) if mx != exp else 1.0
    return float(agree), float(ari)


def eta2(values, lab):
    """Between-cluster share of variance for one axis. Rule 3's "materially different"."""
    v = np.asarray(values, float)
    gm = v.mean()
    tot = ((v - gm) ** 2).sum()
    if tot == 0:
        return 0.0
    bet = sum((lab == c).sum() * (v[lab == c].mean() - gm) ** 2
              for c in range(lab.max() + 1) if (lab == c).any())
    return float(bet / tot)


def axes_of(recs):
    """The five pre-registered routing axes, as scalars. `modality` is two scalars scored as one
    axis (code and prose shares); it qualifies if either does."""
    return {
        "context_volume": [math.log1p(r["inspecting_events"]) for r in recs],
        "generation": [r["authoring_share"] for r in recs],
        "modality_code": [r["artifact_mix"].get("code", 0.0) for r in recs],
        "modality_prose": [r["artifact_mix"].get("prose", 0.0) for r in recs],
        "verification": [r["verify_share"] for r in recs],
        "interactivity": [math.log1p(r["n_user_turns"]) for r in recs],
    }


AXIS_GROUPS = {"context_volume": "context_volume", "generation": "generation",
               "modality_code": "modality", "modality_prose": "modality",
               "verification": "verification", "interactivity": "interactivity"}
# Rule 3: "a partition that splits only on volume is a size bucket". These two axes are
# volume-shaped, so a pass carried by them alone is reported as a FAIL of the rule's intent.
VOLUME_AXES = {"context_volume"}


def profile(recs, lab, c, Z, cent):
    """One cluster's honest feature profile, plus NAMED example windows (nearest the centroid and
    the medoid). Rule 4 is scored off this; roughly twenty defects in this study surfaced as
    plausible wrong numbers and none was caught by reading an aggregate."""
    idx = np.where(lab == c)[0]
    sub = [recs[i] for i in idx]
    n = len(sub)

    def mean(f):
        return round(float(np.mean([f(r) for r in sub])), 3)

    amix = collections.Counter()
    for r in sub:
        for a, v in r["action_mix"].items():
            amix[a] += v
    artmix = collections.Counter()
    for r in sub:
        for a, v in r["artifact_mix"].items():
            artmix[a] += v
    d = ((Z[idx] - cent[c]) ** 2).sum(1)
    near = idx[np.argsort(d)[:N_EXAMPLES]]
    D = np.sqrt(np.maximum(((Z[idx][:, None, :] - Z[idx][None, :, :]) ** 2).sum(-1), 0.0))
    medoid = int(idx[D.sum(1).argmin()])
    return {
        "cluster": int(c), "n": n, "share": round(n / len(recs), 3),
        "mean_action_mix": {a: round(v / n, 3) for a, v in amix.most_common(6)},
        "mean_artifact_mix": {a: round(v / n, 3) for a, v in artmix.most_common(4)},
        "lang_top": dict(collections.Counter(r["lang_top"] for r in sub).most_common(4)),
        "lang_dominates_rate": mean(lambda r: 1.0 if r["lang_dominates"] else 0.0),
        "mean_distinct_files": mean(lambda r: r["distinct_files"]),
        "mean_distinct_dirs": mean(lambda r: r["distinct_dirs"]),
        "mean_distinct_exes": mean(lambda r: r["distinct_exes"]),
        "median_volume": float(np.median([r["volume"] for r in sub])),
        "verification_rate": mean(lambda r: 1.0 if r["verification"] else 0.0),
        "mean_verify_share": mean(lambda r: r["verify_share"]),
        "mean_authoring_share": mean(lambda r: r["authoring_share"]),
        "mean_user_turns": mean(lambda r: r["n_user_turns"]),
        "repos": dict(collections.Counter(r["repo"] for r in sub).most_common(3)),
        "medoid": recs[medoid]["label"],
        "examples": [{"label": recs[i]["label"], "repo": recs[i]["repo"],
                      "volume": recs[i]["volume"], "n_actions": recs[i]["n_actions"],
                      "files": recs[i]["distinct_files"], "dirs": recs[i]["distinct_dirs"],
                      "exes": recs[i]["distinct_exes"], "lang": recs[i]["lang_top"],
                      "verify": recs[i]["verification"], "user_turns": recs[i]["n_user_turns"],
                      "authoring_share": recs[i]["authoring_share"],
                      "top_actions": recs[i]["top_actions"],
                      "top_artifacts": recs[i]["top_artifacts"]} for i in near],
    }


def cluster(frame_path, out, drop_lang=False):
    all_recs = [json.loads(l) for l in open(frame_path)]
    recs = [r for r in all_recs if r["n_actions"] >= MIN_ACTION_EVENTS]
    langs = [l for l, _ in collections.Counter(
        r["lang_top"] for r in recs if r["lang_top"]).most_common(N_LANG_ONEHOT)]
    X, cols, groups = matrix(recs, langs)
    if drop_lang:
        keep = [i for i, g in enumerate(groups) if g != "lang"]
        X = X[:, keep]
        cols = [cols[i] for i in keep]
        groups = [groups[i] for i in keep]
    s = scaler(X, groups)
    Z = apply_scaler(X, s)

    sess = sorted({r["session"] for r in recs})
    rng = np.random.default_rng(SEED)
    perm = rng.permutation(len(sess))
    half = set(sess[i] for i in perm[:len(sess) // 2])
    A = np.array([i for i, r in enumerate(recs) if r["session"] in half])
    B = np.array([i for i, r in enumerate(recs) if r["session"] not in half])

    res = {
        "n_windows_frame": len(all_recs),
        "n_windows_scored": len(recs),
        "excluded_thin": len(all_recs) - len(recs),
        "min_action_events": MIN_ACTION_EVENTS,
        "n_features": len(cols), "feature_groups": dict(collections.Counter(groups)),
        "lang_onehot": langs, "drop_lang": drop_lang,
        "n_sessions": len(sess), "split": {"A_windows": len(A), "B_windows": len(B),
                                           "A_sessions": len(half),
                                           "B_sessions": len(sess) - len(half)},
        "thresholds": {"max_cluster_share": MAX_CLUSTER_SHARE,
                       "near_singleton_share": NEAR_SINGLETON_SHARE,
                       "max_singleton_share": MAX_SINGLETON_SHARE,
                       "stability_floor": STABILITY_FLOOR,
                       "eta2_material": ETA2_MATERIAL, "min_axes": MIN_AXES},
        "sweep": [], "ward_sweep": [],
    }
    axes = axes_of(recs)

    for k in K_RANGE:
        cent, lab, wss = fit(Z, k)
        sizes = sorted(collections.Counter(lab.tolist()).values(), reverse=True)
        sizes += [0] * (k - len(sizes))
        largest = sizes[0] / len(recs)
        near = sum(n for n in sizes if 0 < n < NEAR_SINGLETON_SHARE * len(recs)) / len(recs)

        # Stability: fit each half, score the OTHER half's points against those centroids, and
        # compare with that half's own partition. Both directions; headline is the mean.
        agrees, aris = [], []
        for tr, te in ((A, B), (B, A)):
            s_tr = scaler(X[tr], groups)
            cent_tr, _, _ = fit(apply_scaler(X[tr], s_tr), k)
            la = assign(apply_scaler(X[te], s_tr), cent_tr)
            s_te = scaler(X[te], groups)
            _, lb, _ = fit(apply_scaler(X[te], s_te), k)
            a, r = agreement(la, lb, k)
            agrees.append(a)
            aris.append(r)
        stab = float(np.mean(agrees))

        e = {a: round(eta2(v, lab), 3) for a, v in axes.items()}
        qual = sorted({AXIS_GROUPS[a] for a, v in e.items() if v >= ETA2_MATERIAL})
        nonvol = [a for a in qual if a not in VOLUME_AXES]
        row = {
            "k": k, "sizes": sizes, "largest_share": round(largest, 3),
            "near_singleton_share": round(near, 3),
            "silhouette": round(silhouette(Z, lab), 3),
            "stability": round(stab, 3),
            "stability_both": [round(x, 3) for x in agrees],
            "ari_both": [round(x, 3) for x in aris],
            "eta2": e, "axes_qualifying": qual, "axes_nonvolume": nonvol,
            "rule1_mass": bool(largest <= MAX_CLUSTER_SHARE and near <= MAX_SINGLETON_SHARE),
            "rule2_stability": bool(stab >= STABILITY_FLOOR),
            "rule3_separation": bool(len(qual) >= MIN_AXES and len(nonvol) >= MIN_AXES),
        }
        row["passes_1_2_3"] = bool(row["rule1_mass"] and row["rule2_stability"]
                                  and row["rule3_separation"])
        row["profiles"] = [profile(recs, lab, c, Z, cent) for c in range(k)
                           if (lab == c).any()]
        res["sweep"].append(row)
        print(f"k={k:2d} sizes={sizes} largest={largest:.3f} near_singleton={near:.3f} "
              f"sil={row['silhouette']:.3f} stab={stab:.3f} "
              f"axes={qual} rules={int(row['rule1_mass'])}{int(row['rule2_stability'])}"
              f"{int(row['rule3_separation'])}")

    # ---------------------------------------------------------------- post-hoc diagnostics
    # ADDED AFTER THE SWEEP RAN, and they change no threshold and no rule — every pass/fail above
    # is computed from the constants committed in 5331f01. They exist because the sweep produced a
    # statistically clean result (stability 0.96+, mass and separation both passing) whose profiles
    # looked like two things the store already records, and rule 4 cannot be scored honestly
    # without knowing whether that is true.
    #
    #   null        the same sweep on a matrix whose columns are independently permuted. Marginals
    #               preserved, joint structure destroyed. If stability clears 0.70 here too, then
    #               rule 2's floor is measuring k-means' determinism rather than real structure.
    #   reduction   the partition refit on 2 columns only (log volume + the verify flag) and
    #               matched against the full 49-column partition. Rule 3 forbids a size bucket;
    #               this measures how much of the partition survives when everything except size
    #               and verification is thrown away.
    #   recovers    purity of the partition against two booleans the transcript ALREADY carries:
    #               `isSidechain` and verification-present. Against the base rate, so a cluster
    #               that merely reflects the majority class does not score as a discovery.
    res["diagnostics"] = {"null": [], "reduction": [], "recovers": []}
    rngp = np.random.default_rng(SEED + 991)
    Xp = X.copy()
    for j in range(Xp.shape[1]):
        Xp[:, j] = Xp[rngp.permutation(len(Xp)), j]
    sp = scaler(Xp, groups)
    Zp = apply_scaler(Xp, sp)
    vol_j = cols.index("volume:events")
    ver_j = cols.index("verify:present")
    X2 = X[:, [vol_j, ver_j]]
    g2 = ["volume", "verify"]
    Z2 = apply_scaler(X2, scaler(X2, g2))
    side = np.array([r["sidechain_share"] > 0.5 for r in recs])
    ver = np.array([r["verification"] for r in recs])
    for row in res["sweep"]:
        k = row["k"]
        _, labp, _ = fit(Zp, k)
        sizesp = sorted(collections.Counter(labp.tolist()).values(), reverse=True)
        ag = []
        for tr, te in ((A, B), (B, A)):
            s_tr = scaler(Xp[tr], groups)
            c_tr, _, _ = fit(apply_scaler(Xp[tr], s_tr), k)
            la = assign(apply_scaler(Xp[te], s_tr), c_tr)
            _, lb, _ = fit(apply_scaler(Xp[te], scaler(Xp[te], groups)), k)
            ag.append(agreement(la, lb, k)[0])
        res["diagnostics"]["null"].append({
            "k": k, "largest_share": round(sizesp[0] / len(recs), 3),
            "silhouette": round(silhouette(Zp, labp), 3),
            "stability": round(float(np.mean(ag)), 3)})

        cent, lab_full, _ = fit(Z, k)
        _, lab2, _ = fit(Z2, k)
        a2, r2 = agreement(lab2, lab_full, k)
        res["diagnostics"]["reduction"].append({
            "k": k, "agreement_volume_verify_vs_full": round(a2, 3), "ari": round(r2, 3)})

        def purity(b):
            hit = sum(max((b[lab_full == c]).sum(), (~b[lab_full == c]).sum())
                      for c in range(k) if (lab_full == c).any())
            base = max(b.sum(), (~b).sum()) / len(b)
            return round(hit / len(b), 3), round(base, 3)
        ps, bs = purity(side)
        pv, bv = purity(ver)
        res["diagnostics"]["recovers"].append({
            "k": k, "sidechain_purity": ps, "sidechain_base_rate": bs,
            "verification_purity": pv, "verification_base_rate": bv,
            "cluster_sidechain_rates": [round(float(side[lab_full == c].mean()), 3)
                                        for c in range(k) if (lab_full == c).any()],
            "cluster_verify_rates": [round(float(ver[lab_full == c].mean()), 3)
                                     for c in range(k) if (lab_full == c).any()]})
        print(f"k={k:2d} NULL sil={res['diagnostics']['null'][-1]['silhouette']} "
              f"stab={res['diagnostics']['null'][-1]['stability']} | "
              f"vol+verify reproduces full partition {a2:.3f} | "
              f"sidechain purity {ps} (base {bs}) verify purity {pv} (base {bv})")

    # Robustness only, never the headline: does a different algorithm find the same shape?
    L = linkage(Z, method="ward")
    for k in K_RANGE:
        lab = fcluster(L, k, criterion="maxclust") - 1
        sizes = sorted(collections.Counter(lab.tolist()).values(), reverse=True)
        near = sum(n for n in sizes if 0 < n < NEAR_SINGLETON_SHARE * len(recs)) / len(recs)
        res["ward_sweep"].append({
            "k": k, "sizes": sizes, "largest_share": round(sizes[0] / len(recs), 3),
            "near_singleton_share": round(near, 3),
            "silhouette": round(silhouette(Z, lab), 3),
            "eta2": {a: round(eta2(v, lab), 3) for a, v in axes.items()}})
    json.dump(res, open(out, "w"), indent=1)
    print(f"\nfull results -> {out}")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("frame"); f.add_argument("--roots", nargs="+", required=True)
    f.add_argument("-o", required=True)
    c = sub.add_parser("cluster"); c.add_argument("frame"); c.add_argument("-o", required=True)
    c.add_argument("--drop-lang", action="store_true",
                   help="robustness arm only; the pre-registered feature set includes lang")
    a = ap.parse_args()
    if a.cmd == "frame":
        frame(a.roots, a.o)
    else:
        cluster(a.frame, a.o, a.drop_lang)


if __name__ == "__main__":
    main()
