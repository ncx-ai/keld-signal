// internal/config/envblock_test.go
package config

import (
	"strings"
	"testing"
)

func TestUpsertEnvBlockPreservesGeminiKey(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body := "CODEX_API_KEY=codex-xyz\n"
	out := UpsertEnvBlock(current, body)
	if !strings.Contains(out, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved, got:\n%s", out)
	}
	if !strings.Contains(out, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("expected start marker present, got:\n%s", out)
	}
	if !strings.Contains(out, "# <<< keld-managed <<<") {
		t.Fatalf("expected end marker present, got:\n%s", out)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("expected body in block, got:\n%s", out)
	}
}

func TestUpsertEnvBlockIdempotent(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body := "CODEX_API_KEY=codex-xyz\n"
	out1 := UpsertEnvBlock(current, body)
	out2 := UpsertEnvBlock(out1, body)
	if out1 != out2 {
		t.Fatalf("expected idempotent behavior:\nfirst:\n%s\nsecond:\n%s", out1, out2)
	}
	if strings.Count(out2, "# >>> keld-managed (do not edit) >>>") != 1 {
		t.Fatalf("expected single block after re-upsert:\n%s", out2)
	}
}

func TestUpsertEnvBlockReplaceBody(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body1 := "CODEX_API_KEY=codex-xyz\n"
	out1 := UpsertEnvBlock(current, body1)

	body2 := "CODEX_API_KEY=codex-new\n"
	out2 := UpsertEnvBlock(out1, body2)

	if !strings.Contains(out2, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved after replace, got:\n%s", out2)
	}
	if !strings.Contains(out2, "CODEX_API_KEY=codex-new") {
		t.Fatalf("expected new body in block, got:\n%s", out2)
	}
	if strings.Contains(out2, "CODEX_API_KEY=codex-xyz") {
		t.Fatalf("expected old body replaced, got:\n%s", out2)
	}
	if strings.Count(out2, "# >>> keld-managed (do not edit) >>>") != 1 {
		t.Fatalf("expected single block after replace:\n%s", out2)
	}
}

func TestRemoveEnvBlockStripsMarkersOnly(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	body := "CODEX_API_KEY=codex-xyz\n"
	withBlock := UpsertEnvBlock(current, body)

	out := RemoveEnvBlock(withBlock)
	if !strings.Contains(out, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved after remove, got:\n%s", out)
	}
	if strings.Contains(out, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("expected start marker removed, got:\n%s", out)
	}
	if strings.Contains(out, "# <<< keld-managed <<<") {
		t.Fatalf("expected end marker removed, got:\n%s", out)
	}
	if strings.Contains(out, "CODEX_API_KEY=codex-xyz") {
		t.Fatalf("expected block body removed, got:\n%s", out)
	}
}

func TestRemoveEnvBlockWhenAbsent(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	out := RemoveEnvBlock(current)
	if out != current {
		t.Fatalf("expected no-op when block absent; got:\n%s", out)
	}
}

func TestUpsertEnvBlockToEmpty(t *testing.T) {
	body := "CODEX_API_KEY=codex-xyz\n"
	out := UpsertEnvBlock("", body)
	if !strings.Contains(out, "# >>> keld-managed (do not edit) >>>") {
		t.Fatalf("expected start marker in result, got:\n%s", out)
	}
	if !strings.Contains(out, body) {
		t.Fatalf("expected body in result, got:\n%s", out)
	}
	if !strings.Contains(out, "# <<< keld-managed <<<") {
		t.Fatalf("expected end marker in result, got:\n%s", out)
	}
}

// --- Review-requested edge case tests (should PASS as-is) ---

func TestUpsertEnvBlockNoTrailingNewline(t *testing.T) {
	// Input has no trailing newline at all.
	current := "GEMINI_API_KEY=sk-abc"
	body := "CODEX_API_KEY=codex-xyz\n"
	out := UpsertEnvBlock(current, body)
	if !strings.Contains(out, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved byte-exact, got:\n%q", out)
	}
}

