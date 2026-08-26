# Block model — open work

Rewritten 2026-08-26. The previous version was stale in a way worth naming: it still listed the
Signal-side "Phases 3-5" from a plan that assumed **the tick becomes the block trigger**. That was
superseded — blocks tile active time, so there are no gaps to fill and the tick's gap-finding is
obsolete. Ticking checkboxes would have preserved a dead plan.

Atlas's side is NOT duplicated here. It lives in `~/keld/keld-atlas`:
`docs/superpowers/plans/2026-08-25-workstreams-v2-roadmap.md`.

---

## SHIPPED

Signal v2 is complete end-to-end and ships OFF behind `KELD_BLOCKS`.

- The cutter — 20-minute cap, 15-minute idle, detector ablated (`0e8244e`, `681f60c`), pinned equal
  to the measured arm (`7a75ca3`).
- `covers` — the episode-to-block mapping (`76e8087`).
- `blockdigest.py` + `POST /blocks` (`c0c7ab5`).
- The Go emitter and its start (`ff30d0b`, `505013b`).
- `evidence` + `status` on every workstream dimension (`874c727`) — 924 of 12,016 dimension-slots
  were being silently suppressed.
- Data formats out of `lang`, extension map extended (`db26c44`).
- CI now gates merges on all 37 sidecar test files (`6ec59a9`) — it ran none of them before.
- AGENTS.md covers the cutter and the four decisions someone will try to revert (`730db55`).

**Verified end-to-end on real data, 2026-08-26:** a real transcript through the new sidecar's
`/blocks` yields 20-minute blocks with `project`/`branch`/`language`/`skill` attributed and their
evidence counts; Atlas's own validation and storage accept them and upsert on re-delivery.

---

## OPEN — Signal side

- [ ] ⚠️ **`covers` IS EMPTY ON HISTORICAL BLOCKS, and it is the field Atlas needs.** Found on the
      first real run, 2026-08-26. `resolve.RecentUserPromptIDs` reads only the FILE TAIL —
      `idTailBytes = 16 MB`, seeking to `size - idTailBytes` — because it was built for "recent
      prompts for a model's context window". But the emitter drains blocks CHRONOLOGICALLY from a
      cursor, so on a transcript larger than 16 MB every block whose prompts fall before that
      offset gets `covers: []`. Measured: a 20 MB transcript, 72 blocks emitted, **0 covers on all
      of them** — their prompts sit in the first 4 MB.
      The bigger the session, the more blocks lose their episode mapping, and `covers` is precisely
      the Atlas display join (`covers[].prompt_id` -> `turn_key`). Unit tests cannot catch this:
      any fixture small enough to be a fixture has its head inside the tail window.
      Fix is a design choice, not a one-liner: give the reader the block's TIME RANGE and let it
      seek, or drive prompt ids from the store's `prompt` index (which has every turn plus its
      timestamp) with the daemon supplying the human-prompt filter it already owns.

- [x] **The emitter's loop DOES work — verified against reality 2026-08-26.** Ran an isolated
      daemon (`KELD_HOME` + `KELD_BLOCKS=1`, 20s interval) against this repo's own transcript:
      the sweep found closed blocks, emitted **72 blocks in 9 batches** under
      `corr_scheme: "block"` with id `<session>@<start>`, all eight dimensions attributed
      (including `repo`, since the daemon supplies `resolved`), schema 21.
      Both cursor paths confirmed: against a 401 the cursor **held at 0.0 and re-offered the same
      blocks** on the next sweep; against a 201 it **advanced** to 1787519400.

- [ ] **Weight edits heavier than reads.** Spec written (`d9bb785`,
      `2026-08-25-act-weighted-paths-design.md`), unbuilt. Reads outnumber edits 3.4:1 and every
      path touch emits at weight 1.0, so a block that skimmed twelve files and rewrote one publishes
      what it skimmed. ⚠️ The trap the spec exists for: `evidence` is `sum(weights)` and
      `MIN_EVIDENCE` is a SAMPLE SIZE, so a weighted edit would silently lower a floor whose removal
      measures at P(false attribution) 0.031 → 0.50. Weights must drive the SHARE while evidence
      stays a COUNT.
- [ ] ⚠️ **The digest is O(session) per block, so a session costs O(T²).** Measured 1.1 ms at short
      session, 5.9 ms at 8 hours; the growth is `prior`, which spans `[session start, block start)`.
      The corpus holds an 11.7-day session — ~840 blocks each rolling up half of it. Emitter-side and
      per-device, so not urgent, but it is the one superlinear place in the design. Cap the prior's
      span, or cache it per session.

## OPEN — research, nothing depends on it

