"""The system-category lookup. Every case is either a REAL key measured in a corpus or a
distinction the table would silently get wrong without it."""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.systems import (CATEGORIES, BRAND, CLI_CLIENT, LOCAL_HOSTS, NOT_A_SYSTEM,
                                  category_for_host, category_for_brand, category_for_program,
                                  vendor_for_host, vendor_for_brand, vendor_for_program,
                                  system_vendor)


def test_the_published_vocabulary_is_closed_and_every_entry_maps_into_it():
    """A table entry naming a category the vocabulary does not contain would publish a value no
    consumer can render, and nothing else would notice."""
    for brand, cat in BRAND.items():
        assert cat in CATEGORIES, (brand, cat)
    for prog, (vendor, cat) in CLI_CLIENT.items():
        assert cat in CATEGORIES, (prog, cat)
        assert vendor, prog


def test_a_vendor_spanning_two_categories_is_keyed_by_PRODUCT_not_by_company():
    """⚠️ Atlassian sells an issue tracker AND a wiki. Keyed by company, every Confluence page
    edit would publish `issue_tracking`. The bare company name must therefore be ABSENT, so an
    unknown key falls through to `unrecognized` rather than to a confident wrong answer."""
    assert BRAND["jira"] == "issue_tracking"
    assert BRAND["confluence"] == "knowledge_base"
    assert "atlassian" not in BRAND
    for conglomerate in ("google", "microsoft", "adobe", "amazon", "oracle"):
        assert conglomerate not in BRAND, conglomerate


def test_hosts_whose_brand_is_not_in_the_name_still_resolve():
    """⚠️ THE FAILURE THIS GUARDS IS SILENT UNDER-REPORTING. `company.atlassian.net` contains no
    `jira`, `console.aws.amazon.com` has no registrable `aws` label, and a Salesforce org lives
    at `<org>.my.salesforce.com`. Reading the second-level label alone answers `unrecognized`
    for three of the largest enterprise systems there are, and the level would look thin rather
    than broken."""
    assert category_for_host("acme.atlassian.net") == "issue_tracking"
    assert category_for_host("console.aws.amazon.com") == "cloud_infra"
    assert category_for_host("acme.my.salesforce.com") == "crm_sales"
    assert category_for_host("acme.myworkday.com") == "hr_people"
    assert category_for_host("files.acme.sharepoint.com") == "knowledge_base"


def test_loopback_is_not_an_external_system():
    """Measured on corpus B: 1,165 of 1,316 host mentions (88.5%) are loopback. Counted, this
    level would be mostly an artefact of running a dev server."""
    for h in ("localhost", "127.0.0.1", "0.0.0.0", "host.docker.internal"):
        assert category_for_host(h) is None, h
        assert h in LOCAL_HOSTS


def test_a_spec_or_cdn_host_is_dropped_rather_than_called_unrecognized():
    """⚠️ `unrecognized` is a CLAIM -- that a system was used and could not be named. A .docx
    naming `schemas.openxmlformats.org` in its own XML (26 times in a real corpus) is not a
    system anyone used, so calling it unrecognized overstates coverage in the one direction
    nobody would check. Dropped, like loopback."""
    for h in ("schemas.openxmlformats.org", "www.w3.org", "fonts.gstatic.com",
              "registry.npmjs.org", "pypi.org", "cdnjs.cloudflare.com"):
        assert category_for_host(h) is None, h


def test_an_unknown_REGISTRABLE_host_is_unrecognized_rather_than_dropped():
    """An org's own domain cannot be in any table, and must READ AS UNKNOWN COVERAGE rather
    than as an absence of external systems. That distinction is the whole reason `unrecognized`
    is a published value: a consumer must be able to tell "this org uses systems we cannot
    name" from "this org uses none"."""
    assert category_for_host("keld.co") == "unrecognized"
    assert category_for_host("api-gateway-dev.keld.co") == "unrecognized"
    assert category_for_host("some-vendor-we-never-heard-of.com") == "unrecognized"


