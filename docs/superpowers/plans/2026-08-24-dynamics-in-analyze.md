# Dynamics in /analyze — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `/analyze` reports not just what a window contains but **how it is changing** — a recent slice read against a longer baseline — and the sizing of that slice is chosen by measurement, not assertion.

**Architecture:** The reference store (tasks 1-5, complete) makes a window a ~2 ms query, so a single `/analyze` request can afford to roll up a slice AND a baseline and compare them. No scheduler, no fan-out, no Atlas change. The tick model in `2026-08-23-moving-characterization-design.md` is **deferred** — its efficiency justification was spent by the store; what remains of it is a cadence decision, separable from this.

**Spec:** `docs/superpowers/specs/2026-08-23-moving-characterization-design.md` (the tick parts are deferred; the sizing seam, the failure modes and the bar for adaptive methods all still bind).

## Global Constraints

- **Sizing is a SEAM plus a MEASUREMENT.** Fixed slice/baseline is the default and stays the default until an adaptive method beats it under a pre-registered rule. `river` (0.26.1, 3 packages) ships ADWIN, EWMA, PageHinkley, KSWIN. This project already measured change-point boundaries reaching only PARITY with a fixed constant at much higher complexity — that is the bar, not a reason to skip the comparison.
- **`MIN_EVIDENCE = 5` was DERIVED for a 60-minute window** (the first n at which `0.5**n` clears 5%). A shorter slice carries proportionally less evidence; carrying the constant over will mark nearly every slice unattributed. Re-derive it as a function of slice length. **This is the most likely way this design produces a confident wrong number.**
- **A missing bin is not a zero.** `bin` is sparse and only default levels are precomputed; `bin_level` enforces it.
- **`/analyze` forgoes `bin` on the digest path** (task 3: reconcile must be re-scoped per window). Dynamics MAY use bins — that is what they were built for — but must not silently mix a bin-derived number with an event-derived one in the same comparison.
- Never serve past the watermark, and never past the retention serving floor (410 `analyze_expired`).
- `/analyze` takes COORDINATES, never text. No span, no offset, no prompt text in any response or log line. Must not use `_dispatch`; must not require GLiNER2.
- Sidecar tests are standalone scripts with a `__main__` runner, **never pytest**.
- **Inject a mutation per behaviour and confirm the suite bites.** Every task on this branch found a test that passed vacuously.
- Never `git add -A`/`checkout`/`stash`/`clean`. Uncommitted user work must survive: `.gitignore`, `internal/agent/daemon/custom_passes*.go`, a `daemon.go` hunk, `scripts/context_value.py`, `scripts/prompt-v9.md`.

---

### Task 1: The evidence floor as a function of slice length

**Files:** Modify `sidecar/app/analysis/window.py`; test alongside. Durable measurement to `~/keld/refseries-context/dynamics/`.

Do this FIRST. Every later task's attribution rate depends on it, and getting it wrong makes every subsequent measurement meaningless.

- [ ] **Step 1:** Re-derive the floor. The existing derivation: take the 0.50 share floor as the null hypothesis, and `0.5**n` first clears 5% at n=5. Work out what the equivalent statement is for a slice of arbitrary length, and whether the right form is a constant, a function of duration, or a function of observed evidence density.
- [ ] **Step 2:** Measure the consequence over the frozen corpus at several slice lengths (5, 10, 15, 30, 60 min). Report, per length: attribution rate, and how many slices become unattributed versus the current constant. A design that leaves 90% of 10-minute slices unattributed is not usable and the number must be visible before anything is built on it.
- [ ] **Step 3:** Implement, with the derivation in the comment as `MIN_EVIDENCE`'s already is. Keep the 0.50 share floor unchanged.
- [ ] **Step 4:** Run every sidecar suite. The existing 60-minute behaviour must be unchanged — pin that.
- [ ] **Step 5:** Commit.

---

### Task 2: Slice-against-baseline, with the sizing behind a seam

