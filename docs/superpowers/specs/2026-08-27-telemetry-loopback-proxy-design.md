# The tool must not hold an Atlas credential

## The failure, measured

On 2026-08-27 the dev Atlas re-created every ingest token at 16:20:43 UTC. The daemon
noticed within a second, self-healed as designed, and kept publishing. **Telemetry stopped
dead and stayed dead**: `tool_events` froze at 106,390 with its last row at 16:20:44, and
Claude Code's next push at 16:27:58 came back **401**. Re-authenticating at 16:28:04 fixed
`hook.json` and rewrote `settings.json` with a working token — and changed nothing, because
the running Claude Code had read its config at 09:04 and holds the old token in memory.
Telemetry resumed only when the user restarted the tool, roughly 40 minutes later.

⚠️ **`keld signal doctor` said "No problems found" throughout, and it was right.** Every
fact it can reach — service active, daemon reachable, tools configured, token valid — was
correct. The stale copy lives inside a third-party process it cannot inspect. A diagnostic
that is correct while the product is broken is the shape of problem this document exists to
remove.

## Why the obvious fix does not exist

The instinct is to have the hook notice the drift: it runs per-prompt as a child of the
tool, so let it compare the token the tool is using against `hook.json`.

**It cannot. A Claude Code child process sees no `OTEL_*` variables at all** — measured
directly: `env | grep OTEL` in a tool-spawned subprocess returns nothing. Claude Code
applies the `settings.json` `env` block to its own OTEL SDK internally and never exports it
to children. The token the tool is actually using is not observable from anywhere we run.

