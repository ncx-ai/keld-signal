"""`system_categories` — WHAT KIND OF BUSINESS SYSTEM a window touched.

This is a DECLARATIVE LOOKUP, and that is the whole reason it can ship on evidence this
repo's corpora cannot provide. Every other signal here was gated on measurement because it
INFERS something: `activity_class` reads a command and decides what capability was stressed,
`domain` read prose and was refuted four times. This one asserts that Jira is an issue
tracker and Workday is an HR system. That is true by construction, not by induction, so the
question "does a developer's transcript contain Salesforce?" is the wrong gate -- it never
will, and Salesforce is still a CRM.

⚠️ SO BE PRECISE ABOUT WHICH HALF IS MEASURED AND WHICH IS NOT.

  MEASURED, on 904 real sessions across two people's corpora: the KEY EXTRACTION. The three
  lanes this reads -- an MCP tool's brand token, a URL host, a CLI program -- are the same
  ones `levels.py` already derives for `service`/`exe`, and they resolve the brands actually
  present (notion 532 calls, github, slack, apple). What a key yields is checkable here.

  NOT MEASURED, and not measurable here: COVERAGE in an org that runs on Jira, Salesforce,
  Workday or NetSuite. Both corpora are engineering work; corpus B contains zero MCP calls in
  499 sessions. So this level's coverage in a sales or finance org is ASSERTED FROM THE TABLE,
  never observed, and the honest consequence is that `absent` has to stay readable rather than
  reading as "touched nothing" -- see UNRECOGNIZED below.

⚠️ THE CATEGORY VOCABULARY IS PUBLISHED AND THEREFORE SCHEMA-AFFECTING; the TABLE ENTRIES are
not. Adding `figma -> design` needs no bump. Adding a 21st CATEGORY does, because a consumer
renders the closed set. Keep that asymmetry: it is what lets the table grow as connectors
appear without a coordinated release, which is the entire operational argument for a lookup.

⚠️ THIS IS A PRIVACY IMPROVEMENT OVER THE LEVEL IT SITS BESIDE, not a new exposure. The
`service` level already publishes RAW HOSTS, and a real corpus put `api-gateway-dev.keld.co`
and `enrich-sidecar` on the wire -- internal infrastructure names. A category is strictly less
identifying than the host it came from: `cloud_infra` names no host, no environment and no
org. Nothing new crosses; something coarser does.

⚠️ THE TABLE HOLDS PRODUCTS AND SERVICES, NEVER GENERAL-PURPOSE TECHNOLOGY. This is the rule
that decides every entry, and getting it wrong does not produce a wrong category -- it produces
a level that answers the wrong QUESTION.

The question is "which vendor products does this org's work run through", because that is what
says something categorical about the work. `postgres`, `redis`, `kafka`, `elasticsearch`,
`docker`, `kubernetes`, `terraform`, `prometheus` and `airflow` are technologies somebody
RUNS; naming them says only that software was built. `Snowflake`, `Databricks`, `Fivetran`,
`Datadog`, `Okta`, `Jira` and `Workday` are things an org BUYS and works inside, and touching
one is evidence about what kind of work is happening.

Measured consequence of getting this wrong: with `docker` in the table, one corpus reported
741 `cloud_infra` references of which 740 were `docker` -- a level that fired on 15% of
sessions and told a reader nothing except that containers exist.

The boundary case is OSS with a commercial cloud (Grafana, dbt, MongoDB). The rule there is
the NAME AS USED: a bare `docker`/`postgres` invocation is the technology, while `grafana.net`
or `cloud.mongodb.com` is the product, so those resolve by HOST rather than by bare token.

⚠️ AND IT IS A DENOISER. Measured on corpus B: 1,165 of 1,316 host mentions (88.5%) are
`localhost` or `127.0.0.1`. Loopback is not an external system and is dropped here rather than
counted, which is most of what makes the raw `service` level hard to read.
"""

