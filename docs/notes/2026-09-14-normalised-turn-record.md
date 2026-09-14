# Decision: a normalised turn record in the sidecar, not codeburn — 2026-09-14

**Context.** Codex has never had a prompt enriched: `watch/codex.go` requires an
`ordinal` on the `user_message` line and no Codex version (287 rollouts, 20 versions,
4,076 prompts on one machine) has ever written one. Fixing capture alone is not enough:
the sidecar's `turns_in` yields the RAW Claude JSON object, and ingest.py, capture.py,
levels.py, workspace.py, textembed.py and analyze.py all read Claude field names off it.
There is no intermediate record. The Go side already has one (`resolve.TranscriptReader`
per source).

**Decision.** Introduce one turn record (timestamp, role, session/turn id, cwd, model,
tool calls as name+file+command, token split, text kept local) at the `turns_in` seam,
move the six readers onto it with `analyze_window_by_parse` and the 284-transcript
chunk-equivalence test as the byte-identical bar, then add a Codex reader and a Gemini
reader against the record. Three readers, for the three tools `keld signal setup`
configures. codeburn (MIT, ../codeburn) is the field-mapping reference for each reader.

**Rejected: embedding codeburn.** It is CLI-only (no `exports`; `main` is `dist/cli.js`)
and emits aggregated cost, not events. Using it means a Node runtime in the client and a
fork of a 5,561-line parser; its Codex parser alone is 1,409 lines of fork-replay and
compaction edge cases.

**Revisit when** a paying org asks for a fourth tool. At that point 46 bespoke readers is
not sustainable in Python either, and embedding codeburn's parsers as a Node step becomes
the honest option despite the runtime.
