"""SYNTHETIC BUSINESS-DOMAIN SESSIONS, run through the REAL ingest -> analyze path.

⚠️ WHY SYNTHESIS IS LEGITIMATE HERE WHEN IT WAS REFUTED ELSEWHERE IN THIS REPO. Two earlier
attempts to synthesize evaluation data failed and are on the record: the `domain`/context work
could not be validated because two generators disagreed on 2 of 3 sessions, and the attribution
notes say the way forward is labeled real blocks, "not another synthetic sweep". Both were
manufacturing GROUND TRUTH FOR AN INFERENCE -- asking a generator to decide what the right
answer was, which is exactly the thing under test.

This is the opposite shape. `systems.py` is a DECLARATIVE table: that Workday is an HR system
is true by construction, not something a generator gets to vote on. What is genuinely uncertain
is whether a realistic tool call from a sales or finance workflow REACHES that table at all --
whether the MCP name shape, the host shape and the CLI shape survive `levels.py`, the store and
the window rollup. That is plumbing, and synthesis tests plumbing honestly.

⚠️ SO READ THE RESULT NARROWLY. A passing run says: a session that works this way produces
these categories end to end. It does NOT say how often such sessions occur, how a real sales
engineer phrases things, or that the table is complete. Neither corpus available to this repo
contains Jira, Salesforce, Workday or NetSuite at all -- both are engineering work, and one
holds zero MCP calls in 499 sessions -- which is precisely why this file exists and precisely
why it cannot stand in for a real corpus from such an org.
"""
import json, os, sys, tempfile
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis import systems
from app.analysis.analyze import analyze_window
from app.analysis.ingest import ingest_file
from app.analysis.store import open_store

BASE = datetime(2026, 9, 29, 9, 0, 0, tzinfo=timezone.utc)
CWD = "/home/dev/work"
PROJDIR = CWD.replace("/", "-")
# A real Claude connector presents as a uuid, not a readable name -- the brand is only in the
# tool. Using a uuid here keeps the fixture honest about that.
SRV = "c78d9895-d0ef-43c2-b7c3-db6cfc34856e"


def _ts(off):
    return (BASE + timedelta(seconds=off)).isoformat().replace("+00:00", "Z")


def _user(off, i, text):
    return {"type": "user", "uuid": f"u{i}", "promptId": f"p{i}", "timestamp": _ts(off),
            "cwd": CWD, "gitBranch": "main",
            "message": {"role": "user", "content": text}}