# The closed, PUBLISHED vocabulary. Adding to this is a schema change (see above).
CATEGORIES = (
    "issue_tracking", "knowledge_base", "communication", "crm_sales", "support",
    "design", "hr_people", "finance_billing", "analytics_bi", "marketing",
    "code_hosting", "ci_cd", "cloud_infra", "data_platform", "observability",
    "security_iam", "legal_contracts", "scheduling", "storage_files", "ecommerce",
    "ai_ml", "unrecognized",
)

# ⚠️ ONE BRAND MAPS TO ONE CATEGORY, and where a vendor genuinely spans two the choice is
# written down rather than split. Atlassian sells Jira AND Confluence, so the BRAND is not the
# key -- `jira` and `confluence` are separate keys and `atlassian` is deliberately absent.
# Microsoft, Google and Adobe are the same problem at larger scale and get no bare-brand entry
# for the same reason: `google` alone says nothing, `drive`/`bigquery`/`meet` say something.
_TABLE = {
    "issue_tracking": """jira linear asana monday shortcut trello clickup basecamp wrike
        teamwork redmine youtrack pivotaltracker height""",
    "knowledge_base": """notion confluence coda guru slab almanac nuclino tettra
        bookstack outline mediawiki sharepoint""",
    "communication": """slack teams discord zoom webex gmail outlook mailgun twilio
        sendbird intercom-messenger chime""",
    "crm_sales": """salesforce hubspot pipedrive gong outreach salesloft apollo clari
        zoominfo close copper insightly freshsales attio""",
    "support": """zendesk intercom freshdesk helpscout front kustomer gladly gorgias
        servicenow jirasm""",
    "design": """figma sketch canva miro framer invision zeplin abstract penpot excalidraw""",
    "hr_people": """workday bamboohr gusto rippling justworks greenhouse lever ashby
        deel remote namely paylocity paycom adp trinet lattice culdeamp""",
    "finance_billing": """netsuite quickbooks xero stripe bill ramp brex expensify coupa
        chargebee recurly avalara sage freshbooks mercury plaid""",
    "analytics_bi": """looker tableau powerbi amplitude mixpanel metabase hex sigma
        heap pendo posthog redash superset quicksight fullstory""",
    "marketing": """marketo mailchimp braze klaviyo iterable customerio hootsuite
        sprout buffer contentful sanity webflow wordpress hubspotcms optimizely""",
    "code_hosting": """github gitlab bitbucket gerrit sourcehut codeberg gitea
        githubusercontent""",
    "ci_cd": """circleci jenkins buildkite travis appveyor teamcity bamboo argo
        spinnaker harness drone semaphore actions""",
    # ⚠️ PROVIDERS, NOT THE TOOLS THAT TALK TO THEM. `kubernetes`, `docker`, `terraform`,
    # `pulumi`, `ansible`, `nomad` and `consul` were here and are REMOVED -- see the rule at the
    # top. `docker` alone accounted for 740 of 741 references in one corpus.
    "cloud_infra": """aws gcp azure cloudflare vercel netlify heroku fly render
        digitalocean linode hetzner railway supabase firebase fastly akamai""",
    # ⚠️ SERVICES, NOT DATASTORES. `postgres`, `mysql`, `mongodb`, `redis`, `kafka`,
    # `elasticsearch`, `clickhouse` and `duckdb` were here and are REMOVED: they are
    # technologies an engineer runs, not products an org buys, and as hostnames they are
    # overwhelmingly local containers. Likewise `airflow`/`dagster`/`prefect` (OSS schedulers)
    # against `fivetran`/`airbyte`/`segment` (subscriptions).
    "data_platform": """snowflake databricks bigquery redshift fivetran airbyte
        segment confluent astronomer starburst planetscale neon cockroachlabs""",
    # `prometheus` REMOVED (OSS you run). `grafana` kept: it resolves as a host
    # (`grafana.net`) where it is the hosted product, per the boundary rule at the top.
    "observability": """datadog sentry newrelic grafana splunk pagerduty honeycomb
        lightstep dynatrace opsgenie rollbar bugsnag statuspage betterstack""",
    # `vault` REMOVED as a bare token (HashiCorp Vault is run, not bought, and `vault` is a
    # common internal hostname). `hashicorp` covers the vendor where it is actually named.
    "security_iam": """okta auth0 onepassword snyk crowdstrike vanta drata hashicorp
        sumologic duo jumpcloud cyberark lastpass bitwarden dependabot""",
    "legal_contracts": """docusign ironclad pandadoc adobesign hellosign clm
        contractbook juro""",
    "scheduling": """calendly cal doodle savvycal calendar chilipiper""",
    "storage_files": """drive dropbox box onedrive s3 gcs backblaze egnyte""",
    "ecommerce": """shopify woocommerce bigcommerce magento squarespace etsy
        amazonseller faire""",
    "ai_ml": """openai anthropic claude huggingface replicate openrouter together cohere
        mistral perplexity langsmith weightsandbiases wandb modal runpod""",
}

