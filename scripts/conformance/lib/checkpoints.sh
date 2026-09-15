#!/usr/bin/env bash
# The five checkpoints, read through `keld-conform check`, once per TOOL per
# step.
#
# The DECISION lives in Go (internal/conform/checkpoints) so every row is
# table-tested without a daemon; this file only settles and calls it.
#
# ⚠️ A STEP IS PER-TOOL, AND SO IS ITS VERDICT. A step that ran two tools writes
# two verdict files and names the failing TOOL in its failure line: "one break
# reads as one break" is only true if the report says which of them broke.

# step_begin <name> — open a chain step. STEP_SINCE bounds the transcripts this
# step's checkpoints will consider to the ones it produces, so a later step is
# never satisfied by an earlier step's file.
step_begin() {
  STEP=$1
  # One second back: a transcript created in the same second as this timestamp
  # would otherwise be excluded by a strictly-after comparison on a filesystem
  # whose mtime resolution is coarse.
  STEP_SINCE=$(( $(date +%s) - 1 ))
  say "--- step $STEP ---"
}

# checkpoints_run <tool> [quiet] [extra-not-expected] — one evaluation for one
# tool. Writes $WORK/verdict-<step>-<tool>.json and returns the check's status.
checkpoints_run() {
  local tool=$1 quiet=${2:-} extra=${3:-}
  local args=(
    check
    --tool "$tool" --chain "$CHAIN" --step "$STEP" --seed "$SEED"
    --transcript-root "$(tool_transcript_root "$tool")"
    --since "$STEP_SINCE"
    --store "$ISO_HOME/state/refseries.db"
    --mockatlas "$MOCK_ATLAS_URL"
    --mockatlas-state "$ATLAS_STATE"
  )
  local skip
  skip=$(tool_not_expected "$tool")
  [ -n "$extra" ] && skip="${skip:+$skip,}$extra"
  [ -n "$skip" ] && args+=(--not-expected "$skip")

  if [ "$quiet" = "quiet" ]; then
    "$WORK/keld-conform" "${args[@]}" >"$WORK/verdict-$STEP-$tool.json" 2>/dev/null
  else
    "$WORK/keld-conform" "${args[@]}" >"$WORK/verdict-$STEP-$tool.json"
  fi
}

# step_assert <settle-seconds> <not-expected> <tool...> — settle until EVERY
# named tool passes, then report. Returns non-zero on failure instead of
# exiting: the chain decides what to do next, and what it does is stop and mark
# the remaining steps SKIPPED (spec §"Two chains on one VM").
#
# The settle is a POLL on the verdicts rather than a fixed sleep — each lane has
# its own latency (the hook posts synchronously but enrichment publishes from a
# background worker; the watcher polls; the tool's OTLP exporter flushes at
# process exit) — so it returns as soon as everything passes and only spends the
# whole budget on a failure.
step_assert() {
  local budget=$1 extra=$2; shift 2
  local tools="$*"
  [ -n "$tools" ] || { say "step $STEP: no tools in this half — nothing to assert"; return 0; }

  local i t all_ok
  for i in $(seq 1 "$budget"); do
    all_ok=1
    for t in $tools; do
      checkpoints_run "$t" quiet "$extra" || all_ok=0
    done
    if [ "$all_ok" = "1" ]; then
      say "settled after ${i}s"
      for t in $tools; do checkpoints_run "$t" "" "$extra"; done
      say "step $STEP passed for: $tools"
      return 0
    fi
    sleep 1
  done

  echo "conformance: step $STEP FAILED [seed $SEED]" >&2
  for t in $tools; do
    checkpoints_run "$t" "" "$extra" || tail -1 "$WORK/verdict-$STEP-$t.json" >&2
  done
  return 1
}
