# Telemetry loopback proxy, capture triggers and the transcript-first usage mirror

> **Provenance — split out of `AGENTS.md` on 2026-09-28** (at `4d2b6d0`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-09-23**
> (best effort: the newest commit `git log -L` finds over the moved line ranges).
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *Architecture* (the telemetry lane), *Capture triggers* and *The
transcript-first usage mirror* carry the rules that matter before you edit. This
file carries why each one exists and what it cost to learn — including the
2026-09-18 secret-mismatch outage behind the secret file, setup's two refusals and
the daemon's self-repair. Gemini's capture lane is in `gemini-capture.md`.

  ⚠️ **This bullet used to read "the hook posts usage telemetry straight to
  Atlas. No daemon involvement", and the change is the whole point rather than a
  refactor.** A tool reads its configuration ONCE, at startup, and keeps the
  credential in memory — so when the org's ingest token rotated, every running
  tool went on posting the old one and its telemetry was rejected until a human
  restarted the editor. Measured on a real machine: `tool_events` froze for 40
  minutes while `keld signal doctor` reported no problems, **correctly** — every
  fact it can reach was right, and the stale copy lived inside a process it
  cannot inspect. The hook cannot detect it either: a Claude Code child process
  sees **no `OTEL_*` variables at all**, because Claude Code applies its `env`
  block to its own OTEL SDK and exports nothing. So detection was impossible and
  remediation could only ever be "ask the human to restart"; the fix is to stop
  handing the tool a credential. `keld signal setup` writes the loopback address
  and a **stable local secret**, generated once and never rotated — unlike
  `Info.Secret`, regenerated every daemon start, which would rebuild the bug one
  layer down and fire it daily. The token the daemon attaches is read **per
  request**, so a rotation mid-flight is picked up.
  ⚠️ **THAT SECRET NOW HAS ITS OWN FILE (`~/.keld/telemetry-secret`, 0600), AND
  IT USED TO LIVE INSIDE `agent.json` — WHICH COST AN OUTAGE.** Measured
  2026-09-18 on the maintainer's machine: `agent.json` held `26908e20…` while
  `~/.codex/config.toml` and `~/.claude/settings.json` both held `a5629e92…`,
  written at 17:34 by a `keld` 3.0.0-rc.3 still on PATH at
  `/usr/local/keld/keld`. A probe POST to the running proxy with the tools'
  token returned **401**: Codex's telemetry was dead, and Claude Code survived
  only because its running process still held the older, correct value in memory
  — it would have broken on its next restart. `agent.json` is rewritten by
  several writers and the value was protected only by a preservation rule inside
  `agentcfg.Write`, i.e. by every writer remembering to route through it. The
  file is now the SOURCE OF TRUTH and `agentcfg.EnsureTelemetrySecrets` resolves
  it; the value is still **mirrored into `agent.json`** (write-through) so an
  older binary on the same machine reads the same secret rather than minting a
  second one. ⚠️ **Migration ADOPTS, never mints**: on a machine upgrading from
  the old layout the value in `agent.json` is moved into the file, because
  generating a fresh one there 401s every already-configured tool at once, which
  IS the incident. The file is deliberately **not** under `state/`, which
  `keld signal uninstall` removes wholesale.
  ⚠️ **A deliberate rotation is survivable rather than an outage.** The file is
  a small JSON object (`{secret, previous, rotated_at}`; a bare-string file is
  still read as the secret) and `teleproxy` accepts the retired value for
  `KELD_TELEMETRY_SECRET_GRACE` (default **24h**) after the rotation instant, in
  **all three credential shapes** — a grace honoured for Claude Code and Codex
  but not for Gemini breaks one of a person's tools for reasons they cannot see.
  An empty previous, or one with no recorded instant, authenticates nothing:
  `ConstantTimeCompare("", "")` is 1, so that guard is what keeps the route from
  failing open. Nothing in the product rotates on its own.
  ⚠️ **AND `keld signal setup` NOW REFUSES TWICE RATHER THAN REPORTING SUCCESS
  ONTO A BROKEN MACHINE.** (1) After writing the tool configs it POSTs one empty
  OTLP batch (`{"resourceLogs":[]}`) to the running proxy with the credential it
  just wrote; a **401 restores the backups and exits non-zero**, because leaving
  the rejected value in place is leaving the machine in the state the probe just
  proved broken. **No daemon listening is NOT a failure** — `keld-agent install`
  starts the service after setup runs and the macOS wizard onboards before the
  daemon exists, so it says "could not verify (daemon not running)" and carries
  on. (2) If another `keld` on PATH reports a **newer** version it refuses before
  reading or writing anything and names the path — the incident's cause rather
  than its symptom, since the mismatched secret was written by the older of two
  installs. It reuses `keldPATHBinaries()`, doctor's own shadowed-binary
  detection; `version.Newer` orders pre-releases (`3.0.0-rc.3 < 3.0.0`) and
  answers **unknown** for `dev` or anything unparseable, so a source build never
  accuses anyone.
  ⚠️ **AND THE DAEMON NOW REPAIRS ITS OWN BLOCK RATHER THAN REPORTING IT
  BROKEN.** In that same incident the daemon could see BOTH values the whole
  time — the live secret in its own file and the stale one in
  `~/.codex/config.toml` and `~/.claude/settings.json` — and did nothing,
  because the integrations detector never edits a config the manifest already
  records. That refusal is right for the PERSON'S OWN telemetry section and
  wrong for keld's own block, which keld wrote and owns; it generalises the
  exception `HookCommandBroken` already made. The detector compares the VALUES
  inside keld's markers (`integrations/drift.go`: Codex's marker block, Claude
  Code's two `env` keys, Gemini's `otlpEndpoint`) against what the adapter
  would write now, **never a hash of the file** — the tools rewrite their own
  configs (Codex's `hooks.state` at session start, Claude Code's settings.json
  unprompted, both measured the same day), so a whole-file comparison would
  rewrite a healthy config every time one of them did. Drift outside the
  markers, and a conflict in the person's own section, are left alone and keep
  the pane's Set up path. The rewrite goes through `ApplyEntry` — the one
  write path, with the backup and the `configured_at` stamp — is bounded to
  one attempt per tool per daemon run by `attempted`, and ⚠️ **refuses
  unless the running proxy CONFIRMS the credential it is about to write**
  (`telemetry.ProbeSecret`, shared with setup's own post-write probe): writing
  a value the daemon itself rejects would replace a broken config with a
  differently broken one. That refusal HOLDS rather than quarantines — the
  next poll repairs once the proxy answers. What it did is SAID: the
  `integration.configured` event carries a closed `reason`
  (`first_setup`/`hook_command`/`telemetry_drift`), one log line per repair
  rather than per poll, and the row publishes `repaired` with the sentence the
  pane prints — because `broken` with no explanation is what that row said for
  the whole incident.
  ⚠️ **The proxy accepts that secret in THREE shapes, because the tools do not
  agree on one**: `x-keld-ingest-token` (Claude Code, Codex), `?token=` in the
  URL (Gemini — its OTLP SDK cannot send a custom header at all), and
  `x-keld-telemetry-secret`. Assuming a single shape 401'd all three tools live
  while the entire Go suite passed, because the tests used the name the proxy
  chose rather than the ones `telemetry.ClaudeEnv`/`CodexBlockBody`/
  `GeminiTelemetry` emit. Widening where the credential may appear does not
  widen what is accepted.
  ⚠️ **THE PROXY FORWARDS ONLY WHILE `tool_otlp` IS ON, READ PER REQUEST — AND
  UNTIL 2026-09-21 IT DID NOT, WHICH DOUBLE-COUNTED EVERY RUNNING TOOL THE DAY
  THE SWITCH WENT OFF.** A tool reads its telemetry config once, at startup, so
  one configured before the switch went off keeps posting OTLP here from
  memory for the rest of its session; the transcript mirror is on for exactly
  that tool (`promptlog.SourcesFor` is the complement of the switch); and Atlas
  keys a mirrored row (`request_id`) and a tool-sent row
  (`session.id:event.sequence`) differently, so both landed and both were
  priced. The PR that shipped the switch said the complement rule "enforces"
  that the two lanes never both run for one tool — true of what keld WRITES
  into configs, false of what a running tool still SENDS. `Proxy.Forwarding`
  now reads the switch where the bytes arrive: off, an authenticated export is
  answered 202 (the tool must not retry), never forwarded, never recorded as a
  forward (the pane's otel lane must not read "arrived" off bytes that went
  nowhere), counted in `DiscardedSwitchOff`, and announced once per source per
  run as `telemetry.otlp_discarded`. Nothing is lost that the mirror does not
  already carry. Pinned by `teleproxy/forwarding_test.go`, including that a
  Proxy constructed with no hook still forwards — every existing test does.
  ⚠️ **THE TEXT GATE OVER-MATCHED `prompt.id` AND SILENTLY BROKE EVERY
  CORRELATION.** `teleproxy.textKey` matched an attribute key by SHAPE —
  `strings.Contains(k, "prompt")` and seven siblings — and its comment said "a
  key it over-matches costs one dropped attribute". That was wrong about the
  most important attribute on the wire: Claude Code sends `prompt.id` on every
  record, and Atlas joins `Enrichment.corr_id` to `ToolEvent.prompt_id`. So the
  proxy blanked the one field relating a block to the telemetry it describes,
  and the failure was invisible in the way a dropped attribute is not — rows
  arrived, attributed, counted, and joined to nothing. Measured on the dev
  Atlas: of one proxied session's 1,107 events, **0** had a non-empty
  `prompt_id`, while unproxied seed rows kept theirs; after the fix, 10 of 10.
  Reported as "blocks show up but the activity panel is empty", which is that
  join returning nothing. The rule is now two-sided — match a text word, then
  subtract the identifier/measurement SUFFIXES (`.id`, `_id`, `_length`,
  `_tokens`, …) — and both halves are load-bearing: drop the first and text
  leaks, drop the second and correlation dies. An unanticipated shape still
  fails CLOSED, toward privacy. Pinned by `striptext_identity_test.go` against a
  **real captured Claude Code payload**, because the original gate shipped
  green: `prompt.id` was in no fixture, so nothing could see the cost.
  ⚠️ **`keld signal setup` now SAYS to restart the tools, and never used to.**
  The reasoning was written in that package twice and printed zero times. A tool
  reads its telemetry config once at startup, so one already running keeps
  posting wherever it was pointed when it launched; nothing on the machine can
  detect or fix that from outside. Measured: a session started before setup ran
  emitted **0** telemetry events over 11 hours while its blocks published
  normally — blocks are read from the transcript by the daemon and never depend
  on the tool's config, so the visible half of the product stayed healthy and
  hid the silent half. The `done` event carries `restart_required` so an
  installer's UI can say it too.
  ⚠️ **And doctor now asks PER SESSION, because the machine-wide check cannot.**
  `localagent.TelemetryState` asks whether telemetry has arrived AT ALL since the
  credential was written, so on a machine running two editors — one started
  before setup, one after — the second vouches for the first and doctor reports
  "No problems found", correctly and uselessly. `SessionTelemetryState` compares
  the sessions whose transcripts are being written NOW against
  `teleproxy.SessionsOnDisk()`, a bounded (64, oldest-evicted) record of which
  tool session ids the proxy has forwarded for. Session ids are IDENTIFIERS —
  the same class already published as `corr_id`; no text, span or offset is read.
  Three refusals keep it from lying: an **empty record is "not tracked yet"**,
  never "nothing is arriving" (on upgrade the state file has a `last_forward` and
  no sessions, and reporting then would call every running tool broken the day it
  shipped); `agent-*.jsonl` **subagent transcripts are excluded** (they share
  their parent's OTEL session id and are **620 of 671** files here, so including
  them means hundreds of false findings); and the session's start instant is read
  by DECODING lines for a top-level `timestamp`, never by pattern-matching the
  first one — Claude Code opens a transcript with untimestamped `custom-title` /
  `mode` / `file-history-snapshot` records, the same trap `capture.scan`
  documents. Scoped to `claude_code`: Cowork's egress is blocked by design and
  Codex/Gemini transcript names are not their OTLP session ids. The record is
  LOADED at proxy construction, not started empty — otherwise a daemon restart
  makes every session look untracked, and the first forward then writes that
  empty map back, erasing the history rather than merely not reading it.
  ⚠️ **And `teleproxy`'s tests now isolate `KELD_HOME` in a `TestMain`, because
  they were writing the developer's real `~/.keld`.** `New()` resolves
  `StatePath()` at construction and every successful forward persists, while most
  tests there pass `t.TempDir()` only for the SPOOL — so the spool was isolated
  and the state file was not. Running `go test ./...` on a live machine
  overwrote its telemetry record, erased the per-session history, and silently
  turned this very check inconclusive. A test that mutates the machine it runs on
  is a worse defect than the one it checks for.
  ⚠️ **Telemetry now depends on the daemon**, where it did not before. Paid for
  with a bounded spool under `spool/telemetry` and not hoped away; a machine
  whose daemon never starts collects nothing, and `keld signal doctor` is the
  detector — which can only exist AFTER this path, since pre-proxy the client
  kept no record of tool telemetry at all. Delivery is confirmed from the
  RESPONSE, not the status code: captive portals answer **200 with an HTML login
  page**, and a status-only check would delete the batch. A drain **stops on a
  REJECTION** (401/403 — every remaining batch would be told the same thing),
  **ends the sweep on UNAVAILABLE** (net/5xx — never a re-onboard, nothing is
  wrong with the credential), and **continues past a REFUSED payload** (4xx —
  or one bad batch blocks every good one behind it). See
  `docs/superpowers/specs/2026-08-27-telemetry-loopback-proxy-design.md`.

**Cowork went VM-backed, and host-side capture cannot follow it.** Newer Claude
desktop builds run Cowork inside a VM (`vm_bundles/claudevm.bundle`) whose
transcripts live in the VM's disk image, not under `local-agent-mode-sessions`.
Nothing on the host can read them: no folder is shared and the VM's address does
not answer the host. Discovery therefore finds no *live* Cowork transcripts on
these machines — and because the pre-VM session directories are never cleaned up,
a root is still discovered, just permanently stale. `coworkHidden`
(`internal/agent/watch/roots.go`) detects that exact shape — VM images touched
within `coworkActiveWindow` while no Cowork `.jsonl` was written in the same
window — and logs one advisory line per daemon run, because the failure is
otherwise completely silent: Cowork just stops appearing in Atlas. It keys on
transcript **freshness**, not root existence, precisely so the stale-directory
machines are caught. Restoring capture needs a path the host can read (or an
in-VM emitter); `KELD_WATCH_ROOTS=cowork:<dir>` points the watcher at one the day
it exists.

**The transcript-first usage mirror (`internal/agent/promptlog`).** Cowork's own
OTEL is configured to Keld but its sandbox egress blocks `atlas.keld.co`, so the
daemon mirrors the transcript's events into OTLP logs+metrics host-side. That
narrow workaround is now the general mechanism, for the reason the teleproxy
bullet above already documents at length: **a tool reads its telemetry
configuration once, at startup**, so every change Keld makes to it is invisible
until a human restarts the editor, while a transcript needs no configuration, no
credential inside the tool and no restart. Evidence it is enough: this machine's
ledger holds **1,073 delivered blocks (966 Claude Code, 107 Codex) covering
22,746 requests and $3,732.92 of estimated spend, all derived from transcripts
rather than from OTLP**.

⚠️ **THREE MIRRORS, EACH IN ITS OWN TOOL'S NATIVE OTLP SHAPE, so Atlas needs no
new parser.** `claude_code`/`cowork` → `claude_code.user_prompt` +
`claude_code.api_request` (+ `claude_code.token.usage` metrics); `codex` →
`codex.sse_event` with `event.kind == "response.completed"`, the record Atlas
prices Codex off; `gemini` → `gemini_cli.api_response`, the token-bearing event
(NOT `api_request`, which is the request side). Codex and Gemini emit no metrics
— Atlas prices both entirely off their log record, and inventing a metric name
would be a guess. Identity (`user.email`/`account_uuid`/`organization.id`) is
recovered from the Cowork session path/metadata where it exists; elsewhere it is
absent, which costs nothing, because Atlas stamps the authoritative `principal`
from the ingest token that authenticated the request.
**Never emits prompt/response text** — `privacy_test.go` puts a canary in every
text-bearing field each of the three tools writes and asserts none reaches the
wire.

⚠️ **GEMINI CANNOT RIDE THE PER-LINE HOOK.** Its session is ONE JSON document
rewritten whole every turn, so there are no appended lines to observe; it arrives
through `Telemetry.ObserveFile` on `watch.WithDocumentObserver`, the coarse
sibling of `observe` and of the ingest signal, carrying coordinates only.
`geminichat.Session.Responses` is the reader — gated on the `tokens` BLOCK rather
than on a type string, because shape is what stays stable across builds (measured:
203 of 262 messages across 55 real chat files carry it, no user turn does).

⚠️ **ONE RECORD PER REQUEST, NOT PER LINE, AND THE DIFFERENCE IS 1.79x.** Claude
Code writes one assistant line per CONTENT BLOCK and stamps the whole request's
usage on every one of them: measured over the 40 largest real transcripts here,
**13,755 assistant lines carrying a `message.usage` resolve to 7,683 distinct
`requestId`s**, 4,088 of them written as more than one line (max 11), and **0**
of those requests disagree with themselves about their token counts. A record per
line therefore publishes 1.79x the tokens the work cost. **Replicated
independently on a wider sample the same night** — every transcript under
`~/.claude/projects` rather than the 40 largest: **26,410 lines carrying usage,
14,622 distinct `requestId`s, 1.81x, and again 0 requests disagreeing with
themselves.** Two samples, two ratios, one conclusion; the ratio is a property
of how the tool writes, not of which transcripts were read.
⚠️ **AND ATLAS DOES NOT ABSORB IT**, which is what makes this a live data defect
rather than a wasteful payload: `services/otel.py::_dedup_key` prefers
`session.id:event.sequence` and falls back to `request_id` only when one of them
is absent, while the pre-fix mirror stamped a fresh sequence per record. Every
duplicate was therefore stored as its own row. Cowork is the only source that
has been mirrored, so Cowork's tokens and spend in Atlas are inflated by about
that factor for as long as this has run. The client half is fixed here; making a
mirrored row and a tool-sent row COLLAPSE needs Atlas to prefer `request_id` on
`api_request`, which is one line in that function and is not in this repo. The mirror emits on the
FIRST line of a request and drops the rest — one variable, not a set, because
**7,703 request runs and 0 requests that resumed after another intervened** says
a request's lines are contiguous. Codex has the same class of defect from the
other end: **936 of 10,061 real `token_count` records (9.3%) repeat the previous
record's `total_token_usage` exactly** while still carrying a non-zero
`last_token_usage`, so the mirror prices a record only where the cumulative total
ADVANCED — which reconciles with the session's own final total on 22 of 23
rollouts.

**The dedup contract.** Atlas stores one tool event per `(event_ts, dedup_key)`
and upserts, so a mirrored row and a row the tool sent itself must agree on both
halves. Every identifier on a PRICED record is read off the transcript line and
nothing comes from process state — in particular the mirror emits **no
`event.sequence`** on `api_request`, because Atlas keys a Claude-Code row on
`session.id:event.sequence` and falls back to `request_id`
(`services/api/app/services/otel.py::_dedup_key`), and a process-local counter
renumbers on a daemon restart while the tool's own `requestId` does not. ⚠️ **The
remaining gap is Atlas-side and is one line**: the tool DOES send a sequence, so
under the current preference order the two rows do not collapse; preferring
`request_id` on `api_request` closes it. Codex and Gemini key on a content HASH
of the attributes, so for those the mirror's dedup IS the determinism of its
payload — which is why no wall clock and no counter appear in either. ⚠️
**`assistant_response` is no longer mirrored**: it carried only `response_length`
(a measurement of response TEXT, priced by nothing), it could not be told from
`api_request` under the natural key since Claude Code stamps both with the same
`request_id`, and per-request it is not computable without buffering a whole
request.

**Source selection is a parameter, not a setting read here.** `SourcesFromEnv`
still defaults to `{cowork}` with `KELD_WATCH_TELEMETRY` (off/on) and
`KELD_WATCH_TELEMETRY_SOURCES`; `Telemetry.SetSources` REPLACES the set whole, and
is the seam the per-tool `tool_otlp` switch is wired to at the one call site in
`daemon.go`. Per source, mirroring is on or off — half a source is how the same
request gets counted twice. Widening the MECHANISM and flipping the POLICY are
deliberately separate changes: a default flipped in `SourcesFromEnv` would start
mirroring on every machine the moment the binary shipped, beside tools still
posting their own OTLP.

**Codex** and **Gemini** also keep their own watcher roots (`~/.codex/sessions`
and `~/.gemini/tmp/*/chats`) + specialized readers for ENRICHMENT (TranscriptReader
resolves user_message by session_id#ordinal for Codex, by message `id` for Gemini);
that path is unchanged.
