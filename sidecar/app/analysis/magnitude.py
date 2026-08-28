"""Per-turn MAGNITUDES: numbers on a turn, not what the turn was about.

Every other module in this package answers "what was this turn about" and answers it as a
reference — a level and a value. This one answers a different question with a different shape: a
single number per turn. It is separate for that reason, not for tidiness.

Two families, and the distinction is load-bearing rather than cosmetic. The COST kinds
(`KINDS` — token weight and diff magnitude) measure what a turn SPENT, and they are what
`Store.has_magnitudes` and the weighted rollup are scoped to. The CAPTURE kinds
(`CAPTURE_KINDS`, written only under `KELD_CAPTURE=1`) are never a cost: a character count, a
raw token split, a tool outcome. This module used to call itself "ECONOMIC magnitudes: what a
turn COST", which stopped being true of the whole of it the moment the second family arrived.

Attribution today divides a window by how many tool calls touched a thing; the thing an
attribution product is actually dividing is spend, and the numbers that measure spend are
already sitting in every transcript line `transcript.turns_in` parses.

Nothing here reads a `tool_result`, and nothing here retains a byte of the strings it measures.
`message.usage` is on the assistant line itself and `tool_use.input` is a block on that same
line, so both signals come from lines the parse already decodes — no extra file read, no
exposure to the file contents that live in tool results. `edit_bytes` takes a payload and returns
an `int`; that is the whole of its interface, and it is why no code path exists by which a byte of
`old_string`/`new_string`/`content` can reach anything that gets stored or serialised.

## What "token weight" means

`message.usage` carries four counts that are billed at four DIFFERENT prices, so their sum is not
a cost:

    input_tokens                             base rate,          1.00x
    cache_creation.ephemeral_5m_input_tokens 5-minute TTL write, 1.25x
    cache_creation.ephemeral_1h_input_tokens 1-hour TTL write,   2.00x
    cache_read_input_tokens                  cache read,         0.10x
    output_tokens                            output,             5.00x

The weight is their PRICE-WEIGHTED sum, in units of one base input token — "input-token
equivalents". Summing the four raw counts instead would bill a cache read at ten times its price,
and cache reads are the dominant term on real transcripts (measured: median 257,333 tokens on a
usage-bearing line, cache-read dominated), so the naive sum is not a rounding error — it is
mostly wrong.

**Why ratios and not a price table.** The four multipliers above are ratios to whatever the
model's own base input price is, and across the current Claude family they are IDENTICAL: output
is 5x input on Opus 5 ($5/$25), Sonnet 5 ($3/$15), Haiku 4.5 ($1/$5) and Fable 5 ($10/$50);
cache reads are 0.1x and cache writes 1.25x/2x on all of them. So one dimensionless number per
turn needs no price list, never goes stale when prices change, and — the part that matters —
makes no assumption about which model ran. It is NOT a dollar figure, deliberately: a dollar
figure would require a per-model price table that this package has no business carrying and that
would silently rot. To get dollars, multiply by the base input price of the model on the turn
(the `model` ref level records it).

**`service_tier`.** Present on every usage object (29,972 of 29,990 usage-bearing lines of the
frozen corpus read `standard`, the rest absent). `batch` is a flat 50% discount on every count,
which is a ratio like the others, so it is applied. `priority` is NOT: its premium is not a
uniform ratio across models, and inventing one would produce exactly the number-whose-meaning-is-
unclear this module exists to avoid. It is left at 1.0 and this sentence is the record of that.

## What "diff magnitude" means

`Edit` carries `old_string`/`new_string`, `Write` carries `content`, `NotebookEdit` carries
`new_source`, `MultiEdit` carries a list of edits. The magnitude of one edit event is
`max(len(old), len(new))` in BYTES — the byte extent of the file region the model handled.

Two rejected alternatives, both for measured reasons:

  `len(new_string)` alone   reads ZERO for a pure deletion. Deleting 2 KB is not a no-op, and a
                            magnitude that cannot see it would be blind to exactly the class of
                            edit that most needs distinguishing from a typo fix.
  `len(old) + len(new)`     double-counts an anchor. An `Edit`'s `old_string` is a locator — a
                            few lines of surrounding context used to find the site — so adding
                            it to the replacement inflates every small edit by its context.

`max` is the extent of the site. A typo fix in a one-line anchor is ~16 bytes; authoring a file
is thousands. That is the separation the COUNT of edits cannot make, and `edit >= 5` being a
useless predictor is already on the record.

`replace_all` is not multiplied through: the number of occurrences is not in the transcript, and
guessing it would be a fabricated magnitude.
"""

