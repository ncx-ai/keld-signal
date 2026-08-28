package sidecar

import (
	"net"
	"regexp"
	"strings"
	"unicode"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// AnalyzeLabeled is Analyze in the shape the enrichment pipeline consumes: the
// window's deterministic dimensions as enrich.Labeled, plus the same window's
// dynamics as enrich.Dynamic, both keyed by dimension. It satisfies
// enrich.WorkstreamAnalyzer, and is what the daemon wires into
// enrich.WithWorkstreams.
//
// It is the ONE chokepoint where /analyze becomes publishable, which is why both
// halves convert here rather than one here and one somewhere later.
//
// The conversion is deliberately lossy, and this is the whole of it:
//
//   - Share -> Confidence. Share is already a 0..1 dominance fraction (the
//     proportion of the window's evidence the winning value holds; a dimension
//     below the 0.50 floor is reported unattributed rather than won), so it
//     reads exactly as a confidence and needs no rescaling.
//
//   - Value -> Value, unchanged.
//
//   - Provenance is DROPPED, and it is NO LONGER a constant. `repo` publishes
//     `known:daemon_git` where every other dimension publishes
//     `known:tool_inputs`, because `repo`'s rows come from facts the daemon read
//     off disk rather than from tool-call metadata counted in the transcript. So
//     the field now does distinguish something on-device — but it is still
//     dropped here, because Labeled has no field for it and folding it into
//     Producer would make the producer string a second, unparsed data channel.
//     Giving it a real field is a widening of the published contract with its
//     own argument to make; a consumer that needs to know which dimension is
//     daemon-resolved knows it from the dimension NAME, which is fixed.
//
//   - `resolved` travels the OTHER WAY: in, on the request. See
//     enrich.ResolvedFacts for why the daemon is the only component that may
//     resolve it and why the analysis is the right place to send it.
//
//   - Evidence is DROPPED, and this one costs information: share=1.0 over 1
//     observation and share=1.0 over 500 look identical downstream. Labeled has
//     no field for it and widening the published enrichment contract is not
//     this change's job. If Atlas needs to weight a dimension by how much of
//     the window backs it, the right move is a dedicated wire type for
//     workstreams rather than overloading Labeled.
//
//   - Session / WindowStart / WindowEnd stay local: window metadata, useful for
//     debugging on-device, with no business on the published payload.
//
//   - The response's inventory block contributes ALL THIRTEEN of its keys:
//     `physical_acts` (convertActs), `files`/`directories`/`components`
//     (convertPathInventory), `harness_tools`/`integrations`/`file_types`/
//     `subagents`/`mcp_servers` (convertIdentifierInventory), `programs`
//     (convertProgramInventory), `external_systems`
//     (convertExternalSystemInventory), `shell_verbs`
//     (convertShellVerbInventory) and `named_terms` (convertNamedTerms).
//     `named_terms` was withheld until it was decided otherwise; it is the
//     only one drawn from message TEXT rather than tool-call inputs, and the
//     only one observed to contain real person names. See InventoryBlock for
//     that decision and why no person-name filter accompanies it.
//
//   - InventoryOmitted forwards UNCHANGED: it is a map of dimension name to a
//     COUNT of values cut, never a value, so it carries no privacy weight —
//     "programs cut 3" says nothing about which three. Nil rather than an empty
//     map when nothing was cut (see enrich.WindowAnalysis.InventoryOmitted).
//
//   - The SESSION PRIOR block converts field-for-field (see convertPrior) into a
//     map that is SEPARATE from Workstreams and never merged into it. That
//     separation is the design: the prior is reported alongside the window's own
//     answer and never supplies one it lacked, so an unattributed window stays
//     unattributed. The block's `clamped` flag is dropped — AnalyzeResult does
//     not model it (see PriorBlock).
//
//   - The DYNAMICS block converts field-for-field (see convertDynamics) for the
//     six derived fields and drops everything else structurally — AnalyzeResult
//     models no per-side value, no timestamp, no sizer detail. What this function
//     adds on top is a VOCABULARY GATE: `status` and `reading` are closed
//     published sets gated by enrich.SchemaVersion, and the sidecar that computes
//     them is frozen and shipped separately from keld-agent (an older or newer one
//     can sit in ~/.local/bin indefinitely). A value this binary does not
//     recognise is version skew, and forwarding it would publish a label no Atlas
//     consumer's vocabulary contains — the same rule that keeps masked spans to
//     matched ids only. The whole dimension is dropped, not half-published: a
//     reading without a readable status is not interpretable.
//
// Producer is left unset: the pass stamps its own version onto every dimension
// it emits (see enrich.WorkstreamsExtractor), the same way every other pass
// does, so attribution does not depend on which analyzer supplied the map.
//
//   - The WORKSTREAM dimensions carry `status` and `evidence` through onto the
//     Labeled, and that is the whole of what makes an unattributed dimension
//     publishable rather than deleted. It is the SAME VOCABULARY GATE as the two
//     blocks above (enrich.KnownWorkstreamStatus, mirroring the sidecar's
//     window.REASONS): a status this binary cannot read is version skew, and the
//     dimension drops whole rather than publishing a value with an unreadable
//     outcome beside it — a `thin` value rendered as an attributed one is exactly
//     the misreading the status exists to prevent, so a value with no readable
//     status is worse than no value.
//
// ok=false is propagated unchanged — a failed analysis is not an empty one.
//
// TWO WAYS A DIMENSION CAN SAY "no dominant value", and they come from different
// sidecars:
//
//   - JSON null (a nil *Workstream) is what a sidecar OLDER than SCHEMA 16
//     sends, and it is still OMITTED. There is nothing else to do with it: that
//     sidecar deleted the count before answering, so there is no evidence and no
//     status to publish, and a zero Labeled would state an outcome of "" that
//     nobody can read.
//
//   - An object with `status` naming the outcome is what SCHEMA 16 and later
//     send for every dimension, attributed or not, and it is CARRIED. Under
//     `absent` that is a Labeled with an empty Value and Evidence 0 — a real
//     answer ("this level never fired"), readable as such only because Status
//     says so.
//
// An object with NO status at all is the third case and also comes from a
// pre-16 sidecar, which emitted an object only for a dimension it had
// attributed. It is read as "attributed" for exactly that reason. Defaulting it
// to anything else — or dropping it — would blank every workstream on a machine
// whose frozen sidecar has not been updated, and those machines exist: the
// sidecar ships separately and can sit in ~/.local/bin indefinitely.
func (c *Client) AnalyzeLabeled(path, promptID string, spanMinutes int,
	resolved enrich.ResolvedFacts) (enrich.WindowAnalysis, bool) {
	res, ok := c.Analyze(path, promptID, spanMinutes, resolved)
	if !ok {
		return enrich.WindowAnalysis{}, false
	}
	return analysisFrom(res.Workstreams, res.Inventory, res.InventoryOmitted,
		res.Dynamics, res.Effort, res.Prior), true
}

// analysisFrom is THE ONE conversion from a sidecar analysis payload into the
// enrich.WindowAnalysis every published row carries. Every gate described in
// AnalyzeLabeled's comment above lives behind this call.
//
// It is shared by all three callers — a prompt's window (/analyze), a tick's
// window (/tick) and a v2 BLOCK (/blocks) — and that sharing is the point, not
// a convenience. These are the same deterministic analysis over different
// bounds, so a dimension must not be readable on one row and deleted on
// another, and a NEW inventory key must not be able to reach one row's wire
// shape while silently missing from the others. A second copy of this
// assignment list is exactly how that divergence happens; there was one before
// the block path arrived, and this removes it rather than adding a third.
//
// It is not a v2-reaches-into-v1 seam either: it composes the per-field convert
// functions, which are the measured definitions, and holds no window-specific
// or block-specific knowledge at all.
func analysisFrom(ws map[string]*Workstream, inv InventoryBlock, omitted map[string]int,
	dyn DynamicsBlock, eff *EffortBlock, prior PriorBlock) enrich.WindowAnalysis {
	dims := make(map[string]enrich.Labeled, len(ws))
	for dim, w := range ws {
		l, keep := labeledWorkstream(w)
		if !keep {
			continue
		}
		dims[dim] = l
	}
	return enrich.WindowAnalysis{
		Workstreams:      dims,
		PhysicalActs:     convertActs(inv.PhysicalActs),
		Files:            convertPathInventory(inv.Files),
		Directories:      convertPathInventory(inv.Directories),
		Components:       convertPathInventory(inv.Components),
		HarnessTools:     convertIdentifierInventory(inv.HarnessTools),
		Programs:         convertProgramInventory(inv.Programs),
		ExternalSystems:  convertExternalSystemInventory(inv.ExternalSystems),
		Integrations:     convertIdentifierInventory(inv.Integrations),
		NamedTerms:       convertNamedTerms(inv.NamedTerms),
		FileTypes:        convertIdentifierInventory(inv.FileTypes),
		ShellVerbs:       convertShellVerbInventory(inv.ShellVerbs),
		Subagents:        convertIdentifierInventory(inv.Subagents),
		McpServers:       convertIdentifierInventory(inv.McpServers),
		InventoryOmitted: convertInventoryOmitted(omitted),
		Dynamics:         convertDynamics(dyn),
		Effort:           convertEffort(eff),
		Prior:            convertPrior(prior),
	}
}

// labeledWorkstream converts one /analyze dimension into the Labeled the pass
// publishes, reporting whether it may be carried at all. Shared by AnalyzeLabeled
// and the tick path so the two cannot answer the same dimension differently.
//
// The three cases and why each behaves as it does are in AnalyzeLabeled's
// comment above: nil drops (a pre-16 sidecar deleted the count, so there is
// nothing to state), an empty status is read as "attributed" (a pre-16 sidecar
// answered with an object ONLY when it had attributed), an unrecognised status
// drops (version skew — a value whose outcome cannot be read is worse than no
// value, because it renders as a confident one).
//
// Confidence is the dimension's share, unchanged: a 0..1 dominance fraction is
// the natural confidence, and it is now readable as such because Status says
// what it is a fraction OF.
func labeledWorkstream(w *Workstream) (enrich.Labeled, bool) {
	if w == nil {
		return enrich.Labeled{}, false
	}
	status := w.Status
	if status == "" {
		status = "attributed"
	}
	if !enrich.KnownWorkstreamStatus(status) {
		return enrich.Labeled{}, false
	}
	return enrich.Labeled{
		Value: w.Value, Confidence: w.Share, Evidence: w.Evidence, Status: status,
	}, true
}

// convertPrior is the same VOCABULARY GATE convertDynamics is, applied to the
// SESSION PRIOR block: `status` is a closed published set (enrich.PriorStatuses,
// pinned against the sidecar's window.REASONS), and a value this binary does not
// recognise is version skew from a separately-shipped sidecar. The whole
// dimension drops rather than half of it — a `departure` of 0.516 with an
// unreadable status is a number a reader cannot place, since whether the session
// was attributed at all is exactly what the status says.
//
// IT IS A SEPARATE MAP FROM Workstreams, AND THAT IS THE WHOLE DESIGN. The prior
// is a CONTRAST, never a fallback: it is reported alongside the window's own
// answer and never supplies one the window lacked. Nothing here reads
// res.Workstreams, so a dimension the window could not attribute cannot be
// filled in from the session by this function or by anything downstream of it —
// structurally, not by a comment. Inheriting would launder "we do not know" into
// something confident, which is the defect the sidecar's MIN_EVIDENCE exists to
// prevent and which this project has paid for twice.
//
// The DIMENSION SET is the sidecar's decision, forwarded rather than restated
// (the same rule Workstreams already follows). Which dimensions carry a contrast
// is an empirical result that HAS moved (`output_type` was added after the
// first measurement; `tooling` is the one remaining candidate) — and a second
// list on this side would be a second thing to drift.
// It is safe to forward because the sidecar derives the prior's vocabulary from
// its own ALLOCATION list, so a prior can only ever name a value that publishes
// in `workstreams` beside it; `named_terms` is structurally not addable there.
//
// Nil rather than an empty map when nothing survives — including when the whole
// block is absent, which is what a sidecar too old to compute it sends: the pass
// then publishes no key at all instead of an empty object, which would read as
// "we looked at the session and it said nothing".
func convertPrior(b PriorBlock) map[string]enrich.Prior {
	var out map[string]enrich.Prior
	for dim, p := range b.Dimensions {
		// A null dimension is no prior at all; publishing a zero Prior would
		// state a status of "", i.e. a real-looking outcome nobody can read.
		if p == nil {
			continue
		}
		if !enrich.KnownPriorStatus(p.Status) {
			continue
		}
		if out == nil {
			out = make(map[string]enrich.Prior, len(b.Dimensions))
		}
		out[dim] = enrich.Prior{
			Value: p.Value, Share: p.Share, Evidence: p.Evidence, Status: p.Status,
			Agrees: p.Agrees, Departure: p.Departure, Novel: p.Novel,
		}
	}
	return out
}

// convertActs is the same VOCABULARY GATE convertDynamics and convertEffort are,
// applied per ENTRY rather than to the block. `enrich.Acts` is a closed published
// set (22 values) gated by enrich.SchemaVersion, and the sidecar that computes it
// is frozen and shipped separately from keld-agent — an older or newer one can sit
// in ~/.local/bin indefinitely — so a value this binary does not recognise is
// version skew, and forwarding it would publish a label no Atlas consumer's
// vocabulary contains.
//
// PER-ENTRY, and that is the one place the rule differs from its two siblings. A
// dynamics reading or an effort block is a single joined statement, uninterpretable
// in half, so the whole of it drops. An inventory is a list of independent items —
// "what was done" — so one unreadable item costs exactly one item, and dropping the
// list instead would discard every act this binary does understand because of one
// it does not. Nothing publishes a total against which the surviving counts could
// look inconsistent, so there is no partial-sum to be wrong.
//
// NO TRUNCATION. The sidecar publishes this level whole on purpose: its vocabulary
// is closed, so the payload is bounded at 22 entries by construction, and a top-N
// cut would only reintroduce the arbitrary-tie-at-the-boundary effect the open
// levels have to live with (see workstreams.INVENTORY's cap column). Do not add
// one here either.
//
// Nil rather than an empty slice when nothing survives — including when the whole
// block is absent, which is what a sidecar too old to compute it sends: the pass
// then publishes no key at all instead of an empty list, which would read as "we
// looked and the hour did nothing".
func convertActs(items []InventoryItem) []enrich.Act {
	var out []enrich.Act
	for _, it := range items {
		if !enrich.KnownAct(it.Value) {
			continue
		}
		out = append(out, enrich.Act{Value: it.Value, N: it.N})
	}
	return out
}

// notWorkspaceRelative mirrors the assertion
// sidecar/app/test_analysis_window.py pins on the producing side: `reconcile()`
// normalizes every `file`/`dir`/`component` value against the workspace root
// before the sidecar ever answers with it, verified over all 500 real corpus
// transcripts plus John's (zero absolute paths, zero `~`/`/Users`/`/home`
// paths, zero `../` escapes, zero URLs, zero Windows drive paths). Checking it
// AGAIN here, at the decode boundary, is defence in depth — the same reasoning
// as the "loopback is not an external system" filter and the wire-shape test
// below: a sidecar that ever regressed on this must not have its output
// forwarded unfiltered just because nothing local happened to catch it.
var notWorkspaceRelative = regexp.MustCompile(`^/|^~|^[A-Za-z]:|\.\.(/|$)`)

// convertPathInventory converts one of the three OPEN-vocabulary path
// inventories (files/directories/components). PER-ENTRY, the same shape
// convertActs uses, for the analogous reason: an inventory is a list of
// independent items, so one bad entry costs exactly that entry rather than the
// whole list. There is no closed table to check membership against here — see
// enrich.PathCount — so the gate is the structural relative-path invariant
// instead of vocabulary membership.
func convertPathInventory(items []InventoryItem) []enrich.PathCount {
	var out []enrich.PathCount
	for _, it := range items {
		if it.Value == "" || notWorkspaceRelative.MatchString(it.Value) {
			continue
		}
		out = append(out, enrich.PathCount{Value: it.Value, N: it.N})
	}
	return out
}

// identifierShape matches a bare, single-token identifier: the shape a harness
// tool name (Bash, ToolSearch, SendMessage), an MCP tool id (notion-fetch) or a
// shell program name (git, pnpm, docker-compose) all take. Non-empty, and holds
// only letters, digits, underscore, hyphen and dot — no whitespace and no
// path/URL punctuation ("/", "\", ":", "@", ...). It does not by itself reject
// a leading dot: a dot is fine in the MIDDLE of an identifier (docker-compose,
// python3.12), so `programs` layers its own explicit leading-dot rejection on
// top (see convertProgramInventory) rather than this shared shape doing it for
// every caller.
var identifierShape = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// convertIdentifierInventory converts the two OPEN-vocabulary inventories whose
// values are bare identifiers: harness_tools (level `tool`) and integrations
// (level `mcp_tool`). PER-ENTRY, the same shape convertActs/convertPathInventory
// use, for the same reason: an inventory is a list of independent items, so one
// bad entry costs exactly that entry.
//
// Deliberately NOT a hardcoded allowlist of known tool/MCP names. The
// harness's own tool set genuinely grows — ToolSearch, Artifact and
// SendMessage are all recent additions measured in the same corpus this gate
// was built from — and a stale allowlist would silently drop a legitimate new
// tool, which is worse than forwarding an identifier a shape gate already
// bounds.
func convertIdentifierInventory(items []InventoryItem) []enrich.NameCount {
	var out []enrich.NameCount
	for _, it := range items {
		if it.Value == "" || !identifierShape.MatchString(it.Value) {
			continue
		}
		out = append(out, enrich.NameCount{Value: it.Value, N: it.N})
	}
	return out
}

// shellVerbShape bounds `shell_verbs`, and is the one of the four newly-wired
// inventories that cannot use identifierShape: a verb is a COMMAND, not a single
// token — `git rebase`, `pnpm test`, `docker compose up` — so a bare-identifier
// gate would silently drop every multi-word value, which is the whole class this
// dimension exists to carry and the reason it beats `programs` (the binary
// alone). The other three ARE single tokens (`.tsx`, `general-purpose`, `notion`)
// and keep identifierShape.
//
// What it does reject is anything that could not have come out of the sidecar's
// bashlex-based segment head: a path separator (a filename is not a command —
// the same defect convertProgramInventory closes for `programs`), and anything
// long enough to be a command LINE rather than a verb. `sh -c "…"` puts a whole
// script in one argument, and the sidecar's own extraction is what should keep
// that out; this bounds it here too, at the decode boundary, on the same
// defence-in-depth reasoning as notWorkspaceRelative.
var shellVerbShape = regexp.MustCompile(`^[A-Za-z0-9_.:+@=-]+(?: [A-Za-z0-9_.:+@=/-]+)*$`)

// shellVerbMaxLen bounds a verb. Measured, the level holds a segment head plus
// at most a subcommand or two, so this is generous by an order of magnitude and
// exists only so a pathological extraction cannot publish a shell script.
const shellVerbMaxLen = 64

// convertShellVerbInventory converts the `shell_verbs` inventory (level `verb`).
// PER-ENTRY like every sibling: one unusable value costs exactly that value.
func convertShellVerbInventory(items []InventoryItem) []enrich.NameCount {
	var out []enrich.NameCount
	for _, it := range items {
		if it.Value == "" || len(it.Value) > shellVerbMaxLen {
			continue
		}
		if strings.ContainsAny(it.Value, `/\`) || !shellVerbShape.MatchString(it.Value) {
			continue
		}
		out = append(out, enrich.NameCount{Value: it.Value, N: it.N})
	}
	return out
}

// termShape bounds `named_terms`, and is deliberately LOOSER than
// identifierShape: a named term is prose-derived and legitimately multi-word
// ("Developer Preview"), so a bare-identifier shape would silently drop every
// term containing a space — a whole class of the values this inventory exists
// to carry.
//
// What it does reject is a value that could not have come from the sidecar's
// own normalisation: terms.py collapses internal whitespace
// (`" ".join(s.split())`), strips surrounding punctuation and drops anything of
// one character or purely numeric, so a control character or newline arriving
// here means the value did not take that path. Rejecting it is defence against
// a future producer, not distrust of the current one.
//
// It is a BOUND, not a filter on meaning. It cannot tell "ACME" from
// "Federico", and nothing here tries — see InventoryBlock on why a person-name
// filter at spaCy's measured ~1% precision would be worse than none.
const termMaxLen = 128

func termShaped(v string) bool {
	if v == "" || len(v) > termMaxLen {
		return false
	}
	for _, r := range v {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// convertNamedTerms converts the `named_terms` inventory (level `term`),
// PER-ENTRY like every other inventory here: one unusable value costs exactly
// that value, not the list.
func convertNamedTerms(items []InventoryItem) []enrich.NameCount {
	var out []enrich.NameCount
	for _, it := range items {
		if !termShaped(it.Value) {
			continue
		}
		out = append(out, enrich.NameCount{Value: it.Value, N: it.N})
	}
	return out
}

// programLeadingDot is the one extra rejection `programs` layers on top of
// identifierShape. identifierShape's own character class already excludes a
// path separator, so the explicit separator check convertProgramInventory
// applies below is defence in depth against a future, wider identifierShape —
// the leading-dot check is the one doing real work today: it closes a measured
// defect, `.env.example` (a filename, not a program) reaching the sidecar's
// bashlex-based exe extraction. Neither restriction is applied to
// harness_tools/integrations: a tool name or MCP id has no reason to start
// with a dot, but there is also no measured defect there to justify adding an
// unevidenced restriction.
var programLeadingDot = regexp.MustCompile(`^\.`)

// convertProgramInventory converts the `programs` inventory (level `exe`):
// identifier shape, PLUS a rejection of anything containing a path separator or
// starting with a leading dot. PER-ENTRY, same reasoning as
// convertIdentifierInventory.
func convertProgramInventory(items []InventoryItem) []enrich.NameCount {
	var out []enrich.NameCount
	for _, it := range items {
		if it.Value == "" || !identifierShape.MatchString(it.Value) {
			continue
		}
		if strings.ContainsAny(it.Value, `/\`) || programLeadingDot.MatchString(it.Value) {
			continue
		}
		out = append(out, enrich.NameCount{Value: it.Value, N: it.N})
	}
	return out
}

// convertExternalSystemInventory converts the `external_systems` inventory
// (level `service`) on exactly one structural rule: reject a BARE IP LITERAL
// (v4 or v6) and keep everything else — INCLUDING internal and corporate
// hostnames.
//
// ⚠️ THE MEASUREMENT BEHIND THIS DOES NOT GENERALISE, AND THIS GATE MUST NOT BE
// WRITTEN AS IF IT DID. The corpus this dimension was measured over is one
// developer's machine doing open-source work: 0 RFC1918 addresses, 0 `.local`,
// 0 `.internal`, 0 corporate hostnames — because none of those COULD appear on
// it. An enterprise user's transcripts DO produce `jenkins.corp.internal`,
// `gitlab.acme.com`, `10.x.x.x`, so "the corpus was clean" cannot be the
// argument for what this gate does. The argument has to be structural, and it
// is:
//
//   - An IP literal is not a meaningful OBSERVABILITY category on its own: it
//     is unstable (a service's address can change across requests), unreadable
//     without a reverse lookup nobody here performs, and it is the value MOST
//     likely to identify a specific machine or a specific customer's endpoint —
//     closer to a PII-shaped fact than to "which system did this hour touch".
//   - A HOSTNAME, by contrast, comes from a tool-call INPUT (a URL fetched, a
//     host connected to) — the SAME provenance `files`/`branch` already have,
//     and both of those already publish org-identifying strings (a repo path, a
//     branch name) without controversy. "Which internal systems does AI-driven
//     work touch" is precisely the observability question this dimension
//     exists to answer, and an org's own `jenkins.corp.internal` is the most
//     on-topic answer it could give — filtering it out because it LOOKS
//     internal would defeat the dimension for every enterprise user while
//     protecting nothing for the open-source one this corpus happens to be.
//
// So: reject net.ParseIP(value) != nil — it matches both address families and
// nothing else, since no hostname parses as a bare IP — and keep everything
// else whole.
func convertExternalSystemInventory(items []InventoryItem) []enrich.NameCount {
	var out []enrich.NameCount
	for _, it := range items {
		if it.Value == "" || net.ParseIP(it.Value) != nil {
			continue
		}
		out = append(out, enrich.NameCount{Value: it.Value, N: it.N})
	}
	return out
}

// convertInventoryOmitted forwards the sidecar's cut-counts unchanged: it is a
// map of dimension name to a COUNT of values dropped, never a value, so there
// is nothing here to gate. Nil rather than an empty map when nothing was cut —
// including when the sidecar is too old to send the key at all — so the two
// read the same to a consumer, neither able to name a value that was lost.
func convertInventoryOmitted(m map[string]int) map[string]int {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// convertDynamics is the vocabulary gate described above. It returns nil rather
// than an empty map when nothing survives (or when the sidecar sent no block at
// all), so the pass publishes no dynamics key instead of an empty object that
// would read as "we compared and found nothing".
func convertDynamics(b DynamicsBlock) map[string]enrich.Dynamic {
	var out map[string]enrich.Dynamic
	for dim, d := range b.Dimensions {
		// A null dimension is no comparison at all; publishing a zero Dynamic
		// would state a status of "", i.e. a real-looking outcome nobody can read.
		if d == nil {
			continue
		}
		if !enrich.KnownDynamicStatus(d.Status) || !enrich.KnownDynamicReading(d.Reading) {
			continue
		}
		if out == nil {
			out = make(map[string]enrich.Dynamic, len(b.Dimensions))
		}
		out[dim] = enrich.Dynamic{
			Status: d.Status, Reading: d.Reading, Changed: d.Changed,
			Turnover: d.Turnover, Decay: d.Decay,
			ConcentrationShift: d.ConcentrationShift,
		}
	}
	return out
}

// convertEffort is the same vocabulary gate convertDynamics is, applied to the
// effort block. Three closed published sets (enrich.Tempos, enrich.TempoStatuses,
// enrich.AuthoredStatuses) are checked; a value this binary does not recognise is
// version skew from a separately-shipped sidecar, and forwarding it would publish
// a label no Atlas consumer's vocabulary contains.
//
// The WHOLE block is dropped rather than half of it, and rather than being
// repaired: a share with an unreadable status is not interpretable, and silently
// substituting a status we did recognise would invent the one thing this block
// exists to state. An unreadable block is not a failed analysis either — the
// digest and dynamics beside it still publish.
//
// Nil in, nil out: a sidecar too old to compute the block must not produce a
// zeroed Effort, whose every count reads 0 and whose every status reads "".
func convertEffort(b *EffortBlock) *enrich.Effort {
	if b == nil {
		return nil
	}
	if !enrich.KnownTempo(b.Tempo) || !enrich.KnownTempoStatus(b.TempoStatus) ||
		!enrich.KnownAuthoredStatus(b.AuthoredStatus) {
		return nil
	}
	return &enrich.Effort{
		AuthoredBytes:  b.AuthoredBytes,
		AuthoringTurns: b.AuthoringTurns,
		AuthoredStatus: b.AuthoredStatus,
		FastShare:      b.FastShare,
		Gaps:           b.Gaps,
		Tempo:          b.Tempo,
		TempoStatus:    b.TempoStatus,
		RequestTokens:  b.RequestTokens,
		GapP50S:        b.GapP50S,
		GapP90S:        b.GapP90S,
	}
}
