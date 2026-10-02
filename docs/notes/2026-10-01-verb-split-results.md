# The verb-split study: results

**Date:** 2026-10-01. **Scope:** the two `activity_verb` splits proposed in
`docs/superpowers/specs/2026-10-01-activity-verb-mapping-design.md` §3c — `retrieve` →
`research`|`extract`, and `synthesize` → `text.summarize`|`research`. **Status:** scored. The
gate in §7 is the repo owner's and is left open at the end of this note.

Reproduce with `~/.keld/sidecar-venv/bin/python scripts/verbsplit_study.py score` from the repo
root, against the 200-row frame built by Task 3.

---

## 1. The headline is not an accuracy figure. It is that 138 of 200 requests have no determinate verb.

A blind labeller was shown each request's text, tool names and tool arguments — with corpus,
parent class and prior-request context withheld — and asked for the atv1 verb. It returned
`unclear` on **138 of 200 rows (69%)**.

That rate is the measurement, not the noise floor around one. Per cell:

| cell | n | unclear | scoreable | scoreable labels |
|---|---|---|---|---|
| `corpus_b` / `retrieve` | 40 | 4 | **36** | research 15, extract 21 |
| `corpus_b` / `synthesize` | 40 | 28 | 12 | text.summarize 8, research 4 |
| `corpus_a` / `synthesize` | 40 | 32 | 8 | text.summarize 8 (**one label only**) |
| `corpus_a` / `retrieve` | 40 | 35 | 5 | research 4, extract 1 |
| `wildchat` / `synthesize` | 40 | 39 | **1** | text.summarize 1 |

One cell of the five is healthy.

**The splitter answers on rows the labeller could not.** Every one of the 80 `retrieve` rows in
the frame carries empty request text — a bare `Read` or `Grep` with no commentary — and the
labeller marked 39 of those 80 `unclear`. `split_retrieve` has no such hesitation: shown a
whole-file `Read` it returns `research`, shown a `Read` with `offset`/`limit` it returns
`extract`, and it does that whether or not anything in the request indicates why the file was
being opened. **The splitter is therefore more confident than its evidence**, and on this corpus
the gap is about one row in five of everything sampled.

*(Explanation, not measurement.)* The likely reason is that atv1's verbs name deliberate work
products — write this, summarize that, review the other — while a large fraction of a real agent
session is mid-session looking-at-things that produces no work product at all. That is the same
shape as reqclass's `operate` class, which §3b already excludes from the verb axis as not-work.
If that reading is right, the 69% is a property of the taxonomy meeting agent transcripts, and
no splitter improvement addresses it.

**One qualification measured against our own framing.** The labeller did *not* treat a bare tool
call as categorically indeterminate: of the 80 empty-text `retrieve` rows it resolved **41** and
left 39 `unclear`. Rows of identical outward shape were judged both ways, presumably on the tool
arguments. So "empty text ⇒ no determinate verb" is too strong; what holds is the weaker and
still uncomfortable "about half the time, a bare tool call does not say which verb it was."

---

## 2. The `retrieve` split: precision clears the bar, the overall accuracy does not, and abstention is why

The pre-registered bar (committed in `scripts/verbsplit_study.py` at `2ad435a8`, before any
scoring): **precision ≥ 80% over the rows the splitter ANSWERS**, AND **accuracy ≥ majority
baseline + 20 points, with abstention counted wrong**. The two denominators differ on purpose —
the unsplit baseline never abstains.

### `corpus_b` / `retrieve` — the only cell with enough rows and enough label variance to measure

```
n=40  unclear=4  scoreable=36   labels: research 15, extract 21
answered=14  abstained=22  (all 22: coverage:no_partitioned_tool)
precision(answered)      = 85.7%   (12 of 14)      -> bar MET
accuracy(abstention=wrong) = 33.3%  baseline 58.3%  -> margin -25.0 pts -> bar NOT MET
VERDICT: FAIL
```

The failure is not wrongness, it is silence. On the 14 rows the splitter covers it is right 12
times; it declines on 22 of 36. **All 22 abstentions are one cause: a `Bash` tool call, which is
in neither of `split_retrieve`'s two tool partitions.** That is a coverage gap in the splitter —
it says nothing about whether those requests had a determinate verb, and the labeller in fact
resolved all 22. Abstention and error are kept apart in the scorer (`_abstention_reason`) for
exactly this reason; folding a coverage hole into "the splitter judged the evidence
insufficient" would read a defect as epistemic humility.

**Not pre-registered, reported as a diagnostic only:** across the 14 answered rows the labels
split 7 `research` / 7 `extract`, so on its own coverage the splitter runs 85.7% against a 50%
baseline. That is what the discriminator is worth *where it applies*. It is not the bar and does
not substitute for it, because the bar deliberately charges the split for the 61% of the class it
cannot reach.

The two errors are both `extract` predicted where the label was `research` (confusion:
research→research 5, extract→extract 7, extract→research 2). No `research` prediction was wrong.

### `corpus_a` / `retrieve` — 5 scoreable rows, reported for completeness

