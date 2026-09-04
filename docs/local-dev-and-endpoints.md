# Running Keld locally, and pointing it at an Atlas

How to build the agent from this repo, run it against local / dev / prod Atlas, and check
which one it is actually talking to. Written for someone who has cloned the repo and wants a
working agent on their own machine in ten minutes.

## The two halves

A Keld install is a Go daemon (`keld-agent`) plus a Python analysis sidecar the daemon spawns
on an ephemeral loopback port. A release ships both as frozen binaries. A **local dev install
ships neither** — it builds the Go binaries from your working tree and writes a sidecar
*wrapper* that execs `sidecar/serve.py` out of the repo.

⚠️ **That wrapper is the thing to remember about local dev.** Your installed daemon runs
whatever branch is checked out. `git checkout` changes what a running service will execute on
its next sidecar spawn, with no reinstall and no warning. It is exactly what makes testing a
sidecar fix a one-command loop, and exactly what makes "why is prod behaving strangely on my
laptop" a five-minute mystery.

## Build and install from the repo

```sh
make build-binaries      # keld + keld-agent -> ~/.local/bin
make sidecar             # venv + ~/.local/bin/keld-agent-sidecar (execs THIS repo's serve.py)
make install-service     # keld-agent install: LaunchAgent (macOS) / systemd --user (Linux)
```

`make sidecar` needs Python 3.12 — the host default 3.14 has no torch/gliner2 wheels. Pass
`PYTHON=/path/to/python3.12` if it is not on PATH.

After a Go change: `make build-binaries && keld signal restart`. After a *sidecar* change:
nothing — the next spawn picks it up.

## Where the data goes

One file decides it, and it is not the one people look in first.

| file | what it decides |
| --- | --- |
| `~/.keld/hook.json` | **the destination.** `endpoint` + `ingest_token` |
| `~/.keld/agent-config.json` | feature switches: `attribution`, `blocks`, `ml_backend`. Not a destination. |
| `~/.keld/agent.json` | written BY the daemon: its own port and the sidecar's. Not config. |

Every URL the daemon posts to is derived from that one `endpoint`: enrichments, blocks,
client-events, enrichment-settings, the telemetry proxy. Store it as a bare base
(`http://localhost:8000`) — the derivation cuts at `/v1/` if it finds one and appends if it
does not, so both shapes work, which is how two machines quietly end up disagreeing about
which is "the" endpoint.

`hook.json` is normally written for you by `keld login && keld signal setup` against whichever
Atlas you logged into.

⚠️ **The token belongs to the endpoint.** `ingest_token` is issued by the Atlas that will
accept it, so a prod token is meaningless against local. Changing only the URL gives you an
agent posting to the right host and being rejected by it — which looks identical to a broken
agent.

⚠️ **`hook.json` is read once, at daemon start.** The startup poll runs only while a machine is
unconfigured; once there is an endpoint and a token, that pair is fixed for the life of the
process. Editing the file changes nothing until the agent restarts.

## An environment lives in two places

⚠️ **This is the part that costs people an afternoon.** Pointing the daemon somewhere new moves
half your data, and the half that stays behind does not look broken.

| written by | holds | covers |
| --- | --- | --- |
| `keld signal setup` | `~/.claude/settings.json`, `~/.codex/config.toml`, `~/.gemini/settings.json` | the tools' own OTLP telemetry, and the `KELD_CTX_ENDPOINT` on the SessionStart/PreToolUse hooks |
| `keld login && keld signal setup` (or this script) | `$KELD_HOME/hook.json` | the daemon: enrichments, blocks, client-events, org settings, and the telemetry it proxies |

Each tool config carries a **baked endpoint and ingest token** written at setup time. They know
nothing about `hook.json` and are not re-read from it. So a daemon moved to dev while the tools
still point at local gives you one session whose blocks are in dev and whose telemetry and tool
context are in local — joined by nothing, with every piece individually healthy.

