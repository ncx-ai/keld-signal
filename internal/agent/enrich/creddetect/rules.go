// Package creddetect detects leaked credentials in text via a vendored, embedded
// gitleaks ruleset plus a keyword pre-filter.
//
// gitleaks.toml is vendored verbatim from:
//
//	https://raw.githubusercontent.com/gitleaks/gitleaks/v8.30.1/config/gitleaks.toml
//
// pinned to gitleaks release tag v8.30.1 (MIT © gitleaks authors). See NOTICE
// for the full attribution.
package creddetect

import (
	_ "embed"
	"regexp"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

//go:embed gitleaks.toml
var gitleaksTOML []byte

// Rule is one compiled credential-detection rule.
type Rule struct {
	ID          string
	Regex       *regexp.Regexp
	Keywords    []string
	Entropy     float64
	SecretGroup int
}

type tomlConfig struct {
	Rules []struct {
		ID          string   `toml:"id"`
		Regex       string   `toml:"regex"`
		Keywords    []string `toml:"keywords"`
		Entropy     float64  `toml:"entropy"`
		SecretGroup int      `toml:"secretGroup"`
	} `toml:"rules"`
}

// localSecretGroup pins secretGroup for vendored rules that omit the key but
// whose regex DOES capture the secret in a group. It is applied after parsing,
// so gitleaks.toml stays byte-for-byte upstream (see NOTICE) and a future
// refresh of the vendored file cannot silently clobber the fix.
//
// generic-api-key matches "<keyword><words><delimiter><value>" and carries a
// 3.5-bit entropy floor. Without a secretGroup that floor was measured over the
// whole match -- keyword, filler words, delimiter and all -- which is exactly
// the high-variety context the floor is supposed to exclude. Ordinary prose
// ("...key obligations, termination rights...") scored 3.788 bits and reported
// as a leaked credential, while its captured value ("termination") scores
// 2.914. Group 1 is the captured value.
//
// Upstream's own config proves group 1 is the intended secret: the rule's first
// allowlist is regex '^[a-zA-Z_.-]+$' against the default target (the secret).
// Every whole match necessarily contains one of "=", ">", ":", "|", "?", ","
// or "=>", none of which are in that character class, so that allowlist is
// unreachable unless the secret is the capture group rather than the match.
//
// Deliberately a per-rule pin and NOT a blanket "use group 1 when secretGroup
// is unset": 164 of the 221 vendored rules have a capture group, and group 1 is
// not the secret in all of them -- jwt-base64 captures 34 header-prefix markers
// and curl-auth-header 8 alternation branches, so a blanket fallback would mask
// a fragment instead of the token and would measure the entropy floor on the
// wrong text. Pin only rules whose group 1 is verified to be the secret;
// TestLocalSecretGroupOverridesAreValid enforces the preconditions.
var localSecretGroup = map[string]int{
	"generic-api-key": 1,
}

var (
	once    sync.Once
	rules   []Rule
	skipped int
)

func load() {
	var cfg tomlConfig
	if err := toml.Unmarshal(gitleaksTOML, &cfg); err != nil {
		return // leaves rules empty; TestRulesLoad guards this
	}
	for _, r := range cfg.Rules {
		if r.Regex == "" {
			// Path-only gitleaks rules (e.g. pkcs12-file) carry no "regex" key,
			// only a "path" key for filename matching. Compiling the missing
			// field would produce an empty pattern that matches every offset,
			// so treat it like a compile failure and never surface it via
			// Rules().
			skipped++
			continue
		}
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			skipped++ // RE2 incompatibility: skip, never fatal
			continue
		}
		kws := make([]string, len(r.Keywords))
		for i, k := range r.Keywords {
			kws[i] = k // keywords are already lowercase in gitleaks config
		}
		sg := r.SecretGroup
		if sg == 0 {
			sg = localSecretGroup[r.ID] // local pin; 0 (absent) leaves upstream behaviour
		}
		rules = append(rules, Rule{ID: r.ID, Regex: re, Keywords: kws, Entropy: r.Entropy, SecretGroup: sg})
	}
}

// Rules returns the parsed, compiled ruleset (built once).
func Rules() []Rule { once.Do(load); return rules }

// SkippedCount returns how many rules failed to compile as RE2.
func SkippedCount() int { once.Do(load); return skipped }