# brand token -> category, built once.
BRAND = {b: cat for cat, blob in _TABLE.items() for b in blob.split()}

# ⚠️ HOSTS DO NOT EQUAL BRANDS and the difference is where this would silently under-report.
# `app.notion.com` ends in the brand; `company.atlassian.net` does NOT contain `jira`, and
# `console.aws.amazon.com` does not contain `aws` as a registrable label. These are the hosts
# whose brand cannot be read off the name, resolved by SUFFIX so a subdomain cannot evade them.
_HOST_SUFFIX = {
    "atlassian.net": "issue_tracking", "atlassian.com": "issue_tracking",
    "amazonaws.com": "cloud_infra", "amazon.com": "cloud_infra",
    "googleapis.com": "cloud_infra", "cloud.google.com": "cloud_infra",
    "azure.com": "cloud_infra", "windows.net": "cloud_infra",
    "force.com": "crm_sales", "my.salesforce.com": "crm_sales",
    "slack.com": "communication", "office.com": "communication",
    "sharepoint.com": "knowledge_base", "atlassian.io": "knowledge_base",
    "myworkday.com": "hr_people", "successfactors.com": "hr_people",
    "service-now.com": "support", "zendesk.com": "support",
    "githubusercontent.com": "code_hosting", "github.io": "code_hosting",
    "figma.com": "design", "miro.com": "design",
    "snowflakecomputing.com": "data_platform", "databricks.com": "data_platform",
    "datadoghq.com": "observability", "sentry.io": "observability",
    "pagerduty.com": "observability", "grafana.net": "observability",
    "okta.com": "security_iam", "auth0.com": "security_iam",
    "docusign.net": "legal_contracts", "docusign.com": "legal_contracts",
    "hubspot.com": "crm_sales", "pipedrive.com": "crm_sales",
    "myshopify.com": "ecommerce",
}

# ⚠️ A CLI PROGRAM IS A CLIENT FOR A SYSTEM, and the program name is usually NOT the brand.
# `gh` is GitHub, `psql` is Postgres, `aws` is AWS. Bare `git` is deliberately ABSENT: it is a
# local VCS, not an external system, and it is the single highest-frequency program in both
# corpora (1,148 + 390 calls) -- admitting it would make `code_hosting` the answer for
# essentially every engineering window and say nothing.
CLI_CLIENT = {
    # ⚠️ A CLIENT FOR A NAMED VENDOR ONLY. `kubectl`, `helm`, `docker`, `podman`, `terraform`,
    # `pulumi`, `ansible`, `psql`, `mysql`, `mongo` and `redis-cli` were all here and are
    # REMOVED: each talks to whatever cluster or database you point it at, so it names no
    # product and says only that engineering happened. `aws`/`gcloud`/`az` stay -- those name
    # one vendor and nothing else.
    "gh": "code_hosting", "glab": "code_hosting", "hub": "code_hosting",
    "aws": "cloud_infra", "gcloud": "cloud_infra", "az": "cloud_infra",
    "vercel": "cloud_infra", "netlify": "cloud_infra", "heroku": "cloud_infra",
    "fly": "cloud_infra", "flyctl": "cloud_infra", "wrangler": "cloud_infra",
    "doctl": "cloud_infra", "supabase": "cloud_infra", "firebase": "cloud_infra",
    "railway": "cloud_infra", "render": "cloud_infra",
    "snowsql": "data_platform", "fivetran": "data_platform",
    "stripe": "finance_billing",
    "jira": "issue_tracking", "linear": "issue_tracking",
    "slack": "communication",
    "sentry-cli": "observability", "datadog-ci": "observability",
    "snyk": "security_iam", "op": "security_iam", "okta": "security_iam",
    "hf": "ai_ml", "huggingface-cli": "ai_ml", "modal": "ai_ml", "wandb": "ai_ml",
}