- [ ] **A domain-general work-shift detector.** The ablated detector was branch-only and failed its
      own validation (7.1% recall vs a fixed control's 17.3%). The store already holds better
      material: `action` (51,618 events, 22 closed values, 488 of 496 sessions) and `tool` (31,888,
      27 values, 488 sessions) — denser and wider-covering than `file` (337 sessions), and both
      describe what KIND of work rather than what codebase. Candidates: Shannon entropy of the
      tool/action mix per bucket, Jensen-Shannon divergence between consecutive windows,
      rate/burstiness (`latency.py` already has gap percentiles).
      ⚠️ Phase 0a's rejection of `action` does NOT refute this — it tested `action` through
      `EwmaSizer`'s novelty encoding, which asks "is a new value outweighing the incumbent", a
      question that is noise for a level whose 22 values recur constantly and never succeed one
      another. Distributional statistics ask a different question.
      ⚠️ **The blocker is ground truth, not compute.** Every detector so far was scored against
      branch transitions, which is circular for a domain-general detector. Needs a small labelled
      set or a self-supervised target, with the shuffled-truth control retained. Part B of
      `2026-08-25-work-shift-detection-design.md`.
- [ ] **`lift` / `unusually_prominent` / `absent_but_usual`** — measured superior for the routing
      goal, still only in `scripts/refseries.py`. Needs a repo-scoped baseline the payload lacks;
      the store holds every ingested session, so it is the prior's mechanism with a wider scope key.
      Complementary to `prior`, not a substitute (session scope collapses every lift to ×1.0).
      ⚠️ **This is now a dependency of the agreed Atlas page design**: the amber
      `Go absent — usually 50% of this repo` pill IS this feature. The pane degrades honestly
      without it, but the mockup promises it.

---

## KNOWN GAPS — stated, not fixed

- **⚠️ Any corpus measurement of `repo` is uninformative.** `repo` rows are written at INGEST from
  the daemon-supplied `resolved={"repo": ...}`, and the study harness that built the frozen corpus
  never passes it. Verified 2026-08-26: ingesting with `resolved` returns `reparsed: true` and
  `repo` attributes at evidence 49. The dimension is fine; the corpus cannot measure it.
- **The no-repo population is unmeasured.** Zero of 496 sessions lack branch data, so "a repo-less
  session behaves identically" rests on a counterfactual (deleting `branch`/`repo` leaves attribution
  at 95.3%), not on real non-engineering work.
- **Arm C (turns) was never judged.** UNMATCHED at every setting — its blocks are far shorter than
  any A′ candidate, so no same-size comparison existed. Its raw attribution is the corpus best
  (96.2%). Judging turns against minutes needs a control pairing on something other than duration.
- **The detector's equal-weight blind spot.** Three equal-weight segments leave the later ones tied
  at cumulative weight, so the alphabetical tie-break collapses two real transitions into one edge.
  Moot for blocks (ablated), still live for `dynamics`' slice sizing.
- **`tie` / `no_majority` are 46 of 12,016 slots** — real but tiny; the evidence+status work makes
  them visible.

## TECH DEBT — from reviews, triaged as deferred

- `blocks.active_bins` reaches into `store._conn()` with raw SQL rather than a `Store` method.
  Byte-identical to the study helper, which is what makes the oracle exact; refactor with the
  equality test standing guard.
- `bin` outlives `event` (400-day event retention, bins never pruned except `term`), so `active_bins`
  can report a bin active after its events are gone. Not reachable today; any caller must keep the
  `WindowExpired`/410 gate in front of `cut()`.
- `active_bins` reads only PRECOMPUTED levels, so a period whose only events are unbinned levels
  reads as idle. Theoretical.
- ⚠️ **`sidecar.open_store()` opens the corpus READ-WRITE and changes its SHA on close.** Every study
  run needs a verified copy. Wants a `--db` flag or `?mode=ro&immutable=1`.
- `block_sizing_eval.verdict`'s best-candidate ranking treats an UNMATCHED candidate as
  interchangeable with a matched all-passing one.
- The retired `choose_cap` rule ("smallest cap within 5 points") is still live and is what `main()`
  runs with no flag, despite its own pre-registration saying it is not reused.
- `internal/agent/publish/report.go` + `report_show_test.go` (634 lines, untracked) are **DEAD** —
  the rendered report is not going to Atlas. Everything in them is unexported and unreferenced.
  Delete when convenient; left in place because untracked files have no git copy to recover from.
- ⚠️ **`services/api/tests/test_agents.py` fails for everyone** (Atlas repo): `NOW` is hardcoded to
  2026-07-24 against a 30-day window, so it has failed since 2026-08-24 and will keep failing.
  Unrelated to any of this work.
