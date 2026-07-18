# Handoff — enrichment activity-vs-subject work (keld-signal)

**Updated:** 2026-07-18. **Repo:** `~/keld-signal`. **Toolchain:** Go 1.26 at `/opt/homebrew/bin/go` (not on PATH by default — `export PATH="/opt/homebrew/bin:$PATH"`).

## The problem we set out to fix
Enrichment mislabeled the user's **work activity/role** based on the **subject** the prompt discusses: engineering sessions in Claude Code (building marketing/finance/etc. software) got `function_guess` = `mkt`/`fin`/… and skewed `task_type`. Root cause: GLiNER2 is a **bi-encoder** that scores prompt-vs-label-description overlap, and the function labels describe *subjects*, so subject nouns dominate.

## What SHIPPED — v0.4.0 (main @ `7292d92`, tag `v0.4.0`)
Two changes, both validated on a purpose-built eval before shipping:
- **A0 — `task_type` uses the context preamble** (was raw text). Unconditional. `extractors.go` `TaskTypeExtractor`.
- **A4 — compositional `function_guess`**: for interactive coding tools (`claude_code`/`codex`), `function_guess = eng` structurally (not topical). Default-on; **disable with `KELD_ENRICH_COMPOSITIONAL_FUNCTION=off`** (or `0`/`false`). Generic tools keep topical. `a4_compositional.go`.
- **SchemaVersion 2 → 3** (`labels.go`) — signals the derivation change to Atlas (label vocab unchanged).

**Validated numbers (source-attributed confound + gold, 8-thread sidecar):** `leakage(function_guess)` 0.375 → **0.000**; `false_eng` 0 → **0**; confound function accuracy 0.773 → **0.909**; gold-only function accuracy flat at **0.800**. Disable escape-hatch verified (restores 0.375).

## What was REJECTED (measured, not shipped)
- **A1 — tool prior (soft posterior over functions):** INERT. Floor sweep 0.15/0.05/0.02 all left `leakage(function_guess)=0.375`. The model scores `eng` ~0 on subject-heavy prompts, so reweighting surfaced candidates can't promote it. (Deleted from tree.)
- **A2 / A2.1 — rewriting the function label descriptions:** REGRESSED. Verbose+negated labels catastrophic (function acc 0.824→0.059); short/eng-boosted still worse (→0.500). **The v1 labels are the best labels.** Bi-encoders don't understand negation ("not building marketing software" matches "marketing software") and long similar descriptions collapse score separation. (Deleted from tree.)

## The eval harness (durable — this is the main asset for future levers)
- **Command:** `keld-agent eval [--confound] [--context]` — runs the pipeline against the **live** GLiNER2 sidecar and prints per-facet accuracy + (with `--confound`) `leakage(function_guess)`, `leakage(task_type)`, `false_eng`. Code: `internal/agentcli/evalcmd.go`.
- **Data:** `internal/agent/enrich/eval/gold.jsonl` (73 clean rows; 20 have `function_guess` + a realistic `source`) and `confound.jsonl` (24 rows: `c1`=eng-activity/non-eng-subject→`claude_code`; `c2`=genuine non-eng→`generic`; `c3`=fragments). Metrics + `LoadConfound`/`GoldRow.Source` in `eval.go`.
- **Metric definitions:** `subject_leakage_rate` = over c1, fraction where facet ≠ the eng-correct value; `false_eng_rate` = over c2, fraction wrongly predicted `eng`.
- **Result logs:** `docs/superpowers/plans/*results*.txt`, `eval-baseline.txt`, `v0.4.0-validation.txt`.

