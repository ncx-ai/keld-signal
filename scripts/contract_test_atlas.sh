#!/usr/bin/env bash
# AC-9: the golden block payload (a real publish.BlockEnrichment, including the
# additive `projects`/`projects_status`/`attribution` project-attribution fields)
# round-trips into a STOCK local Atlas with ZERO Atlas-side changes. Atlas parses
# blocks with extra="ignore" and stores the whole POSTed body verbatim in the
# `blocks.raw` JSONB column, so `raw->'projects'` is exactly what proves an
# additive field survives a client that knows about it into a server that doesn't.
#
# IDEMPOTENT: block identity at Atlas is (org, source, session_id, start_ts) and
# ingest UPSERTs on it, so re-running this script is safe and must still PASS. The
# assertions below read the row back by session_id (not by "was a row just
# inserted", which a re-run would never see) and compare it to the golden file, so
# a second run against an already-seeded row passes exactly like the first.
#
# Prereqs (NOT started by this script — it is a long-lived foreground service):
#   cd ../keld-atlas && make dev
#   cd ../keld-atlas && make dev-seed        # seeds the Acme + keld orgs/logins
#   Then get a per-user ingest token (make dev-seed does NOT print one — ingest
#   tokens are per-user, not per-org): either log in at http://localhost:3000 as
#   admin@acme.test / acme2026 and copy the token from Integrations' Claude
#   Code/Codex snippet, or run `keld login` + `keld signal setup` against this
#   Atlas and read the token out of the resulting ~/.keld/hook.json.
#   export ATLAS_INGEST_TOKEN=<that token>
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GOLDEN="$SCRIPT_DIR/testdata/golden_block_with_projects.json"

ATLAS_URL="${ATLAS_URL:-http://localhost:8000}"
# Sibling checkout by default (…/projects/keld/{keld-signal,keld-atlas}); override
# if keld-atlas lives somewhere else on this machine.
ATLAS_REPO="${ATLAS_REPO:-$SCRIPT_DIR/../../keld-atlas}"
COMPOSE_FILE="$ATLAS_REPO/docker-compose.yml"
POSTGRES_USER="${POSTGRES_USER:-keld}"
POSTGRES_DB="${POSTGRES_DB:-keld}"

fail() {
	echo "FAIL: $1" >&2
	exit 1
}

if [ ! -f "$GOLDEN" ]; then
	fail "golden payload not found at $GOLDEN"
fi

if [ -z "${ATLAS_INGEST_TOKEN:-}" ]; then
	fail "ATLAS_INGEST_TOKEN is unset. Get a per-user token: cd $ATLAS_REPO && make dev && make dev-seed, then log in at http://localhost:3000 (admin@acme.test / acme2026) -> Integrations, and copy the ingest token from the Claude Code/Codex snippet (or run 'keld login' + 'keld signal setup' against $ATLAS_URL and read it from ~/.keld/hook.json). Then: export ATLAS_INGEST_TOKEN=<token>"
fi

if ! curl -sf -m 5 "$ATLAS_URL/api/health" >/dev/null 2>&1; then
	fail "Atlas is unreachable at $ATLAS_URL. Start it first: cd $ATLAS_REPO && make dev (and, on a fresh stack, make dev-seed)."
fi

command -v python3 >/dev/null 2>&1 || fail "python3 is required to compare JSON payloads"

RESP_FILE="$(mktemp)"
trap 'rm -f "$RESP_FILE"' EXIT

BODY="$(python3 -c '
import json, sys
with open(sys.argv[1]) as f:
    block = json.load(f)
print(json.dumps({"blocks": [block]}))
' "$GOLDEN")"

code=$(curl -s -o "$RESP_FILE" -w '%{http_code}' \
	-X POST "$ATLAS_URL/v1/signal/blocks" \
	-H "x-keld-ingest-token: $ATLAS_INGEST_TOKEN" \
	-H "Content-Type: application/json" \
	--data "$BODY")

if [ "$code" != "201" ]; then
	echo "--- response body ---" >&2
	cat "$RESP_FILE" >&2
	fail "POST $ATLAS_URL/v1/signal/blocks returned $code, want 201"
fi

SESSION_ID="$(python3 -c '
import json, sys
with open(sys.argv[1]) as f:
    print(json.load(f)["session_id"])
' "$GOLDEN")"

WANT="$(python3 -c '
import json, sys
with open(sys.argv[1]) as f:
    block = json.load(f)
print(json.dumps(block["projects"], sort_keys=True, separators=(",", ":")))
' "$GOLDEN")"

# Read the stored row back out of Postgres. The compose service is `postgres`
# (not `db`), and user/db default to `keld`/`keld` per keld-atlas/docker-compose.yml.
STORED="$(docker compose -f "$COMPOSE_FILE" exec -T postgres \
	psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
	"select raw->'projects' from blocks where session_id='$SESSION_ID' order by received_at desc limit 1")" \
	|| fail "could not read blocks.raw->'projects' back from Postgres (is 'make dev' running?)"

STORED="$(printf '%s' "$STORED" | tr -d '\r')"

if [ -z "$STORED" ]; then
	fail "no row found in blocks for session_id=$SESSION_ID — the POST returned 201 but nothing was stored"
fi

GOT="$(python3 -c '
import json, sys
raw = sys.stdin.read()
print(json.dumps(json.loads(raw), sort_keys=True, separators=(",", ":")))
' <<<"$STORED")"

if [ "$GOT" != "$WANT" ]; then
	echo "--- want (golden projects, normalised) ---" >&2
	echo "$WANT" >&2
	echo "--- got (blocks.raw->'projects', normalised) ---" >&2
	echo "$GOT" >&2
	fail "raw->'projects' did not round-trip verbatim"
fi

echo "PASS: POST /v1/signal/blocks -> 201, and blocks.raw->'projects' round-tripped byte-for-byte (normalised) for session_id=$SESSION_ID"