# Loopback and link-local are NOT external systems. 88.5% of one corpus's host evidence.
LOCAL_HOSTS = frozenset(("localhost", "127.0.0.1", "0.0.0.0", "::1", "host.docker.internal"))

# ⚠️ A SPEC URL IS NOT A SYSTEM SOMEBODY USED, and counting it as one is the same error as
# counting loopback. `schemas.openxmlformats.org` appears 26 times in a real corpus because a
# .docx names it in its own XML; nobody visited it. `w3.org`, font CDNs and package registries
# are the same shape: an asset or a namespace, fetched or merely quoted, never a system the
# work was DONE in. Dropped rather than `unrecognized`, because unrecognized is a claim that
# we saw a system and could not name it -- here there is no system.
NOT_A_SYSTEM = (
    "w3.org", "openxmlformats.org", "gstatic.com", "googleusercontent.com",
    "jsdelivr.net", "unpkg.com", "cdnjs.cloudflare.com", "schema.org",
    "npmjs.org", "npmjs.com", "pypi.org", "crates.io", "proxy.golang.org",
    "example.com", "example.org", "localhost.localdomain",
)


def _strip_tld(host):
    """`app.notion.com` -> the registrable-ish label, `notion`. Not a public-suffix parse: this
    only has to recover a brand we already have a table entry for, and anything it gets wrong
    falls through to `unrecognized` rather than to a wrong category."""
    parts = [p for p in host.split(".") if p]
    if len(parts) < 2:
        return parts[0] if parts else ""
    # Drop a trailing TLD, and a country-code second level (`co.uk`, `com.au`).
    if len(parts) >= 3 and len(parts[-1]) == 2 and parts[-2] in ("co", "com", "org", "net", "ac"):
        return parts[-3]
    return parts[-2]


def category_for_host(host):
    """Category for a URL host, or None. Loopback returns None -- it is not external."""
    h = (host or "").strip().lower().rstrip(".")
    if not h or h in LOCAL_HOSTS:
        return None
    if any(h == d or h.endswith("." + d) for d in NOT_A_SYSTEM):
        return None
    for suffix, cat in _HOST_SUFFIX.items():
        if h == suffix or h.endswith("." + suffix):
            return cat
    if "." not in h:
        # ⚠️ A DOTLESS HOST IS NEVER A VENDOR, AND THIS TEST MUST COME BEFORE THE BRAND LOOKUP.
        # It is a container service name, an /etc/hosts entry or a LAN name, and the names
        # people give those collide head-on with the brand table: `redis`, `postgres`, `mysql`,
        # `mongodb`, `kafka`, `elasticsearch` and `docker` are all real vendors AND the
        # conventional hostname for a local container. Checked after the brand lookup,
        # `http://redis:6379` -- a compose service on the developer's own machine -- publishes
        # `data_platform` as though the org ran Redis Cloud.
        #
        # None rather than `unrecognized`: no registrable name means no external party, so
        # there was no system here, as opposed to one we could not name.
        return None
    label = _strip_tld(h)
    if label in BRAND:
        return BRAND[label]
    return "unrecognized"


def category_for_brand(token):
    """Category for an MCP brand token (`notion`, `jira`) or None when the token is empty.
    An unknown token is `unrecognized`, never silently dropped: an org whose connectors this
    table does not know yet must read as UNKNOWN COVERAGE, not as no external systems."""
    t = (token or "").strip().lower()
    if not t:
        return None
    return BRAND.get(t, "unrecognized")


