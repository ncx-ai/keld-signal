package publish

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// renderReport turns an assembled Enrichment into a one-line `summary` and a
// bulleted `report`.
//
// ⚠️ IT READS ONLY THE ENRICHMENT, never the Profile and never the sidecar's
// analyze payload, and that is the whole privacy argument. A rendered string is
// the one thing this codebase cannot gate structurally: a typed field can be
// given no wire representation, but any value can be interpolated into prose.
// Projecting the WIRE TYPE instead of its sources means every fact in the
// report is already published beside it by construction, so the report can add
// no exposure — no new gate, no forbidden-key list, nothing to keep in sync.
// Anyone extending this must keep that property: if you find yourself needing a
// field Enrichment does not carry, publish the field first.
//
// ⚠️ IT STATES CONCLUSIONS AND PRE-COMPUTES RATIOS, and that is not styling.
// Measured on the same windows: a digest that labels each number and states the
// conclusion scored +36.7 on synthesis accuracy, while emitting the same facts
// as raw numbers scored -3.3/-20.0 — WORSE than supplying no context at all —
// and all 14 of that arm's full-document failures were the single question
// where the reader was handed two numbers and had to divide them. So
// `authored_bytes`/`authoring_turns` is rendered as bytes-per-turn, and the
// act counts as a ratio with its reading. The structured fields beside it stay
// authoritative; this is the half that survives being read quickly.
//
// Every clause is DROPPED when the number behind it is missing, so the report
// never asserts something the pipeline did not establish. A facet that was
// skipped or degraded is named rather than silently omitted.
// reportSource is the union of fields the renderer reads. Both wire types fill it, so the
// prompt row and the window row render through ONE implementation — a second copy would drift,
// and the window row is the one nobody looks at until it is wrong.
type reportSource struct {
	Workstreams      map[string]enrich.Labeled
	Dynamics         map[string]enrich.Dynamic
	Prior            map[string]enrich.Prior
	Effort           *enrich.Effort
	PhysicalActs     []enrich.Act
	Files            []enrich.PathCount
	Directories      []enrich.PathCount
	Components       []enrich.PathCount
	HarnessTools     []enrich.NameCount
	Programs         []enrich.NameCount
	ExternalSystems  []enrich.NameCount
	Integrations     []enrich.NameCount
	FileTypes        []enrich.NameCount
	ShellVerbs       []enrich.NameCount
	Subagents        []enrich.NameCount
	McpServers       []enrich.NameCount
	NamedTerms       []enrich.NameCount
	InventoryOmitted map[string]int
	// Text-facet fields, zero for a window row — a tick carries no text facets at all, so
	// the sensitivity clause is simply absent there rather than reporting a false negative.
	Sensitivity      enrich.Labeled
	SensitivitySpans []enrich.Entity
	PipelineStatus   string
	FacetsSkipped    []string
	FacetsDegraded   []string
}

func (e Enrichment) reportSource() reportSource {
	return reportSource{
		Workstreams: e.Workstreams, Dynamics: e.Dynamics, Prior: e.Prior, Effort: e.Effort,
		PhysicalActs: e.PhysicalActs, Files: e.Files, Directories: e.Directories,
		Components: e.Components, HarnessTools: e.HarnessTools, Programs: e.Programs,
		ExternalSystems: e.ExternalSystems, Integrations: e.Integrations,
		FileTypes: e.FileTypes, ShellVerbs: e.ShellVerbs, Subagents: e.Subagents,
		McpServers: e.McpServers, NamedTerms: e.NamedTerms,
		InventoryOmitted: e.InventoryOmitted, Sensitivity: e.Sensitivity,
		SensitivitySpans: e.SensitivitySpans, PipelineStatus: e.PipelineStatus,
		FacetsSkipped: e.FacetsSkipped, FacetsDegraded: e.FacetsDegraded,
	}
}

func (w WindowEnrichment) reportSource() reportSource {
	return reportSource{
		Workstreams: w.Workstreams, Dynamics: w.Dynamics, Prior: w.Prior, Effort: w.Effort,
		PhysicalActs: w.PhysicalActs, Files: w.Files, Directories: w.Directories,
		Components: w.Components, HarnessTools: w.HarnessTools, Programs: w.Programs,
		ExternalSystems: w.ExternalSystems, Integrations: w.Integrations,
		FileTypes: w.FileTypes, ShellVerbs: w.ShellVerbs, Subagents: w.Subagents,
		McpServers: w.McpServers, NamedTerms: w.NamedTerms,
		InventoryOmitted: w.InventoryOmitted, PipelineStatus: w.PipelineStatus,
	}
}

