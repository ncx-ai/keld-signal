# v2 Signal — status and handoff

Continues `2026-08-22-window-context-handoff.md`. Everything below was measured on branch
`feat/llm-classify-study`; where a number is thin, it says so. **The branch is not merged and not
pushed** — 218 commits, one machine, no upstream. Feature branches through to v2 by decision.

## The headline

On-device enrichment now works **without GLiNER2 at all**. That was the goal of this stretch and it
is done: the sidecar is the analysis-and-enrichment *service*, GLiNER2 is one capability it may load
lazily, and `ml_backend:"deterministic"` runs enrichment with no model. GLiNER2 is currently used by
nothing.

## What is established

**The service starts and serves without the model.** `mlBackend` used to provision ~1.9 GB *before*
spawning, and deterministic mode never started the service at all — so the mode built for
model-free enrichment produced nothing. Now the service starts for every mode except `off`,
provisioning runs alongside, and the GLiNER2 worker spawns only on an actual inference. Measured on
a real 64 MB transcript: `/analyze` answers in 0.8-1.0s with the worker reported `down` throughout.
"No binary installed" is distinguished from "not ready yet" — the former runs without window
analysis rather than wedging forever.

**Sensitivity is detection-only.** The pass used to ask GLiNER2 to *classify* into
`{none,pii,secrets,phi,pci,proprietary}` and published that guess whenever no entity fired — the
code's own comment called it "the weak classifier". Removed. The class is now purely
`sensitivityFromEntities`, so `phi` is *derived* from an `ssn` detection rather than guessed. The
`-tags pii` gold gate runs with a **nil Model**, which structurally pins that no GLiNER2 dependency
returns.

**PII detection is presidio, region-scoped, and measured.**

    real-corpus precision      ~1%  ->  10/10      (2,000 prompts, seeded)
    prompts publishing `pii`   240.5/1,000 -> 4.5/1,000
    false `pci`                2 -> 0        `phi`  0 -> 0
    gold-set sensitive_recall  0.143 -> 1.000     accuracy 0.881 -> 1.000

25 checksum-validated recognizers in three tiers (universal / `us` default / 11 opt-in regions),
`KELD_PII_REGIONS` with an Atlas override path already shaped. `SchemaVersion` 6 -> 7.

**Credentials sit at 20/24, deliberately.** gitleaks rules plus two structural detectors: a URI
userinfo password (`net/url` as the *validator*; offsets from raw source bytes because `url.Parse`
returns a percent-decoded value and no offsets) and a Twilio auth token (the account SID is an
anchor only, never reported — a bare 32-hex run is an MD5).

**The reference store exists** (tasks 1-2 of 5). 5-minute bins, exact partial edges, a `bin_level`
foreign-key registry so a sparse level physically cannot be misread as "no evidence". Incremental
ingest proven equivalent to a full parse across **284 transcripts, 40 chunks each: 0 rows differed**.
A 47 MB file costs 1.82s for its whole 41-chunk lifetime versus 0.76s for one parse.

## What is refuted — all measured, do not revisit without new evidence

- **GLiNER2 as a classifier of the assistant's activity.** 37.8% against a 67.8% majority baseline
  (30 points *worse* than a constant), degenerate at 91% one class, confidently wrong (0.975 on a
  window where the assistant was building an SVG). Down-route rate 59.5% — for routing, the errors
  run in the direction that breaks the answer. Offering `reasoning` cost `editing` half its
  predictions. Full record: `~/keld/refseries-context/prose-activity/`.
- **spaCy NER for `person`/`address` on developer text.** 858 `person` spans with ~zero real names
  (`JSON` x132, `Docker`, hex colours, a bare emoji at 0.85); 140 `address` spans, 140 false. 92% of
  all spans. Both types dropped by decision.
- **Importing gitleaks as a library.** The real engine — 222 rules, allowlists, stopwords, correct
  `secretGroup` — scores 17/24, *bit-for-bit identical* to our 490-line loader on all 42 rows, for
  204 modules and ~8 MB.