```
n=40  unclear=35  scoreable=5   labels: research 4, extract 1
answered=4  abstained=1
precision = 100.0% (4 of 4)   accuracy = 80.0%  baseline = 80.0%  margin +0.0
VERDICT: FAIL on the margin (the baseline is 80% because 4 of 5 rows carry one label)
UNDERPOWERED
```

Four correct answers out of four is not evidence of an 80% precision rate; it is four answers.
The margin is +0.0 against a baseline that a constant predictor would also reach. **Nothing
should be concluded from this cell in either direction.**

---

## 3. The `synthesize` split is UNDERPOWERED. No pass and no fail is announced on it.

Twenty scoreable rows exist across both of our corpora, and corpus A's eight all carry the same
label.

```
corpus_b/synthesize   n=40 unclear=28 scoreable=12  (text.summarize 8, research 4)
  answered=7  abstained=5 (all designed:one_prior_retrieval)
  precision = 71.4% (5 of 7)   accuracy = 41.7%  baseline = 66.7%  margin -25.0 pts

corpus_a/synthesize   n=40 unclear=32 scoreable=8   (text.summarize 8 — ZERO LABEL VARIANCE)
  answered=1  abstained=7 (all designed:one_prior_retrieval)
  precision = 100.0% (1 of 1)   accuracy = 12.5%
  baseline = 100.0% by construction; no margin over it is reachable, so none is printed
```

A cell in which every scoreable row carries one label cannot measure a classifier. Its "accuracy"
is the rate at which the classifier emits that one label, and its baseline is 100% — a constant
predictor wins outright. `corpus_b/synthesize`'s 12 rows are a real but tiny sample: its 71.4%
precision rests on seven answers and moving one of them moves the figure by 14 points.

Unlike the `retrieve` cell, these abstentions are **designed**, not a coverage gap:
`split_synthesize` declines when a request's prior contains exactly one retrieval, which it was
written to treat as genuinely ambiguous. All 12 abstentions across both cells are that case.

**The honest statement: the `synthesize` split was not measured.** The numbers above are
recorded so a later study does not re-derive them, and for no other purpose.

### The `wildchat` / `synthesize` cell is dead

One scoreable row of 40. It is excluded from scoring. Two independent reasons, and either alone
is sufficient: 39 of its 40 rows are `unclear`, and **0 of its 40 rows contain `retrieve` in
their prior** — so `split_synthesize`'s only discriminator cannot fire there and the cell could
only ever return `text.summarize`. A cell where one answer is unreachable measures a base rate.

---

## 4. The study has no adequate holdout. The spec was wrong about this twice, and corpus A does not rescue it.

**§4 named WildChat as the holdout.** It cannot be one, for a structural reason:
`reqclass.route_class` reaches `retrieve` only through a retrieval TOOL CALL, and a chat export
has no tool calls at all, so the `wildchat`/`retrieve` cell is empty **by construction** — Task 3
found 0 available rows, not 0 sampled ones. The `synthesize` half fails for the second reason in
§3 above. Measured, for the contrast: `retrieve` appears in the prior of **28 of 40** corpus A
synthesize rows and **29 of 40** corpus B ones, against **0 of 40** in WildChat.

