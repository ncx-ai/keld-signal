# Task 2 report
Status: DONE.
- Emission in levels.py inside the existing assistant/evidence guard; INVENTORY entries cap 9; Go four-stop path; SchemaVersion 27.
- Tests: emission tests in test_analysis_levels.py via the real _write/iter_turns/events_for_turns idiom (no invented helper); registration test in test_reqclass.py.
- Deviation: `verbs` is a local variable in events_for_turns (bash_refs unpack), so the module is imported as `atv1_verbs`.
- Fixture verify before rebaseline: purely additive (ref/activity_verb, ref/activity_verb_tokens None -> rows; 251 -> 257 rows; no other level changed). Rebaselined; IDENTICAL.
- Unlisted but required: docs/superpowers/specs/golden/turn-record-{bin,event}.jsonl (test_reader_golden) regenerated; diff 44 insertions, 0 deletions, all activity_verb. scripts/testdata/golden_block_with_projects.json schema_version 26->27. Go tests pinning 26 / twenty inventory keys updated.
- Sidecar suite: all pass. go test ./...: all 63 packages ok.
- Eval (internal/agent/enrich/eval) owed a re-run per brief; not run.
