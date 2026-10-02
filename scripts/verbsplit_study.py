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




# --------------------------------------------------------------------------
# The scorer. Appended AFTER the splitters were committed (2ad435a8) and after
# the labels were committed (a32c2017). The splitters above are NOT edited here:
# this is the first context in the study permitted to read the labels, and an
# agent that can see the answers editing the classifier destroys the ordering
# the whole study rests on.
# --------------------------------------------------------------------------

LABELS = "scripts/verbsplit-labels.txt"
PREDS = "/tmp/claude-1000/verbsplit/preds.json"

# Scored per (cls, corpus) and NEVER pooled into one headline: the cells differ
# in size by more than an order of magnitude and in label variance from 2 to 0,
# so a pooled number would be corpus_b/retrieve wearing four other cells' names.
CELLS = (("retrieve", "corpus_b"), ("retrieve", "corpus_a"),
         ("synthesize", "corpus_b"), ("synthesize", "corpus_a"),
         ("synthesize", "wildchat"))


def _load():
    lab = {}
    for line in open(LABELS):
        line = line.strip()
        if line and not line.startswith("#"):
            i, v = line.split()
            lab[i] = v
    rows = {}
    for line in open(FRAME):
        r = json.loads(line)
        rows[r["id"]] = r
    preds = json.load(open(PREDS))
    assert len(lab) == len(rows) == len(preds) == 200, (len(lab), len(rows), len(preds))
    return lab, rows, preds


def _abstention_reason(row):
    """Why the splitter answered None. Two kinds, and they mean different things.

    A COVERAGE gap (no tool in either partition -- in practice Bash, every time)
    is a defect of the splitter: it says nothing about the request. A DESIGNED
    abstention (an empty prior, or exactly one prior retrieval) is the splitter
    declining on evidence it judged insufficient. Folding them together would
    read a coverage hole as epistemic humility.
    """
    if row["cls"] == "retrieve":
        return "coverage:no_partitioned_tool"
    prior = row.get("prior") or []
    if not prior:
        return "designed:empty_prior"
    return "designed:one_prior_retrieval"


def _score():
    import collections
    lab, rows, preds = _load()

    print("=" * 78)
    print("⚠️ `unclear` rows are UNSCOREABLE and are reported separately -- they are")
    print("   neither right nor wrong, and folding them into either would invent a")
    print("   judgement the labeller declined to make.")
    print("⚠️ 138 of 200 rows are `unclear`. That rate is the study's finding, not")
    print("   its noise floor. See docs/notes/2026-10-01-verb-split-results.md.")
    print("⚠️ NO ADEQUATE HOLDOUT EXISTS. WildChat cannot be one (its retrieve cell")
    print("   is empty by construction); corpus_a is exhausted at n=5 and n=8.")
    print("=" * 78)

    for cls, corpus in CELLS:
        ids = [i for i in lab if rows[i]["cls"] == cls and rows[i]["corpus"] == corpus]
        scoreable = [i for i in ids if lab[i] != "unclear"]
        dist = collections.Counter(lab[i] for i in scoreable)
        print(f"\n{cls}/{corpus}  n={len(ids)}  unclear={len(ids)-len(scoreable)}  "
              f"scoreable={len(scoreable)}  labels={dict(dist)}")
        if len(scoreable) < 5:
            print("  DEAD CELL -- fewer than 5 scoreable rows. Not scored.")
            continue
        answered = [i for i in scoreable if preds.get(i) is not None]
        abstained = [i for i in scoreable if preds.get(i) is None]
        reasons = collections.Counter(_abstention_reason(rows[i]) for i in abstained)
        prec = (sum(1 for i in answered if preds[i] == lab[i]) / len(answered)
                if answered else None)
        acc = sum(1 for i in scoreable if preds.get(i) == lab[i]) / len(scoreable)
        base = dist.most_common(1)[0][1] / len(scoreable)
        print(f"  answered={len(answered)}  abstained={len(abstained)} {dict(reasons)}")
        print(f"  precision(answered)={'n/a' if prec is None else f'{100*prec:.1f}%'}"
              f"   accuracy(abstention=wrong)={100*acc:.1f}%")
        if len(dist) < 2:
            print(f"  baseline={100*base:.1f}% -- ⚠️ ZERO LABEL VARIANCE. Every scoreable")
            print("     row carries one label, so the majority baseline is 100% by")
            print("     construction and no margin over it is reachable. A margin")
            print("     printed here would be a meaningless number, so none is.")
        else:
            print(f"  baseline={100*base:.1f}%  margin={100*(acc-base):+.1f} pts")
        if prec is not None and len(dist) >= 2:
            ok_p = prec >= BAR_PRECISION
            ok_m = (acc - base) >= BAR_MARGIN
            print(f"  BAR: precision>={100*BAR_PRECISION:.0f}% {'MET' if ok_p else 'NOT MET'}"
                  f"  AND  margin>=+{100*BAR_MARGIN:.0f} {'MET' if ok_m else 'NOT MET'}"
                  f"  =>  {'PASS' if (ok_p and ok_m) else 'FAIL'}")
        if len(scoreable) < 20:
            print("  ⚠️ UNDERPOWERED -- report the number, claim nothing from it.")

    print("\n" + "=" * 78)
    print(f"BAR (pre-registered, Task 5 commit 2ad435a8): precision >= "
          f"{100*BAR_PRECISION:.0f}% over ANSWERED rows")
    print(f"AND accuracy (abstention counted wrong) >= majority baseline + "
          f"{100*BAR_MARGIN:.0f} points.")
    print("⚠️ A FAILING SPLIT PUBLISHES NOTHING -- there is no unsplit parent verb.")
    print("=" * 78)


if __name__ == "__main__":
    _selftest()
    if len(sys.argv) > 1 and sys.argv[1] == "predict":
        rows = [json.loads(l) for l in open(FRAME)]
        preds = {r["id"]: predict(r) for r in rows}
        json.dump(preds, open("/tmp/claude-1000/verbsplit/preds.json", "w"))
        import collections
        print(collections.Counter((r["cls"], preds[r["id"]]) for r in rows))
    if len(sys.argv) > 1 and sys.argv[1] == "score":
        _score()