import collections

# Price ratios, in units of the model's own base input-token price. Uniform across the current
# Claude family — see the module docstring for why that is what makes this table safe to hold.
CACHE_WRITE_5M = 1.25
CACHE_WRITE_1H = 2.00
CACHE_READ = 0.10
OUTPUT = 5.00

# `service_tier` multipliers. Only `batch` is a uniform ratio; everything else stays at 1.0 (see
# the docstring). A tier this map has never heard of is 1.0 rather than an error: the weight of a
# turn must not become an exception because a provider added a tier name.
TIER = {"batch": 0.5}

# The magnitude kinds this module produces, and therefore the `level` values the `mag` rows carry
# and the `kind` values the store's `turn_magnitude` table holds. Named here so producer and
# consumer share one definition rather than two matching string literals.
#
# `tokens` and `request_tokens` are THE SAME NUMBER recorded under two different rules, and the
# distinction is load-bearing rather than redundant. One API request is written to the transcript
# as SEVERAL assistant lines, each repeating the request's `usage`:
#
#   TOKENS          the request's cost, on EVERY line of the request. This is the ROLLUP WEIGHT:
#                   the weight of a reference event is the cost of the request that produced it,
#                   and every line of a request cost the same request. Measured on the frozen
#                   corpus: 10,827 of 15,066 `tool_use` blocks (72%) sit on a line that is NOT
#                   the first of its request, so recording the weight once per request would
#                   leave nearly three quarters of all tool-call evidence weightless — dropped
#                   from a weighted rollup entirely. It also has to be per-line for the weighted
#                   rollup to REDUCE to the event-weighted one when every request costs the same,
#                   which is what makes the two comparable at all.
#   REQUEST_TOKENS  the request's cost, ONCE per `requestId`. This is the SPEND SERIES: it is
#                   what sums to what a window actually cost. `tokens` does not — summing it
#                   multiplies a request by its line count (median 2, up to 12 measured) — and a
#                   number that looks like a spend total while over-counting by 2x is exactly the
#                   plausible-wrong-number failure this codebase keeps paying for. Hence two
#                   names, one per question, instead of one number and a caveat.
TOKENS = "tokens"
REQUEST_TOKENS = "request_tokens"
EDIT_BYTES = "edit_bytes"
# The COST kinds. `Store.has_magnitudes` is scoped to exactly this tuple, because it answers
# "was anything costed here", and the capture kinds below are not costs.
KINDS = (TOKENS, REQUEST_TOKENS, EDIT_BYTES)

# The CAPTURE kinds: written only under `KELD_CAPTURE=1`, and never a cost. They ride
# `turn_magnitude` rather than a table of their own because that table's `kind` is a DIMENSION
# -- a new magnitude is data, not DDL (see the table's own comment in store.py).
#
# `say_*` are per-role CHARACTER COUNTS of message text and `tok_*` is the raw token split. Both
# are computed by `levels.events_for_turns` already and were discarded on the way in. The token
# split is NOT a second spelling of `TOKENS`: that one is price-weighted and answers what a turn
# COST, while `tok_in_cached / (tok_in_cached + tok_in_fresh)` answers how much of the context
# was reused, which no cost figure expresses.
#
# `tool_errors` / `tool_result_chars` come from `analysis/capture.py`, not from
# `events_for_turns`, because a `tool_result` line is filtered out before that function sees it.
#
# ⚠️ THESE STRINGS ARE NOT WHAT THE STORE WRITES -- they are what it MUST write. The stored kind
# is built by `store._aggregate_mag` as `f"{row kind}_{row level}"` out of `levels.py`'s own
# strings, so renaming a level there would silently write a kind no constant here names and
# leave a downstream feature column reading all-zero. `test_magnitude.py` pins the derived set
# against this tuple over real `events_for_turns` output, which is the same assertion this
# module already carries for `KINDS`.
#
# ⚠️ `SAY_THINK` IS A KIND NOTHING WRITES IN PRACTICE, DELIBERATELY. Thinking-block LENGTH is not
# in this data -- every block a platform writes carries a signature and an empty `thinking`
# string (measured 9,148 in `text.think_blocks`, re-measured 7,648 with 0 of nonzero length) --
# and `_aggregate_mag` drops zeros, so the row is emitted and never stored. It is kept because
# the drop is the only thing suppressing it: a producer that ever writes real thinking text
# populates it with no code change. `SAY_THINK_BLOCKS` is the signal that IS available: the
# COUNT of thinking blocks on the turn, i.e. whether it thought at all.
SAY_USER = "say_user"
SAY_USER_ECHO = "say_user_echo"
SAY_ASST = "say_asst"
SAY_THINK = "say_asst_think"
SAY_THINK_BLOCKS = "say_asst_think_blocks"
TOK_OUT = "tok_out"
TOK_IN_FRESH = "tok_in_fresh"
TOK_IN_CACHED = "tok_in_cached"
TOOL_ERRORS = "tool_errors"
TOOL_RESULT_CHARS = "tool_result_chars"
CAPTURE_KINDS = (SAY_USER, SAY_USER_ECHO, SAY_ASST, SAY_THINK, SAY_THINK_BLOCKS,
                 TOK_OUT, TOK_IN_FRESH, TOK_IN_CACHED,
                 TOOL_ERRORS, TOOL_RESULT_CHARS)