*(The spec's stated motive for choosing WildChat was also wrong: it was justified by `is_report()`
having been tuned on our corpus, and neither shipped discriminator calls `is_report()`. The
circularity being guarded against does not exist in this study's mechanism.)*

**Corpus A was then ruled the holdout, and it is too thin to be one.** At 5 and 8 scoreable rows
it decides nothing, and it is **exhausted** — the frame already took 40 of 44 available
`retrieve` rows and 40 of 51 `synthesize` rows, which is nearly its whole population. Sampling
more from it is not available. Corpus B could supply more (8,788 `synthesize` and 13,781
`retrieve` rows available, against 40 sampled of each), but enlarging only corpus B buys a healthier measurement cell and leaves
the independence axis exactly as thin.

**So: this study measured one split on one person's corpus, with no second corpus able to check
it.** Everything in §2 above is an in-sample figure on `corpus_b`. The rest of this note's
numbers are too small to be anything.

---

## 5. Disclosures

**The labeller saw only the first 1500 characters of each row, and 45 of 200 rows (22%) exceed
that.** This was raised as a live alternative explanation for part of the 69%. Measured, it
mostly runs the other way:

- 155 of 200 rows were rendered **whole**, and **103 of those 155 (66.5%) are still `unclear`**.
  Truncation cannot account for the unclear rate on three-quarters of the corpus, because
  three-quarters of the corpus was not truncated.
- Within `synthesize` (the only class with any text at all — all 80 `retrieve` rows have empty
  text, so truncation cannot touch them), the long rows are **less** often unclear than the short
  ones: corpus_a 3/8 (38%) long vs 29/32 (91%) short; corpus_b 1/6 (17%) vs 27/34 (79%).
- WildChat runs the opposite way — 31 of 31 long rows unclear, 8 of 9 short — but that cell is
  dead for other reasons and its prose is different in kind.

**Not settled, stated as unsettled:** this shows truncation does not *explain* the unclear rate;
it does not show that a full rendering would have changed no individual long row's label. A
re-render is cheap if someone wants that specific question answered.

**Other disclosures:**

- **`Bash` coverage gap.** 27 of the 80 `retrieve` rows call `Bash`, which is in neither of
  `split_retrieve`'s partitions; all 27 abstain, 22 of them inside the scored `corpus_b` cell.
  The `RETRIEVE` Bash regex named in spec §4 as part of what the study would measure was **not**
  partitioned by the splitter. This is a known, reportable gap — not a finding about the
  requests.
- **Corpus A is a single transcript file.** Its rows are serially correlated, so its n overstates
  the evidence it carries. This was accepted with the weakness stated (controller Ruling 7)
  rather than rebuilt.
- **WildChat has no usage data**, so the frame substitutes `out = len(text)//4` for the
  `out >= 400` test inside `route_class`'s synthesize/acknowledge boundary. Unmeasured
  approximation; it affects which WildChat rows entered the frame at all.
- **Blindness holds, and is provable from git.** The labels were committed at `a32c2017`
  (2026-10-01T15:36:40-04:00), the splitters at `2ad435a8` (15:42:37-04:00), six minutes later.
  The labeller and the splitter author were different contexts that never saw each other's work;
  the splitter author reports never having opened the labels file or its history, and
  `test_text_is_not_read` (run over all 200 rows, with a non-empty-frame assert so it cannot pass
  vacuously) pins that neither discriminator reads prose.
- **The scorer was checked, not trusted.** The headline precision was recomputed by a `jq` + `awk`
  pipeline sharing no code with `_score`: `scoreable=36 answered=14 correct=12 precision=0.8571`.
  Identical.

---

## 6. The gate — options, not a decision

Spec §7 defines three outcomes. Against the measurements above:

- The **`retrieve` split FAILS the pre-registered bar** on the only cell able to carry a verdict
  (`corpus_b`: precision 85.7% MET, margin −25.0 pts NOT MET). The conjunctive bar is not met.
- The **`synthesize` split was not measured** — 20 scoreable rows, 8 of them single-label. It is
  neither passed nor failed here.
- No adequate holdout exists for either.

The outcomes as §7 writes them, with what each would mean given the above:

1. **Both pass → nine verbs.** Not reachable on this evidence.
2. **One passes → that split, with the other abstaining.** Reachable only by a decision the
   study does not support: `retrieve` met precision but not the margin, and nothing in §7's text
   is satisfied by half a conjunctive bar. ⚠️ **§7 and §4 contradict each other on what this
   option ships.** §7 says "ship that one split and the unsplit parent for the other"; §4 says
   "A FAILING SPLIT PUBLISHES NOTHING — there is no unsplit parent to fall back on", because
   `retrieve` and `synthesize` are reqclass CLASS names and atv1 has no verb meaning "retrieved
   something, unspecified". §4's reading is the one consistent with the vocabulary; §7's phrasing
   appears to be an error. **Resolving that is part of the gate decision, not part of this note.**
3. **Neither passes → the six verbs already shipped in Tasks 1-2** (`code.write`, `code.edit`,
   `text.create`, `text.transform`, `review`, `plan`), still covering all four reachable
   families, with the outcome recorded beside the six prior negatives on the sibling `context`
   axis. This is what the current code already does: `retrieve` and `synthesize` abstain and
   publish no verb, pinned by the Task 2 fixture (`activity_class_tokens` 922 vs
   `activity_verb_tokens` 850, the 72-token difference being exactly the abstaining `retrieve`
   request). **Choosing this costs the verb axis 37.3% of engineering requests**, per §4.

Two further options the spec does not enumerate, surfaced because the measurements point at them
and for no stronger reason:

4. **Close the `Bash` coverage gap and re-score.** 22 of the 36 scoreable `corpus_b` rows are
   unanswerable by the current splitter for a reason unrelated to the hypothesis. Whether the
   margin bar is reachable at all is currently untested, not tested-and-failed. ⚠️ This would be
   a splitter edited by a context that has read the labels, which is a different and weaker
   study than the one just run — it would need a fresh blind labelling pass to mean anything.
5. **Treat the 69% as the result and stop.** If most agent requests have no determinate verb,
   then a split that is right 86% of the time on the minority that does is answering a smaller
   question than the spec assumed, and the `activity_verbs` level publishes a distribution over
   whichever requests happen to be legible rather than over the work.

**No recommendation is made here.** The numbers are above; the choice is the repo owner's.

---

## What a pass would and would not have meant

Worth restating since it was pre-registered: a passing split would have meant the verb is
recoverable from **structural** evidence — tool identity, tool arguments, request order — on real
requests, which is the load-bearing difference between this study and the six refused attempts on
the sibling `context` axis, all of which resolved to reading prose. It would **not** have moved
`context` any closer. This axis does not advance the business-understanding goal and nothing here
should be read as if it did.