def test_a_dotless_host_is_not_an_external_system_at_all():
    """⚠️ THE OPPOSITE CASE, and confusing the two overstates coverage. A host with no
    registrable name is a container service, an /etc/hosts entry or a LAN name -- there is no
    external party involved, so it is None. `dimensions.LOOPBACK` already treats these as
    loopback for the raw `service` level; this keeps the two levels from disagreeing about
    whether the same host was a third party."""
    for h in ("enrich-sidecar", "keld", "db", "redis", "api"):
        assert category_for_host(h) is None, h


def test_git_is_absent_from_the_cli_table_and_gh_is_present():
    """⚠️ THE MOST EXPENSIVE POSSIBLE ENTRY. Bare `git` is a LOCAL version-control program, not
    an external system, and it is the single most frequent command in both corpora (1,148 +
    390 calls). Admitting it would make `code_hosting` the answer for essentially every
    engineering window, which is the same failure as a constant classifier. `gh` genuinely
    talks to GitHub."""
    assert "git" not in CLI_CLIENT
    assert category_for_program("git") is None
    assert category_for_program("gh") == "code_hosting"


def test_general_purpose_technology_is_not_in_the_table():
    """⚠️ THE RULE THAT DECIDES EVERY ENTRY: products and services an org BUYS, never
    technology an engineer RUNS. Getting this wrong does not give a wrong category, it makes
    the level answer the wrong question -- `docker` in the table produced 741 `cloud_infra`
    references in one corpus of which 740 were docker, firing on 15% of sessions to report
    that containers exist.

    These are the exact tokens that were in the table and were removed."""
    for tech in ("postgres", "mysql", "mongodb", "redis", "kafka", "elasticsearch",
                 "clickhouse", "duckdb", "docker", "kubernetes", "terraform", "pulumi",
                 "ansible", "nomad", "consul", "prometheus", "airflow", "dagster",
                 "prefect", "vault"):
        assert tech not in BRAND, tech
        assert tech not in CLI_CLIENT, tech
    for prog in ("kubectl", "helm", "docker", "podman", "terraform", "psql", "mysql",
                 "mongo", "redis-cli"):
        assert category_for_program(prog) is None, prog


def test_a_named_vendor_cli_is_still_a_client():
    """The exclusion above must not take the clients that DO name one vendor and nothing
    else: `aws` is AWS, `gh` is GitHub, `stripe` is Stripe."""
    assert category_for_program("aws") == "cloud_infra"
    assert category_for_program("gcloud") == "cloud_infra"
    assert category_for_program("gh") == "code_hosting"
    assert category_for_program("stripe") == "finance_billing"


def test_an_ordinary_local_command_is_None_not_unrecognized():
    """`ls` is not an uncategorised external system; it is not one at all. Returning
    `unrecognized` for every local command would drown the level in its own noise."""
    for p in ("ls", "cat", "grep", "make", "sed"):
        assert category_for_program(p) is None, p


def test_the_real_brands_measured_in_the_corpora_resolve():
    """The keys actually observed, not invented ones: notion dominates corpus A (532 calls),
    slack and github appear in both."""
    assert category_for_brand("notion") == "knowledge_base"
    assert category_for_brand("slack") == "communication"
    assert category_for_host("app.notion.com") == "knowledge_base"
    assert category_for_host("github.com") == "code_hosting"
    assert category_for_host("raw.githubusercontent.com") == "code_hosting"


def test_the_enterprise_systems_this_level_exists_for_resolve():
    """⚠️ NONE OF THESE APPEAR IN EITHER CORPUS, and that is the point of a declarative table
    rather than a learned one. Both corpora are engineering work; an org running on Jira,
    Salesforce, Workday or NetSuite is exactly the population this level is FOR, and its
    coverage there is asserted from the table, never measured here. These cases are the
    assertion, written down so a table edit that breaks it fails."""
    for brand, cat in (("jira", "issue_tracking"), ("salesforce", "crm_sales"),
                       ("workday", "hr_people"), ("netsuite", "finance_billing"),
                       ("zendesk", "support"), ("figma", "design"),
                       ("looker", "analytics_bi"), ("marketo", "marketing"),
                       ("docusign", "legal_contracts"), ("shopify", "ecommerce"),
                       ("snowflake", "data_platform"), ("okta", "security_iam"),
                       ("datadog", "observability"), ("calendly", "scheduling"),
                       ("dropbox", "storage_files")):
        assert category_for_brand(brand) == cat, brand