func renderReport(e reportSource) (summary, report string) {
	ws := func(dim string) string {
		if l, ok := e.Workstreams[dim]; ok && l.Value != "" {
			return l.Value
		}
		return ""
	}
	// `repo` is the authoritative identity (the checkout's origin, resolved by the daemon
	// from .git/config); `project` is the workspace DIRECTORY BASENAME, which is
	// machine-local. Prefer repo and fall back, rather than publishing the weaker key when
	// the stronger one is present. Absent repo is normal — plenty of real work happens in a
	// directory that is not a checkout.
	project, branch := ws("project"), ws("branch")
	identity := ws("repo")
	if identity == "" {
		identity = project
	}
	outputType, language := ws("output_type"), ws("language")

	// Headline: what and where, from the two dimensions that are near-always
	// attributed. Both absent means the window characterised nothing, and a
	// headline naming neither would be noise.
	head := strings.TrimSpace(strings.Join(nonEmpty(identity, branch), " · "))

	topComponent := ""
	if len(e.Components) > 0 {
		topComponent = e.Components[0].Value
	}
	sumParts := nonEmpty(language, outputType)
	switch {
	case len(sumParts) > 0 && topComponent != "":
		summary = fmt.Sprintf("%s work in %s", strings.Join(sumParts, " "), topComponent)
	case len(sumParts) > 0:
		summary = fmt.Sprintf("%s work", strings.Join(sumParts, " "))
	case identity != "":
		summary = fmt.Sprintf("work in %s", identity)
	default:
		summary = "no attributable activity in this window"
	}
	if e.Effort != nil && e.Effort.Tempo != "" {
		summary += ", " + e.Effort.Tempo
	}
	summary += "."

	var b []string
	if head != "" {
		b = append(b, "## "+head, "", summary, "")
	} else {
		b = append(b, summary, "")
	}

	if s := workClause(e, language, outputType, branch); s != "" {
		b = append(b, "- **Work**  "+s)
	}
	if s := effortClause(e); s != "" {
		b = append(b, "- **Effort**  "+s)
	}
	if s := activityClause(e); s != "" {
		b = append(b, "- **Activity**  "+s)
	}
	if s := toolsClause(e); s != "" {
		b = append(b, "- **Tools**  "+s)
	}
	if s := materialClause(e); s != "" {
		b = append(b, "- **Material**  "+s)
	}
	if s := changeClause(e); s != "" {
		b = append(b, "- **Change**  "+s)
	}
	if s := priorClause(e); s != "" {
		b = append(b, "- **Against the session**  "+s)
	}
	if s := sensitivityClause(e); s != "" {
		b = append(b, "- **Sensitivity**  "+s)
	}
	if s := caveatClause(e); s != "" {
		b = append(b, "- **Caveat**  "+s)
	}
	return summary, strings.TrimSpace(strings.Join(b, "\n"))
}

func workClause(e reportSource, language, outputType, branch string) string {
	var parts []string
	if outputType != "" {
		parts = append(parts, outputType)
	}
	if language != "" {
		if l, ok := e.Workstreams["language"]; ok {
			parts = append(parts, fmt.Sprintf("in %s (%.0f%%)", language, 100*l.Confidence))
		}
	}
	if branch != "" {
		parts = append(parts, "on branch `"+branch+"`")
	}
	if len(e.Components) > 0 {
		var cs []string
		for _, c := range e.Components[:min(2, len(e.Components))] {
			cs = append(cs, fmt.Sprintf("`%s` (%d refs)", c.Value, c.N))
		}
		parts = append(parts, "in "+strings.Join(cs, " and "))
	}
	if len(parts) == 0 {
		// Nothing descriptive was attributed. Still name the identity rather than dropping
		// the bullet: "we know where, not what" is a different and more useful statement
		// than silence, and the unattributed list below is the substance of it.
		if id := e.Workstreams["repo"].Value; id != "" {
			parts = append(parts, "in "+id)
		} else if p := e.Workstreams["project"].Value; p != "" {
			parts = append(parts, "in "+p)
		} else {
			return ""
		}
	}
	s := strings.Join(parts, ", ") + "."
	// Name what the hour could NOT attribute — an absent dimension is a fact,
	// and a reader who cannot tell it from "not applicable" reads confidence
	// the window never had.
	var missing []string
	for _, dim := range []string{"project", "branch", "model", "output_type", "language", "skill", "tooling"} {
		if l, ok := e.Workstreams[dim]; !ok || l.Value == "" {
			missing = append(missing, dim)
		}
	}
	if len(missing) > 0 {
		s += fmt.Sprintf(" Unattributed: %s.", strings.Join(missing, ", "))
	}
	return s
}

