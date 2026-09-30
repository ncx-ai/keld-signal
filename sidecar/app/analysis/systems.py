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
        teamwork redmine youtrack pivotaltracker height
        azuredevops smartsheet airtable aha productboard zenhub favro targetprocess rally
        polarion zohoprojects teamgantt
    """,
    "knowledge_base": """notion confluence coda guru slab almanac nuclino tettra
        bookstack outline mediawiki sharepoint
        gitbook readme quip slite document360 helpjuice glean stackoverflowteams
        dropboxpaper
    
        googledocs""",
    "communication": """slack teams discord zoom webex gmail outlook mailgun twilio
        sendbird chime
        mattermost rocketchat zulip googlechat ringcentral dialpad aircall sendgrid
        postmark vonage bandwidth telnyx missive superhuman
    
        googlemeet""",
    "crm_sales": """salesforce hubspot pipedrive gong outreach salesloft apollo clari
        zoominfo close copper insightly freshsales attio
        dynamics zohocrm sugarcrm gainsight chorus avoma lusha 6sense demandbase drift
        qualified highspot seismic showpad mindtickle nutshell keap
    """,
    "support": """zendesk intercom freshdesk helpscout front kustomer gladly gorgias
        servicenow
        zohodesk talkdesk genesys five9 dixa crisp tidio hiver helpshift liveagent
        reamaze forethought ada
    """,
    "design": """figma sketch canva miro framer invision zeplin abstract penpot excalidraw
        lucidchart lucid whimsical mural balsamiq axure adobexd creativecloud photoshop
        illustrator indesign marvel milanote protopie rive spline
    """,
    "hr_people": """workday bamboohr gusto rippling justworks greenhouse lever ashby
        deel remote namely paylocity paycom adp trinet lattice cultureamp
        ukg ceridian dayforce hibob personio factorial humaans zenefits workable
        smartrecruiters jobvite icims jazzhr recruitee teamtailor pinpoint 15five
        leapsome betterworks peakon officevibe bonusly docebo cornerstone learnupon
    """,
    "finance_billing": """netsuite quickbooks xero stripe bill ramp brex expensify coupa
        chargebee recurly avalara sage freshbooks mercury plaid
        adyen paypal braintree paddle lemonsqueezy zuora tipalti concur intacct blackline
        floqast gocardless wise revolut mollie razorpay checkout worldpay maxio orb
        metronome stigg anrok sovos vertex airbase divvy navan tripactions
    
        sap""",
    "analytics_bi": """looker tableau powerbi amplitude mixpanel metabase hex sigma
        heap pendo posthog redash superset quicksight fullstory
        googleanalytics matomo plausible fathom hotjar contentsquare quantummetric
        logrocket smartlook thoughtspot domo qlik sisense holistics lightdash omni preset
        deepnote
    
        googlesheets""",
    "marketing": """marketo mailchimp braze klaviyo iterable customerio hootsuite
        sprout buffer contentful sanity webflow wordpress optimizely
        pardot eloqua activecampaign constantcontact convertkit beehiiv substack
        mailerlite brevo sendinblue omnisend attentive postscript onesignal airship
        leanplum clevertap moengage appsflyer adjust semrush ahrefs moz screamingfrog
        googleads unbounce instapage typeform jotform sprinklr khoros emplifi storyblok
        prismic strapi directus wix
    """,
    "code_hosting": """github gitlab bitbucket gerrit sourcehut codeberg gitea
        githubusercontent
        azurerepos perforce phabricator forgejo sourceforge launchpad gitee
    """,
    "ci_cd": """circleci jenkins buildkite travis appveyor teamcity bamboo argo
        spinnaker harness drone semaphore actions
        gitlabci azurepipelines codebuild codepipeline cloudbuild octopus bitrise
        codemagic expo jfrog artifactory nexus sonarqube sonarcloud codecov coveralls
        chromatic percy browserstack saucelabs lambdatest
    """,
    # ⚠️ PROVIDERS, NOT THE TOOLS THAT TALK TO THEM. `kubernetes`, `docker`, `terraform`,
    # `pulumi`, `ansible`, `nomad` and `consul` were here and are REMOVED -- see the rule at the
    # top. `docker` alone accounted for 740 of 741 references in one corpus.
    "cloud_infra": """aws gcp azure cloudflare vercel netlify heroku fly render
        digitalocean linode hetzner railway supabase firebase fastly akamai
        oci ibmcloud alibabacloud ovh scaleway vultr upcloud equinix civo koyeb
        northflank platformsh aptible porter qovery cloudways kinsta wpengine pantheon
        bunny keycdn stackpath imperva
    """,
    # ⚠️ SERVICES, NOT DATASTORES. `postgres`, `mysql`, `mongodb`, `redis`, `kafka`,
    # `elasticsearch`, `clickhouse` and `duckdb` were here and are REMOVED: they are
    # technologies an engineer runs, not products an org buys, and as hostnames they are
    # overwhelmingly local containers. Likewise `airflow`/`dagster`/`prefect` (OSS schedulers)
    # against `fivetran`/`airbyte`/`segment` (subscriptions).
    "data_platform": """snowflake databricks bigquery redshift fivetran airbyte
        segment confluent astronomer starburst planetscale neon cockroachlabs
        mongodbatlas elasticcloud pinecone weaviate qdrant rudderstack hightouch census
        mparticle tealium matillion talend informatica dbtlabs montecarlo atlan collibra
        alation timescale singlestore yugabyte tidb upstash firebolt motherduck teradata
        vertica synapse
    """,
    # `prometheus` REMOVED (OSS you run). `grafana` kept: it resolves as a host
    # (`grafana.net`) where it is the hosted product, per the boundary rule at the top.
    "observability": """datadog sentry newrelic grafana splunk pagerduty honeycomb
        lightstep dynatrace opsgenie rollbar bugsnag statuspage betterstack
        appdynamics instana logzio loggly papertrail coralogix chronosphere pingdom
        uptimerobot incidentio firehydrant rootly blameless squadcast xmatters victorops
        cronitor
    """,
    # `vault` REMOVED as a bare token (HashiCorp Vault is run, not bought, and `vault` is a
    # common internal hostname). `hashicorp` covers the vendor where it is actually named.
    "security_iam": """okta auth0 onepassword snyk crowdstrike vanta drata hashicorp
        sumologic duo jumpcloud cyberark lastpass bitwarden dependabot
        pingidentity forgerock workos clerk stytch frontegg descope entra azuread
        sailpoint saviynt beyondtrust delinea sentinelone sophos trendmicro paloalto
        fortinet zscaler netskope wiz orca lacework aqua sysdig tenable qualys rapid7
        veracode checkmarx semgrep secureframe hyperproof auditboard onetrust trustarc
    """,
    "legal_contracts": """docusign ironclad pandadoc adobesign hellosign 
        contractbook juro
        dropboxsign signnow signeasy zohosign oneflow scrive yousign conga icertis
        agiloft linksquares evisort lexion spotdraft concord contractworks clio mycase
        smokeball practicepanther filevine everlaw relativity logikcull casetext
        lexisnexis westlaw bloomberglaw
    """,
    "scheduling": """calendly cal doodle savvycal calendar chilipiper
        acuity setmore youcanbookme zcal reclaim clockwise vimcal undock
    
        googlecalendar""",
    "storage_files": """drive dropbox box onedrive s3 gcs backblaze egnyte
        pcloud mega wasabi sharefile tresorit icloud filebase
    """,
    "ecommerce": """shopify woocommerce bigcommerce magento squarespace etsy
        faire
        commercetools swell vtex shopware ecwid bigcartel gumroad sendowl podia teachable
        thinkific kajabi ebay walmart mercadolibre
    """,
    "ai_ml": """openai anthropic claude huggingface replicate openrouter together cohere
        mistral perplexity langsmith wandb modal runpod
        gemini googleai vertexai bedrock sagemaker azureopenai groq fireworks deepinfra
        anyscale baseten lambdalabs coreweave paperspace comet neptune roboflow labelbox
        snorkel langfuse helicone portkey braintrust humanloop promptlayer
    """,
}

# ⚠️ A CONGLOMERATE'S PRODUCT NEEDS AN ENTRY IN BOTH LANES, AND THE FIRST PASS GAVE IT ONLY ONE.
# `drive.google.com` and `sheets.google.com` were added to _HOST_SUFFIX and nowhere else, so a
# URL resolved and an MCP CONNECTOR did not -- `mcp__<uuid>__googlesheets-read` answered
# `unrecognized`. Measured against a public MCP registry's usage counts, Google Sheets is the
# second most-used server there (56,138 uses), Google Calendar 15,718, Google Drive 8,102. The
# host lane is the rarer path for exactly these products, so the gap fell on the common case.
# `drive` and `teams` stay host-only deliberately -- they are ordinary English words and belong
# to AMBIGUOUS -- but `googlesheets`, `googledocs`, `googlecalendar` and `googlemeet` are not
# words and belong in the brand table.

# brand token -> category, built once.
BRAND = {b: cat for cat, blob in _TABLE.items() for b in blob.split()}

# ⚠️ SOME VENDOR NAMES ARE ORDINARY ENGLISH WORDS, AND THE TWO LANES DO NOT DESERVE EQUAL TRUST.
#
# A HOST is strong evidence: `monday.com` is Monday.com and nothing else, because the
# registrable label had to match exactly and somebody had to register it. An MCP TOOL TOKEN is
# weak: `mcp_provider` takes the first `-`/`_`-delimited word of a tool name, so
# `monday-standup-notes` yields `monday`, `actions-list-runs` yields `actions` and
# `heap-dump` yields `heap`. Each would publish a confident, wrong category.
#
# That is the failure this module already calls the worse one -- "a lookup that over-fires is
# worse than one that misses, because unlike a miss it is invisible to the reader". So these
# resolve BY HOST ONLY. The vendor is not dropped; `monday.com` still reports issue_tracking.
# What is refused is inferring the vendor from a bare word somebody used as a verb.
#
# `_VERB_HEADS` already covers the subset that are also API verbs (`close`, `send`, `update`).
# This is the complement: nouns and adjectives, which no verb list would catch.
AMBIGUOUS = frozenset("""
    abstract actions ada apollo aqua argo bamboo bill box buffer bunny cal calendar chime
    chorus clockwise comet concord crisp drift drive drone duo expo factorial faire fireworks
    front guru harness heap height hex lattice lever lucid marvel mercury modal monday mural
    namely neptune nexus omni orb outline pinpoint porter preset qualified rally reclaim
    remote rive sage seismic sigma sketch spline sprout swell teams together vertex whimsical
    wise
