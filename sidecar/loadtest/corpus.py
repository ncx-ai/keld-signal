"""Realistic sidecar request payloads. Schemas mirror what the Go enrich client
sends (internal/agent/enrich/labels.go): classification tasks + entity labels.
Deterministic under a seeded Random; no PII in the corpus."""

TASKS = {
    "task_type": ["summarization", "translation", "code_generation", "information_extraction",
                  "classification", "reasoning", "question_answering", "text_generation",
                  "rewriting", "general"],
    "domain": ["software", "legal", "medical", "finance", "science",
               "business", "education", "creative", "general"],
    "sensitivity": ["none", "pii", "secrets", "phi", "pci", "proprietary"],
    "activity_type": ["generate", "transform", "analyze", "retrieve", "converse", "review"],
}

ENTITY_LABELS = {
    "language": "Programming languages such as Python, Rust, TypeScript",
    "framework": "Software frameworks such as Django, React, FastAPI",
    "library": "Software libraries or packages such as numpy, pandas, requests",
    "org": "Organizations, companies, or institutions",
    "product": "Named products, tools, or services",
    "email": "Email addresses",
    "person": "Personal names of individuals",
}

_BASE = [
    "Write a Python function that parses a CSV file and returns rows as dicts.",
    "Refactor this Django view to use the ORM efficiently and add pagination.",
    "Summarize the quarterly revenue report and highlight risks for finance.",
    "Debug why the FastAPI websocket disconnects under load and propose a fix.",
    "Translate this API error message into French for the product UI.",
    "Given these logs, analyze the root cause of the memory spike in the service.",
]

LEN_BUCKETS = [200, 1000, 5000, 15000, 20000]


def _text(rng, target_len):
    parts = []
    total = 0
    while total < target_len:
        s = rng.choice(_BASE)
        parts.append(s)
        total += len(s) + 1
    return (" ".join(parts))[:target_len]


def make_request(rng, target_len):
    """Return (path, body) for a random endpoint at ~target_len characters."""
    text = _text(rng, target_len)
    kind = rng.choice(("classify", "entities", "extract"))
    if kind == "classify":
        return "/classify", {"text": text, "tasks": TASKS}
    if kind == "entities":
        return "/entities", {"text": text, "labels": ENTITY_LABELS}
    return "/extract", {"text": text, "labels": ENTITY_LABELS,
                        "tasks": {"task_type": TASKS["task_type"]}}