def category_for_program(prog):
    """Category for a CLI program, or None when it is not a client for an external system.
    None, not `unrecognized`: `ls` is not an uncategorised external system, it is not one at
    all, and counting every local command as unrecognized would drown the level."""
    return CLI_CLIENT.get((prog or "").strip().lower())


# ---------------------------------------------------------------------------------------
# WHAT WAS DONE INSIDE THE SYSTEM, not just which system it was.
#
# `crm_sales` says the work touched a CRM. `crm_sales:update` says a record was CHANGED, which
# is pipeline management rather than a lookup, and that is a different kind of work by anyone's
# reading. The verb is already sitting in the MCP tool name -- `jira-create-issue`,
# `salesforce-query-records`, `docusign-send-envelope` -- so this costs no new evidence.
#
# ⚠️ THIS IS NOT `system_categories` x `activity_class` AND THE JOIN IS NOT EQUIVALENT.
# `activity_class` is ONE label for a whole inference request, covering every tool that request
# issued. A request that calls `notion-fetch` and then greps a file has a single activity class,
# and which of its tools the verb belonged to is gone. Pairing at the REFERENCE keeps them
# attached. A consumer wanting the coarser view still has `system_categories` beside this.
#
# ⚠️ MCP ONLY, DELIBERATELY. A URL host names a system and no action -- visiting
# `acme.atlassian.net` says nothing about whether anything was written. A CLI could be parsed
# for its subcommand, but `shell.py` already derives verbs on its own terms and a second,
# differently-normalised verb vocabulary over the same commands is how two levels start
# disagreeing about the same call. So a host or CLI reference publishes its CATEGORY and no
# action, which is the honest amount: we know the system, not what happened in it.
ACTIONS = ("read", "search", "create", "update", "delete", "send", "run")

# Tool-name verbs -> the closed set. Keyed on the token, so `notion-update-page` and
# `jira-transition-issue` both land on `update` without either being special-cased.
_VERB = {
    "get": "read", "fetch": "read", "read": "read", "list": "read", "show": "read",
    "describe": "read", "download": "read", "export": "read", "view": "read",
    "search": "search", "query": "search", "find": "search", "filter": "search",
    "lookup": "search",
    "create": "create", "add": "create", "new": "create", "insert": "create",
    "upload": "create", "duplicate": "create", "clone": "create",
    "update": "update", "edit": "update", "patch": "update", "set": "update",
    "move": "update", "transition": "update", "assign": "update", "rename": "update",
    "append": "update", "comment": "update", "close": "update", "resolve": "update",
    "archive": "update", "approve": "update", "merge": "update",
    "delete": "delete", "remove": "delete", "destroy": "delete",
    "send": "send", "post": "send", "reply": "send", "notify": "send",
    "publish": "send", "share": "send", "invite": "send", "message": "send",
    "run": "run", "execute": "run", "trigger": "run", "start": "run", "sync": "run",
    "refresh": "run", "import": "run",
}


def action_for_tool(tool, brand=None):
    """The normalised action an MCP tool name describes, or None when no token maps.

    None rather than a default: a tool whose verb this does not know must publish its CATEGORY
    with NO action rather than a guessed one, because a wrong verb here is a false statement
    about what someone did in a system of record -- `crm_sales:delete` where nothing was
    deleted. Silence is recoverable; a confident wrong verb is not.
    """
    t = (tool or "").strip().lower()
    if not t:
        return None
    toks = [x for x in t.replace("_", "-").split("-") if x]
    b = (brand or "").strip().lower()
    # Drop a leading brand token so `notion-update-page` is read as update, not as `notion`.
    if b and toks and toks[0] == b:
        toks = toks[1:]
    for tok in toks:
        if tok in _VERB:
            return _VERB[tok]
    return None


def system_action(category, tool, brand=None):
    """`<category>:<action>`, or None when either half is missing."""
    if not category:
        return None
    act = action_for_tool(tool, brand)
    return f"{category}:{act}" if act else None
