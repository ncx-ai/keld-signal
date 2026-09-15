#!/usr/bin/env bash
# The five checkpoints, read through `keld-conform check`.
#
# The DECISION lives in Go (internal/conform/checkpoints) so every row is
# table-tested without a daemon; this file only settles and calls it.

# checkpoints_settle <seconds> — wait for the lanes to catch up.
#
# Each lane has its own latency and none of them is instant: the hook posts a
# pointer synchronously but enrichment publishes from a background worker; the
# watcher polls every KELD_WATCH_POLL and paces first-sight ingest signals four
# per poll; and the tool's OTLP exporter flushes at process exit, not per turn.
# So the settle is a POLL on the verdict rather than a fixed sleep — it returns
# as soon as everything passes, and only spends the whole budget on a failure.
checkpoints_settle() {
  local budget=${1:-45} i
  for i in $(seq 1 "$budget"); do
    if checkpoints_run quiet; then
      say "settled after ${i}s"
      return 0
    fi
    sleep 1
  done
  return 1
}

# checkpoints_run [quiet] — one evaluation. Writes the JSON report to
# $WORK/verdict-<step>.json and returns the check's exit status.
checkpoints_run() {
  local quiet=${1:-}
  local args=(
    check
    --tool "$TOOL" --chain "$CHAIN" --step "$STEP" --seed "$SEED"
    --transcript-root "$(tool_transcript_root "$TOOL")"
    --since "$STEP_SINCE"
    --store "$ISO_HOME/state/refseries.db"
    --mockatlas "$MOCK_ATLAS_URL"
    --mockatlas-state "$ATLAS_STATE"
  )
  local skip
  skip=$(tool_not_expected "$TOOL")
  [ -n "$skip" ] && args+=(--not-expected "$skip")

  if [ "$quiet" = "quiet" ]; then
    "$WORK/keld-conform" "${args[@]}" >"$WORK/verdict-$STEP.json" 2>/dev/null
  else
    "$WORK/keld-conform" "${args[@]}" >"$WORK/verdict-$STEP.json"
  fi
}

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

# step_end <settle-seconds> — settle, then report. Fails the run on a failed
# required checkpoint, printing the machine-parseable last line.
step_end() {
  if ! checkpoints_settle "${1:-45}"; then
    checkpoints_run || true
    echo "conformance: step $STEP FAILED" >&2
    tail -1 "$WORK/verdict-$STEP.json" >&2
    fail "checkpoints did not pass within the settle budget"
  fi
  checkpoints_run
  say "step $STEP passed"
}
