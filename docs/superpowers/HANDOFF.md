# Handoff — enrichment activity-vs-subject work (keld-signal)

**Updated:** 2026-07-17 (v0.5.0/A6 shipped). **Repo:** `~/keld-signal`. **Toolchain:** Go 1.26 at `/opt/homebrew/bin/go` (not on PATH by default — `export PATH="/opt/homebrew/bin:$PATH"`).

## The problem we set out to fix
Enrichment mislabeled the user's **work activity/role** based on the **subject** the prompt discusses: engineering sessions in Claude Code (building marketing/finance/etc. software) got `function_guess` = `mkt`/`fin`/… and skewed `task_type`. Root cause: GLiNER2 is a **bi-encoder** that scores prompt-vs-label-description overlap, and the function labels describe *subjects*, so subject nouns dominate.

## What SHIPPED — v0.4.0 (main @ `7292d92`, tag `v0.4.0`)
Two changes, both validated on a purpose-built eval before shipping:
- **A0 — `task_type` uses the context preamble** (was raw text). Unconditional. `extractors.go` `TaskTypeExtractor`.
- **A4 — compositional `function_guess`**: for interactive coding tools (`claude_code`/`codex`), `function_guess = eng` structurally (not topical). Default-on; **disable with `KELD_ENRICH_COMPOSITIONAL_FUNCTION=off`** (or `0`/`false`). Generic tools keep topical. `a4_compositional.go`.
- **SchemaVersion 2 → 3** (`labels.go`) — signals the derivation change to Atlas (label vocab unchanged).

**Validated numbers (source-attributed confound + gold, 8-thread sidecar):** `leakage(function_guess)` 0.375 → **0.000**; `false_eng` 0 → **0**; confound function accuracy 0.773 → **0.909**; gold-only function accuracy flat at **0.800**. Disable escape-hatch verified (restores 0.375).

## What SHIPPED — v0.5.0 (A6, schema v4)
One change, validated on the eval before shipping:
- **A6 — `task_type` classifies against readable label DESCRIPTIONS, not bare id strings.** task_type was the last facet handed the bare vocab words (`codegen`, `other`, …), so `other` was an undefined catch-all swallowing engineering work phrased as debug/fix/refactor/CI/infra/ops. The load-bearing choice: codegen's label text = **"software engineering"** (NOT "codegen"/"code generation" — the narrow token only captures greenfield "write code"). Default-on; **disable with `KELD_ENRICH_TASKTYPE_DESCRIPTIONS=off`** (or `0`/`false`). Code: `a6_tasktype.go` (`TaskTypeDefs` + flag), routed via `classifyPass` in `extractors.go`. **SchemaVersion 3 → 4** (`labels.go`).

**Validated numbers (confound + gold, warm sidecar):** `leakage(task_type)` 0.625 → **0.062**; gold task_type accuracy 0.580 → **0.696**; combined task_type accuracy 0.548 → **0.742**; `leakage(function_guess)` and `false_eng` unchanged at **0.000**; all other facets flat. Escape-hatch verified (restores 0.625).

**How A6 was found (measure-first, reusable method):** a *label bakeoff* — the throwaway `internal/agentcli/diag_test.go` classified each c1 row against candidate label sets directly against the live sidecar (many hypotheses, one cheap `Classify` each, scored on c1-leak AND gold codegen-recall/non-codegen-preservation) *before* any source change or slow full-eval. It found "software engineering" (leak 0.062, gold 10/10) strictly dominated "codegen"/"code generation" (0.625/0.688) and enumerated descriptions (A6 v1, inert — diluted the codegen token, the A2 failure mode). Recreate that bakeoff for the next label experiment; it's far faster than the full `eval` binary. (The diag file was deleted after use — it's in git history / this handoff.)