def test_an_empty_key_is_None_and_an_unknown_key_is_unrecognized():
    """Two different facts. Nothing to look up is not the same as looked-up-and-missing."""
    assert category_for_brand("") is None
    assert category_for_brand(None) is None
    assert category_for_brand("zzzz") == "unrecognized"
    assert category_for_host("") is None


def test_a_subdomain_never_crosses_only_the_vendor_does():
    """⚠️ THE ONE PRIVACY EDGE IN THIS DIMENSION. Enterprise SaaS hosts a customer on their own
    subdomain -- `acme.atlassian.net`, `acme.myworkday.com`, `acme.my.salesforce.com` -- so the
    first label is frequently the CUSTOMER'S OWN NAME. The vendor half must come from this
    module's table, never from the host string, or this level would publish who an org's
    customers are."""
    for host, vendor in (("acme.atlassian.net", "atlassian"),
                         ("bigco.myworkday.com", "workday"),
                         ("contoso.my.salesforce.com", "salesforce"),
                         ("acme.zendesk.com", "zendesk")):
        got = vendor_for_host(host)
        assert got == vendor, (host, got)
        assert host.split(".")[0] not in (got or ""), (host, got)


def test_an_unrecognized_system_publishes_no_vendor_pair():
    """This dimension means "we can name this". A system we could not name is already reported
    by `system_categories` as `unrecognized`; pairing it here with no vendor would be a second,
    emptier way of saying the same thing -- and the rendering it supports shows only nameable
    systems."""
    assert category_for_host("keld.co") == "unrecognized"
    assert vendor_for_host("keld.co") is None
    assert system_vendor("unrecognized", "anything") is None
    assert system_vendor(None, "jira") is None
    assert system_vendor("issue_tracking", None) is None


def test_the_vendor_vocabulary_is_closed_even_though_it_is_large():
    """⚠️ NOT the open-vocabulary exposure `named_terms` is. A published vendor can only ever be
    a token this module already holds, so the set of values that can ever cross is bounded by
    the table and enumerable from it."""
    known = set(BRAND) | {v for v, _ in _host_vendors()} | {v for v, _ in CLI_CLIENT.values()}
    for host in ("app.notion.com", "acme.atlassian.net", "github.com", "totally-unknown.io"):
        v = vendor_for_host(host)
        assert v is None or v in known, (host, v)
    for tok in ("jira", "notion", "zzzz", ""):
        v = vendor_for_brand(tok)
        assert v is None or v in known, (tok, v)


def _host_vendors():
    from app.analysis.systems import _HOST_SUFFIX
    return list(_HOST_SUFFIX.values())


def test_the_pair_resolves_for_every_lane():
    """All three key lanes produce a pair, because a vendor seen only by URL is as real as one
    seen through a connector."""
    assert system_vendor(category_for_brand("jira"), vendor_for_brand("jira")) == "issue_tracking:jira"
    assert system_vendor(category_for_host("app.notion.com"),
                         vendor_for_host("app.notion.com")) == "knowledge_base:notion"
    assert system_vendor(category_for_program("gh"),
                         vendor_for_program("gh")) == "code_hosting:github"
    assert system_vendor(category_for_program("git"), vendor_for_program("git")) is None


def test_the_published_vocabulary_is_declared_in_one_place():
    """⚠️ A closed set a consumer RENDERS must have exactly one definition in the producer.

    Atlas stores these as columns and displays nothing for a value it does not know, so a tenth
    class added as a bare `return "..."` inside classify_bash would publish rows that render as
    nothing, on every screen, in silence. This reads reqclass's own SOURCE -- the same technique
    `DynamicStatuses` uses against dynamics.py -- so the declaration cannot fall behind the code
    that produces the values."""
    import inspect, re
    from app.analysis import reqclass
    src = inspect.getsource(reqclass)
    # Every string literal either classifier can hand back, including the ternary arms.
    returned = set(re.findall(r'return "([a-z_]+)"', src))
    returned |= set(re.findall(r'else "([a-z_]+)"', src))
    returned -= {"", "n"}
    missing = returned - set(reqclass.CLASSES)
    assert not missing, (
        f"reqclass can return {sorted(missing)}, absent from CLASSES. A consumer renders this "
        f"as a closed set and shows nothing for a value it does not know.")
    assert len(reqclass.CLASSES) == 9, reqclass.CLASSES