So detection can only ever be indirect (the daemon sees *its own* 401s, not the tool's), and
remediation can only ever be "ask the human to restart their editor". Neither is automatic.

**The failure class is "a third-party process holds a credential in memory". The only way to
remove it is to stop giving the tool a credential.**

## The design

`keld signal setup` writes the daemon's loopback telemetry endpoint into each tool's config
instead of Atlas's URL and the org ingest token. The daemon receives OTLP, attaches its
CURRENT token, and forwards. A token rotation is then invisible to the tool: nothing it
holds went stale, so nothing needs restarting.

This is not a new mechanism. `promptlog` already POSTs OTLP logs and metrics to Atlas using
`tok.Get` — the daemon's live, self-healing token (`daemon.go:891`) — for Cowork, whose
sandbox egress cannot reach Atlas. This generalises that path from one source to all of them.

⚠️ **It also fixes Gemini, whose exposure is worse and is not obvious.** Gemini's token
rides in the URL query string (`endpointWithToken`), so a rotation leaves a dead token
sitting in a settings file rather than a header.

### The blast radius is two struct fields

All three tool writers derive their config from `telemetry.SetupParams{Endpoint,
IngestToken, BinPath}`:

| tool | today | after |
|---|---|---|
| Claude Code | `OTEL_EXPORTER_OTLP_ENDPOINT` + `OTEL_EXPORTER_OTLP_HEADERS: x-keld-ingest-token=…` | loopback URL + `x-keld-telemetry-secret=…` |
| Codex | `[otel] exporter = { otlp-http = { endpoint, headers } }` | same shape, loopback + local secret |
| Gemini | `otlpEndpoint` with `?token=` | loopback URL with the local secret |

So the change is: populate those two fields differently, and stand up something to receive
what they now point at. The per-tool writers are untouched.

## The two things that would have broken it

Both come from the temptation to reuse the existing ingress. The design deliberately does
not.

### A dedicated fixed listener

⚠️ **The ingress port is EPHEMERAL.** `bindAddr()` returns `127.0.0.1:0` (`daemon.go:329`)
and the port is published in `agent.json` — observed across three restarts in one hour:
41433, 35285, 42571. Writing that port into `settings.json` would go stale **on every daemon
restart**, converting a rare failure into a daily one.

The telemetry route therefore gets its own listener on a **fixed** loopback port:
**`127.0.0.1:14318`**, a compiled-in constant with a `KELD_TELEMETRY_PORT` override.

**Deliberately NOT 4317 or 4318.** Those are the OTLP gRPC and HTTP standard ports and would
collide with any real local collector — on a developer machine that is a realistic conflict,
and the failure mode is telemetry silently going somewhere else. 14318 is the OTLP/HTTP port
offset into a range nothing conventionally claims, so it stays mnemonic without inheriting
the collision.

A port that cannot be bound is reported, never papered over: the daemon logs once per run
and `doctor` surfaces it. Pointing tools at a dead port silently is the defect being fixed,
so it must not be reintroduced as the error path.

### A stable local secret

⚠️ **The ingress secret is REGENERATED EVERY RUN** — `agentcfg.NewSecret()` at
`daemon.go:656`. Writing *that* into tool configs would reintroduce this exact bug one layer
down, and the daily-not-rare version of it.

The telemetry route gets a **stable** secret: generated once, persisted in `agent.json`
beside the existing fields, and **never rotated**. Its whole job is to be writable into a
config file that stays valid indefinitely.

**Why a secret at all**, when the sidecar binds loopback with no auth: this route injects
billable usage events into the user's org, and on a multi-user host any local user could
forge them. That is the same reason the ingress authenticates. The sidecar's endpoints
process text the caller already holds; this one does not.

## Delivery: spool and forward

Routing through the daemon trades away a real property — telemetry is currently
daemon-independent, and a stopped daemon costs nothing. The trade is accepted, and paid for
rather than hoped away.

Received batches are written to a bounded, drop-oldest spool and drained when Atlas is
reachable, exactly as `clientevents` does, with **its own directory** (`paths.SpoolDir()/telemetry`)
— a shared one would cross-post bodies between routes, which `features` already documents as
a trap. A daemon restart then costs latency, not data; the tools' own OTLP buffering covers
the seconds-long window.

⚠️ **Anti-spam is inherited, not invented, and that is the point.** A 401 on forward goes
through the existing `reauther`, which single-flights re-onboarding behind
`KELD_REAUTH_COOLDOWN` (60s) — so a burst of 401s across publish, settings-poll and
telemetry becomes ONE re-onboard, not three, and not a loop. `internal/retry` classifies
transient (net faults, 408/429/5xx) from permanent and treats **unknown errors as permanent
by design**, so an unrecognised failure stops rather than hammering. No new backoff loop is
written; a hand-rolled one here is the thing to refuse.

A batch that fails on a permanent, non-auth status is spooled once and dropped by the
bounded spool's own eviction, never retried forever.

⚠️ **The drain STOPS on the first auth failure**, re-onboards once, and resumes. It must not
walk the rest of the spool learning the same thing: a laptop that was offline for a week
comes back with hundreds of batches and, if its token was revoked while it slept, a per-batch
401 becomes a per-batch re-onboard — a self-inflicted burst against Atlas at the exact moment
an org least wants one. Single-flighting inside `reauther` is necessary but not sufficient
here; the drain loop itself has to yield.

⚠️ **Delivery is confirmed from the RESPONSE, not the status code alone.** Hotel and airport
captive portals answer every request with **200 and an HTML login page**. A forwarder that
keys success on the status will consider the batch delivered and delete it — silent data loss
on precisely the networks employee laptops sit on. The response must be recognisably Atlas's;
anything else is a failure that KEEPS the data.

⚠️ **The token is read per request** (`tok.Get`), never captured when the drain starts, so a
rotation mid-drain is picked up by the remaining batches. Capturing it would rebuild, inside
the daemon, the same stale-credential-in-memory bug this document exists to remove.

## A privacy gain worth taking

The daemon becomes a conduit for OTLP payloads it did not author. That is an opportunity:
it should **enforce the invariant rather than trust it**, dropping any prompt- or
response-text field on the way through.

Today this changes nothing observable — Gemini is written `logPrompts: false`, Codex
`log_user_prompt = false`, and Claude Code's prompt logging defaults off. That is exactly
why it should be done now: the guarantee stops depending on three separate tools' defaults
staying as they are, and becomes structural. `promptlog` already holds to "never emits
prompt/response text"; this applies the same rule at the new boundary.

## Migration

No flag day. An existing install keeps pushing directly to Atlas until `keld signal setup`
runs again, at which point it converges. Org-managed settings that override the endpoint
keep working and keep pushing directly — those machines are precisely what the doctor check
below is for.

⚠️ The two paths must stay distinguishable in Atlas, or a debugging session will not know
which one a given machine is on. The forwarder stamps its own `source_origin` so a
proxied event is identifiable from a directly-pushed one.

## The doctor check

Separately approved and useful in both worlds. `keld signal doctor` compares the `hook.json`
write time against the last successful telemetry forward and reports **"telemetry configured
but not flowing — restart your AI tools"** when a machine is configured and silent.

Before the proxy lands this is the primary defence. After it lands it is the safety net for
the populations the proxy does not reach: direct-push installs that have not re-run setup,
and org-managed endpoints.

It must obey the rule `localagent.ModelState` already follows: **never report a problem from
an inconclusive check.** "No telemetry yet on a machine installed four minutes ago" is not a
fault, and neither is a daemon that has been up for ten seconds.

## Not in scope

- Restarting or signalling the tools. We do not control them, and the proxy removes the need.
- Changing what telemetry is collected, or the OTLP schema.
- Rotating the local secret. Its value is that it never changes.
- A direct-to-Atlas fallback when the daemon is down. It was considered and rejected: it
  reintroduces the stale-token path as the fallback, so the bug bites exactly when the
  daemon is unavailable — the worst possible moment.

## Testing

⚠️ **The happy path is not where this breaks.** It runs on employee laptops: suspended mid-flush,
on hotel wifi, behind a corporate proxy, offline for a week, with a full disk. Every scenario
below is a test, and each names the wrong behaviour it exists to catch.

### A. Anti-spam under adversity — the headline requirement

- **A revoked token against a FULL spool must produce ONE re-onboard, not one per batch.** A
  laptop offline for a week comes back with hundreds of spooled batches and a token revoked
  while it slept. Draining them naively yields a 401 per batch and, without single-flighting,
  a re-onboard per 401 — a self-inflicted burst against Atlas at exactly the moment the org
  is least likely to want one. The drain must stop on the first auth failure, re-onboard
  once through `reauther`'s cooldown, and resume; it must not walk the whole spool learning
  the same thing hundreds of times.
- **A permanently-failing Atlas must not become a hot loop.** 400/404/422 is spooled once and
  evicted by the bounded spool, never retried forever. Asserted by counting requests over a
  simulated hour, not by inspecting code.
- **A token rotated MID-DRAIN** is picked up by the remaining batches without restarting the
  drain, because the forwarder reads `tok.Get` per request rather than capturing it.

### B. Laptop reality

- **Suspend/resume mid-forward.** The in-flight connection dies with a net error, not an HTTP
  status. It must classify as transient, spool, and retry — not be mistaken for a permanent
  failure and dropped.
- ⚠️ **A captive portal returns 200 with an HTML body.** This is the nastiest realistic case:
  hotel and airport wifi answer every request with a success status and a login page. A
  forwarder that keys success on the status code alone will consider the batch delivered and
  **delete it**. The response must be validated as Atlas's, and anything else treated as a
  failure that KEEPS the data.
- **VPN up/down and corporate TLS interception.** Certificate errors are permanent-looking but
  are an environment condition; they must spool rather than discard, because the same laptop
  will be on the VPN in an hour.
- **Days offline.** The spool fills, drop-oldest evicts, and the NEWEST events must survive —
  the failure to catch is an eviction policy that keeps the oldest and discards what just
  happened.

### C. Resources and interruption

- **Disk full while spooling.** The write fails; the daemon must stay up and keep serving the
  listener, degrading to in-memory loss with a stated client-event rather than crashing or
  wedging the tools that are POSTing to it.
- **Kill -9 mid-spool-write.** The next start must not choke on a torn file: a partial batch
  is discarded and the rest of the spool drains.
- **An oversized batch** is rejected at the listener with a clear status rather than buffered
  into memory pressure — the ingress already caps bodies at 1 MiB and this route needs its own
  deliberate bound.

### D. Contention on the fixed port

- **Port 14318 already bound** by something else: reported once per run and surfaced in
  `doctor`; setup must not write an endpoint the daemon could not bind.
- ⚠️ **Two daemons.** A developer running `keld-agent run` by hand while the service is running
  is normal on an engineer's laptop. The second must fail to bind and say so plainly, not
  silently steal or silently lose telemetry. This is the case a fixed port introduces and the
  ephemeral one never had.
- **Several tools POSTing concurrently** (Claude Code, Codex and Gemini all open) interleave
  without corrupting the spool.

### E. Time

- ⚠️ **A laptop that wakes with a skewed clock must not make `doctor` lie.** The check compares
  the `hook.json` write time against the last successful forward; if the clock jumps backwards
  those are unordered, and the honest answer is "cannot determine", never a confident
  "telemetry is broken". This is `localagent.ModelState`'s rule — never report a problem from
  an inconclusive check — applied to a second surface.
- **A freshly installed machine is silent and fine.** No finding within the settling window.

### F. Invariants that must not drift

- **`ClaudeEnv`, `CodexBlockBody` and `GeminiTelemetry` emit no Atlas ingest token and no Atlas
  URL.** This is the regression test for the original bug and should read like one.
- **The telemetry secret survives a daemon restart while the ingress secret does not**, asserted
  in the same test, because their difference *is* the design.
- **A payload carrying a prompt-text field is forwarded without it**, including a field name no
  current tool emits — the guard must be structural, not a list of today's three tools.

### What cannot be tested here, stated rather than implied

No automated test can prove a real Claude Code picks up a loopback endpoint from
`settings.json` and pushes to it; that needs one human run per tool. The suite covers
everything on our side of the socket, and the manual step is one line in the release checklist
rather than an assumption.

## Risks, named

- **Telemetry now depends on the daemon.** Mitigated by the spool, but a machine whose
  daemon never starts collects nothing where it previously collected everything. `doctor`
  is the detector, and this is the strongest argument for the check landing FIRST.
- **A fixed port can collide.** Reported loudly rather than silently mis-delivered, and
  overridable — but it is a new way for a machine to need attention.
- **The daemon sees payloads it did not author.** The text-stripping guard is the answer,
  and it must be applied at the boundary rather than trusted from the tools' configs.
