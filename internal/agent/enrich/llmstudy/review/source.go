package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
)

// DefaultCorpusPath is where the inputs-and-outputs record lives, relative to the repo root.
//
// The file is the project owner's and is deliberately untracked, so nothing here may write to
// it and no test may require it: every entry point that reads it skips when it is absent.
// That is also why the emitted packets are written outside the tracked tree — emitting them
// into the repository would commit the owner's document by another route.
const DefaultCorpusPath = "docs/qwen-inputs-and-outputs.md"

// CorpusPathFromEnv resolves the source document. REVIEW_CORPUS overrides.
func CorpusPathFromEnv(repoRoot string) string {
	if p := os.Getenv("REVIEW_CORPUS"); p != "" {
		return p
	}
	return repoRoot + "/" + DefaultCorpusPath
}

// LoadCorpus parses the document at path and returns it with its content digest. The digest
// is recorded in the answer key so a scored round can be tied to the exact corpus it was cut
// from — the document grows, and a verdict scored against a different revision of it is a
// different measurement.
func LoadCorpus(path string) (Corpus, string, int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Corpus{}, "", 0, err
	}
	sum := sha256.Sum256(b)
	c, skipped, err := ParseCorpus(string(b))
	if err != nil {
		return Corpus{}, "", skipped, fmt.Errorf("%s: %w", path, err)
	}
	return c, hex.EncodeToString(sum[:]), skipped, nil
}
