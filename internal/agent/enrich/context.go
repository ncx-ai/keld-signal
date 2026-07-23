package enrich

// interactiveCodingTools receive conversation-context augmentation by default:
// their prompts are fragments of an ongoing coding session that need surrounding
// context to classify well. Other sources are one-shot and stay unaugmented.
var interactiveCodingTools = map[string]bool{"claude_code": true, "codex": true, "gemini_cli": true}

// ContextEligible reports whether a source should receive context augmentation.
func ContextEligible(source string) bool { return interactiveCodingTools[source] }
