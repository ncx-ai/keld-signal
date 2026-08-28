package eval

// Sensitivity gold-set floors — regression FLOORS, not targets.
//
// These two live in an UNTAGGED file on purpose. The sensitivity facet takes no
// evidence from GLiNER2 at all (SensitivityExtractor is ModelFree: gitleaks +
// the sidecar's presidio /pii route), so its gate must be runnable without the
// model — see sensitivity_eval_test.go, which measures them under `-tags pii`
// against /pii alone. sidecar_eval_test.go (`-tags sidecar`) also compiles this
// file and reuses them, so the two runs are gated on identical numbers.
//
// Calibrated 2026-08-23 against presidio /pii + creddetect over the corrected
// 165-row gold set (16 sensitive rows, 144 labelled none, 5 unlabelled), which
// measured: sensitive_recall 1.000 (16/16), sensitivity_acc 0.994 (159/160).
// Floors sit ~0.05 below the measured run, the file's own convention. Detection
// here is DETERMINISTIC — regex + Luhn + libphonenumber, no sampling — so the
// margin is not absorbing run-to-run noise (there is none); it is absorbing a
// presidio/gitleaks upgrade shifting a borderline row. At 16 sensitive rows a
// 0.95 recall floor means no miss at all is tolerated (15/16 = 0.938 fails),
// which is the right strictness for the safety-critical dimension.
//
// The single accuracy miss is a KNOWN detector false positive, not a fixture
// defect: gitleaks `generic-api-key` matches the English prose "key
// obligations, termination" in row 110. The rule's entropy floor is applied to
// the whole match rather than to capture group 1 (no `secretGroup` in
// gitleaks.toml), so the prose prefix inflates the entropy past 3.5. Fixing
// that is a creddetect change with its own eval, not a gold-set change.
//
// SUPERSEDES the 2026-07-14 calibration (sensitive_recall 0.565 / acc 0.811,
// floors 0.50/0.75). Those were measured against a GLiNER2 NER that no longer
// runs, over a gold set whose every sensitive row carried a PUBLISHED example
// value (4111 1111 1111 1111, 123-45-6789) — so post-well-known-gate they
// measured the gate, not the detector: real recall had fallen to ~0.3 and the
// safety-critical floor was failing for the wrong reason.
const (
	minSensitiveRecall = 0.95
	minSensitivityAcc  = 0.94
)
