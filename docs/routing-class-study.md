# Derived routing classes: stable, coherent, nameable — and a re-description of two raw features

**Verdict: the pre-registration's rule-5 null. Route on the raw features. Do not ship a derived
routing class.**

Rules fixed in `ROUTING-CLASS-PREREGISTRATION.md` before any clustering ran; every threshold is in
`scripts/routing_class.py` as committed at **5331f01**, before the frame was built. Harness
`scripts/routing_class.py` (`frame` then `cluster`); full sweep + per-cluster profiles + named
examples in `routing-class-results.json`.

    corpus            ~/keld/refseries-context/frozen-corpus (495 transcripts, 1 sha-duplicate and
                      4 empty files dropped), 718,251 reference rows, span 60 / stride 50
    windows in frame  1,019
    excluded          201 (19.7%) with fewer than MIN_ACTION_EVENTS = 5 action events
    SCORED            818 windows over 346 transcripts
    features          49 columns in 6 groups — action 22, artifact 13, lang 9, breadth 3,
                      volume 1, verify 1; each z-scored, each GROUP weighted equally
    held-out split    BY TRANSCRIPT (173/173 sessions, 397/421 windows), never by window

## The sweep

| k | sizes | largest | near-singleton | silhouette | stability | qualifying axes | rules 1/2/3 |
|---|---|---|---|---|---|---|---|
| 2 | 494, 324 | **0.604** | 0.000 | 0.274 | 0.969 | ctx, interact, verify | **0**/1/1 |
| 3 | 285, 278, 255 | 0.348 | 0.000 | 0.226 | 0.974 | ctx, gen, interact, verify | 1/1/1 |
| 4 | 252, 244, 197, 125 | 0.308 | 0.000 | 0.224 | 0.961 | + modality | 1/1/1 |
| 5 | 242, 235, 139, 139, 63 | 0.296 | 0.000 | 0.202 | 0.814 | all five | 1/1/1 |
| 6 | 241, 240, 170, 109, 56, 2 | 0.295 | 0.002 | 0.217 | 0.906 | all five | 1/1/1 |
| 7 | 193, 160, 147, 142, 117, 57, 2 | 0.236 | 0.002 | 0.173 | **0.591** | all five | 1/**0**/1 |
| 8 | 159, 142, 142, 117, 107, 91, 58, 2 | 0.194 | 0.002 | 0.169 | **0.567** | all five | 1/**0**/1 |
| 9 | 200, 159, 157, 123, 119, 30, 26, 2, 2 | 0.244 | 0.005 | 0.174 | **0.614** | all five | 1/**0**/1 |
| 10 | 157, 150, 119, 118, 109, 105, 30, 26, 2, 2 | 0.192 | 0.005 | 0.166 | **0.656** | all five | 1/**0**/1 |
| 11 | …, 2, 2, 1 | 0.192 | 0.006 | 0.168 | **0.591** | all five | 1/**0**/1 |
| 12 | …, 7, 3, 2 | 0.172 | 0.015 | 0.170 | **0.648** | all five | 1/**0**/1 |

**k = 3, 4, 5, 6 pass rules 1, 2 and 3 as written.** Ward linkage on the same matrix finds the same
shape (k=2 largest 0.632, silhouette 0.267; k=3 0.368/0.214; k=6 0.295/0.205) — the partition is not
an artifact of k-means.

The prior semantic-clustering refutation's failure mode **did not recur**: no threshold here
produced 107 singletons or a 38% catch-all. Mass and coherence hold simultaneously at four values of
k. That much was worth trying and the pre-registration's prediction — low-dimensional repetitive
level vectors partition far better than open-vocabulary topics — is confirmed.

## What the clusters actually are — k=4, with named windows

Every profile below is the cluster's own feature means; the examples are the three windows nearest
its centroid, with the medoid marked.

**C1 — n=252 (30.8%). "A long, broad, closely-steered coding hour that ran its tests."**
verification 1.000 · median volume 1,789 events · 22.7 files / 8.8 dirs / 23.3 exes · 6.3 user turns
· authoring share 0.214 · code 0.895 · TypeScript 118 / Python 68 / Go 57

    6e2a58f9-20260726T0145  (MEDOID)  keld-atlas  vol 2307  acts 175  25f/10d/22x  TypeScript
        read:64 search:31 edit:26 version control:18 run a service:14   code:78 prose:1
    36821116-20260727T1959            keld-atlas  vol 2060  acts 147  17f/9d/26x   TypeScript
        read:44 edit:33 run a service:19 search:15 version control:11   code:87
    9aaa379a-20260801T1845            keld-atlas  vol 2197  acts 147  15f/6d/33x   TypeScript
        read:57 edit:23 search:14 run a service:12 version control:7    code:38 prose:1