func TestUpsertEnvBlockOnlyKeyNoNewline(t *testing.T) {
	// Input is ONLY the key line, with no trailing newline whatsoever.
	current := "GEMINI_API_KEY=sk-abc"
	body := "CODEX_API_KEY=codex-xyz\n"
	out := UpsertEnvBlock(current, body)
	if !strings.Contains(out, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY still present byte-exact, got:\n%q", out)
	}
}

func TestUpsertAndRemoveEnvBlockNotAtEOF(t *testing.T) {
	// A pre-existing keld block that is NOT at the end of the file: a line
	// follows it. Both Upsert(replace) and Remove must preserve that
	// trailing line as well as GEMINI_API_KEY.
	body1 := "CODEX_API_KEY=codex-old\n"
	block := KeldEnvStart + "\n" + body1 + KeldEnvEnd + "\n"
	current := "GEMINI_API_KEY=sk-abc\n" + block + "AFTER=1\n"

	// Sanity: AFTER=1 really does follow the block in our fixture.
	if !strings.HasSuffix(strings.TrimRight(current, "\n"), "AFTER=1") {
		t.Fatalf("fixture invalid: AFTER=1 not at end, got:\n%q", current)
	}

	replaced := UpsertEnvBlock(current, "CODEX_API_KEY=codex-new\n")
	if !strings.Contains(replaced, "AFTER=1") {
		t.Fatalf("expected AFTER=1 preserved after replace, got:\n%s", replaced)
	}
	if !strings.Contains(replaced, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved after replace, got:\n%s", replaced)
	}

	removed := RemoveEnvBlock(current)
	if !strings.Contains(removed, "AFTER=1") {
		t.Fatalf("expected AFTER=1 preserved after remove, got:\n%s", removed)
	}
	if !strings.Contains(removed, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved after remove, got:\n%s", removed)
	}
}

func TestRemoveAndUpsertCollapseTwoSequentialBlocks(t *testing.T) {
	// Two sequential (malformed) keld blocks, with non-block lines before
	// and after both. Remove/Upsert must collapse them into a single block
	// (or none) without eating the surrounding lines.
	blockA := KeldEnvStart + "\n" + "A=1\n" + KeldEnvEnd + "\n"
	blockB := KeldEnvStart + "\n" + "B=2\n" + KeldEnvEnd + "\n"
	current := "X=1\n" + blockA + blockB + "Y=2\n"

	removed := RemoveEnvBlock(current)
	if strings.Count(removed, KeldEnvStart) != 0 {
		t.Fatalf("expected all blocks removed, got:\n%s", removed)
	}
	if !strings.Contains(removed, "X=1") || !strings.Contains(removed, "Y=2") {
		t.Fatalf("expected surrounding lines X=1 and Y=2 preserved, got:\n%q", removed)
	}

	upserted := UpsertEnvBlock(current, "C=3\n")
	if strings.Count(upserted, KeldEnvStart) != 1 {
		t.Fatalf("expected exactly one block after upsert-collapse, got:\n%s", upserted)
	}
	if !strings.Contains(upserted, "X=1") || !strings.Contains(upserted, "Y=2") {
		t.Fatalf("expected surrounding lines X=1 and Y=2 preserved, got:\n%q", upserted)
	}
	if !strings.Contains(upserted, "C=3") {
		t.Fatalf("expected new body present, got:\n%s", upserted)
	}
}

func TestCRLFInputPreservesGeminiKeyAndBlockMatching(t *testing.T) {
	// CRLF line endings: GEMINI_API_KEY must survive, and marker matching
	// (which trims \r along with other whitespace) must still work for an
	// existing CRLF-flavored block.
	block := KeldEnvStart + "\r\n" + "A=1\r\n" + KeldEnvEnd + "\r\n"
	current := "GEMINI_API_KEY=sk-abc\r\n" + block

	if !HasEnvBlock(current) {
		t.Fatalf("expected block detected in CRLF input, got:\n%q", current)
	}

	removed := RemoveEnvBlock(current)
	if !strings.Contains(removed, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved in CRLF input, got:\n%q", removed)
	}
	if strings.Contains(removed, KeldEnvStart) || strings.Contains(removed, "A=1") {
		t.Fatalf("expected CRLF block fully removed, got:\n%q", removed)
	}
}

