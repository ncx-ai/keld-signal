// Package tools provides tests for the Codex adapter.
package tools

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/config"
)

func TestCodexApplyFreshAddsBlock(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	plan := a.Apply(nil, p, false)
	if plan.Conflict != "" || !plan.Changed {
		t.Fatalf("fresh apply should succeed: %+v", plan)
	}
	if !strings.Contains(plan.AfterText, "[otel]") {
		t.Fatalf("AfterText should contain [otel], got: %s", plan.AfterText)
	}
	if plan.Managed["block"] != true {
		t.Fatalf("managed block should be true: %+v", plan.Managed)
	}
	if plan.Managed["created"] != true {
		t.Fatalf("managed created should be true for nil currentText: %+v", plan.Managed)
	}
}

func TestCodexConflictOnExistingOtel(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	cur := "[otel]\nexporter = \"otherthing\"\n"
	plan := a.Apply(&cur, p, false)
	if plan.Conflict == "" {
		t.Fatalf("expected conflict, got %+v", plan)
	}
	// replace=true should resolve by swapping just the [otel] table
	rep := a.Apply(&cur, p, true)
	if rep.Conflict != "" || !rep.Changed {
		t.Fatalf("replace should succeed: %+v", rep)
	}
}

func TestCodexApplyReplacePreservesNonOtelContent(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}

	// Config with both [otel] (conflicting) and [model] (unrelated, must survive).
	cur := "[otel]\nexporter = \"otherthing\"\n\n[model]\nname = \"x\"\n"

	// No-replace should conflict.
	plan := a.Apply(&cur, p, false)
	if plan.Conflict == "" {
		t.Fatalf("expected conflict for existing [otel] without replace flag: %+v", plan)
	}

	// replace=true: must succeed AND preserve [model].
	rep := a.Apply(&cur, p, true)
	if rep.Conflict != "" {
		t.Fatalf("replace should succeed, got conflict: %s", rep.Conflict)
	}
	if !rep.Changed {
		t.Fatalf("replace should mark Changed=true")
	}
	if !strings.Contains(rep.AfterText, "[model]") {
		t.Fatalf("[model] table not preserved after replace; AfterText:\n%s", rep.AfterText)
	}
	if !strings.Contains(rep.AfterText, "name = \"x\"") {
		t.Fatalf("[model].name not preserved after replace; AfterText:\n%s", rep.AfterText)
	}
	if !strings.Contains(rep.AfterText, "[otel]") {
		t.Fatalf("[otel] (keld's) missing from AfterText:\n%s", rep.AfterText)
	}
}

func TestCodexApplyCreatedFalseWhenCurrentTextExists(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	cur := "[model]\nname = \"x\"\n"
	plan := a.Apply(&cur, p, false)
	if plan.Conflict != "" {
		t.Fatalf("no conflict expected: %+v", plan)
	}
	if plan.Managed["created"] != false {
		t.Fatalf("managed created should be false when currentText is non-nil: %+v", plan.Managed)
	}
}

func TestCodexRemove(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	// Apply onto a config with unrelated content so we can assert it survives Remove.
	cur := "[model]\nname = \"x\"\n"
	plan := a.Apply(&cur, p, false)
	if plan.Conflict != "" {
		t.Fatalf("apply should succeed: %+v", plan)
	}
	managed := plan.Managed
	afterApply := plan.AfterText
	if !config.HasKeldBlock(afterApply) {
		t.Fatalf("apply should have inserted a keld block; AfterText:\n%s", afterApply)
	}

	rem := a.Remove(&afterApply, managed)
	if !rem.Changed {
		t.Fatalf("remove should report Changed=true when block was present")
	}
	if rem.Conflict != "" {
		t.Fatalf("remove should not conflict: %+v", rem)
	}
	if config.HasKeldBlock(rem.AfterText) {
		t.Fatalf("keld block still present after remove; AfterText:\n%s", rem.AfterText)
	}
	// Unrelated content must be preserved.
	if !strings.Contains(rem.AfterText, "[model]") || !strings.Contains(rem.AfterText, "name = \"x\"") {
		t.Fatalf("unrelated [model] content not preserved after remove; AfterText:\n%s", rem.AfterText)
	}
}

func TestCodexStatus(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	// Apply to get a configured text.
	plan := a.Apply(nil, p, false)
	if plan.Conflict != "" {
		t.Fatalf("apply should succeed: %+v", plan)
	}
	after := plan.AfterText
	status := a.Status(&after, plan.Managed)
	if !status.Configured {
		t.Fatalf("status should be configured after apply; AfterText:\n%s", after)
	}

	// Status on nil should be not configured.
	status2 := a.Status(nil, nil)
	if status2.Configured {
		t.Fatalf("status should not be configured when text is nil")
	}
}

func TestCodexConflictMessageNotReplace(t *testing.T) {
	a := &CodexAdapter{}
	p := SetupParams{Endpoint: "https://e", IngestToken: "tok"}
	cur := "[otel]\nexporter = \"otherthing\"\n"
	plan := a.Apply(&cur, p, false)
	// The conflict message must start with the exact prefix from codex.py.
	prefix := "your ~/.codex/config.toml can't be safely modified by Keld "
	if !strings.HasPrefix(plan.Conflict, prefix) {
		t.Fatalf("conflict message prefix mismatch.\nGot:  %q\nWant prefix: %q", plan.Conflict, prefix)
	}
}