// effortClause does the division the measured failure mode was about: a reader
// handed total bytes and a turn count had to compute the per-turn figure, and
// that was the one question the losing arm failed on every time.
func effortClause(e reportSource) string {
	if e.Effort == nil {
		return ""
	}
	var parts []string
	if e.Effort.AuthoredBytes != nil && e.Effort.AuthoringTurns > 0 {
		per := float64(*e.Effort.AuthoredBytes) / float64(e.Effort.AuthoringTurns)
		parts = append(parts, fmt.Sprintf("%s authored over %d turns — averaging ~%s per turn",
			humanBytes(*e.Effort.AuthoredBytes), e.Effort.AuthoringTurns, humanBytes(int64(per))))
	} else if e.Effort.AuthoredStatus != "" && e.Effort.AuthoredStatus != "attributed" {
		parts = append(parts, "authored volume "+e.Effort.AuthoredStatus)
	}
	if e.Effort.FastShare != nil {
		parts = append(parts, fmt.Sprintf("%.0f%% of gaps under 5s", 100*(*e.Effort.FastShare)))
	}
	if len(parts) == 0 {
		return ""
	}
	s := strings.Join(parts, ", with ") + "."
	if e.Effort.Tempo != "" {
		s += " " + capitalise(e.Effort.Tempo) + "."
	}
	return s
}

// activityClause reports the acts AND the edit-to-read ratio, because the two
// counts side by side are exactly the shape a reader has to divide.
func activityClause(e reportSource) string {
	if len(e.PhysicalActs) == 0 {
		return ""
	}
	var named []string
	counts := map[string]int{}
	for _, a := range e.PhysicalActs {
		counts[a.Value] = a.N
	}
	for _, a := range e.PhysicalActs[:min(4, len(e.PhysicalActs))] {
		named = append(named, fmt.Sprintf("%d %s", a.N, a.Value))
	}
	s := strings.Join(named, ", ") + "."
	if ed, rd := counts["edit"], counts["read"]; ed > 0 && rd > 0 {
		r := float64(ed) / float64(rd)
		reading := "exploration-led"
		switch {
		case r >= 1.0:
			reading = "a rewrite pattern rather than exploration"
		case r >= 0.4:
			reading = "read-and-revise"
		}
		s += fmt.Sprintf(" The %.1f:1 edit-to-read ratio is %s.", r, reading)
	}
	return s
}

func toolsClause(e reportSource) string {
	var parts []string
	if s := countList(e.HarnessTools, 3); s != "" {
		parts = append(parts, s)
	}
	if s := plainList(e.Programs, 3); s != "" {
		parts = append(parts, "ran "+s)
	}
	if s := plainList(e.ExternalSystems, 3); s != "" {
		parts = append(parts, "reached "+s)
	}
	if len(e.Integrations) > 0 {
		parts = append(parts, fmt.Sprintf("%d integration %s", totalN(e.Integrations),
			plural(totalN(e.Integrations), "call", "calls")))
	}
	if len(parts) == 0 {
		return ""
	}
	s := strings.Join(parts, "; ") + "."
	// A truncated inventory must not read as a short one — the same rule
	// `inventory_omitted` exists for, restated where a human will see it.
	if n := e.InventoryOmitted["programs"]; n > 0 {
		s += fmt.Sprintf(" (%d further %s not listed.)", n, plural(n, "program", "programs"))
	}
	return s
}

// changeClause reports only the STATED reading, never the raw turnover/decay
// numbers, and names every dimension that could not be compared — a metric
// missing because a comparison was impossible reads identically to one that
// held steady unless the difference is stated.
// materialClause carries the inventories that describe WHAT was handled rather than how:
// file types, the subagents delegated to, MCP servers, and the named terms lifted from
// message text. Named terms are last and labelled as spoken-about, because they are the one
// inventory not derived from tool-call inputs and a reader should weigh them differently.
func materialClause(e reportSource) string {
	var parts []string
	if s := plainList(e.FileTypes, 4); s != "" {
		parts = append(parts, "file types "+s)
	}
	if s := plainList(e.ShellVerbs, 3); s != "" {
		parts = append(parts, "commands "+s)
	}
	if s := countList(e.Subagents, 3); s != "" {
		parts = append(parts, "delegated to "+s)
	}
	if s := plainList(e.McpServers, 3); s != "" {
		parts = append(parts, "MCP "+s)
	}
	if s := countList(e.NamedTerms, 4); s != "" {
		parts = append(parts, "spoken about: "+s)
	}
	if len(parts) == 0 {
		return ""
	}
	return capitalise(strings.Join(parts, "; ")) + "."
}

