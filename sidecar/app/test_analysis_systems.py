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


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
