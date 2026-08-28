package llmstudy

import (
	"strings"
	"testing"
)

// Code counts are structurally zero for a copywriter or accountant. Including them
// would score all non-engineering work as trivial, so they must not appear.
func TestDigestFactsExcludeCodeCounts(t *testing.T) {
	sig := Signals{Turns: 9, UserTurns: 3, ToolCalls: 12, ToolVariety: 4,
		Corrections: 2, CodeBlocks: 7, CodeLines: 300, AssistantChars: 5000}
	line := DigestFactsLine(sig)
	for _, banned := range []string{"code_blocks", "code_lines", "300"} {
		if strings.Contains(line, banned) {
			t.Errorf("facts line leaks engineering-specific count %q: %s", banned, line)
		}
	}
	for _, want := range []string{"turns=9", "user_turns=3", "corrections=2"} {
		if !strings.Contains(line, want) {
			t.Errorf("facts line missing %q: %s", want, line)
		}
	}
}

// The correction count is the anti-rubberstamping lever, so it must always be present
// even at zero — an absent field lets the model assume the happy path.
func TestDigestFactsAlwaysStatesCorrections(t *testing.T) {
	if line := DigestFactsLine(Signals{Turns: 4, UserTurns: 1}); !strings.Contains(line, "corrections=0") {
		t.Fatalf("corrections must be stated even at zero: %s", line)
	}
}

func TestFactsFromCountsOutcomes(t *testing.T) {
	f := FactsFrom(
		Signals{Turns: 10, UserTurns: 4, ToolCalls: 20, ToolVariety: 5, Corrections: 1},
		[]Outcome{{Corrected: true}, {Corrected: false}, {Terminal: true}},
	)
	if f.CorrectedTurns != 1 {
		t.Errorf("CorrectedTurns = %d, want 1", f.CorrectedTurns)
	}
	if f.Turns != 10 || f.UserTurns != 4 {
		t.Errorf("counts not carried: %+v", f)
	}
	if !strings.Contains(f.Line(), "corrected_turns=1") {
		t.Errorf("Line() omits corrected_turns: %s", f.Line())
	}
}

// The tool profile says what KIND of work happened, and must expand collapsed runs.
func TestToolProfileRanksAndExpandsRuns(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleTool, "Bash go test ./one/ (x3)"},
		{RoleTool, "Read page.tsx"},
		{RoleTool, "Bash make build"},
		{RoleUser, "not a tool"},
	}}
	got := ToolProfile(w)
	if len(got) != 2 {
		t.Fatalf("want 2 distinct tools, got %+v", got)
	}
	if got[0].Name != "Bash" || got[0].Count != 4 {
		t.Errorf("Bash should lead with 4 (3 collapsed + 1), got %+v", got[0])
	}
	if got[1].Name != "Read" || got[1].Count != 1 {
		t.Errorf("second should be Read x1, got %+v", got[1])
	}
}

// The Block is the authoritative context. It must carry the anchors that make the
// prose factual: where the work lives, what tools it went through, and the reliable
// enrichment output.
func TestFactsBlockCarriesAnchors(t *testing.T) {
	f := FactsFrom(Signals{Turns: 9, UserTurns: 3, ToolCalls: 5, Corrections: 2}, nil).
		WithPlace("keld-signal", "feat/digest", "Keld").
		WithFocus("software", "eng", 0.82).
		WithEnrichment([]string{"settings poll", "retry logic"}, []string{"vendor: Notion"})
	f.Tools = []ToolCount{{"Bash", 4}, {"Read", 1}}

	block := f.Block()
	for _, want := range []string{
		"corrections=2", "repo keld-signal", "branch feat/digest", "project Keld",
		"Bash x4", "domain=software", "function=eng", "settled 82%",
		"settings poll", "vendor: Notion",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("Block omits %q:\n%s", want, block)
		}
	}
}

// A blank section must be omitted, not printed empty — an empty label invites the
// model to fill it in.
func TestFactsBlockOmitsEmptySections(t *testing.T) {
	block := FactsFrom(Signals{Turns: 3, UserTurns: 1}, nil).Block()
	for _, absent := range []string{"working in:", "tool profile:", "recurring topics:", "extracted specifics:"} {
		if strings.Contains(block, absent) {
			t.Errorf("Block should omit %q when there is nothing to say:\n%s", absent, block)
		}
	}
	if !strings.Contains(block, "counts:") {
		t.Error("counts must always be present")
	}
}
