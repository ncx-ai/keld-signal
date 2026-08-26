# Block model — open work, as of 2026-08-25

Everything outstanding from the block-bound study and its wiring. Ordered by what blocks what, not
by size. Results referenced here live in `~/keld/refseries-context/blocks/`, never in this repo.

**Landed today:** the measured cutter (`sidecar/app/analysis/blocks.py`, 20-minute cap + 15-minute
idle), its equality oracle against the study arm, the detector ablation, `/analyze`'s additive
`block` key, `SCHEMA` 14→15, CI gating on all 37 sidecar test files, AGENTS.md coverage, and the
Signal↔Atlas contract corrected against the measurements.

---

## IN FLIGHT

- [ ] **`evidence` + `status` on every workstream dimension.** Sub-floor dimensions are suppressed
      today; measured, 924 of 12,016 dimension-slots hold real evidence and publish nothing (198 of
      them held FOUR observations against a floor of 5). Chain: `workstreams.payload` →
      `sidecar.Workstream` → **`enrich.Labeled`** (the drop point — no field for either) →
      `enrich/workstreams.go`'s `if l.Value == "" { continue }`. Sidecar `SCHEMA` 15→16,
      `enrich.SchemaVersion` 20→21.
      ⚠️ **The floor does NOT move.** 5 stays 5; `attributed` keeps its exact meaning. Removing the
      floor was measured at P(false attribution) 0.031 → 0.50.
- [ ] **Data formats out of `lang`; extend the extension map.** Markdown is 7.4% of `lang` events
      and is not a programming language; JSON+YAML are 0.3%. They move to the `artifact` dimension.
      Map extension is unvalidatable on this corpus (only `.mjs`/`.html` are real gaps) — insurance
      for other users.

## NEXT — Signal side

- [ ] **Spec: weight edits heavier than reads.** Reads outnumber edits 3.4:1 (read 34.5%, search
      17.3%, edit 11.5%, create 3.3%), and every path touch currently emits at weight 1.0, so a
      block that skimmed twelve files and rewrote one publishes what it skimmed.
      ⚠️ **The trap that must be solved first:** `attribution` computes `total = sum(n for _, n in
      items)` — evidence IS the sum of weights, and `MIN_EVIDENCE = 5` is derived as a SAMPLE SIZE
      (`0.5**n` first below 5% at n=5). Weighting an edit at 3.0 lets two edits clear a floor
      calibrated for five observations — silently lowering the floor. Weights must drive the SHARE
      while evidence stays a COUNT, which means `rollup`/`attribution` carrying both, and those are
      shared by `dynamics`, `prior` and `workstreams`.
      ⚠️ **Feasibility unknown:** it is not yet established that `reconcile()` knows whether a file
      touch was a read or an edit at the point it emits the row. Check before designing.
      ⚠️ **No ground truth exists** for "what language was this block really about", so this is
      judgement informed by measurement, not validation. Sweep 1:1/2:1/3:1/5:1, count blocks whose
      published value changes, hand-review a sample, and check stability across the range.
- [ ] **Phase 2 — `covers`.** Map prompt ids to block spans, `complete:false` when an episode runs
      past a block's end. Buildable now; the daemon owns the human-prompt filter, the store owns
      the times.

## BLOCKED ON ATLAS

Building these now adds to the pile of inert code the tick already sits in — every Atlas consumer
joins `enrichment.corr_id == tool_event.prompt_id`, so a block row is stored and joins to nothing.

- [ ] **Phase 3 — the Go wire.** `WindowEnrichment` becomes the primary row; the per-prompt
      `Enrichment` shrinks to `sensitivity` plus correlation.
- [ ] **Phase 4 — the tick becomes the trigger.** Coverage 55.0% → 99.5%. `frontier()` /
      `tail_closed()` survive unchanged and are load-bearing.
- [ ] **Phase 5 — flip `KELD_TICK` on.** One line, the day Atlas has a time+identity join.
- [ ] **Atlas itself** — `2026-08-25-atlas-block-activity-design.md`, v1 as a PATH not a parameter,
      `v1compat/` boundary, the unplugging checklist.

## RESEARCH — unblocked, nothing depends on it