Run `scripts/keld-endpoint.sh` with no arguments to see both halves at once. A real example from
a working machine, where three of four were local and nobody had noticed:

```
AI tools (written by `keld signal setup`)
  claude_code · telemetry      http://localhost:8000
  claude_code · tool-context   http://localhost:8000/v1/tool-context
  codex · telemetry            http://localhost:8000/v1/logs
  gemini · telemetry           https://atlas.keld.co        <- still prod
```

## Switching between local, dev and prod

`scripts/keld-endpoint.sh` keeps one saved profile per environment — endpoint *and* token
together, for the reason above — and swaps them.

```sh
scripts/keld-endpoint.sh                  # both halves: the tools and every agent
scripts/keld-endpoint.sh save local       # save the current hook.json as "local"
scripts/keld-endpoint.sh use dev          # daemon + tools, then restart the agent
scripts/keld-endpoint.sh list
```

`use` writes `hook.json`, restarts the agent, and then re-runs `keld signal setup --api-url
<endpoint>` so the tools move with it. `--daemon-only` skips the tool half and says plainly that
your data is now split. **Restart any open AI tool session afterwards** — those configs are read
at process start, so a running Claude Code or Codex keeps posting to the old environment until
it is restarted.

If the tool half fails, it is almost always that you have no stored credentials for that Atlas
yet: `keld login --api-url <endpoint> && keld signal setup --api-url <endpoint> -y`.

To create a profile for an environment you have never signed into, log into it once and save
the result:

```sh
keld login                 # against that Atlas
keld signal setup          # writes hook.json
scripts/keld-endpoint.sh save prod
```

`set <url> [token]` is the escape hatch for a one-off host with a token you already hold. It
warns when you keep a token issued by a different host, rather than letting it fail silently
later.

Profiles live in `$KELD_HOME/endpoints/*.json`, mode 0600. They contain live credentials —
they are not something to copy into a ticket, and `status` prints only a hash prefix of a token
for exactly that reason.

## A second agent, side by side

`KELD_HOME` moves the whole state directory, so a scratch agent can run beside your real one
without touching it:

```sh
KELD_HOME=~/.keld-smoke KELD_ATTRIBUTION=1 KELD_BLOCKS=1 ./keld-agent run
KELD_HOME=~/.keld-smoke scripts/keld-endpoint.sh use local
KELD_HOME=~/.keld-smoke scripts/watch-attribution-drain.sh
```

Everything reads it: `hook.json`, `agent-config.json`, the spool, the job store, `agent.json`.
`KELD_CTX_ENDPOINT` / `KELD_CTX_TOKEN` override the file for a single run without writing
anything, which is the right tool for "post this one session somewhere else".

## Checking it works

```sh
keld signal status                        # configured? running? talking to whom?
keld signal doctor                        # the deeper check
scripts/watch-attribution-drain.sh        # the attribution queue, live
```

For the raw truth, ask the sidecar directly — its port is in `agent.json` and **changes on
every daemon start**, so read it rather than remember it:

```sh
PORT=$(python3 -c "import json,os;print(json.load(open(os.path.expanduser('~/.keld/agent.json')))['sidecar_port'])")
curl -s "http://127.0.0.1:$PORT/metrics" | python3 -m json.tool | head -40
```

## When nothing is arriving

In the order that finds it fastest:

1. **Is it pointed where you think?** `scripts/keld-endpoint.sh` — a stale profile after an
   environment switch is the most common answer.
2. **Did the daemon restart since you edited `hook.json`?** It reads it once.
3. **Is the sidecar alive?** `agent.json` naming a port nothing listens on means the daemon
   outlived its sidecar.
4. **Is Atlas rejecting the token?** A token from another environment fails as authentication,
   not as a connection, so the daemon looks healthy in every log you check first.
5. **Is only *some* of it missing?** Blocks present and telemetry absent (or the reverse) is the
   two-halves problem above, not a bug — check the tool row and the daemon row against each
   other.
