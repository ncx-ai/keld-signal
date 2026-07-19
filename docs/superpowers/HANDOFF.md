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

## What's BUILT on `feat/credential-detection` (Phase 0 + Phase 1; pending merge)
Deterministic credential-detection layer for the `secrets` sensitivity class — first increment of the credential-leak-detection spec (`docs/superpowers/specs/2026-07-18-credential-leak-detection-design.md`; research notes alongside).
- **New `creddetect` package:** vendored gitleaks **v8.30.1** ruleset (222 rules, 0 skipped, MIT-attributed, embedded), parsed via `go-toml/v2`; `Detect(text)` = keyword pre-filter → RE2 regex → per-rule entropy floor → deduped spans. Regex-less path-only rules (pkcs12-file) skipped at load. Unioned into `SensitivityExtractor` via the existing `found`/`sensitivityFromEntities` path (a credential registers as `api_key` → `secrets`, but precedence holds: SSN+key stays `phi`).
- **Eval substrate (Phase 0):** `creds.jsonl` (42 rows: 24 creds across formats + 18 decoys incl. git-SHA/UUID/placeholder/redacted) + `secret_recall`/`secret_fpr` metrics, `keld-agent eval --creds`.
- **MEASURED (live sidecar):** `secret_recall` 0.875 → **0.917** (GLiNER-only → union; +1 cred); `secret_fpr` **0.167 → 0.167** (detector added ZERO false positives); **zero regression** — the `--confound --context` block is byte-identical to v0.6.0 on every facet/metric.
- **Honest finding — union delta is modest because GLiNER already catches known formats (0.875 baseline).** The deterministic layer's value here is *guaranteed recall + zero added FPs*, not a big jump. Per-row diagnosis of the residual:
  - **2 misses (both det=false):** JWT (corpus artifact — jwt.io example payload below gitleaks' `{17,}` length floor; real JWTs match) and a `mongodb+srv://` URI (genuine gitleaks connection-string gap → L2 target).
  - **3 FPs — ALL placeholders** (`YOUR_API_KEY`, `<API_KEY>`, `<YOUR_SECRET_HERE>`), all flagged by **GLiNER** with the deterministic layer correctly **NOT** firing. This is the L3 precision-gate target, exactly as the research predicted.
- **→ REPRIORITIZED next plan (L2/L3):** **placeholder precision-gating is the highest-value next step** (fixes 3/3 FPs; clean pattern: a GLiNER-only secrets hit whose span is a placeholder shape `YOUR_*`/`<...>`/`****` → suppress). Then L2 connection-string/contextual recall. The gitleaks weekly-sync workflow is still pending.

## Calibration instrument (BUILT on `feat/calibration-eval`; pending merge) + KEY FINDING
`keld-agent eval --calibration` now reports per-facet accuracy stratified by GLiNER2's returned confidence — reliability bins + ECE (`eval.Calibration`, `Pred.Conf`). No pipeline change. Spec/plan: `docs/superpowers/{specs,plans}/2026-07-18-confidence-calibration-eval*.md`.
- **DECISIVE FINDING (gold set): GLiNER2 is systematically OVERCONFIDENT, and errors live in the HIGH-confidence bins, not a low-confidence tail:**
  - `task_type` ECE 0.238 — 55/69 preds in `[0.9,1.0]` at conf **0.98 but acc 0.76**.
  - `domain` ECE **0.434** (worst) — conf 0.85→acc 0.125; nearly meaningless confidence.
  - `speech_act` ECE 0.211 — 55/73 top-bin, conf 0.99→acc 0.78.
  - `activity_type` ECE **0.146** (best) — top bin conf 0.97→acc **1.00**; the "confident⇒correct" pattern.
  - Rule-influenced (`sensitivity` 67/69 forced conf 1.0; `function_guess` A4-forced) behaved as expected — separation validated.
- **IMPLICATIONS for the task_type focus (measure-first redirect):**
  1. **Abstain (Lever F) is NOT the play for `task_type`/`domain`/`speech_act`** — errors are high-confidence and ~80% of preds are top-bin, so a confidence threshold can't recover accuracy. These need a BETTER CLASSIFIER (labels/approach), not calibration/abstain.
  2. **`activity_type` IS a clean abstain candidate** (conf≥0.9 ⇒ ~100% acc). Cheapest Lever-F win if we want one.
  3. **`domain` is the worst-calibrated & lowest-accuracy (0.449) facet** — likely needs the most fundamental rework.
  - A future abstain-lever spec (Lever F) should target `activity_type` only; task_type work should focus on the classifier itself. Note: raw top-confidence is a weak discriminator here, but the top-vs-2nd **margin** is untested (deferred option (b)) — worth checking before fully closing Lever F for task_type.

## What's BUILT on `feat/tasktype-taxonomy` (schema v6; pending merge)
task_type redesigned into a **routing-aligned taxonomy** (routing key for Keld Inference Exchange order books; see memory `tasktype-routing-purpose`). Spec/plan/research: `docs/superpowers/{specs,plans}/2026-07-18-tasktype-*`.
- **New 10-entry vocab:** summarization, translation, code_generation, information_extraction, classification, reasoning, question_answering, **text_generation** (new), **rewriting** (new), **general** (replaces "other"). **DROPPED `agentic_tool_use`** (a workflow shape, not an inference job — was ~half of all task_type errors; agentic-ness lives on the speech_act axis). Renamed codegen/extraction/rag_qa to HF conventions. SchemaVersion 5→6.
- **Gold set relabeled** (review-gated): agentic_tool_use rows routed to underlying job; "other" split into text_generation/rewriting/general; + coverage rows. 2 human corrections + 1 review fix applied.
- **Bakeoff-tuned descriptions** (v2 winner). **MEASURED (live sidecar):**
  - task_type gold accuracy **0.696 → 0.744** (+4.8pts); the agentic_tool_use high-confidence error class is GONE.
  - Per-category: code_generation 13/14, question_answering 6/6, translation 4/4, summarization 9/10, rewriting 4/5; text_generation 6/10, general 6/10.
  - **Zero regression:** leakage(task_type) 0.062 flat, leakage(function)/false_eng 0, function_guess 0.909 / sensitivity 0.818 / speech_act ~0.72 / subcategory 0.650 all flat.
- **FINDING — `reasoning` is a genuine bi-encoder ceiling (4/9).** All 6 reasoning wordings tried topped out at ~4/9; it conflates with question_answering ("reason about X" ≈ "answer about X"), empirically confirming the research prediction. NOT fixable by label wording — needs a different mechanism (NLI-style classifier / Lever E) or is accepted as-is. text_generation→general and general→question_answering are the other residual confusions.
- **Modality axis (image/video/audio) explicitly deferred** to a future separate axis; `data_analysis` deferred to v2. Both are spec non-goals.

## Placeholder precision-gate (BUILT on `feat/placeholder-gate`; pending merge)
Credential L3 part 1 — suppress placeholder/redacted values from triggering `secrets`. Spec/plan: `docs/superpowers/{specs,plans}/2026-07-18-placeholder-precision-gate*`.
- **`creddetect.IsPlaceholder(text)`** — conservative predicate (templates `<...>`/`${...}`/`{{...}}`, `YOUR_`/`MY_` prefixes, all-caps-underscore tokens, mask runs `****`/`xxxx`, literal placeholder words). Keys on placeholder SHAPE + absence of secret entropy, so real secrets return false (unit-tested against every corpus secret). **Empty text fails open** (no value to judge → don't gate → recall preserved) — this keeps the SSN-precedence path intact.
- **Gated in `SensitivityExtractor`:** GLiNER + creddetect sensitive spans whose text IsPlaceholder are dropped (span-level, so a real secret alongside a placeholder survives).
- **MEASURED (live sidecar):** `secret_fpr` **0.167 → 0.056** (3 FPs → 1; 2 fixed), `secret_recall` **0.917 flat** (zero recall loss), sensitivity accuracy 0.818 + all leakage flat (zero regression).
- **FINDING — the 1 residual FP is a DIFFERENT mechanism.** "config.yaml still has the placeholder <YOUR_SECRET_HERE>" produces NO GLiNER entity; `secrets` comes from the zero-shot **sensitivity classifier** ranking (the words "placeholder"/"secret" in the sentence). The entity-span gate structurally can't touch a classifier-driven FP — that's a separate, smaller lever (e.g. downgrade an entity-less, creddetect-less classifier-only "secrets" call; needs its own recall measurement first). Not pursued now (scope + measure-first).

## Sensitivity reframe (BUILT on `fix/sensitivity-reframe`; pending merge)
**Reframe:** `sensitivity` detects concrete leaked DATA (credentials + personal/financial identifiers), NOT content domain. The class is a rollup of which concrete entity is present (SSN→phi, card→pci, credential→secrets, other personal id→pii). Medical-context detection explicitly REJECTED ("diagnosed with diabetes" is a topic, not a leak). `proprietary` deprecated (kept in vocab for contract stability, never emitted). Spec: `docs/superpowers/specs/2026-07-18-sensitivity-concrete-data-reframe-design.md`.
- **NO pipeline code change** — `SensitivityFromEntity` already maps entity→class exactly this way; the "PHI misses" were mislabeled GOLD (labeling medical topic as phi even with no SSN).
- **Gold relabeled** (review-gated) to concrete-identifier semantics: medical-context rows → none/pii; 3 proprietary rows → none. Added a clarifying comment on `SensitivityFromEntity`.
- **Fixed a metric bug:** `sensitive_recall` counted blank-gold rows in its denominator (blank != "none"), polluting the number. Now skips blank gold (consistent with accuracy).
- **MEASURED:** sensitivity accuracy **0.812 → 0.857**; sensitive_recall (corrected) **→ 0.929** (13/14). The old 0.684 was TWO artifacts: mislabeled medical-context targets + the blank-row metric bug.
- **FINDING (1 residual miss):** the MRN row (gold now pii) — GLiNER misreads "MRN 4482910" as an SSN → outputs phi. Deliberately surfaced (relabeled honestly rather than relying on the misread). Plus a spurious `person` FP on "the patient" (generic). Both are GLiNER entity-precision issues for a future lever, not fixed here.

## Domain descriptions — "A6 for domain" (BUILT on `feat/domain-descriptions`; pending merge)
Domain was stuck at ~0.46 because it classified against BARE label strings (`Domains = ["software",…]`) — the exact issue A6 fixed for task_type — with business+software collapsing into a "general" magnet. Gave domain readable **`DomainDefs` descriptions** (labels-within-GLiNER2, NO new model — fits the single-resident-model / CPU-only constraint), routed `DomainEntitiesExtractor` through the labeled path (map description→id), aliased the enrichtest fake's domain keywords.
- **Bakeoff-tuned** (bare 0.462 → 0.615 → **0.654**). Load-bearing: narrow `general` (kill the magnet), broad-but-not-magnet `software`, mid `business` (the diffuse hard case — broader→magnet, narrower→under-fires).
- **MEASURED:** domain **0.462 → 0.654** (+19pts, ~40% relative); ALL other facets flat (task_type 0.744, sensitivity 0.857, function 0.800, speech_act, subcategory). Zero regression.
- **How the target was found:** cheap ceiling experiment — a reasoning model (subagent, blind) scored domain **0.859** vs GLiNER 0.449 (~41pt headroom) but task_type only **0.833** vs 0.744 (~9pt, mostly gold ambiguity). So domain was the high-impact target, NOT task_type. (Lever E / a second model is ruled out by the on-device constraints anyway — see memory `signal-ondevice-model-constraints`.)
- **FINDING (residual):** `business` (8/22) is inherently diffuse — overlaps software/finance/general even for a reasoning model. Label wording can't fully separate it; ~0.65 is near the practical bi-encoder ceiling for domain on this gold. Small domains (science/education/creative, 2-3 rows each) are under-sampled.

## Eval set EXPANDED 82 → 165 rows (multi-judge consensus) — new baselines
The gold set was the recurring limiter (82 hand-built rows, thin categories, wide error bars). Expanded with **independently-verified** labels:
- **Method:** 4 subagent generators produced 83 diverse realistic prompts targeting the gaps (thin domains science/education/creative/medical, confusion pairs, coding/agentic/general mix, some with concrete PII). Then **3 blind judges** (independent subagents) labeled task_type + domain; **consensus** = gold. Agreement was very high: **79/83 task_type unanimous, 80/83 domain unanimous, ZERO 3-way splits** — the 7 majority calls were all defensible borderline cases. sensitivity labeled per the reframe (concrete-identifier rows get their class; rest `none`). This is the trust mechanism — no single-source (circular) labeling.
- **New coverage (165 rows):** task_type all 10 categories 8-23 each; domain software 42 / business 34 / general 19 / finance 15 / legal 14 / medical 11 / science 9 / creative 9 / education 8 (thin domains went 2-3 → 8-9); sensitivity none 139 / pii 10 / secrets 5 / pci 4 / phi 2.
- **KEY RESULT — prior wins HELD on the fresh independent data (not overfit):** task_type 0.744→**0.733** (stable), domain 0.654→**0.683** (UP — the descriptions generalize), sensitivity 0.857→0.831 / recall 0.929→0.905 (stable), other facets flat. These are the **new baselines** (165 rows, ~2× tighter error bars). Domain rising on unseen data confirms the A6-for-domain win was real, not a small-set artifact.
- Generators/judges cost ~7 subagent calls (not a workflow). Prompts + judge outputs in `/tmp/evalexp/` (throwaway).
- **Follow-up: extended labels to activity_type + speech_act** on the 83 new rows (3 more blind judges each, consensus: activity 74/83 unanimous, speech_act 72/83 unanimous, 0 splits). Coverage now activity_type 103/165, speech_act 164/165. New baselines: **activity_type 0.680** (was 0.700 on ~20 rows), **speech_act 0.701** (was 0.691). function_guess/subcategory NOT extended — they're source-conditioned (A4 compositional) and need source-attributed rows, not a clean judge pass.
- **Facet status on the firm 165-row set:** task_type 0.733 (near bi-encoder ceiling; reasoning 7/20 is the mechanism-bound residual — reasoning↔QA + reasoning↔code-context), domain 0.683, sensitivity 0.831/recall 0.905, function_guess 0.800, activity_type 0.680, speech_act 0.701, subcategory 0.650. activity_type (0.680, uses descriptions already) is the next diagnostic candidate; subcategory (0.650) is fine-grained + conditioned.

## Agentic-framework corpus + Meta augmentation (BUILT on `feat/agentic-corpus`) — SURPRISING findings
Second eval corpus for agentic-framework traffic (Mastra/LangChain/LangGraph/CrewAI). Spec: `docs/superpowers/specs/2026-07-19-agentic-corpus-design.md`.
- **Built:** `Meta`+`Preamble()` extended with agentic fields (framework/agent_role/workflow/step/recent_steps), appended AFTER coding fields so coding preambles stay byte-identical (main gold task_type 0.733 unchanged — verified). Eval GoldRow + Meta() + LoadAgentic + AccuracyByShape + `keld-agent eval --agentic`. Corpus `agentic.jsonl` = 43 rows (30 clean sub-tasks + 13 full raw LLM calls, avg ~1600 chars), multi-judge-labeled (3 blind judges × task_type/domain/activity/speech_act; task_type/domain/speech_act 0 splits).
- **MEASURED (augmented = agentic Meta in preamble; bare = no context):**
  - task_type: **augmented 0.674, bare 0.744 (Δ −0.070)** — augmentation HURT. By shape: clean 19-21/30, raw 10-11/13.
  - domain: **augmented 0.581, bare 0.581 (Δ 0)** — augmentation did nothing. clean 18/30, raw 7/13.
- **TWO SURPRISES (both overturn my priors, both measure-first):**
  1. **Naive agentic-metadata augmentation HURTS/does-nothing.** framework/agent_role/workflow/step/recent_steps inject subject-laden noise (e.g. "sales_data_pipeline", "research_agent") that pulls the classifier — the SAME subject-contamination pattern as the original task_type problem. Dumping all metadata is NOT the win.
  2. **Raw full LLM calls classify BETTER than clean sub-tasks** (raw ~0.77-0.85 task_type vs clean ~0.63-0.70). The scaffolding's explicit task framing ("Summarize the above…", "Classify this ticket now") is a STRONG signal; the terse clean sub-tasks are more ambiguous. The "scaffolding drowns the task" worry was wrong for these.
  - domain on agentic (0.58) is worse than human (0.68) — agentic domains are diffuse business/ops.
- **→ This directly validates the user's multi-stage idea** (resolve RELEVANT actor/intent from the system prompt, THEN augment selectively) OVER naive metadata-dump: since dumping all metadata hurts, the win must be SELECTIVE intent resolution. Next experiment (planned in the spec): stage-1 GLiNER extraction of actor/intent from the raw system prompt → selective stage-2 augmentation; measure vs this baseline. Also: consider WHY augmentation hurts (which fields) before building it.
- Corpus is 43 rows (target was 60-80); can grow. Prompts/judge outputs in `/tmp/evalexp/`.

## Facet-selective agentic augmentation (BUILT on `feat/agentic-selective-augment`) — a measured WIN
The agentic baseline showed naive full-metadata augmentation HURTS task_type. Ablation on 60 clean rows found the policy is facet-OPPOSITE: task_type wants NO agentic metadata (bare 0.833 vs all 0.617); domain wants FULL agentic metadata (all 0.817 vs bare 0.717). Built the two-change policy:
- **`Meta.PreambleCoding()`** (coding fields only, drops agentic) — used by task_type + the other classifyPass facets (activity/personal/subcategory) + the task_type escape-hatch. **`Meta.Preamble()`** stays full (coding+agentic). Coding-tool requests: PreambleCoding == Preamble (byte-identical → all prior numbers unchanged).
- **domain now augments with the agentic preamble** when `Meta.HasAgentic()` (was: no preamble at all). To avoid corrupting entity offsets, agentic domain splits into two calls: entities from raw text, domain-classify over the agentic preamble. Coding rows keep the single bundled Extract (unchanged).
- **MEASURED (88-row agentic corpus, OLD full-augment vs NEW selective):** task_type **0.64→0.80 (+0.16)**, domain **0.73→0.78 (+0.05)**. Clean rows drive it (task_type +0.21, domain +0.10); raw rows ~neutral (they embed metadata in the system prompt). No-regression on coding/human gold guaranteed by construction (byte-identical coding preamble; domain non-agentic path unchanged) + verified.
- Corpus grew to 88 rows (60 clean + 28 raw, all 9 domains) via a 2nd generate+judge batch (zero consensus splits).
- **Policy VALIDATED for activity_type:** measured on 85 agentic rows, activity_type = 0.624 coding-preamble (current, drops agentic) vs 0.518 full-agentic — agentic metadata HURTS activity_type −0.11, like task_type. So routing activity_type (+personal/subcategory) through PreambleCoding was correct. Final policy: task_type/activity_type DROP agentic; domain ADDS agentic; speech_act is text-only.
- **PRODUCTION-REACHABILITY GAP (noted, not built — YAGNI, no consumer yet):** the agentic Meta is eval-only. The request path (`spool.Pointer` → `queue.Job` → `daemon.contextMeta`) has NO agentic fields; `contextMeta` only populates coding-tool context. To make the win reachable, an agentic integration needs: agentic fields added to spool.Pointer + queue.Job + ingress.JobFrom + a daemon Meta builder for agentic sources. Build this when a real agentic/exchange integration exists (not speculatively). Watch: agentic requests should put the framework in Meta.Framework (NOT Meta.Tool) to match the measured corpus (corpus agentic rows had Tool="").
- **Multi-stage follow-on still open** (lower ROI): raw calls already classify well and external metadata is ~neutral for raw, so stage-1 actor/intent derivation is deprioritized vs the clean-shape win shipped.

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
