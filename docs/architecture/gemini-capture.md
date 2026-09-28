# Gemini capture: two chat shapes, the credential path, the router and the tool name

> **Provenance — split out of `AGENTS.md` on 2026-09-28** (at `4d2b6d0`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-09-22**
> (best effort: the newest commit `git log -L` finds over the moved line ranges).
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *Gemini capture* states the rules. This file carries the incidents
behind each one — every one of them found by the conformance chain rather than
by a unit test. The chat-file reader is `internal/geminichat`.

⚠️ **GEMINI WRITES TWO DIFFERENT CHAT SHAPES AND BOTH ARE IN THE WILD; READING
EITHER ALONE LEAVES A WHOLE POPULATION UNCAPTURED.** Under
`~/.gemini/tmp/<project>/chats/`:
- `session-<ts>-<id>.json` — ONE JSON DOCUMENT, session id at the top level and
  the turns in a `messages` array, `content` a bare STRING (258 of 262 measured
  messages) or an array of `{text}` blocks (4). Measured on a developer machine:
  **55 files, 262 messages, 58 user prompts**, the oldest from 2025-09 — every
  one written by builds up to and including **0.37.1**.
- `session-<ts>-<id>.jsonl` — ONE OBJECT PER LINE: a session-meta first line
  carrying `sessionId`, `{"$set":…}` MUTATION lines that are not turns, and one
  line per message. Written by **0.60.0**, the version CI installs from npm
  `@latest`.

⚠️ **AND THE HISTORY IS THE OPPOSITE OF WHAT IT LOOKS LIKE.** This client
originally parsed the LINE form only, and was written correctly for it — but
`watch.transcriptFiles` filtered on the `.jsonl` EXTENSION, so on every machine
running a build that had moved to the document form it read nothing at all and
said nothing. The conformance chain exposed that: `transcript` reported "0
transcript(s)" with the chat file sitting in the directory it had just walked.
Switching wholesale to the document form then simply MOVED the blind spot to
0.60.0 — three chain A cells green on the fix and still unable to find a
transcript. So neither shape is "the" format and neither may be dropped.
**`internal/geminichat` decides by CONTENT, not extension**: a file that parses
as one object with a `sessionId` is a document, otherwise it is read as lines —
so a build that renames the file without changing the format, or the reverse,
cannot silently stop being readable. A `$set` line is NOT a turn: 0.60.0 puts
the CLI's own `<session_context>` preamble inside the first one, so following it
would take boilerplate for the user's first prompt and shift every later ordinal.
**`internal/geminichat` is now the ONE place that knows the shape** — watch,
resolve and the conformance checkpoint all read through it, so the predicate
deciding which messages are genuine prompts, and therefore what every ordinal
means, has one definition rather than three copies each commented to stay in
step. Its fixture is a REAL captured 0.37.1 file. The watcher's Gemini lane is
therefore a DOCUMENT lane (`watch.scanDocument`): its cursor counts PROMPTS
ALREADY OFFERED rather than bytes, because the file is rewritten whole on every
turn and "bytes appended" names nothing; forward-only first sight and the
first-sight ingest signal are unchanged.

⚠️ **AND GEMINI'S TELEMETRY CREDENTIAL CANNOT RIDE A QUERY STRING.** Gemini
cannot carry an auth HEADER (its `OTEL_EXPORTER_OTLP_HEADERS` is honoured only
in a "trusted" workspace), so the token rides the URL — and it used to ride
`?token=`, on the stated belief that "gemini's exporter preserves the URL's
query string when it appends the signal path". It does not, and the composition
is not URL-aware at all: the SDK does plain string concatenation,
`${endpoint}/v1/logs`, over a base it first normalises through `new URL(...).href`
— which appends the missing root slash. So `http://127.0.0.1:14318?token=SECRET`
became `http://127.0.0.1:14318/?token=SECRET/v1/logs`: path `/`, token
`SECRET/v1/logs`. Measured on gemini-cli 0.37.1 against a live proxy, every
export failed, alternating **404** (no route at `/`) and **401** (that is not the
secret), printed as raw `OTLPExporterError` stack traces in the user's terminal.
The token is now a PATH SEGMENT (`telemetry.GeminiTokenPath`, `<base>/t/<token>`),
which survives the concatenation because appending to a URL that already has a
path is what the SDK assumes it is doing; the proxy serves both `/v1/…` and
`/t/{token}/v1/…` and still ACCEPTS the query form, since a machine configured
by an older release keeps its settings file across an upgrade and locking it out
would add a second outage to the one it already has. **`/v1/traces` is now
accepted and DISCARDED** (counted, not silent): Gemini builds a trace exporter
unconditionally with no per-signal switch, so with no route there every run
printed a 404 for a signal Atlas does not read.

⚠️ **AND THE MOCK MODEL COULD NOT ANSWER GEMINI'S ROUTER, WHICH READ AS A KELD
FAILURE.** Gemini CLI classifies every prompt before choosing a model
(`NumericalClassifierStrategy` → `BaseLlmClient.generateJson`) with
`responseMimeType: "application/json"` and a `responseJsonSchema` — captured from
0.37.1: `{complexity_reasoning: STRING, complexity_score: INTEGER}`. Answered
with prose it reports "API returned invalid content after all retries" and the
process **EXITS 41**. Measured: ~3 minutes of retries per prompt, then either a
slow success (the router falls back) or a hard failure — so the same defect
looked like "Gemini is slow" locally and killed all three chain A cells in CI, on
macOS, Linux and Windows alike, while the harness reported it against Keld.
`mockllm` now SYNTHESISES the reply FROM THE SCHEMA THE REQUEST CARRIES rather
than hardcoding the one observed, because a fixed answer for a known schema is
the "fixture that resembles the code rather than the tool" failure one level up.

⚠️ **AND FORWARD-ONLY FIRST SIGHT DROPPED EVERY ONE-SHOT GEMINI RUN.**
Forward-only exists so installing Keld does not enrich a machine's entire past,
and on a LINE source that is cheap: a transcript is appended to over time, so
first sight lands on a file still growing and the next prompt is captured.
A DOCUMENT is different — `gemini -p` writes a whole NEW session file per
invocation, so its only prompt is already in the file the first time the watcher
sees it, and forward-only skipped it permanently. There is no second chance and
**no hook to fall back on**: Gemini's `BeforeAgent` event carries no prompt id,
which `internal/hook` already treats as a silent no-op, so the hook keld writes
into `~/.gemini/settings.json` is structurally inert. So `scanDocument` reads
from the beginning when the file's mtime is NEWER than the watcher's own start
instant — a file being written now cannot be the history that rule protects
against. The bound is one SESSION (tens of prompts, not a corpus) and the queue
dedups by prompt id, so the one ambiguous case — a session predating the daemon
that is appended to afterwards — costs its earlier turns being offered once.

⚠️ **AND THE TWO GEMINI CAPTURE LANES CALL THE TOOL BY DIFFERENT NAMES.** The
watcher root, `resolve.GeminiReader`, and the conformance tool id all say
`gemini_cli`; the hook keld writes into `~/.gemini/settings.json` says
`--source gemini`, because `tools.GeminiAdapter` is `Name()`d "gemini".
`resolve.Resolve` dispatches on that string and an unregistered source is a
deliberate SKIP rather than an error — so EVERY hook-delivered Gemini prompt
resolved no text and published nothing, in silence. Measured in the conformance
chain once the transcript format was fixed: `transcript` PASS (2 files, 2 prompt
ids read), `publish` **0**, with the hook independently verified to fire.
The reader is now registered under BOTH names (the
`NewClaudeReaderForSource("cowork")` idiom), which restores the lane with no
wire change. ⚠️ It does **not** settle which name is right: a hook-sourced row
publishes `source_id` "gemini" and a watcher-sourced one "gemini_cli", so one
tool wears two names in the org's data. Unifying them is a deliberate
Atlas-side decision about existing rows, not a rename to be done in passing.

⚠️ **`traces: false` IS NO LONGER WRITTEN INTO `~/.gemini/settings.json`.** It was
belt-and-braces — span CONTENT is gated by `shouldIncludePayloads = traces &&
logPrompts`, so setting both hardened the guarantee against a future build
flipping the `logPrompts` default. That future arrived in the other direction:
measured on 0.37.1, `"traces"` and `shouldIncludePayloads` appear ZERO times in
its bundle, and every invocation printed "Invalid configuration in
~/.gemini/settings.json … Unrecognized key(s) in object: 'traces' … Please fix
the configuration" — about a file Keld wrote, blamed on the user. A key a tool
does not recognise is not free insurance. `logPrompts: false` is the real control
and is still set; the test assertion is INVERTED rather than deleted so the key
cannot return as harmless hardening.
