package enrich

import (
	"os"
	"strings"
)

// A6 (KELD_ENRICH_TASKTYPE_DESCRIPTIONS): give the task_type facet readable,
// discriminative label DESCRIPTIONS instead of classifying against the bare
// vocabulary strings ("codegen", "other", ...).
//
// Why: task_type is the only facet that skips the LabelDef path — it hands the
// zero-shot model the bare id words, so "other" is an undefined catch-all that
// absorbs any engineering verb the model doesn't recognise as literal
// code-writing. The diagnostic showed 10/16 c1 rows (all genuine coding work:
// debug / fix / trace / refactor / CI / infra / ops) landing in other /
// extraction / classification — none of it subject leakage, all of it
// activity-shape confusion. Subject-masking (Lever D) fixed 1/10; the fix is to
// tell the model what each task_type MEANS, and in particular that codegen
// covers the full breadth of hands-on software work.
//
// A2 mechanistic guardrails honoured: descriptions are SHORT, use POSITIVE
// discriminative tokens, and avoid negation (bi-encoders can't parse "not X").
//
// Validated as a strict, dominating win (task_type leak 0.625→0.062; gold
// task_type accuracy 0.580→0.696; function leak + false_eng flat at 0), so it is
// ON by default; set KELD_ENRICH_TASKTYPE_DESCRIPTIONS to "off", "0", or "false"
// (case-insensitive) to disable it and restore bare-string task_type
// classification.
func taskTypeDescriptionsEnabled() bool {
	switch strings.ToLower(os.Getenv("KELD_ENRICH_TASKTYPE_DESCRIPTIONS")) {
	case "off", "0", "false":
		return false
	default:
		return true
	}
}

// TaskTypeDefs pairs each canonical task_type id (now the routing-aligned Keld
// Inference Exchange vocabulary — see labels.go) with the SHORT phrase the
// model actually scores against. Order-independent; the model ranks them.
// These descriptions are PROVISIONAL — a later bakeoff finalizes the wording
// against the new vocab; do not treat them as validated.
//
// The load-bearing choice, carried over from the prior vocab, is
// code_generation = "software engineering" (NOT "codegen" / "code
// generation"). The narrow phrase captures greenfield "write code" but not the
// debug/fix/refactor/CI/infra/ops work that is most of real engineering, which
// then fell into other/extraction/classification. A label bakeoff over the
// confound + gold sets found "software engineering" flips c1 task_type leak
// 0.625 → 0.062 AND lifts gold codegen recall to 10/10 with non-codegen
// preservation up too — a strict, dominating win. Enumerated descriptions (A6
// v1) were INERT on c1 (they diluted the codegen token: the A2 "verbose
// descriptions collapse separation" failure mode), which is why these are
// short. Do not lengthen them without re-running the bakeoff.
var TaskTypeDefs = []LabelDef{
	{"summarization", "summarizing text into a shorter form"},
	{"translation", "translating text between languages"},
	{"code_generation", "software engineering"},
	{"information_extraction", "extracting structured data or entities from text"},
	{"classification", "categorizing or labeling an input"},
	{"reasoning", "reasoning, analysis, math, or planning"},
	{"question_answering", "answering a question from documents or knowledge"},
	{"text_generation", "writing new content from scratch"},
	{"rewriting", "editing or rewriting existing text"},
	{"general", "a general or unclear request"},
}
