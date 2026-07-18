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
		re, err := regexp.Compile(r.Regex)
		if err != nil {
			skipped++ // RE2 incompatibility: skip, never fatal
			continue
		}
		kws := make([]string, len(r.Keywords))
		for i, k := range r.Keywords {
			kws[i] = k // keywords are already lowercase in gitleaks config
		}
		rules = append(rules, Rule{ID: r.ID, Regex: re, Keywords: kws, Entropy: r.Entropy, SecretGroup: r.SecretGroup})
	}
}

// Rules returns the parsed, compiled ruleset (built once).
func Rules() []Rule { once.Do(load); return rules }

// SkippedCount returns how many rules failed to compile as RE2.
func SkippedCount() int { once.Do(load); return skipped }