func TestMarkerTextEmbeddedInValueLineIsNotABoundary(t *testing.T) {
	// A line whose VALUE happens to contain the marker text is not itself a
	// marker line (exact-line match only) and must be preserved verbatim.
	weirdLine := `WEIRD="# >>> keld-managed (do not edit) >>>"`
	current := weirdLine + "\nGEMINI_API_KEY=sk-abc\n"

	removed := RemoveEnvBlock(current)
	if !strings.Contains(removed, weirdLine) {
		t.Fatalf("expected embedded-marker value line preserved verbatim, got:\n%q", removed)
	}
	if !strings.Contains(removed, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved, got:\n%q", removed)
	}

	upserted := UpsertEnvBlock(current, "CODEX_API_KEY=codex-xyz\n")
	if !strings.Contains(upserted, weirdLine) {
		t.Fatalf("expected embedded-marker value line preserved verbatim after upsert, got:\n%q", upserted)
	}
	if !strings.Contains(upserted, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved after upsert, got:\n%q", upserted)
	}
}

// --- Guard against marker lines embedded in body (issue 3) ---

func TestUpsertEnvBlockGuardsBodyEndMarkerLine(t *testing.T) {
	// body is documented as always internally-constructed, but this test
	// proves the defensive guard: a body line that is exactly the end
	// marker must not corrupt the file on a later Remove pass, must not
	// leak block-internal content outside the block, and must not drop
	// GEMINI_API_KEY.
	current := "GEMINI_API_KEY=sk-abc\n"
	maliciousBody := "SOMEVAR=1\n" + KeldEnvEnd + "\nAFTER_FAKE=2\n"

	out := UpsertEnvBlock(current, maliciousBody)
	removed := RemoveEnvBlock(out)

	if !strings.Contains(removed, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved, got:\n%q", removed)
	}
	if strings.Contains(removed, "AFTER_FAKE=2") {
		t.Fatalf("body content leaked outside the block after remove, got:\n%q", removed)
	}
	if strings.Count(removed, KeldEnvEnd) != 0 {
		t.Fatalf("expected no stray end-marker text left over, got:\n%q", removed)
	}
}

func TestUpsertEnvBlockGuardsBodyStartMarkerLine(t *testing.T) {
	// Same guard, but for a body line equal to the start marker.
	current := "GEMINI_API_KEY=sk-abc\n"
	maliciousBody := "SOMEVAR=1\n" + KeldEnvStart + "\nAFTER_FAKE=2\n"

	out := UpsertEnvBlock(current, maliciousBody)
	removed := RemoveEnvBlock(out)

	if !strings.Contains(removed, "GEMINI_API_KEY=sk-abc") {
		t.Fatalf("expected GEMINI_API_KEY preserved, got:\n%q", removed)
	}
	if strings.Contains(removed, "AFTER_FAKE=2") {
		t.Fatalf("body content leaked outside the block after remove, got:\n%q", removed)
	}
	if strings.Count(removed, KeldEnvStart) != 0 {
		t.Fatalf("expected no stray start-marker text left over, got:\n%q", removed)
	}
}

func TestHasEnvBlock(t *testing.T) {
	current := "GEMINI_API_KEY=sk-abc\n"
	if HasEnvBlock(current) {
		t.Fatal("expected no block in plain content")
	}
	withBlock := UpsertEnvBlock(current, "CODEX_API_KEY=codex-xyz\n")
	if !HasEnvBlock(withBlock) {
		t.Fatalf("expected block to be detected, got:\n%s", withBlock)
	}
}