## What's BUILT on `feat/speech-act-facet` (schema v5; pending merge/release)
The **speech_act facet** — first-class emitted facet classifying the current prompt as `command` / `question` / `statement` / `fragment`. New Wave1 `SpeechActExtractor` (`speechact.go`) classifies **`ctx.Text` only** (mood is a property of the ask, not the context) via `classifyLabeled` (a text-param split of `classifyPass`). Emitted in `Profile` + carried on the Atlas wire (`publish.go`). **SchemaVersion 4 → 5** (genuine additive contract change).
- **Purpose:** subject-independent structural signal, same "structure over subject" family as A0/A4/A6. Emitted now; a FOLLOW-UP spec will use it to *condition* task_type/activity (deferred — designed against the baseline below).
- **Eval substrate added:** adversarial **`s1` class** (20 rows in `confound.jsonl`: mood-is-the-trap — questions/statements in coding context, fragments, control commands) + `speech_act` backfilled onto all 73 gold rows (function-based convention: "can you X?" action-requests = `command`, info-seeking = `question`). Metrics `speech_act_accuracy` (+ per-mood) and `s1_downstream_baseline` in `eval.go`, printed by `keld-agent eval`.
- **Label wording = bakeoff-selected** (same method as A6). Winner: `command`="a task to carry out" (NOT "a command/instruction" — many imperatives read as *describing a task* to the bi-encoder; task framing lifted command recall 35→44/65, overall 0.624→0.731). `question`="a question asking for information", `statement`="a statement describing a situation", `fragment`="a short follow-up or acknowledgement".
- **Validated numbers:** `speech_act_accuracy` **0.731** (gold+confound) / 0.699 (gold-only). Per-mood: question 14/14, statement 5/5, command 44/65, **fragment 5/9**. `s1_downstream_baseline` = **0.750** (current unconditioned pipeline mislabels 75% of trapped mood pairs — the headroom the conditioning lever targets). **Zero regression:** gold-only accuracy on every prior facet byte-identical to v0.5.0; leakage(function)=0, leakage(task_type)=0.062, false_eng=0 all flat.
- **FINDING (sub-0.8 bar):** command recall (~0.68) and fragment (~0.56) are the weak axes. Fragment is an inherent ceiling (terse, context-dependent — no wording moved it past 5-6/9). Command leaks to statement/question. These are the targets for the conditioning lever and/or more `statement`/`fragment` eval rows.

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
- **Lever D** (strip topical signal / entity-masking before the function pass) — ❌ **rejected for task_type** (measured). Diagnostic: task_type "leakage" on c1 is NOT subject-driven — it's activity-shape confusion (engineering verbs → other/extraction/classification). An *oracle* subject-mask (the theoretical ceiling of D) fixed only 1/10 leaks (0.625→0.562), and the entity pass detects nothing in these prompts. A6 (broadening the codegen label) was the right tool instead. D may still apply to *function* leakage on generic tools — untried there.
- **Lever E** (model upgrade: NLI hypothesis templates, or a small on-device instruction-tuned LLM classifier that can *reason* activity-vs-subject) — ⬜ untried. Held in reserve; A6 solved the measured task_type problem cheaply, so E is only justified if a future measured problem tops out the cheap levers.
- **Lever F** (calibration / abstain-to-prior on low-margin/conflicting predictions) — ⬜ untried. Note: the A6 residual errors are *high-confidence* wrong, not low-margin, so F would not have helped them.
- **A6** (task_type readable descriptions) → ✅ **SHIPPED v0.5.0** (see above). This was the actual fix for the "task_type leakage ~0.625" open item.

