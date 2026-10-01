"""The two verb splitters, and the bar they are judged against.

⚠️ BOTH DISCRIMINATORS ARE STRUCTURAL -- tool identity, tool arguments, request
order. NEVER TEXTUAL. Six routes to inferring meaning from prose have been
measured and refused on this question (GLiNER2 on prompts 12.7%; GLiNER2 on paths
one domain only; keyword lists 17%; agent self-report, which never reaches the
model; document-type recognition, parked; conversation-domain on WildChat, every
arm below its bar). A splitter that resolves to "read the text and decide" is the
seventh attempt at a refuted thing. `test_text_is_not_read` pins this.

Committed BEFORE scoring. The author has not seen the labels.
"""
import json, sys

BAR_PRECISION = 0.80
BAR_MARGIN = 0.20          # over the majority-class baseline, per parent class

# Every name here is in reqclass.RETRIEVE_TOOLS; the three added beyond the
# brief (read_console_messages, notion-get-users, notion-get-teams) are
# enumerations of what exists, the same act as LS.
RETRIEVE_BROAD = {"LS", "Glob", "find", "WebSearch", "ListAgents", "CronList",
                  "notion-search", "notion-ai-search", "ToolSearch",
                  "read_console_messages", "notion-get-users", "notion-get-teams"}
RETRIEVE_POINTED = {"Grep", "notion-query-data-sources", "list_network_requests"}


def split_synthesize(row):
    """`text.summarize` vs `research`, from SEQUENCE alone.

    A synthesize request has NO tool calls by construction -- route_class reaches
    it only when `names` is empty -- so its own request carries no tool evidence.
    What it has is position: a synthesis that FOLLOWS retrieval is reporting on
    what was gathered (`research`); one that follows none condensed what was
    already in context (`text.summarize`).
    """
    prior = row.get("prior") or []
    if not prior:
        return None                       # nothing to reason from; abstain
    gathered = sum(1 for c in prior if c == "retrieve")
    if gathered >= 2:
        return "research"
    if gathered == 0:
        return "text.summarize"
    return None                           # exactly one: genuinely ambiguous


def split_retrieve(row):
    """`extract` vs `research`, from TOOL IDENTITY AND ARGUMENTS.

    Pulling a known thing out is `extract`: a Grep for a pattern, or a Read
    bounded by offset/limit. Reading to understand is `research`: a whole-file
    Read, an LS, a find. A tool in neither set (e.g. Bash) is skipped, and a row
    with no covered tool abstains.
    """
    for name, inp in (row.get("tools") or []):
        bare = name.split("__")[-1]
        if bare in RETRIEVE_POINTED:
            return "extract"
        if bare in RETRIEVE_BROAD:
            return "research"
        if bare in ("Read", "NotebookRead"):
            inp = inp or {}
            return "extract" if (inp.get("offset") or inp.get("limit")) else "research"
        if bare in ("WebFetch", "notion-fetch", "get_page_text", "read_page"):
            return "research"
    return None


def predict(row):
    return (split_synthesize(row) if row["cls"] == "synthesize"
            else split_retrieve(row))


FRAME = "/tmp/claude-1000/verbsplit/frame.ndjson"


def _selftest():
    assert split_retrieve({"tools": [["Grep", {"pattern": "x"}]]}) == "extract"
    assert split_retrieve({"tools": [["Read", {"file_path": "a.go"}]]}) == "research"
    assert split_retrieve({"tools": [["Read", {"file_path": "a.go", "offset": 10,
                                               "limit": 20}]]}) == "extract"
    assert split_retrieve({"tools": [["LS", {}]]}) == "research"
    assert split_retrieve({"tools": []}) is None
    assert split_synthesize({"prior": ["retrieve", "retrieve"]}) == "research"
    assert split_synthesize({"prior": ["author_code", "verify"]}) == "text.summarize"
    assert split_synthesize({"prior": ["retrieve", "verify"]}) is None
    assert split_synthesize({"prior": []}) is None
    test_text_is_not_read()
    print("ok selftest")


def test_text_is_not_read():
    """REVIEW FOCUS 5: the structural constraint, enforced rather than asserted.

    Replacing a row's prose with unrelated prose of the same length must not
    change any prediction. If it does, a splitter is reading text and the study
    has become the seventh attempt at a refuted approach.
    """
    rows = [json.loads(l) for l in open(FRAME)]
    assert rows, "empty frame: the test would pass vacuously"
    for r in rows:
        before = predict(r)
        scrambled = dict(r, text="lorem ipsum dolor sit amet " * (len(r["text"]) // 27 + 1))
        assert predict(scrambled) == before, f"{r['id']}: prediction moved with text"
    print(f"ok text_is_not_read ({len(rows)} rows)")


if __name__ == "__main__":
    _selftest()
    if len(sys.argv) > 1 and sys.argv[1] == "predict":
        rows = [json.loads(l) for l in open(FRAME)]
        preds = {r["id"]: predict(r) for r in rows}
        json.dump(preds, open("/tmp/claude-1000/verbsplit/preds.json", "w"))
        import collections
        print(collections.Counter((r["cls"], preds[r["id"]]) for r in rows))