**C2 — n=244 (29.8%). "A narrow, mostly autonomous hour that executed code."**
verification 1.000 · median volume 454 · 4.8 files / 2.2 dirs / 15.7 exes · 2.8 user turns ·
authoring 0.138 · Go 107 / Python 54

    agent-a8-20260805T1853  (MEDOID)  keld-atlas  vol 385  acts 27  7f/2d/12x  Go  1 turn
        read:11 search:7 transform:4 version control:2 build:1          code:8 prose:3
    16c69396-20260812T0002            keld-signal vol 476  acts 31  3f/2d/22x  Go  10 turns
        read:13 run code:5 search:5 delegate:3 version control:2        code:4 prose:1
    agent-ac-20260812T0059            keld-signal vol 454  acts 51  3f/3d/18x  Go  0 turns
        read:18 version control:12 search:10 run code:4 test:3          code:5 prose:3

**C0 — n=197 (24.1%). "A read-and-search sweep over code that never executed anything."**
verification **0.000** · median volume 487 · 10.8 files / 4.7 dirs / 10.6 exes · 2.0 user turns ·
authoring 0.144 · code 0.906 · Python 111 / TypeScript 65

    agent-a3-20260805T2347  (MEDOID)  keld-atlas  vol 533  acts 30  10f/3d/7x  Python
        read:13 search:9 version control:5 transform:3                  code:21 prose:2
    agent-aa-20260804T2037            keld-atlas  vol 505  acts 40  9f/3d/11x  Python
        read:18 search:16 transform:3 version control:3                 code:21
    agent-a3-20260804T2115            keld-atlas  vol 508  acts 39  13f/5d/12x Python
        read:19 version control:7 search:7 transform:6                  code:21

**C3 — n=125 (15.3%). "A thin hour with no file to attribute it to."**
verification 0.104 · median volume 144 · **1.4 files / 1.0 dirs** · lang dominates in only 14.4% ·
44 of 125 windows have no dominant language at all · delegate 0.093 · prose 0.217

    agent-af-20260804T2033  (MEDOID)  keld-atlas
    agent-ab-20260810T1845            keld-atlas  vol 108  acts  7  1f/1d/5x  Python
        read:3 transform:2 search:1 version control:1                   code:2
    agent-aa-20260810T2347            keld-atlas  vol 142  acts 11  1f/1d/4x  Python
        search:6 read:4 transform:1                                     code:2
    agent-a2-20260803T0110            keld-atlas  vol 149  acts 12  1f/1d/7x  Go
        read:9 search:2 transform:1                                     code:4

The same two dimensions produce every other passing k. At **k=3** the clusters are
(verify + large: `6e2a58f9-20260726T0145`), (verify + small: `agent-a8-20260805T1853`), (no verify:
`agent-aa-20260805T2132`). At **k=5** and **k=6** the extra clusters are the *no-file* case —
`16c69396-20260812T2002`, mean 0.21 and 0.11 distinct files, 56 of 63 and 56 of 56 windows with no
dominant language — plus at k=6 a 2-window YAML cluster (`32b52295-20260806T2107`).

**Per-cluster verification rates, every passing k, in order:**

    k=3   1.000  0.914  0.000
    k=4   1.000  1.000  0.104  0.000
    k=5   1.000  1.000  0.508  0.000  0.000
    k=6   1.000  1.000  0.464  0.018  0.000  0.000

## Rule 4 is where it dies, and three diagnostics say why

The clusters ARE nameable — the four lines above are honest one-liners drawn from the profiles, which
is more than the prior semantic attempt ever managed. **What they name is a conjunction of two raw
features: how much happened, and whether anything ran.** Three post-hoc diagnostics, added after the
sweep and changing no threshold:

**1. Two columns reproduce the 49-column partition.** Refitting on `log(volume)` and the
`verification` flag alone and best-matching against the full partition:

    k        2      3      4      5      6      7      8     …    12
    agree  0.914  0.918  0.861  0.729  0.647  0.653  0.577  …  0.476

At every k that passes rules 1-3, **86-92%** of the assignment survives discarding action mix,
artifact mix, language and breadth entirely.

**2. The partition recovers a boolean the transcript already carries.** Purity of the partition
against `verification present`, against its 0.622 base rate:

    k=2 0.913   k=3 0.971   k=4 0.984   k=5 0.962   k=6 0.966

Against `isSidechain` — the other candidate hidden variable — purity is 0.643 / 0.678 / 0.713 /
0.697 / 0.704 against a 0.599 base rate, i.e. barely above chance. The `agent-*` transcripts
crowding the small clusters are there because they are *small*, not because they are subagents.

**3. Only one of the five routing axes is independent of size.** Pearson r against log volume over
the 818 windows:

    context_volume  +0.914      interactivity  +0.497      modality_prose  -0.162
    breadth (files) +0.805      generation     +0.401      verification    +0.114