def test_unrecognized_pairs_with_an_ACTION_but_never_with_a_VENDOR():
    """⚠️ THE ASYMMETRY IS DELIBERATE AND LOOKS LIKE AN OVERSIGHT, so it is pinned here.

    `system_vendor("unrecognized", ...)` is None because there is no vendor to name -- the pair
    would carry nothing the bare category does not already say. `system_action` DOES pair, because
    there IS something to say: `unrecognized:update` reports that something was WRITTEN in a
    system we cannot name, which is a materially different fact from `unrecognized:read` and is
    not recoverable from `system_categories`.

    A consumer may hide both; the producer must not conflate "nothing to report" with "the
    reader chose not to look"."""
    assert system_vendor("unrecognized", "whatever") is None
    from app.analysis.systems import system_action
    assert system_action("unrecognized", "acme-update-thing") == "unrecognized:update"
    assert system_action("unrecognized", "acme-fetch-thing") == "unrecognized:read"


def test_an_ordinary_english_word_resolves_from_a_HOST_but_not_from_a_TOOL_TOKEN():
    """⚠️ THE TWO LANES DO NOT DESERVE EQUAL TRUST, and treating them alike publishes confident
    wrong categories that no reader can detect.

    A host is strong evidence: `monday.com` is Monday.com, because the registrable label had to
    match exactly and somebody had to register it. A tool token is weak: `mcp_provider` takes
    the first word of a tool NAME, so `monday-standup-notes` yields `monday`,
    `actions-list-runs` yields `actions`, `heap-dump` yields `heap`. Each would have published
    a category about a system nobody used.

    This is the failure this module calls the worse one -- over-firing is invisible to the
    reader in a way a miss is not. The vendor is NOT dropped; only the weak lane is refused."""
    from app.analysis.systems import AMBIGUOUS
    for word, host, cat in (("monday", "monday.com", "issue_tracking"),
                            ("bill", "bill.com", "finance_billing"),
                            ("front", "front.com", "support"),
                            ("heap", "heap.io", "analytics_bi"),
                            ("together", "together.ai", "ai_ml")):
        assert word in AMBIGUOUS, word
        # weak lane: refused, and refused as `unrecognized` -- a connector WAS used.
        assert category_for_brand(word) == "unrecognized", word
        assert vendor_for_brand(word) is None, word
        # strong lane: still fully resolved.
        assert category_for_host(host) == cat, host
        assert vendor_for_host(host) == word, host


def test_an_unambiguous_vendor_is_untouched_by_the_ambiguity_rule():
    """The refusal must cost nothing for names that are not ordinary words."""
    for tok, cat in (("jira", "issue_tracking"), ("notion", "knowledge_base"),
                     ("salesforce", "crm_sales"), ("workday", "hr_people"),
                     ("cultureamp", "hr_people")):
        assert category_for_brand(tok) == cat, tok
        assert vendor_for_brand(tok) == tok, tok


def test_every_table_token_is_reachable():
    """⚠️ A TOKEN NOTHING CAN EMIT IS A DEAD ENTRY THAT READS AS COVERAGE. `intercom-messenger`
    was one: `mcp_provider` splits on `-`, so no lane could ever produce it, yet it sat in the
    table looking like Intercom was handled. A hyphen or underscore makes a token unreachable
    by construction, and that is checkable."""
    for tok in BRAND:
        assert "-" not in tok and "_" not in tok, (
            f"{tok!r} can never be produced: both lanes split on - and _")
    for vendor, _cat in _host_vendors():
        assert vendor and "-" not in vendor, vendor


