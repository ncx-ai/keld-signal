package creddetect

import "testing"

func TestIsPlaceholderPositives(t *testing.T) {
	for _, s := range []string{
		"YOUR_API_KEY", "<API_KEY>", "<YOUR_SECRET_HERE>", "${DATABASE_URL}",
		"{{token}}", "sk_live_****", "AKIA****************", "REPLACE_WITH_YOUR_TOKEN",
		"XXXXXXXX", "PLACEHOLDER", "changeme", "TODO", "your-token-here", "$DATABASE_URL",
	} {
		if !IsPlaceholder(s) {
			t.Errorf("IsPlaceholder(%q) = false, want true", s)
		}
	}
}

func TestIsPlaceholderEmptyIsNotPlaceholder(t *testing.T) {
	// Empty/whitespace text = no value to judge. Must NOT gate (fail open for
	// recall) — a detected entity with no surfaced text should still count.
	for _, s := range []string{"", "   "} {
		if IsPlaceholder(s) {
			t.Errorf("IsPlaceholder(%q) = true, want false (empty must fail open for recall)", s)
		}
	}
}

func TestIsPlaceholderNegatives_RealSecrets(t *testing.T) {
	// Every one of these is a real (fake-but-realistic) secret from the corpus —
	// a false positive here is RECALL LOSS. They MUST NOT be placeholders.
	for _, s := range []string{
		"AKIAIOSFODNN7EXAMPLE", "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"sk_live_4eC39HqLyjWDarjtT1zdp7dc", "Hunter2!Prod",
		"sk-proj-abc123DEF456ghi789JKL012mno345",
		"xoxb-2345678901-2345678901234-AbCdEfGhIjKlMnOpQrStUvWx",
		"AIzaSyDdI0hCZtE6vySjMm-WEfRq3CPzqKqqsHI",
		"Tr0ub4dor&3", "CorrectHorseBattery9!",
	} {
		if IsPlaceholder(s) {
			t.Errorf("IsPlaceholder(%q) = true, want false (real secret — would lose recall)", s)
		}
	}
}
