package projects

import "testing"

func TestEvidenceForSummarisesOnlyAttributedBlocksNeverAsARule(t *testing.T) {
	blocks := []AttributedBlock{
		{ProjectID: "p1", Dims: dimsWith(map[string]string{
			DimBranch: "feature/KELD-42-fix", DimLanguage: "go", DimTooling: "git", DimWorkspace: "keld-signal",
		})},
		{ProjectID: "p1", Dims: dimsWith(map[string]string{
			DimBranch: "feature/KELD-42-fix-2", DimLanguage: "go", DimTooling: "git", DimWorkspace: "keld-signal",
		})},
		{ProjectID: "p2", Dims: dimsWith(map[string]string{
			DimBranch: "main", DimLanguage: "python",
		})},
	}

	ev := EvidenceFor("p1", blocks)
	if len(ev.Branches) != 2 {
		t.Fatalf("branches = %+v, want 2 distinct", ev.Branches)
	}
	if len(ev.Languages) != 1 || ev.Languages[0].Name != "go" || ev.Languages[0].Count != 2 {
		t.Fatalf("languages = %+v", ev.Languages)
	}
	if len(ev.TicketKeys) != 1 || ev.TicketKeys[0].Name != "KELD" || ev.TicketKeys[0].Count != 2 {
		t.Fatalf("ticket keys = %+v, want KELD x2", ev.TicketKeys)
	}
	if len(ev.Workspaces) != 1 || ev.Workspaces[0].Name != "keld-signal" || ev.Workspaces[0].Count != 2 {
		t.Fatalf("workspaces = %+v", ev.Workspaces)
	}

	// p2's data must never leak into p1's evidence, and evidence is never fed
	// back into Attribute as a rule (attribute_test.go's
	// TestEvidenceFieldsAreNeverMatchInputs is the direct proof of that).
	ev2 := EvidenceFor("p2", blocks)
	if len(ev2.Languages) != 1 || ev2.Languages[0].Name != "python" {
		t.Fatalf("p2 languages = %+v", ev2.Languages)
	}
}

func TestEvidenceForUnknownProjectIsEmpty(t *testing.T) {
	blocks := []AttributedBlock{{ProjectID: "p1", Dims: dimsWith(map[string]string{DimLanguage: "go"})}}
	ev := EvidenceFor("does-not-exist", blocks)
	if len(ev.Languages) != 0 || len(ev.Branches) != 0 {
		t.Fatalf("evidence for unknown project = %+v, want empty", ev)
	}
}