## Open items / remaining problems
1. ~~`task_type` leakage still ~0.625~~ → **FIXED by A6 (v0.5.0): now 0.062.** Next enrichment-quality direction (agreed): a **speech-act pre-classifier** (imperative / interrogative / declarative) fed as context to task_type + other facets — same "structure over subject" family as A0/A4. BLOCKED ON MEASUREMENT: the eval sets are ~all imperative (16/16 c1, c2/c3 commands/drafts), so speech-act is currently unfalsifiable. **Step 1 = expand the eval set with question/statement rows + gold labels** (this is the immediate next task), THEN probe the cheap soft version (speech-act tag in the preamble, A0-style) aimed at activity_type + question-vs-command — NOT at task_type leak, which A6 owns.
2. **Confound set is small (16 c1 rows → 0.06-granular).** Expand it (more verticals, and real prompts as keld runs across more projects — the harness is ready). Only one local Claude transcript exists (this session), so real prompts are scarce for now.
3. **v0.4.0 release** finished fully green (all jobs, `.pkg` published). **v0.5.0** cut via `scripts/cut-release.sh` (minor bump); confirm its CI: `gh run list -R ncx-ai/keld-signal`. GitHub's API has 503'd on past releases — if a leg fails transiently, `gh run rerun <id> -R ncx-ai/keld-signal --failed`.
4. **Local machine still runs v0.3.8** (launchd `co.keld.agent`; binary at `/usr/local/keld/keld-agent`). Install the latest after its pkg builds: `sudo installer -pkg ~/Downloads/keld-v0.5.0-arm64.pkg -target /` (download the asset from the GitHub release first). NOTE: during A6 dev an 8-thread exp daemon (`/tmp/keld-agent-exp`) was left running with two sidecars (ports 56302/61622); `pkill -f keld-agent-exp` and `/usr/local/bin/keld-agent start` to restore the launchd daemon before installing.
5. **Older unrelated open item:** the pkg is **unsigned** (no Apple Developer secrets). CLI `sudo installer` works unsigned; for notarized distribution, that's the Apple Developer Program ($99/yr) + 5 GitHub secrets + two CI gaps (keychain import; `.p8`-to-file) noted earlier.

## Mechanistic learnings (save future dead-ends)
- GLiNER2 = bi-encoder: keys on token/semantic overlap, **no negation**, short discriminative labels beat verbose prose. Don't re-attempt label rewrites without this in mind.
- Sidecar loads model **on-demand** (first inference request); it doesn't warm proactively. (v0.3.9 daemon warmup covers the daemon path; the eval self-warms.)
- Eval MUST attribute per-row `source` realistically (eng/software → coding tool; non-eng → generic) or a tool-conditioned rule blows up `false_eng` as an artifact.

## Process
This work used the superpowers loop (brainstorm → spec → plan → subagent-driven execute → review → release), measure-first with a strict no-regression gate. Specs/plans under `docs/superpowers/specs/` and `docs/superpowers/plans/`. Progress ledger at `.superpowers/sdd/progress.md` (git-ignored).

---

## RESUME PROMPT (paste after `/clear`)

> Resuming work on **keld-signal** enrichment quality (`~/keld-signal`). Read `docs/superpowers/HANDOFF.md` first — it has full state. TL;DR: v0.4.0 shipped the activity-vs-subject fix (A0 task_type context + A4 compositional function; function-leakage 0.375→0). **v0.5.0 shipped A6** — task_type now classifies against readable label descriptions with codegen = "software engineering", cutting task_type leakage 0.625→0.062 and lifting gold task_type accuracy 0.580→0.696 (function-leakage/false_eng still 0). Rejected & why: A1 (tool prior, inert), A2 (label rewrites, bi-encoder can't negate), Lever D for task_type (leak is activity-shape not subject — oracle mask fixed 1/10). Durable eval harness: `keld-agent eval --confound --context` on a warm sidecar (HANDOFF recipe); for label experiments recreate the fast *bakeoff* (see "How A6 was found"). **Next task:** a **speech-act pre-classifier** (imperative/interrogative/declarative) as context for task_type + activity — but it's BLOCKED ON MEASUREMENT (eval sets are ~all imperative), so **step 1 = expand the eval set with question/statement rows + gold labels**, then probe the cheap soft (preamble-tag) version. Confirm v0.5.0 CI is green and install it locally if not. Use the measure-first, strict-no-regression loop; ask me before building the speech-act lever.
