# An unpaired daemon collects: startup without a config, and pair-to-send

> **Provenance — split out of `AGENTS.md` on 2026-09-28** (at `4d2b6d0`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-09-23**
> (best effort: the newest commit `git log -L` finds over the moved line ranges).
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *An unconfigured agent does not fail, and an unpaired one COLLECTS*
states the rule every sender follows. This file carries the crash-loop that
made "idle, do not fail" a rule, and why idling was then replaced by
collecting. What each lane buffers, replays and loses: `docs/durability.md`.

**An unconfigured agent IDLES, it does not fail.** The service is routinely
registered *before* onboarding runs (the documented macOS pkg order), so a
missing `~/.keld/hook.json` is a normal startup state, not a crash. `Run` waits
on `daemon.awaitConfig` (re-reads hook.json every `KELD_CONFIG_POLL`, default
5s; announces the wait once, not per poll) and starts the instant `keld signal
setup` writes the token — no restart needed. Returning an error here instead
cost a tester **69 launchd spawns in 12 minutes**, because the plist's
`KeepAlive` was an unconditional `<true/>`. That is now the
`SuccessfulExit=false` dictionary, so a clean exit is final while a real crash
still restarts; systemd's `Restart=on-failure` was already the equivalent
(don't add `RestartSec` — see the note in `service.go`), and the Windows
`ONLOGON` scheduled task never retried at all.

⚠️ **AND AN UNPAIRED AGENT COLLECTS — IT NO LONGER IDLES AT ALL.** The bullet
above used to describe the whole daemon: `Run` started nothing but the
integrations detector until `awaitConfig` saw `hook.json`, so a machine between
install and login had **no telemetry proxy listening** (every AI tool `keld
signal setup` had already configured posted into a closed port), no transcript
watcher, no block emitter and no enrichment. That gate is left over from the
design where the hook POSTed telemetry straight to Atlas and without a token
there genuinely was nothing to do. **Collect always, pair to send**
(`daemon/pairing.go`, `daemon/senders.go`): every collector is constructed and
started immediately, and every sender resolves its endpoint through `pairing`,
which answers `""` until the pairing arrives. A sender handed `""` **HOLDS** —
it spools the batch, keeps the cursor, or re-spools the pointer — and never
reports success and never drops; said three ways, `publish.ErrNotPaired`,
`clientevents.ErrNotPaired` and `settings.ErrNotPaired`. The endpoint gets the
treatment the ingest token already had (a getter read per request, not a string
captured at construction), which is what lets a pairing be adopted mid-run with
no restart. **Enrichment is the one collector with nowhere local to put its
OUTPUT** — a block is cut into the ledger and re-offered by a held cursor, a
telemetry batch lands in the proxy's spool, a finished profile has neither — so
the worker holds each job back in the enrich spool on the existing "not ready
yet is never un-enrichable" path, consuming no retry attempt; the spool drain is
held with it, or the drain and the deferral chase each other once per sweep.
Both are gated on Atlas being ON as well as on the pairing, since a local-only
machine publishes through `localOnlySender` and must keep enriching. One log
line when collection starts unpaired and one when the pairing lands, following
`awaitConfig`'s announce-once idiom; `keld signal status`/`doctor` and the
health strip's `atlas` row say **collecting, not paired** — `n/a` with reason
`not_paired`, never `failed` (which accuses a healthy machine) and never
silence (which renders as "we could not tell").