func changeClause(e reportSource) string {
	if len(e.Dynamics) == 0 {
		return ""
	}
	var moved, notComparable []string
	for _, dim := range sortedKeys(e.Dynamics) {
		d := e.Dynamics[dim]
		if d.Reading != "" {
			moved = append(moved, dim+" "+d.Reading)
		} else {
			notComparable = append(notComparable, dim+" ("+d.Status+")")
		}
	}
	var s string
	if len(moved) > 0 {
		s = strings.Join(moved, ", ") + "."
	} else {
		s = "nothing comparable."
	}
	if len(notComparable) > 0 {
		s += " Not comparable: " + strings.Join(notComparable, ", ") + "."
	}
	return s
}

func priorClause(e reportSource) string {
	if len(e.Prior) == 0 {
		return ""
	}
	var novel, diverges []string
	evidence := 0
	for _, dim := range sortedKeys(e.Prior) {
		p := e.Prior[dim]
		if p.Evidence > evidence {
			evidence = p.Evidence
		}
		if p.Novel != nil && *p.Novel {
			novel = append(novel, dim)
		}
		if p.Agrees != nil && !*p.Agrees {
			diverges = append(diverges, dim)
		}
	}
	// A dimension that is BOTH novel and divergent is one fact, not two: the window's value
	// is new, which is why it disagrees. Saying "new to this session: branch; diverges on
	// branch" reads as two findings and invites double-counting.
	novelSet := map[string]bool{}
	for _, d := range novel {
		novelSet[d] = true
	}
	var onlyDiverges []string
	for _, d := range diverges {
		if !novelSet[d] {
			onlyDiverges = append(onlyDiverges, d)
		}
	}
	var parts []string
	if len(novel) > 0 {
		parts = append(parts, "new to this session: "+strings.Join(novel, ", "))
	}
	if len(onlyDiverges) > 0 {
		parts = append(parts, "diverges on "+strings.Join(onlyDiverges, ", "))
	}
	if len(parts) == 0 {
		parts = append(parts, "consistent with the session so far")
	}
	s := capitalise(strings.Join(parts, "; "))
	if evidence > 0 {
		s += fmt.Sprintf(" (%d prior observations).", evidence)
	} else {
		s += "."
	}
	return s
}

// sensitivityClause never lets a check that did not run publish a confident
// negative — a degraded scan says so in the same breath as the "none".
func sensitivityClause(e reportSource) string {
	if e.Sensitivity.Value == "" {
		return ""
	}
	degraded := false
	for _, f := range e.FacetsDegraded {
		if f == "sensitivity" {
			degraded = true
		}
	}
	if e.Sensitivity.Value == "none" {
		if degraded {
			return "none found by the checks that ran — the PII scan was unavailable, so only " +
				"credential patterns were checked. Not a clean result."
		}
		return "none detected."
	}
	s := fmt.Sprintf("**%s**", e.Sensitivity.Value)
	if n := len(e.SensitivitySpans); n > 0 {
		s += fmt.Sprintf(", %d masked span(s)", n)
	}
	if degraded {
		s += " — from a partial scan; treat as a floor, not a total"
	}
	return s + "."
}

// caveatClause surfaces a thin or partial run at the bottom of the report
// rather than leaving it to the structured fields, so a reader who only reads
// the prose still learns the answer is incomplete.
func caveatClause(e reportSource) string {
	var parts []string
	if e.PipelineStatus != "" && e.PipelineStatus != "enriched" {
		parts = append(parts, "pipeline "+e.PipelineStatus)
	}
	if len(e.FacetsSkipped) > 0 {
		parts = append(parts, "not run: "+strings.Join(e.FacetsSkipped, ", "))
	}
	if len(e.FacetsDegraded) > 0 {
		parts = append(parts, "ran on partial evidence: "+strings.Join(e.FacetsDegraded, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return capitalise(strings.Join(parts, "; ")) + "."
}

func countList(items []enrich.NameCount, n int) string {
	if len(items) == 0 {
		return ""
	}
	var out []string
	for _, it := range items[:min(n, len(items))] {
		out = append(out, fmt.Sprintf("%s (%d)", it.Value, it.N))
	}
	return strings.Join(out, ", ")
}

func plainList(items []enrich.NameCount, n int) string {
	if len(items) == 0 {
		return ""
	}
	var out []string
	for _, it := range items[:min(n, len(items))] {
		out = append(out, "`"+it.Value+"`")
	}
	return strings.Join(out, ", ")
}

func totalN(items []enrich.NameCount) int {
	t := 0
	for _, it := range items {
		t += it.N
	}
	return t
}

func nonEmpty(vs ...string) []string {
	var out []string
	for _, v := range vs {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// humanBytes keeps the unit ATTACHED to the number. A bare 20633 beside a bare
// 26 is the shape that measured worse than no context at all.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