**Files:** Create `sidecar/app/analysis/dynamics.py`; modify `analyze.py`, `main.py`; tests alongside.

**Produces:** `dynamics(store, session, slice_start, slice_end, baseline_start) -> dict` and a `Sizer` seam with a `FixedSizer` implementation. `/analyze` gains the dynamics block.

- [ ] **Step 1: Write the failing tests.** A constructed series where the dominant `workspace` flips mid-window must report high turnover; a stationary series must report ~zero. Assert on both, because a metric that only fires is indistinguishable from one that always fires.
- [ ] **Step 2: Run and watch them fail.**
- [ ] **Step 3: Implement.** Compute at minimum: **turnover** (share of slice evidence in values absent from the baseline), **concentration shift** (dominant value's slice share minus its baseline share), **emergence/decay** (values entering or leaving). Decide the exact definitions and justify them — an unnormalised turnover will scale with evidence volume rather than change, which is the trap.
- [ ] **Step 4:** Every sidecar suite, plus `go test ./...` (the response gains fields; the Go client must decode unchanged or you must say why).
- [ ] **Step 5:** Commit.

---

### Task 3: The pre-registered comparison — does an adaptive sizer beat fixed?

**Files:** Create `scripts/sizer_eval.py`. Durable results to `~/keld/refseries-context/dynamics/`.

**This is an experiment, not a feature.** Follow the method that made the activity study decisive: pre-register the rule in a committed file BEFORE running, then report against it whatever the outcome.

- [ ] **Step 1: Pre-register.** Ground truth for "the work changed" is DETERMINISTIC and already in the store: a transition is where the dominant allocation value (`workspace`, then `branch`) changes across the series. Write down the scoring rule (how close a detected change point must be to count as a hit, how false positives are counted), the sample, and the decision rule, and commit it before running anything.
- [ ] **Step 2: Implement the rival sizers** behind Task 2's seam: `FixedSizer`, EWMA fast/slow, `ADWIN`, `PageHinkley`, `KSWIN` — the last three from `river`. Add `river` to `sidecar/requirements.txt` ONLY if the experiment justifies keeping it; if fixed wins, the dependency does not ship. **Verify river's install does not move numpy or torch** — presidio 2.2.363 taught this the hard way.
- [ ] **Step 3: Run** over the frozen corpus. Report per sizer: hit rate against real transitions, false-positive rate, and median distance from a detected point to the nearest real transition.
- [ ] **Step 4: Report against the pre-registered rule**, including if the answer is "fixed wins and the dependency is not worth it". That is a successful outcome.
- [ ] **Step 5:** Commit the script and `RESULTS.md`.

---

### Task 4: Which dynamics earn a place on the wire

**Files:** Durable results to `~/keld/refseries-context/dynamics/`; then modify whatever Task 2 shipped.

The store makes these cheap. Cheap is not the same as worth publishing — this project has the measurement to prove it: a 16 KB characterisation scored WORSE than nothing (-3.3 / -20.0) while a digest carrying the same facts scored +36.7, because it stated a conclusion rather than leaving the reader to divide two numbers.

- [ ] **Step 1:** For each dynamic from Task 2, decide what question it answers and whether a reader could act on it. Prefer a stated conclusion over a raw pair of numbers — that is the measured lesson.
- [ ] **Step 2:** Measure the distribution of each over the corpus. A metric that is ~constant across every window carries no information regardless of how principled it is.
- [ ] **Step 3:** Drop the ones that do not survive, and say which and why. Bump `enrich.SchemaVersion` if the published vocabulary changes — the standing rule, and the unpublished-branch argument is retired.
- [ ] **Step 4:** Commit with the measurements in the message.

## Not in this plan

- **The tick / regular cadence.** Deferred; a cadence decision, not a prerequisite.
- **Codex and Gemini.** `/analyze` cannot resolve their prompt ids; unchanged here.
- **Cross-session or cross-machine baselines.** The store is per-machine, per-session-file.