def test_a_conglomerate_product_host_resolves_even_though_the_company_does_not():
    """`google` is absent from the brand table on purpose -- the company name says nothing
    about the work. Its PRODUCT hosts say plenty, and without these every Drive or Docs
    reference fell through `_strip_tld` to `google` and answered `unrecognized`."""
    assert category_for_host("drive.google.com") == "storage_files"
    assert category_for_host("docs.google.com") == "knowledge_base"
    assert category_for_host("mail.google.com") == "communication"
    assert category_for_host("teams.microsoft.com") == "communication"
    assert "google" not in BRAND and "microsoft" not in BRAND


def test_no_token_appears_in_two_categories():
    """⚠️ `BRAND` is a dict comprehension over `_TABLE`, so a token listed under two categories
    is not an error -- it SILENTLY takes whichever category the iteration reaches last, and the
    other category quietly loses it.

    At 254 tokens that risk was theoretical; at 659 across 21 categories it is a real one, and
    the failure is invisible from every direction: the table looks right, the tests pass, and
    one category is simply wrong about one vendor. Pinned by construction rather than by care."""
    from app.analysis.systems import _TABLE
    import collections
    seen = collections.defaultdict(list)
    for cat, blob in _TABLE.items():
        for t in blob.split():
            seen[t].append(cat)
    dupes = {t: c for t, c in seen.items() if len(c) > 1}
    assert not dupes, (
        f"these tokens are claimed by two categories and one claim is silently lost: {dupes}")


def test_the_expanded_table_reaches_the_systems_a_real_company_runs_on():
    """The audit's point: the table was hand-written against generic enterprise and had holes
    a mid-size company would fall straight through. These are the ones that were missing."""
    for brand, cat in (("azuredevops", "issue_tracking"), ("smartsheet", "issue_tracking"),
                       ("dynamics", "crm_sales"), ("gainsight", "crm_sales"),
                       ("adyen", "finance_billing"), ("concur", "finance_billing"),
                       ("zuora", "finance_billing"), ("ukg", "hr_people"),
                       ("workable", "hr_people"), ("googleanalytics", "analytics_bi"),
                       ("pardot", "marketing"), ("semrush", "marketing"),
                       ("wiz", "security_iam"), ("sailpoint", "security_iam"),
                       ("mongodbatlas", "data_platform"), ("pinecone", "data_platform"),
                       ("appdynamics", "observability"), ("incidentio", "observability"),
                       ("clio", "legal_contracts"), ("dropboxsign", "legal_contracts"),
                       ("bedrock", "ai_ml"), ("vertexai", "ai_ml"),
                       ("talkdesk", "support"), ("lucidchart", "design")):
        assert category_for_brand(brand) == cat, (brand, category_for_brand(brand))


def test_a_conglomerate_product_resolves_in_BOTH_lanes():
    """⚠️ THE FIRST PASS GAVE THESE ONLY THE HOST LANE, and the host lane is the RARER path for
    exactly these products. `mcp__<uuid>__googlesheets-read` answered `unrecognized` while
    `sheets.google.com` resolved fine -- so the gap fell on the common case and the test that
    existed (`test_a_conglomerate_product_host_resolves...`) passed throughout, because it only
    ever asked about hosts.

    Measured against a public MCP registry's usage counts: Google Sheets is the second
    most-used server listed (56,138 uses), Google Calendar 15,718, Google Drive 8,102.

    `drive` and `teams` are deliberately NOT here: they are ordinary English words, belong to
    AMBIGUOUS, and stay host-only."""
    for tok, cat in (("googlesheets", "analytics_bi"), ("googledocs", "knowledge_base"),
                     ("googlecalendar", "scheduling"), ("googlemeet", "communication"),
                     ("gmail", "communication")):
        assert category_for_brand(tok) == cat, (tok, category_for_brand(tok))
    assert category_for_host("sheets.google.com") == "analytics_bi"
    for word in ("drive", "teams"):
        from app.analysis.systems import AMBIGUOUS
        assert word in AMBIGUOUS and category_for_brand(word) == "unrecognized", word


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
