# Project attribution

> **Provenance — split out of `AGENTS.md` on 2026-09-17** (at `67be5f5`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-09-03**.
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *Project attribution* states the toggle, the privacy consequence and
the two model children. This file carries the scoring decision, its evaluation
and the packaging traps. Live follow-ups: `docs/notes/whats-next-attribution.md`;
runbook: `docs/attribution-smoke.md`.

**PROJECT ATTRIBUTION (`internal/agent/attrib/`, `daemon/attrib.go`,
`sidecar/app/analysis/attribution.py`, `sidecar/app/verifier.py`) — which declared
project a closed BLOCK belongs to, decided on device.** OFF by default
(`KELD_ATTRIBUTION`, or `attribution` in `~/.keld/agent-config.json`). An org declares
projects (`settings.RemoteProject`: id/title/description/team/repos/keywords/ticket key)
via `KELD_PROJECTS_FILE` or the settings poll's `projects` key; the daemon pushes them
down with `POST /projects` and the block emitter's `OnPublished` hook schedules a durable
job per published block. `POST /attribute` takes COORDINATES and the block's own
already-computed dims and answers with project IDS, confidences, closed enums and integer
timings — no text, no span, no offset, in either direction.
- **The score is embedding + a deterministic boost, and NOTHING is assigned without the
  encoder.** `BOOST_CAP` (0.35) sits below `THRESHOLD - BAND` (0.41) by construction, so a
  boost-only score cannot cross the bar; a machine with no weights answers
  `degraded:weights_unavailable` and the durable job re-attributes it later. There is
  exactly one attribution path, the benchmarked one. `source` is therefore
  `embedding|verifier` — **"metadata" is not producible** — and `encoder_state` is
  `warm|absent`, never `cold`: the route never loads the encoder inside a request, it
  answers `pending` and warms on a background thread.
- **⚠️ TWO NEW MODEL CHILDREN AND A NEW NATIVE DEPENDENCY, ON THE SAME BUDGET.** The
  **text encoder** (Qwen3-Embedding-0.6B, ~1.2 GB of weights, 1.70 GB resident /
  2.35-2.43 GB peak) is the SAME child the signal-embeddings path uses — one provisioner,
  never two fetching into one directory. The **verifier** (Gemma 4 E2B Q4_K_M GGUF, ~3 GB,
  `llama-cpp-python` — the repo's first **compiled native** runtime dependency, pinned
  exactly for that reason) runs in its OWN recycled worker child with its own
  `WorkerManager`, its own `KELD_VERIFIER_RSS_MARGIN_MB` (512, half GLiNER2's, because a
  fixed-`n_ctx` GGUF has less room to drift than a transformer on unbounded input), and
  `KELD_ATTRIBUTION_VERIFIER=1` as its opt-IN — ⚠️ **OFF by default since 2026-09-03**
  (it was on within the gate until then): the one real-data A/B went 1-for-3 for minutes of
  CPU per block, its 3 GB beside the encoder's 1.7 GB exhausted swap on the benchmark
  machine, and every figure in `docs/notes/whats-next-attribution.md` §8 was measured
  without it, so the default now matches what was measured. The provisioner reads the same
  switch, so a machine that never opts in never fetches the GGUF. Both halves
  (`attrib.VerifierEnabled`, `verifier.enabled`) mirror one table and change together.
  So `KELD_SIDECAR_MEM_BUDGET_MB` is now
  spent by **three** children, not one: the verifier's manager subtracts the parent's RSS
  plus the other two children's high-water peaks before computing its own hard limit
  (`_verifier_reserve_rss`), and reports the overrun rather than absorbing it — `/metrics`
  gained a `verifier` block beside `worker` and `embed`. ⚠️ **Both children are polled by
  `lifespan`'s one poll loop.** The verifier's manager shipped UNPOLLED for a whole branch,
  and `poll()` is the sole driver of the RSS ceiling, the recycle, the idle unload and the
  pressure eviction — so a llama.cpp child held its weights and a 4096-token KV cache for
  the sidecar's entire life, unbounded and unmeasured. A manager that is constructed is not
  a manager that is guarded.
- **⚠️ `llama_cpp` must be in the PyInstaller spec, and its absence is invisible.** It is a
  ctypes binding: `libllama` plus ~10 ggml shared objects are opened by a path computed at
  import time (PyInstaller's binary analysis cannot follow that), and the one
  `from llama_cpp import Llama` lives inside `Verifier.__init__` — PyArmor-encrypted
  bytecode under `KELD_OBFUSCATE=1`, which CI sets for releases. So the module AND its
  libraries are both invisible, and a spec missing them ships a binary that starts, is
  healthy, classifies, scans for PII and fails EVERY verdict. Exactly the freeze_support()
  and presidio failure class. `make freeze-check` / `make obfuscate-check` now spawn the
  verifier child and demand a real verdict (`keld-agent-sidecar --selftest verifier`), and
  that arm FAILS rather than skips when no GGUF is present — a gate that passes quietly on
  the machines lacking the model is the same as no gate.
  ⚠️ **BUT IT IS DEVELOPER-MANUAL TODAY, NOT CI.** Nothing under `.github/` invokes
  `scripts/freeze-check-local.sh` or `--selftest`: `ci.yml` excludes both targets
  deliberately (they need the ~5 GB sidecar venv and the GLiNER2 weights, and take
  minutes), and `installers.yml` runs its own inline `/classify` smoke, which by
  construction cannot reach the verifier's import path. So the arm exists and must be run
  by hand before a release; **nothing automatically stops this defect returning.** Wiring
  it into `installers.yml`'s per-release smoke — where the freeze already happens — is the
  follow-up, and it needs a GGUF on that runner or an explicit
  `KELD_FREEZE_CHECK_VERIFIER=0` waiver.
- **⚠️ TURNING ATTRIBUTION ON STARTS ENCODING MESSAGE TEXT ON DEVICE.**
  `daemon/sidecarenv.go` sets `KELD_TEXTEMBED=1` (set-if-absent) whenever attribution is
  on, because `/attribute` needs the same encoder. Nothing derived from it is PUBLISHED by
  that — feature rows stay gated on `KELD_FEATURES`/`KELD_FEATURES_PUBLISH` and the org's
  `features` toggle, and `/attribute` answers with ids — but the encoder does read message
  text locally, which is new behaviour on a machine that had the toggle off, and it is the
  toggle this file describes as the one deciding whether text is read to keep something
  derived from it. `KELD_TEXTEMBED=0` explicitly still wins; `/attribute` then answers
  `skipped:disabled` for every block.
- **The two model downloads are gated on a KNOWN NON-EMPTY project list.** 4.2 GB fetched
  for an org that has declared nothing buys nothing — every `/attribute` answers
  `skipped:no_projects` without loading a model — and Atlas does not serve `projects` yet,
  so that is currently every machine without `KELD_PROJECTS_FILE`. The gate is read live per
  published block, so a list arriving on a later poll starts the fetch with no restart.
- **⚠️ `skipped:no_projects` is NON-TERMINAL while the daemon holds a list, and the daemon
  re-posts after a sidecar respawn.** `attribution._projects` is module state in the sidecar
  PARENT, so the supervisor's crash-restart takes it, while the daemon's change-gated POST
  concludes there is nothing new to say: every subsequent block was published attributed to
  nothing AND THE JOB DELETED, permanently, until the daemon itself restarted. Both halves
  are fixed — `Supervisor.SetOnRespawn` re-posts the list, and the attributor HOLDS the job
  (the shape `degraded:weights_unavailable` already uses) whenever it believes projects are
  declared. The second half also closes the startup race, where the first sweep runs
  concurrently with the first POST. Anything else the daemon pushes DOWN once, rather than
  riding each request the way `PIIRegions` does, has the same shape and belongs on that hook.
- **Version skew HOLDS rather than quarantines.** An older frozen sidecar 404s `/attribute`;
  that is surfaced as `AttributeResult.RouteUnsupported` and holds the job, because the
  sidecar updates on its own cadence and the work becomes doable when it catches up. A
  genuine quarantine (4 real errors) now emits `attribution.job_quarantined` — `Store.List`
  skips subdirectories, so `spool/attrib/bad/` is never re-read and the loss is otherwise
  invisible to the fleet.
- **The decision is RELATIVE, not an absolute bar: `cut = max(null, top - MARGIN)`.**
  An absolute threshold conflated two questions and real-transcript evaluation showed it
  (2026-09-02, 21 real blocks: every block carries a per-block score offset, so one bar
  admits 0.4x false positives beside 0.6+ true ones). LEVEL — does the block belong to
  anything? — is answered by a competitor: `NULL_DOC` is embedded beside the projects and
  a project attributes only by BEATING "nothing" in the same ranking (the null gets no
  boost). SHAPE — is there a clear winner? — is `MARGIN` (0.08, `KELD_ATTRIBUTION_MARGIN`):
  everything within MARGIN of the top is assigned, and `VERIFY_HALO` (0.04,
  `KELD_ATTRIBUTION_VERIFY_HALO`) around the cut is where the verifier adjudicates. One
  deliberate consequence: a strong exact-match boost (repo + ticket) CAN carry a project
  past the null on its own — with an encoder present; with none, nothing is ever assigned
  (AC-4 as amended).
- ⚠️ **WHAT IS SCORED CHANGED ON 2026-09-03, AND THE CHANGE IS MEASURED: THE WHOLE BLOCK,
  MEAN-POOLED, CENTRED.** Until then `_span_texts` returned the USER stream only, on the
  argument that "scoring a project against the model's own prose would attribute work to
  whatever the assistant happened to name" — plausible, written at implementation time, and
  never measured; the eval recorded the cost the day it shipped (benchmark 0.929 with
  assistant text, ported pipeline 0.823 without) and kept the rule. On 61 real, labelled
  blocks (`docs/notes/whats-next-attribution.md` §9): user text alone put **28%** of blocks
  on the right project; the whole block, mean-pooled and centred, put **92%** there. Three
  facts behind that: a real block's user words are often "continue" while the reply names
  the work; **24 of 25 blocks with no user text at all** — agent continuations — have
  assistant text, so this is also how the structurally-silent third of a machine's work
  attributes; and MEAN beats MAX (92% vs 82%) because the best of ~12 messages is high for
  anything. The feared failure did occur, on 4 of 61. Only the USER's words still feed
  `concepts` (which publishes phrases) and the verifier prompt.
  **Centring** (`attribution.Offsets`) subtracts each document's running mean similarity
  over the messages the machine has scored — because the null is written as speech and the
  projects as artifacts, the null out-scored every project on 66% of individual messages
  regardless of topic, and the projects' own baselines spanned 0.093 against a MARGIN of
  0.08. It is a running mean of SCALARS persisted at `~/.keld/state/attribution-offsets.json`
  (two floats per document, never a vector), keyed by the document's TEXT so a reworded
  project starts fresh, and GATED all-or-nothing at `KELD_ATTRIBUTION_MIN_BACKGROUND` (50)
  messages: below it the decision is exactly the uncentred one, because a ten-message
  offset measured non-monotone. Whole-block without centring measured 59%. The attribution
  meta carries `centred` and `background_n`, and `model_versions.scoring` names the rule,
  because centred and uncentred rows differ on ~40% of blocks and nothing else would say so.
  **Rollback is one environment variable:** `KELD_ATTRIBUTION_SCORING=user-max` restores the
  pre-2026-09-03 decision exactly (user turns only, MAX, no centring, no baseline observed)
  and stamps `scoring: user-max-uncentred-v0`. The baseline file is `{"stats", "seen"}`:
  `seen` is the last 2,000 block keys folded in, so a RETRIED job (held on publish failure,
  re-POSTed after a sidecar respawn) enters the running mean once, not once per attempt. A
  baseline that cannot be saved is re-learned after a restart and warned about ONCE per
  process — never silently, because a machine that can never persist sits uncentred for its
  first 50 messages after every restart.
- **Quality.** The 0.823 recorded before this was measured under the OLD absolute threshold
  with user-only text and the verifier on — a pipeline that no longer exists in three ways.
  `sidecar/app/test_attribution_quality.py` (opt-in, `KELD_ATTRIBUTION_EVAL=1`) now scores
  the shipped configuration — whole-block, mean, centred (primed over the fixtures), no
  verifier unless `KELD_ATTRIBUTION_EVAL_VERIFIER=1` — and its floor is a regression tripwire
  under THAT measurement, not the design gate. MARGIN/VERIFY_HALO are starting points
  awaiting calibration on LABELED REAL blocks — the correction flywheel, not another
  synthetic sweep. Runbook: `docs/attribution-smoke.md`.