def _calls(off, i, tools):
    """One assistant turn issuing `tools` -- a list of (name, input) in the real block shape."""
    return {"type": "assistant", "uuid": f"a{i}", "timestamp": _ts(off), "cwd": CWD,
            "gitBranch": "main", "requestId": f"r{i}",
            "message": {"role": "assistant", "model": "claude-opus-5",
                        "content": [{"type": "tool_use", "id": f"t{i}_{j}",
                                     "name": n, "input": inp}
                                    for j, (n, inp) in enumerate(tools)],
                        "usage": {"input_tokens": 100, "output_tokens": 40,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


def mcp(tool, **inp):
    return (f"mcp__{SRV}__{tool}", inp)


def bash(cmd):
    return ("Bash", {"command": cmd})


def fetch(url):
    return ("WebFetch", {"url": url})


# Each domain: the tool calls a realistic session in that function would issue, and the
# categories that MUST appear. Written as the work, not as the answer -- the expectation is
# derived from the table, and a table edit that breaks one of these fails here.
DOMAINS = {
    "sales": ([mcp("salesforce-query-records", soql="SELECT Id FROM Opportunity"),
               mcp("salesforce-update-record", id="006xx"),
               mcp("gong-list-calls", account="Acme"),
               fetch("https://acme.my.salesforce.com/lightning/o/Opportunity/list")],
              {"crm_sales"}),
    "people_ops": ([mcp("workday-get-worker", worker_id="W-1"),
                    mcp("greenhouse-list-candidates", job="Backend Engineer"),
                    fetch("https://acme.myworkday.com/acme/d/task/1")],
                   {"hr_people"}),
    "finance": ([mcp("netsuite-query-transactions", period="2026-09"),
                 mcp("quickbooks-create-invoice", customer="Acme"),
                 bash("stripe invoices list --limit 20")],
                {"finance_billing"}),
    "support": ([mcp("zendesk-list-tickets", view="urgent"),
                 mcp("intercom-reply-conversation", id="c-9"),
                 fetch("https://acme.zendesk.com/agent/tickets/4821")],
                {"support"}),
    "product_mgmt": ([mcp("jira-create-issue", project="PLAT", summary="Rate limit 429s"),
                      mcp("jira-transition-issue", key="PLAT-77"),
                      fetch("https://acme.atlassian.net/browse/PLAT-77")],
                     {"issue_tracking"}),
    "knowledge": ([mcp("notion-fetch", page="Runbook"),
                   mcp("notion-update-page", page="Runbook"),
                   mcp("confluence-get-page", id="55")],
                  {"knowledge_base"}),
    "comms": ([mcp("slack-post-message", channel="#incidents"),
               mcp("slack-list-channels")],
              {"communication"}),
    "design": ([mcp("figma-get-file", key="abc"),
                fetch("https://www.figma.com/file/abc/Checkout")],
               {"design"}),
    "data_analytics": ([mcp("snowflake-run-query", sql="select 1"),
                        mcp("looker-run-look", look_id="42"),
                        bash("snowsql -q 'select count(*) from orders'")],
                       {"data_platform", "analytics_bi"}),
    "legal": ([mcp("docusign-send-envelope", template="MSA"),
               fetch("https://demo.docusign.net/Signing/1")],
              {"legal_contracts"}),
    "marketing": ([mcp("marketo-get-campaign", id="7"),
                   mcp("mailchimp-create-campaign", list_id="l1")],
                  {"marketing"}),
    "it_security": ([mcp("okta-list-users", filter="active"),
                     bash("snyk test --all-projects")],
                    {"security_iam"}),
}


def _write(tmp, name, tools):
    d = os.path.join(tmp, "projects", PROJDIR)
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, f"sess-{name}.jsonl")
    lines = [_user(0, 0, f"do the {name} task"), _calls(10, 0, tools),
             _user(20, 1, "thanks"), _calls(30, 1, [("Read", {"file_path": "/x/notes.md"})]),
             _user(40, 2, "target")]
    with open(p, "w") as fh:
        for o in lines:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return p


def _weighted(tmp, name, tools, key):
    """Inventory rows as {value: n}, keeping the COUNT -- the token dimensions ride `n`."""
    p = _write(tmp, name, tools)
    st = open_store(os.path.join(tmp, "state", "refseries.db"))
    ingest_file(st, p)
    out = analyze_window(p, "p2", span_minutes=60, store=st, nlp=None)
    inv = (out.get("inventory") or {}).get(key) or []
    return {(r["value"] if isinstance(r, dict) else r[0]):
            (r.get("n") if isinstance(r, dict) else r[1]) for r in inv}


def _inventory(tmp, name, tools, key):
    p = _write(tmp, name, tools)
    st = open_store(os.path.join(tmp, "state", "refseries.db"))
    ingest_file(st, p)
    out = analyze_window(p, "p2", span_minutes=60, store=st, nlp=None)
    inv = (out.get("inventory") or {}).get(key) or []
    return {row["value"] if isinstance(row, dict) else row[0] for row in inv}


def _categories(tmp, name, tools):
    return _inventory(tmp, name, tools, "system_categories")


def test_each_business_domain_reaches_its_category_end_to_end():
    """The plumbing claim, one domain at a time: a realistic session in that function produces
    its category through ingest, the store and the window rollup -- not through a direct call
    to the lookup."""
    with tempfile.TemporaryDirectory() as tmp:
        for name, (tools, want) in DOMAINS.items():
            got = _categories(os.path.join(tmp, name), name, tools)
            missing = want - set(got)
            assert not missing, f"{name}: expected {sorted(want)}, published {sorted(got)}"


def test_no_domain_publishes_a_category_from_another_domain():
    """⚠️ A LOOKUP THAT OVER-FIRES IS WORSE THAN ONE THAT MISSES. A sales session that also
    reports `hr_people` would put a category on a block nobody worked in, and unlike a miss it
    is invisible to whoever reads it. `unrecognized` is exempt: it is the honest bucket."""
    others = set()
    for _, want in DOMAINS.values():
        others |= want
    with tempfile.TemporaryDirectory() as tmp:
        for name, (tools, want) in DOMAINS.items():
            got = set(_categories(os.path.join(tmp, name), name, tools)) - {"unrecognized"}
            stray = got - want
            assert not stray, f"{name}: also published {sorted(stray)}, expected only {sorted(want)}"


# What each domain's session should say was DONE, not merely which system was touched.
# Derived from the tool names above; a table or normaliser edit that breaks one fails here.
ACTIONS_WANTED = {
    "sales": {"crm_sales:search", "crm_sales:update", "crm_sales:read"},
    "people_ops": {"hr_people:read"},
    "finance": {"finance_billing:search", "finance_billing:create"},
    "support": {"support:read", "support:send"},
    "product_mgmt": {"issue_tracking:create", "issue_tracking:update"},
    "knowledge": {"knowledge_base:read", "knowledge_base:update"},
    "comms": {"communication:send", "communication:read"},
    "design": {"design:read"},
    "data_analytics": {"data_platform:run", "analytics_bi:run"},
    "legal": {"legal_contracts:send"},
    "marketing": {"marketing:read", "marketing:create"},
    "it_security": {"security_iam:read"},
}


def test_the_action_taken_inside_the_system_publishes_too():
    """⚠️ THE DISTINCTION THIS EXISTS FOR: `crm_sales` says a CRM was touched, and a session
    that only ever READ one is not doing the same work as a session that UPDATED records.
    Asserted end to end, so a verb that stops surviving the store or the rollup fails here
    rather than quietly reducing this dimension to its sibling."""
    with tempfile.TemporaryDirectory() as tmp:
        for name, want in ACTIONS_WANTED.items():
            tools = DOMAINS[name][0]
            got = _inventory(os.path.join(tmp, name), name, tools, "system_actions")
            missing = want - got
            assert not missing, f"{name}: expected {sorted(want)}, published {sorted(got)}"


def test_an_unknown_verb_publishes_the_category_and_no_action():
    """A tool whose verb the normaliser does not know must still say WHICH system was used.
    Dropping the whole reference would hide the system; guessing a verb would be a false
    statement about what happened inside somebody's system of record."""
    with tempfile.TemporaryDirectory() as tmp:
        tools = [mcp("notion-wibble", page="x")]
        cats = _inventory(tmp, "odd", tools, "system_categories")
        acts = _inventory(tmp, "odd", tools, "system_actions")
        assert "knowledge_base" in cats, cats
        assert not acts, f"guessed an action from an unknown verb: {acts}"


# The named product inside each category -- what a reader actually recognises.
VENDORS_WANTED = {
    "sales": {"crm_sales:salesforce", "crm_sales:gong"},
    "people_ops": {"hr_people:workday", "hr_people:greenhouse"},
    "finance": {"finance_billing:netsuite", "finance_billing:quickbooks",
                "finance_billing:stripe"},
    "support": {"support:zendesk", "support:intercom"},
    "product_mgmt": {"issue_tracking:jira", "issue_tracking:atlassian"},
    "knowledge": {"knowledge_base:notion", "knowledge_base:confluence"},
    "comms": {"communication:slack"},
    "design": {"design:figma"},
    "data_analytics": {"data_platform:snowflake", "analytics_bi:looker"},
    "legal": {"legal_contracts:docusign"},
    "marketing": {"marketing:marketo", "marketing:mailchimp"},
    "it_security": {"security_iam:okta", "security_iam:snyk"},
}


def test_the_named_vendor_inside_each_category_publishes_too():
    """`issue_tracking` says a tracker was used; `issue_tracking:jira` says which one. Asserted
    end to end so a vendor that stops surviving the store or the rollup fails here."""
    with tempfile.TemporaryDirectory() as tmp:
        for name, want in VENDORS_WANTED.items():
            tools = DOMAINS[name][0]
            got = _inventory(os.path.join(tmp, name), name, tools, "system_vendors")
            missing = want - got
            assert not missing, f"{name}: expected {sorted(want)}, published {sorted(got)}"


def test_a_customers_own_subdomain_never_reaches_the_wire():
    """⚠️ THE PRIVACY CHECK, END TO END RATHER THAN AT THE LOOKUP. Enterprise SaaS puts the
    CUSTOMER'S NAME in the first label -- `acme.atlassian.net`. Nothing published may contain
    it: the vendor half comes from the table, never from the host string."""
    with tempfile.TemporaryDirectory() as tmp:
        tools = [fetch("https://supersecretcustomer.atlassian.net/browse/X-1"),
                 fetch("https://supersecretcustomer.myworkday.com/d/task/1")]
        for key in ("system_vendors", "system_categories", "system_actions"):
            for value in _inventory(tmp, "sub", tools, key):
                assert "supersecretcustomer" not in value, (key, value)


def test_tokens_are_charged_once_per_REQUEST_not_once_per_reference():
    """⚠️ THE SUBTLE ONE. A category is emitted per tool REFERENCE, but output tokens belong
    to the inference REQUEST -- one assistant turn has one output budget however many tools it
    calls. A turn calling `notion-fetch` three times must charge its output ONCE.

    Charged per reference, the figure would rank systems by how CHATTY their API is rather
    than by how much work went through them -- and the sibling `system_categories` count
    already reports call frequency, so the token dimension would be reporting it a second
    time in a more confusing unit.

    _calls() writes output_tokens=40 per turn, and only one of this session's two turns
    touches a system."""
    with tempfile.TemporaryDirectory() as tmp:
        three = [mcp("notion-fetch", page="a"), mcp("notion-fetch", page="b"),
                 mcp("notion-update-page", page="c")]
        got = _weighted(tmp, "thrice", three, "system_category_tokens")
        assert got.get("knowledge_base") == 40, (
            f"expected the turn's 40 output tokens charged once, got {got}")


def test_a_call_touching_two_systems_counts_fully_toward_both():
    """Double-attribution, asserted rather than assumed. Splitting would invent a ratio with
    nothing behind it; the over-count is bounded at the 2.7% of real requests that touch more
    than one system. A consumer must never sum these rows, which is why both are equal to the
    turn's whole output rather than to halves of it."""
    with tempfile.TemporaryDirectory() as tmp:
        both = [mcp("jira-create-issue", p="X"), mcp("slack-post-message", ch="#x")]
        got = _weighted(tmp, "two", both, "system_category_tokens")
        assert got.get("issue_tracking") == 40, got
        assert got.get("communication") == 40, got


def test_an_unrecognized_system_contributes_no_token_row():
    """Matching `system_vendors`: a system we cannot name says so once, in
    `system_categories`, and does not reappear carrying a token weight."""
    with tempfile.TemporaryDirectory() as tmp:
        tools = [fetch("https://some-unknown-vendor.example.net/x")]
        assert not _weighted(tmp, "unk", tools, "system_category_tokens")
def test_browser_automation_verbs_resolve_to_an_action():
    """⚠️ MEASURED GAP, not a hypothetical. On two real corpora 167 of 765 MCP calls resolved
    NO action, and 160 of those were Claude's own browser tools. Without these the browser lane
    publishes a category and no verb, so an afternoon spent driving a browser is
    indistinguishable from one unrecognised call."""
    assert systems.action_for_tool("computer") == "run"
    assert systems.action_for_tool("javascript_tool") == "run"
    assert systems.action_for_tool("navigate") == "read"
    assert systems.action_for_tool("resize_window") == "update"
    assert systems.action_for_tool("mark_chapter") == "update"


def test_generic_tokens_are_deliberately_absent_from_the_verb_table():
    """`_VERB` matches ANY token of ANY MCP tool name, so a generic word silently re-labels
    unrelated tools in a system of record — the failure `action_for_tool`'s docstring refuses.
    `browser_batch` resolving to nothing is the intended outcome, not an oversight.

    ⚠️ If a future change makes this pass by adding `batch`/`tool`/`browser`, it has widened the
    table in exactly the way that produces false statements about someone's work."""
    assert systems.action_for_tool("browser_batch") is None
    assert systems.action_for_tool("tool") is None
    assert systems.action_for_tool("some_batch_operation") is None


def test_adding_browser_verbs_did_not_move_any_existing_resolution():
    """The regression guard for the change above: every verb that resolved before must resolve
    to the same action now. A new token that shadows an old one is the real risk here."""
    for tool, expected in (("notion-fetch", "read"), ("notion-update-page", "update"),
                           ("notion-search", "search"), ("notion-create-pages", "create"),
                           ("preview_start", "run"), ("tabs_close", "update")):
        assert systems.action_for_tool(tool) == expected, tool


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")