- **Shape-plus-keyword credential detection, on this corpus.** 2,137 prompts: 6 candidates, and all
  6 are git SHAs or 40-char prose runs. Zero credentials. User prompts here contain essentially no
  raw credentials (one 32-hex token total), so there is almost no recall to win.
- **Adaptive windowing, previously.** Change-point boundaries reached only parity with a fixed
  constant at much higher complexity. Not settled for *drift detection* on a metric stream, but it
  sets the bar: fixed stays default until something beats it under a pre-registered rule.

## What is open, in the order to take it

1. **Store tasks 3-5.** `/analyze` reads the store (where the per-prompt cost actually disappears),
   the watcher signals advancement, retention. **Task 5 must handle Task 1's carried concern:**
   pruning raw events would silently strip the partial *edges* from old windows, because interiors
   come from `bin` but edges come from `event`. Snap visibly or refuse.
2. **The moving characterization.** The actual goal; the store is only its prerequisite. Spec:
   `2026-08-23-moving-characterization-design.md`. Tick over a recent slice against a longer
   baseline, applied to the activity in the slice. **How the slice and baseline are SIZED is a seam
   plus a measurement, not a constant** — `river` ships ADWIN, EWMA fast/slow, PageHinkley, KSWIN.
   `MIN_EVIDENCE = 5` was derived for a 60-minute window and must be re-derived per slice or nearly
   every tick reports unattributed.
3. **Codex and Gemini produce no workstreams at all.** `/analyze` resolves prompts by Claude-Code
   UUID; theirs are `<sessionID>#<ordinal>` and `<sessionId>########<ordinal>`. Cowork is
   whitelisted but host-side capture is VM-blocked, so today this is **Claude Code only**. This is
   the one scope question that decides whether v2 is multi-tool or Claude-Code-first.
4. **Push the branch.** 218 commits, one machine, no upstream. Cheapest risk on the list.
5. **The eval's `task_type`/`domain` floors measure GLiNER2**, which does not run. They currently
   assert nothing about the shipping system.

Parked deliberately, and fine: analysis in a recycled worker child (no memory pressure without
GLiNER2 — the parent is ~1.1 GB against a 4 GB budget); credential recall at 20/24;
`"proprietary"` structurally unemittable (no entity type rolls up to it); GLiNER2 unused.

## How to work on this — lessons that cost real time here

- **A fixture built from published example values measures the gate, not the detector.** This bit
  three times: the gold set's `pci` rows were all `4111 1111 1111 1111` and its `phi` rows all
  `123-45-6789`; `creds.jsonl` was half documentation constants *plus four plain length errors*
  against the vendored rules. Recall read as 0.143 and 0.417 when the real numbers were 1.000 and
  0.708.
- **Tests pass vacuously more often than they fail loudly.** Four doubles kept passing after a
  source switch with the deterministic layer silently covering their assertions; two fixtures were
  detected by *nothing*, so masking and no-leak assertions ran over empty span lists; one
  equivalence table was trivially true until discriminating rows were added. **Inject a mutation
  and confirm the suite bites.**
- **Measure on the corpus, not the fixture.** Every unmeasured precision claim on this branch was
  wrong. The URI detector's first cut produced 43 spans across 42 prompts and *one distinct value*.
  No fixture could have shown that.
- **Check your own tooling's version.** Ten recognizers "enumerated" from presidio did not exist in
  the pinned version — the probe ran against a newer one in a throwaway venv. Four regions would
  have shipped silently dead.
- **Never a broad `pgrep`/`pkill -f`.** It self-matched or over-matched four times, once killing
  another agent's freeze run. Match on a recorded pid.
- **A dependency's constraints are part of the dependency.** presidio 2.2.363 added
  `numpy<2.5.0`, which would have silently downgraded numpy under torch — a runtime break, not an
  install break. Pinned 2.2.362 after verifying upstream's reason for the cap does not reproduce
  here.
- **`SchemaVersion` bumps on a published-vocabulary change.** The "branch is unpublished so no
  consumer holds a baseline" argument is retired at v2.