Rule 3's letter is satisfied — three to five axis groups clear eta² 0.10 at every passing k. Its
*intent* ("a partition that splits only on volume is a size bucket, and volume is already published
as `evidence`") is not: four of the six axis scalars are size in another coordinate system, and the
only axis genuinely orthogonal to size is `verification`, r = +0.114.

## The objection to rule 2, reported separately as instructed

**Rule 2's 0.70 stability floor is passed by structureless noise at every k this study cares about.**
Permuting each feature column independently — marginals preserved, joint structure destroyed — and
re-running the identical split-half procedure:

    k            2      3      4      5      6      7      8      9     10     11     12
    real      0.969  0.974  0.961  0.814  0.906  0.591  0.567  0.614  0.656  0.591  0.648
    NULL      1.000  0.980  0.961  0.871  0.781  0.766  0.711  0.608  0.647  0.621  0.588
    null sil  0.199  0.152  0.139  0.104  0.105  0.109  0.102  0.080  0.077  0.068  0.105

The null clears 0.70 at k=2 through 8 and **matches the real data exactly at k=4** (0.961 vs 0.961).
What split-half agreement measures on a unimodal cloud is k-means' determinism — a low-dimensional
convex partition of a single blob is reproducible whether or not the blob has structure. The real
data's silhouette (0.166-0.274) is only ~0.06-0.08 above the null's, which is the same message: this
is one cloud being cut, not clusters being found.

**The verdicts above are reported under the rule as written** (k=3-6 pass rule 2). The objection is
that a future study should not treat stability alone as evidence of structure; the informative
comparison is stability *against a marginal-preserving null*, and that costs one extra fit.

## The dimension that already had signal is not a cluster boundary anywhere

The refutation that motivated this experiment left one survivor: collapsed to "was this hour
authoring or not", the action level scored **0.756 against a 0.538 baseline (+0.218)** on
hand-labelled windows. **No k in this sweep produces an authoring cluster.** The highest mean
authoring share of any cluster at any k in 2..12 is **0.257** (k=8, C0, n=107); at the passing k it
is 0.214, 0.215, 0.215. The reason is in the marginal: authoring share has median **0.157** and only
**18 of 818 windows** reach 0.40, seven reach 0.50. Every window is read-dominated (`read` 0.32-0.42,
`search` 0.15-0.25 of the action mix in every cluster at every k), so a variance-minimising method
never cuts there.

This is the study's most useful negative: **derivation-by-clustering and the surviving hand-designed
two-way split disagree, and only the hand-designed one has ground truth behind it.** Unsupervised
structure in this feature space is dominated by how much work happened; the human-meaningful axis is
a minority direction it will not find. Rule 5's null is therefore not a consolation — it is the
correct read of a feature space whose principal variation is size.

## Recommendation

1. **Route on the raw features. Do not ship a derived class.** A router can consume `volume`,
   `verification_present` and breadth directly, and at the passing k those three *are* the partition
   (0.861-0.918 reproduction from two of them). A derived label adds a name, a schema version, a
   vocabulary to maintain and a bump-and-re-eval obligation, and adds no information. This project
   has already paid once for shipping a partition that looked clean and meant nothing.
2. **`volume` is already published as `evidence`**, so the only genuinely new bit here is one
   deterministic boolean: *did anything execute in this window* (`test` / `build` / `run code`
   present). If any part of this ships it is that boolean and not a taxonomy — and it needs its own
   pre-registration plus a demonstration that a routing decision actually changes on it, which this
   study cannot supply.
3. **If a human-readable activity dimension is still wanted, it is the two-way authoring split**,
   pursued as a supervised question with labels, on a fresh sample, with its own pre-registration.
   Clustering will not find it: it is a minority direction in this feature space and the sweep
   demonstrates that at every k.

## Limitations

- **Circularity of rule 3, stated in the harness before the run.** The routing axes are functions of
  the fitted features, so "clusters separate on axis X" is a check that the partition is more than
  one-dimensional, never independent evidence.
- **No ground truth for "routable to model X".** Nothing here shows that routing on any of this
  saves money or improves answers. That needs an A/B against real model outcomes and was
  pre-registered as out of scope. No such claim is made.
- **One corpus, two machines, overwhelmingly agentic coding work** (keld-atlas and keld-signal are
  85%+ of every cluster). A corpus with real prose or spreadsheet work might make `modality` an
  independent axis rather than a size correlate; on this corpus it is not.
- **19.7% of windows excluded** as thin (<5 action events). They are not judged, and the "no file to
  attribute" clusters (C3 at k=4; the 0.11-file clusters at k=5/6) show the boundary is soft — thin
  windows survive the cut and then form their own cluster.
- **The interactivity axis is not a fitted feature** (the pre-registered feature list does not
  contain it), so it is scored for rule 3 but never influenced the partition.
- `GLiNER2` is not compared against, per rule 6.

## Incidental defect found while building the frame — worth fixing separately

`levels.events_for_turns` stamps every row with `session = os.path.basename(path)[:8]`. On this
corpus that is **not unique**: subagent transcripts are named `agent-<hash>.jsonl`, so `agent-a6`,
`agent-ad`, `agent-ac` … all collide, and **445 of 500 transcripts share an 8-character prefix with
another file** (16 distinct colliding prefixes). Anything that groups rows by `session` silently
merges those transcripts. Measured here: the first frame built came out at **550 windows against a
true 1,022**, a 46% loss, with no error raised. The study works around it with a per-file id; the
production consequence — whether any consumer of the stored `session` column groups by it — was not
investigated and is not this study's to fix.

This study measured only. No facet, vocabulary, schema version or pipeline stage was changed.

