# Research notes — routing-aligned task_type taxonomy

**Date:** 2026-07-18. Deep-research pass (102 agents, adversarially verified; weak
claims killed in verification). Informs the task_type taxonomy redesign (task_type
= routing key for Keld Inference Exchange order books; see memory
`tasktype-routing-purpose`).

## Verdict: proposed 8-set is well-supported; 3 concrete refinements

Proposed: summarization, translation, code_generation, extraction, classification,
reasoning, question_answering, content_generation + `general` fallback; agentic on
a separate axis.

### Validated (high confidence)
- **6 of 8 map to recurring canonical categories** across HF Inference API,
  Super-NaturalInstructions (1616 tasks/76 types), BIG-bench, HELM: summarization,
  translation, classification, question_answering, extraction, code_generation.
- **Two-axis design is right** — HELM taxonomizes `task` separately from audience/
  time/language; providers (OpenAI Batch, DigitalOcean) separate SLA/endpoint from
  task. Keeping agentic/tool-use + SLA off the task_type axis is validated.
- **Category COUNT is not the constraint** — a GLiClass/GLiNER bi-encoder shows only
  7–20% throughput loss from 1→128 labels (vs ~52× for cross-encoders). ~8–10
  labels + fallback is cheap; the real constraint is **inter-category separability**.
- **code_generation stays** — coding is the single largest enterprise AI spend
  (Menlo 2025: $4.0B/55% of departmental), and our workload is coding-assistant/
  agentic prompts. (No provider/academic API reifies it as a named task, but volume
  + our context justify it.)

### Three refinements
1. **ADD a rewriting/editing/paraphrase category (highest-priority gap).** OpenAI's
   1.5M-conversation study: "Writing" is the largest work task (~24% of all msgs),
   and **~2/3 of Writing msgs MODIFY user-supplied text** (edit/rewrite/critique) vs
   generate new. Currently forced awkwardly into content_generation/summarization.
2. **`reasoning` is the weakest member** — in BIG-bench/HELM it's a cross-cutting
   *capability* (logical/causal/analogical, scattered), not a clean task type; it's
   the category most prone to classifier conflation. Keep-with-caution or demote to
   a cross-cutting tag.
3. **Rename toward HF/academic conventions** (the de-facto zero-shot label strings,
   which improve bi-encoder matching): extraction → **information_extraction**;
   content_generation → consider **text_generation**; keep summarization/translation/
   question_answering/classification/code_generation; keep `general` fallback.

### Predicted classifier conflation pairs (medium conf) — the bakeoff must watch these
- reasoning vs classification / question_answering
- extraction vs classification (HF: token-classification IS both)
- extraction vs summarization
- content_generation vs summarization / translation

### data_analysis — secondary candidate (medium)
a16z 2025 CIO survey names data_analysis among top enterprise uses (with internal
search + support, which subsume into question_answering). Consider; not required v1.

## Caveats (important)
- **Volume data is CONSUMER (ChatGPT), not our coding-assistant/agentic workload** —
  so code_generation/extraction/tool jobs are UNDER-represented in that data and
  over-represented in our real traffic. The "Writing dominates" figure may not
  transfer; the rewriting gap likely still does.
- **No industry standard routes by NLP task type** (they use endpoint/modality/SLA) —
  the task-category-orderbook concept is a novel, literature-permitted design choice,
  not a discoverable ground truth (Flan: "definition of 'task'…not easily simplified
  to one ontology"). Optimize for classifiability + routability, not "correctness".
- Killed in verification: HELM "settles on 16", a proven granularity sweet-spot,
  hierarchical-classification benefits — so optimal-count guidance is weak; ~8–10 is
  "defensible", not "proven".

## Open questions → the bakeoff / gold-set answers these
1. Actual task-type distribution of OUR workload (coding/agentic), not consumer.
2. How well GLiNER separates the 4 conflation pairs above — the empirical test the
   label bakeoff runs on candidate taxonomies.

## Key sources
- HF inference tasks: https://huggingface.co/docs/inference-providers/en/tasks/index
- Super-NaturalInstructions: https://aclanthology.org/2022.emnlp-main.340/
- Flan Collection (task-ontology caveat): (Longpre et al.)
- OpenAI usage study (Writing/rewriting volume): NBER WP 34255
- Menlo Ventures 2025 (coding spend); a16z 2025 CIO survey
- GLiClass label-scaling: GLiClass docs/benchmarks
