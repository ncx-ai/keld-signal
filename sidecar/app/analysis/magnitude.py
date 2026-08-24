"""Per-turn ECONOMIC magnitudes: what a turn COST, not what it was about.

Every other module in this package answers "what was this turn about" and answers it as a
reference — a level and a value. This one answers a different question with a different shape: a
single number per turn. It is separate for that reason, not for tidiness. Attribution today
divides a window by how many tool calls touched a thing; the thing an attribution product is
actually dividing is spend, and the numbers that measure spend are already sitting in every
transcript line `transcript.turns_in` parses.

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
KINDS = (TOKENS, REQUEST_TOKENS, EDIT_BYTES)

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