### How to run the eval fast (8-thread sidecar)
The sidecar defaults to a **2-thread** cap and loads its model **on-demand** (~54s cold). For eval speed, run a daemon with thread caps raised (they're set-if-absent in `sidecarenv.go`, so operator env wins):
```
export PATH="/opt/homebrew/bin:$PATH"
/usr/local/bin/keld-agent stop      # avoid two daemons
go build -o /tmp/keld-agent-exp ./cmd/keld-agent
OMP_NUM_THREADS=8 MKL_NUM_THREADS=8 OPENBLAS_NUM_THREADS=8 NUMEXPR_NUM_THREADS=8 \
  KELD_SIDECAR_MAX_THREADS=8 KELD_SIDECAR_BIN=/usr/local/keld/keld-agent-sidecar/keld-agent-sidecar \
  /tmp/keld-agent-exp run >/tmp/exp-daemon.log 2>&1 &      # 8-thread daemon
# wait for sidecar, warm it (POST /classify to the sidecar_port in ~/.keld/agent.json), then:
/tmp/keld-agent-exp eval --confound --context
# teardown: kill the daemon, then /usr/local/bin/keld-agent start  (restore launchd)
```
Gate a change: run `--confound --context` (leakage/false_eng) AND `--context` (gold-only, no-regression). Keep a change only if leakage↓, gold accuracy Δ≥0, `false_eng` flat.

## Lever menu (from the original brainstorm) — STATUS
- **Lever A** (source/tool prior) → A1 — ❌ rejected (inert).
- **Lever B** (rewrite labels) → A2/A2.1 — ❌ rejected (v1 best).
- **Lever C** (compositional: function from tool/activity) → **A4 SHIPPED**. Remaining piece: **A5** (domain-conditioned function *candidates* for generic tools, e.g. restrict to {eng,it,data} when domain=software). NOTE: "Lever C" is largely done — A4 was its core.
- **Lever D** (strip topical signal / entity-masking before the function pass) — ⬜ untried.
- **Lever E** (model upgrade: NLI hypothesis templates, or a small on-device instruction-tuned LLM classifier that can *reason* activity-vs-subject) — ⬜ untried.
- **Lever F** (calibration / abstain-to-prior on low-margin/conflicting predictions) — ⬜ untried.

## Open items / remaining problems
1. **`task_type` leakage still ~0.625** on the expanded confound — A4 fixed function, not task_type. Prime target for the next lever (E or D likely most promising, since A0 already gave task_type all the context it can use).
2. **Confound set is small (16 c1 rows → 0.06-granular).** Expand it (more verticals, and real prompts as keld runs across more projects — the harness is ready). Only one local Claude transcript exists (this session), so real prompts are scarce for now.
3. **v0.4.0 release CI** was in progress at handoff (run `29622840008`). GitHub's API 503'd badly on earlier releases (v0.3.9) — if the installers leg failed transiently, rerun: `gh run rerun <id> -R ncx-ai/keld-signal --failed` once the API is healthy. Release is "latest" once GoReleaser publishes; the `.pkg` attaches via installers.yml.
4. **Local machine still runs v0.3.8** (launchd `co.keld.agent`). Install v0.4.0 after its pkg builds: `sudo installer -pkg ~/Downloads/keld-v0.4.0-arm64.pkg -target /` (Apple-Silicon: the postinstall now `mkdir -p /usr/local/bin` since v0.3.7).
5. **Older unrelated open item:** the pkg is **unsigned** (no Apple Developer secrets). CLI `sudo installer` works unsigned; for notarized distribution, that's the Apple Developer Program ($99/yr) + 5 GitHub secrets + two CI gaps (keychain import; `.p8`-to-file) noted earlier.

## Mechanistic learnings (save future dead-ends)
- GLiNER2 = bi-encoder: keys on token/semantic overlap, **no negation**, short discriminative labels beat verbose prose. Don't re-attempt label rewrites without this in mind.
- Sidecar loads model **on-demand** (first inference request); it doesn't warm proactively. (v0.3.9 daemon warmup covers the daemon path; the eval self-warms.)
- Eval MUST attribute per-row `source` realistically (eng/software → coding tool; non-eng → generic) or a tool-conditioned rule blows up `false_eng` as an artifact.

## Process
This work used the superpowers loop (brainstorm → spec → plan → subagent-driven execute → review → release), measure-first with a strict no-regression gate. Specs/plans under `docs/superpowers/specs/` and `docs/superpowers/plans/`. Progress ledger at `.superpowers/sdd/progress.md` (git-ignored).

---

## RESUME PROMPT (paste after `/clear`)

> Resuming work on **keld-signal** enrichment quality (`~/keld-signal`). Read `docs/superpowers/HANDOFF.md` first — it has full state. TL;DR: v0.4.0 shipped the activity-vs-subject fix (A0 task_type context + A4 compositional function; function-leakage 0.375→0, false_eng 0). A1 (tool prior) and A2 (label rewrites) were measured and rejected. There's a durable eval harness (`keld-agent eval --confound --context`) with a source-attributed confound set and leakage/false_eng metrics; run it on an 8-thread sidecar per the HANDOFF recipe. **Next task:** pursue the remaining levers to cut the still-high `task_type` leakage (~0.625) and further reduce residual `function` leakage — candidates are **A5** (domain-conditioned function for generic tools), **Lever D** (entity-masking), **Lever E** (NLI or small on-device LLM classifier), **Lever F** (calibration/abstain). Use the same measure-first, strict-no-regression loop: implement each behind a flag, run the eval, keep only if leakage↓ with gold flat and false_eng≈0. Confirm the v0.4.0 release finished (`gh run list -R ncx-ai/keld-signal`) and, if I haven't, install v0.4.0 locally. Ask me which lever to start with before building.