""".split())

# ⚠️ HOSTS DO NOT EQUAL BRANDS and the difference is where this would silently under-report.
# `app.notion.com` ends in the brand; `company.atlassian.net` does NOT contain `jira`, and
# `console.aws.amazon.com` does not contain `aws` as a registrable label. These are the hosts
# whose brand cannot be read off the name, resolved by SUFFIX so a subdomain cannot evade them.
_HOST_SUFFIX = {
    # ⚠️ The VENDOR here is the company whose host this is, which is not always the product
    # the category names. `atlassian.net` serves both Jira and Confluence and the URL path
    # would be needed to tell them apart -- which this never sees -- so the vendor is
    # `atlassian` and the category is the more common of the two. Naming the vendor `jira`
    # would claim a precision the evidence does not have.
    "atlassian.net": ("atlassian", "issue_tracking"),
    "atlassian.com": ("atlassian", "issue_tracking"),
    "amazonaws.com": ("aws", "cloud_infra"), "amazon.com": ("aws", "cloud_infra"),
    "googleapis.com": ("gcp", "cloud_infra"), "cloud.google.com": ("gcp", "cloud_infra"),
    "azure.com": ("azure", "cloud_infra"), "windows.net": ("azure", "cloud_infra"),
    "force.com": ("salesforce", "crm_sales"),
    "my.salesforce.com": ("salesforce", "crm_sales"),
    "slack.com": ("slack", "communication"), "office.com": ("microsoft365", "communication"),
    "sharepoint.com": ("sharepoint", "knowledge_base"),
    "atlassian.io": ("atlassian", "knowledge_base"),
    "myworkday.com": ("workday", "hr_people"),
    "successfactors.com": ("successfactors", "hr_people"),
    "service-now.com": ("servicenow", "support"), "zendesk.com": ("zendesk", "support"),
    "githubusercontent.com": ("github", "code_hosting"),
    "github.io": ("github", "code_hosting"),
    "figma.com": ("figma", "design"), "miro.com": ("miro", "design"),
    "snowflakecomputing.com": ("snowflake", "data_platform"),
    "databricks.com": ("databricks", "data_platform"),
    "datadoghq.com": ("datadog", "observability"), "sentry.io": ("sentry", "observability"),
    "pagerduty.com": ("pagerduty", "observability"),
    "grafana.net": ("grafana", "observability"),
    "okta.com": ("okta", "security_iam"), "auth0.com": ("auth0", "security_iam"),
    "docusign.net": ("docusign", "legal_contracts"),
    "docusign.com": ("docusign", "legal_contracts"),
    "hubspot.com": ("hubspot", "crm_sales"), "pipedrive.com": ("pipedrive", "crm_sales"),
    "myshopify.com": ("shopify", "ecommerce"),
    # ⚠️ THE CONGLOMERATES, BY PRODUCT HOST. `google`, `microsoft` and `adobe` are deliberately
    # absent from the brand table because the company name says nothing about the work -- but
    # their PRODUCT hosts say plenty, and without these every Google Docs or Drive reference
    # resolved to `unrecognized` through `_strip_tld` finding only `google`.
    "drive.google.com": ("drive", "storage_files"),
    "docs.google.com": ("googledocs", "knowledge_base"),
    "sheets.google.com": ("googlesheets", "analytics_bi"),
    "calendar.google.com": ("googlecalendar", "scheduling"),
    "mail.google.com": ("gmail", "communication"),
    "meet.google.com": ("googlemeet", "communication"),
    "teams.microsoft.com": ("teams", "communication"),
    "outlook.office.com": ("outlook", "communication"),
    "onedrive.live.com": ("onedrive", "storage_files"),
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
    "gh": ("github", "code_hosting"), "glab": ("gitlab", "code_hosting"),
    "hub": ("github", "code_hosting"),
    "aws": ("aws", "cloud_infra"), "gcloud": ("gcp", "cloud_infra"),
    "az": ("azure", "cloud_infra"), "vercel": ("vercel", "cloud_infra"),
    "netlify": ("netlify", "cloud_infra"), "heroku": ("heroku", "cloud_infra"),
    "fly": ("fly", "cloud_infra"), "flyctl": ("fly", "cloud_infra"),
    "wrangler": ("cloudflare", "cloud_infra"), "doctl": ("digitalocean", "cloud_infra"),
    "supabase": ("supabase", "cloud_infra"), "firebase": ("firebase", "cloud_infra"),
    "railway": ("railway", "cloud_infra"), "render": ("render", "cloud_infra"),
    "snowsql": ("snowflake", "data_platform"), "fivetran": ("fivetran", "data_platform"),
    "stripe": ("stripe", "finance_billing"),
    "jira": ("jira", "issue_tracking"), "linear": ("linear", "issue_tracking"),
    "slack": ("slack", "communication"),
    "sentry-cli": ("sentry", "observability"), "datadog-ci": ("datadog", "observability"),
    "snyk": ("snyk", "security_iam"), "op": ("onepassword", "security_iam"),
    "okta": ("okta", "security_iam"),
    "hf": ("huggingface", "ai_ml"), "huggingface-cli": ("huggingface", "ai_ml"),
    "modal": ("modal", "ai_ml"), "wandb": ("wandb", "ai_ml"),
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
    for suffix, (_vendor, cat) in _HOST_SUFFIX.items():
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
    if t in AMBIGUOUS:
        # A real vendor, but this lane cannot establish it -- see AMBIGUOUS. `unrecognized`
        # rather than None: a connector WAS used, we simply cannot name what kind it is.
        return "unrecognized"
    return BRAND.get(t, "unrecognized")


def vendor_for_host(host):
    """The VENDOR a URL host belongs to, or None when it is not a recognised one.

    Only ever a token from this module's own table -- never the raw host -- so the published
    vocabulary stays closed even though it is larger than the category set. `acme.atlassian.net`
    yields `atlassian`, never `acme`: a subdomain is frequently the CUSTOMER's own name and must
    not cross.
    """
    h = (host or "").strip().lower().rstrip(".")
    if not h or h in LOCAL_HOSTS or "." not in h:
        return None
    if any(h == d or h.endswith("." + d) for d in NOT_A_SYSTEM):
        return None
    for suffix, (vendor, _cat) in _HOST_SUFFIX.items():
        if h == suffix or h.endswith("." + suffix):
            return vendor
    label = _strip_tld(h)
    return label if label in BRAND else None


def vendor_for_brand(token):
    """The vendor an MCP brand token names, or None when the table does not know it or the
    token is one of the ordinary English words that only a HOST can establish."""
    t = (token or "").strip().lower()
    return t if (t in BRAND and t not in AMBIGUOUS) else None


def vendor_for_program(prog):
    """The vendor a CLI program is a client for, or None."""
    got = CLI_CLIENT.get((prog or "").strip().lower())
    return got[0] if got else None


def system_vendor(category, vendor):
    """`<category>:<vendor>`, or None when either half is missing or the category is not a
    recognised one.

    ⚠️ `unrecognized` NEVER pairs. The whole content of this level is "we can name this", so a
    pair whose vendor we could not name has nothing to say -- and the rendering decision it
    supports is to show only nameable systems. An unnameable one is already carried by
    `system_categories` as `unrecognized`, which is where that fact belongs.
    """
    if not category or not vendor or category == "unrecognized":
        return None
    return f"{category}:{vendor}"


def category_for_program(prog):
    """Category for a CLI program, or None when it is not a client for an external system.
    None, not `unrecognized`: `ls` is not an uncategorised external system, it is not one at
    all, and counting every local command as unrecognized would drown the level."""
    got = CLI_CLIENT.get((prog or "").strip().lower())
    return got[1] if got else None


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