# tool name -> (old-side key, new-side key). A tool absent from this map has no diff magnitude,
# which is the correct answer for `Read`/`Bash`/`Grep` and for a tool that does not exist.
# `Write` and `NotebookEdit` have no old side: the payload IS the new content.
_EDIT_KEYS = {
    "Edit": ("old_string", "new_string"),
    "Write": (None, "content"),
    "NotebookEdit": (None, "new_source"),
}
# `MultiEdit` is a list of `Edit`-shaped entries under one tool call. Its magnitude is the sum of
# its entries', because one call really did handle all of them.
_MULTI = "MultiEdit"


def _count(v):
    """A token count as a number, or 0. A transcript is data from another process: a missing,
    null, or non-numeric count must read as nothing rather than take the parse down."""
    if isinstance(v, bool) or not isinstance(v, (int, float)):
        return 0
    return v


def _nbytes(v):
    """The UTF-8 byte length of a payload value, or 0 — and the ONLY place in this package that
    touches an edit payload. Bytes, not runes, so a non-ASCII edit is not undercounted."""
    return len(v.encode("utf-8", "replace")) if isinstance(v, str) else 0


def token_weight(usage):
    """`message.usage` -> price-weighted token count, in input-token equivalents.

    See the module docstring for the definition and for why it is a ratio rather than a dollar
    figure. Returns 0.0 for anything that is not a usage object, because a turn without usage
    (every user turn, and an assistant turn mid-stream) genuinely incurred nothing of its own.
    """
    if not isinstance(usage, dict):
        return 0.0
    fresh = _count(usage.get("input_tokens"))
    read = _count(usage.get("cache_read_input_tokens"))
    out = _count(usage.get("output_tokens"))
    # The sub-object is the authority when present, because it is the only thing that separates
    # the 1.25x TTL from the 2x one; the flat `cache_creation_input_tokens` is their sum and
    # cannot. When only the flat field exists (the committed fixture corpus, and any older
    # producer) it is billed at the 5-minute rate — the overwhelmingly common TTL — rather than
    # dropped, since dropping it would understate a real cost.
    cc = usage.get("cache_creation")
    if isinstance(cc, dict):
        w5 = _count(cc.get("ephemeral_5m_input_tokens"))
        w1 = _count(cc.get("ephemeral_1h_input_tokens"))
    else:
        w5, w1 = _count(usage.get("cache_creation_input_tokens")), 0
    w = (fresh
         + CACHE_WRITE_5M * w5
         + CACHE_WRITE_1H * w1
         + CACHE_READ * read
         + OUTPUT * out)
    return float(w) * TIER.get(usage.get("service_tier"), 1.0)