- [ ] **A domain-general work-shift detector.** The ablated detector was branch-only and failed its
      own validation (7.1% recall vs a fixed control's 17.3%). The store already holds the right
      material: `action` (51,618 events, 22 closed values, 488 of 496 sessions) and `tool` (31,888,
      27 values, 488 sessions) — denser and wider-covering than `file` (337 sessions), and both
      describe what KIND of work rather than what codebase.
      Candidates: Shannon entropy of the tool/action mix per bucket; Jensen–Shannon divergence
      between consecutive windows; rate/burstiness (`latency.py` already has gap percentiles).
      ⚠️ Phase 0a's rejection of `action` does NOT refute this — it tested `action` through
      `EwmaSizer`'s novelty encoding, which asks "is a new value outweighing the incumbent", a
      question that is noise for a level whose 22 values recur constantly and never succeed one
      another. Distributional statistics ask a different question.
      ⚠️ **The blocker is ground truth, not compute.** Every detector so far was scored against
      branch transitions, which is circular for a domain-general detector. Needs either a small
      labelled set or a self-supervised target, with the shuffled-truth control retained.
      This is Part B of `2026-08-25-work-shift-detection-design.md`.
- [ ] **`lift` / `unusually_prominent` / `absent_but_usual`** — measured superior for the routing
      goal, still living only in `scripts/refseries.py`. Needs a repo-scoped baseline the payload
      lacks; the store already holds every ingested session, so it is the prior's mechanism with a
      wider scope key. Complementary to `prior`, not a substitute (session scope collapses every
      lift to x1.0).
- [ ] **Why does `repo` attribute 0 of 1,502 blocks?** Possibly related: `scripts/test_act_artifact.py`
      fails 21/22 with `KeyError: 'repo'` on a committed study frame that predates the dimension —
      reproduced at HEAD, so pre-existing, but it is the second sign that `repo` was added and never
      fully landed. It is in ALLOCATION and never fires on this
      corpus. Either the remote is unresolvable for these sessions or the level is not being
      emitted. Unexplained.

## KNOWN GAPS — stated, not fixed

- **The no-repo population is unmeasured.** Zero of 496 sessions lack branch data, so the claim
  that a repo-less session behaves identically rests on a counterfactual (deleting `branch`/`repo`
  from repo-having sessions leaves attribution at 95.3%), not on real non-engineering work.
- **Arm C (turns) was never judged.** UNMATCHED at every setting — its blocks are far shorter than
  any A′ candidate, so no same-size comparison existed. Its raw attribution is the corpus best
  (96.2%). Judging turns against minutes needs a control pairing on something other than duration.
- **The detector blind spot every prior study inherited:** three EQUAL-weight segments leave the
  later ones tied at cumulative weight, so the alphabetical tie-break collapses two real transitions
  into one edge. Verified against shipped code, never measured on the corpus. Now moot for blocks
  (the detector is ablated) but still live for `dynamics`' slice sizing.
- **`tie` / `no_majority` are 46 of 12,016 slots** — real but tiny; the evidence+status work makes
  them visible.

## TECH DEBT — from reviews, triaged as deferred

- `blocks.active_bins` reaches into `store._conn()` with raw SQL rather than a `Store` method.
  Byte-identical to the study helper, which is what makes the oracle exact; refactor with the
  equality test standing guard.
- `bin` outlives `event` (400-day event retention, bins never pruned except `term`), so
  `active_bins` can report a bin active after its events are gone. Not reachable today; any caller
  must keep the `WindowExpired`/410 gate in front of `cut()`.
- `active_bins` reads only PRECOMPUTED levels, so a period whose only events are unbinned levels
  reads as idle. Theoretical.
- `sidecar.open_store()` opens the corpus **read-write** and changes its SHA on close — every study
  run needs a verified copy. Wants a `--db` flag or `?mode=ro&immutable=1`.
- `block_sizing_eval.verdict`'s best-candidate ranking treats an UNMATCHED candidate as
  interchangeable with a matched all-passing one.
- The retired `choose_cap` rule ("smallest cap within 5 points") is still live and is what
  `main()` runs with no flag, despite its own pre-registration saying it is not reused.
- `internal/agent/publish/report.go` + `report_show_test.go` (634 lines, untracked) are **DEAD** —
  the rendered report is not going to Atlas. Everything in them is unexported and nothing outside
  the package references them, so they compile and nothing else notices. Delete when convenient;
  left in place because untracked files have no git copy to recover from.
