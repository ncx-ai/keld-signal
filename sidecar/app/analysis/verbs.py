"""`activity_verb` -- the atv1 VERB an inference request's capability maps to.

⚠️ THIS ADDS NO CLASSIFIER. It maps `reqclass.route_class`'s existing nine-value
output -- measured at macro F1 0.920 against its author's blind labels and 0.815
against an independent labeller's -- onto the `verb` column of
`keld-activity-types-v1.csv` (atv1). Four of the six work-bearing classes map
DEFINITIONALLY: `verify -> review` is a statement about what two words mean, and
measuring it would measure a labeller's reading of a dictionary.

⚠️ THREE CLASSES PUBLISH NO VERB, AND THAT IS A SCOPE DECISION RATHER THAN A GAP
IN atv1. `operate` is state change -- `git push`, `mkdir`, `docker run`. atv1 is a
taxonomy of model CAPABILITIES (its own verb_notes: "Richest verb - context drives
model choice and price"), and composing `git push` stresses none that routing could
act on. Mapping it to atv1's `other` was considered and REJECTED: it would bucket
running a command with a genuine abstention and discard a separation reqclass
measures at F1 0.920. `acknowledge` is a bare "done"; `unclassified` is an honest
abstention and stays one.

⚠️ `synthesize` AND `retrieve` ABSTAIN, AND THERE IS NO UNSPLIT PARENT TO FALL
BACK TO -- those are reqclass CLASS names, not atv1 verbs, and atv1 has no value
meaning "retrieved something, unspecified". They resolve only once the split study
(docs/superpowers/specs/2026-10-01-activity-verb-mapping-design.md section 4)
passes its pre-registered bar. `retrieve` is 37.3% of engineering requests, so
this abstention is most of the axis's missing coverage, not a rounding error.

THE VOCABULARY IS DECLARED WHOLE AND DOES NOT GROW. All nine verbs are listed
below although only six are produced today, so the study's outcome changes which
values are PRODUCED and never the published vocabulary -- a passing study needs no
second schema bump.
"""

# All nine. Cap 9 at the emission site is this whole closed set, so the level can
# never be truncated and `inventory_omitted` can never name it.
VERBS = ("code.write", "code.edit", "text.create", "text.transform",
         "text.summarize", "review", "extract", "plan", "research")

# ⚠️ FAMILY IS A PURE FUNCTION OF VERB -- verified across all 68 atv1 rows, every
# verb maps to exactly one family. It is therefore NOT a published level: Atlas
# renders the family column from this table, which ships as part of the contract.
# `system_categories` earns its own level because it is strictly LESS identifying
# than its neighbour `external_systems`; family has no comparable justification,
# and a level costs a schema bump, a fixture rebaseline and an Atlas column.
VERB_FAMILY = {
    "code.write": "code",          "code.edit": "code",
    "text.create": "language",     "text.transform": "language",
    "text.summarize": "language",
    "review": "understanding",     "extract": "understanding",
    "plan": "agentic",             "research": "agentic",
}

EXCLUDED = frozenset({"operate", "acknowledge", "unclassified"})
PENDING_SPLIT = frozenset({"synthesize", "retrieve"})

# `Write` creates; everything else in AUTHOR_TOOLS modifies something that exists.
_CREATE_TOOLS = {"Write"}
_EDIT_TOOLS = {"Edit", "MultiEdit", "NotebookEdit"}

_DIRECT = {"verify": "review", "delegate": "plan"}
_AUTHOR = {"author_code": ("code.write", "code.edit"),
           "author_prose": ("text.create", "text.transform")}

# All classes that are explicitly handled (excluded, pending, direct, or author).
# Used to ensure no new class added to reqclass silently abstains.
HANDLED = EXCLUDED | PENDING_SPLIT | frozenset(_DIRECT) | frozenset(_AUTHOR)


def verb_for(cls, tools):
    """Map one request's activity class + tool evidence to an atv1 verb, or None.

    `tools` is the list of (name, input) pairs `reqclass.route_class` already
    receives. None means "publish no verb for this request" and is returned for
    an excluded class, a pending split, an unknown class, and an authoring class
    whose evidence does not say which side it is.
    """
    # Explicitly excluded classes: not work, scope decision not a gap in atv1.
    if cls in EXCLUDED:
        return None
    # Pending splits: awaiting a study to resolve the choice.
    if cls in PENDING_SPLIT:
        return None
    # Direct mappings: definitional, F1 0.920.
    if cls in _DIRECT:
        return _DIRECT[cls]
    # Authoring: splits on tool evidence (create vs edit).
    if cls in _AUTHOR:
        create, edit = _AUTHOR[cls]
        # DO NOT GUESS. `classify_bash` and CODE_TOOLS reach `author_code`
        # with no authoring tool in evidence (`python3 -c`, `javascript_tool`).
        # A guess would publish a false claim about the work from evidence
        # that does not contain the answer. Likewise for `author_prose`.
        saw_create = False
        saw_edit = False
        for name, _ in tools:
            bare = name.split("__")[-1]
            if bare in _CREATE_TOOLS:
                saw_create = True
            if bare in _EDIT_TOOLS:
                saw_edit = True
        # If both create and edit are present, prefer create: it is the
        # larger claim about the work (a new thing vs modifying existing).
        if saw_create:
            return create
        if saw_edit:
            return edit
        return None
    # Unknown class: not a handled reqclass.
    return None


def family_for(verb):
    """The atv1 family of a verb, or None. A 9->4 rollup, never an inference."""
    return VERB_FAMILY.get(verb)