def edit_bytes(name, inp):
    """One `tool_use` block -> the byte extent of the edit it performed, or 0.

    RETURNS AN INT. That is the privacy contract: this is the only function in the package that
    reads `old_string`/`new_string`/`content`/`new_source`, and a length is the only thing it can
    hand back. There is no variant that returns the text, and `levels.py` calls nothing else on
    those keys — `app/test_magnitude.py` asserts both, the second by reading `levels.py` itself.
    """
    if not isinstance(inp, dict):
        return 0
    if name == _MULTI:
        edits = inp.get("edits")
        if not isinstance(edits, list):
            return 0
        return sum(edit_bytes("Edit", e) for e in edits)
    keys = _EDIT_KEYS.get(name)
    if keys is None:
        return 0
    old_key, new_key = keys
    old = _nbytes(inp.get(old_key)) if old_key else 0
    return max(old, _nbytes(inp.get(new_key)))


# --- the WINDOW rollup of the diff magnitude -------------------------------------------------
#
# `edit_bytes` above is per turn; a window wants the total and, inseparably, the COUNT it was
# spread over. Both publish, because the study's finding is precisely that the count cannot
# substitute for the sum and the sum cannot be read without the count: held at a FIXED edit
# count, per-window byte totals span 22x-87x p10->p90, so windows indistinguishable under
# `edit >= 5` differ by two orders of magnitude in bytes authored.
#
# WHAT GATES IT. A count, never the sum -- and that direction is the whole of the token-weight
# study's lesson. `window.MIN_EVIDENCE` is a COUNT threshold derived from a question about a
# RATIO ("could this unanimity have come from a coin"); compared against a byte sum it is
# vacuous, because a sum in the thousands clears a floor of 5 unconditionally. That is not a
# harmless no-op: it deletes the floor while leaving it visible in the code, which is exactly
# how the token-weight study got apparent +187/+123 attributions that collapsed to ~0 once the
# gate was count-derived.
#
# So no significance floor is applied here AT ALL, and that is the considered answer rather than
# an omission. A sum is a total, not an estimate from a sample: one 22 KB `Write` really did
# author 22 KB, and abstaining on it because 1 < 5 would discard a fact in the name of a test
# that does not apply to it. What the count buys instead is READABILITY -- it is published
# beside the sum so that one 22 KB authoring is never confused with fifty 400 B fixes -- and the
# one thing it does gate is the difference between `attributed` and `absent`.
#
# `recorded` is that gate: did ANY turn in the window have a magnitude recorded, of any kind?
#   recorded  -- the window's turns were costed, and no `edit_bytes` among them means the work
#                genuinely edited nothing. A sum of no terms is unambiguous, so this publishes
#                0, not an abstention. (Contrast `latency.tempo`, where 0/0 is NOT 0.0.)
#   not       -- nothing was costed, so there is nothing to sum. Either the window holds no
#                assistant turn at all, or the series predates magnitudes entirely: a v5 store
#                upgraded in place has none until its next ingest (see `store.SCHEMA_VERSION`'s
#                5 -> 6 note). Publishing `0` for that would state that nothing was authored on
#                the strength of never having looked.
#
# There is deliberately no READING here, unlike `latency.tempo`. A tempo reading flips at a
# floor the package already holds (0.50, `window.dominant`'s); a byte sum has no measured cut
# point anywhere -- the study reports the spread and no boundary inside it -- so `small`/`large`
# would be a fabricated vocabulary. The block publishes labelled numbers and states nothing it
# did not measure.
AUTHORED_STATUSES = ("attributed", "absent")

Authored = collections.namedtuple("Authored", "nbytes turns status")


def authored(values, recorded=False):
    """A window's per-turn `edit_bytes` values -> `Authored(nbytes, turns, status)`.

    `values` is one number per edit-bearing TURN (the store's `turn_magnitudes` already groups
    that way; the parse path must group by timestamp to match). Zeros are dropped rather than
    counted: `_aggregate_mag` never stores a zero, so keeping them here would make the two paths
    disagree on `turns` while agreeing on every byte.

    `recorded` says whether the window carries a COST magnitude (`Store.has_magnitudes` is scoped
    to `KINDS`, not `CAPTURE_KINDS`) -- see the block comment above for why that, and not the byte
    sum, decides between a truthful 0 and an abstention.

    Returns an `int` byte count. That is the same privacy contract `edit_bytes` states: a length
    is the only thing this module can hand back, and there is no variant that returns the text.
    """
    vals = [float(v) for v in values if v]
    if not vals and not recorded:
        return Authored(None, 0, AUTHORED_STATUSES[1])
    return Authored(int(sum(vals)), len(vals), AUTHORED_STATUSES[0])
