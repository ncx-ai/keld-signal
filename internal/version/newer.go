package version

import (
	"strconv"
	"strings"
)

// Newer reports whether a is a strictly newer build than b, and whether that
// question could be answered at all.
//
// ⚠️ AN ORDERING, WHERE Skew DELIBERATELY HAS NONE. Skew compares by identity
// because it only has to say "these two halves disagree"; the version guard in
// `keld signal setup` has to say something stronger — that the binary about to
// write every tool's config is not the OLDER of two on this machine. That
// question has no identity answer, so this parses.
//
// Unparseable is UNKNOWN, never "older": the guard must fail toward doing
// nothing. A version string this cannot read is a version string nobody should
// be refused over.
func Newer(a, b string) (newer, known bool) {
	av, aok := parse(a)
	bv, bok := parse(b)
	if !aok || !bok {
		return false, false
	}
	return compare(av, bv) > 0, true
}

// parsed is a version split into its numeric core and its pre-release
// identifiers.
type parsed struct {
	core []int
	pre  []string
}

func parse(v string) (parsed, bool) {
	v = Normalize(v)
	if v == "" || v == Unknown {
		return parsed{}, false
	}
	// Build metadata (`+…`) has no ordering, per semver; drop it.
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	core := v
	var pre []string
	if i := strings.IndexByte(v, '-'); i >= 0 {
		core = v[:i]
		if rest := v[i+1:]; rest != "" {
			pre = strings.Split(rest, ".")
		}
	}
	var nums []int
	for _, part := range strings.Split(core, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return parsed{}, false
		}
		nums = append(nums, n)
	}
	if len(nums) == 0 {
		return parsed{}, false
	}
	return parsed{core: nums, pre: pre}, true
}

// compare returns -1, 0 or 1 by semver precedence.
func compare(a, b parsed) int {
	for i := 0; i < len(a.core) || i < len(b.core); i++ {
		x, y := 0, 0
		if i < len(a.core) {
			x = a.core[i]
		}
		if i < len(b.core) {
			y = b.core[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	// A pre-release precedes its own release: 3.0.0-rc.3 < 3.0.0. That is the
	// incident's own shape — the binary shadowing the install was 3.0.0-rc.3.
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) || i < len(b.pre); i++ {
		if i >= len(a.pre) {
			return -1 // a shorter identifier set precedes a longer one
		}
		if i >= len(b.pre) {
			return 1
		}
		if c := comparePre(a.pre[i], b.pre[i]); c != 0 {
			return c
		}
	}
	return 0
}

// comparePre orders two pre-release identifiers: numeric ones numerically and
// below alphanumeric ones, alphanumeric ones by ASCII.
func comparePre(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	aNum, bNum := aErr == nil, bErr == nil
	switch {
	case aNum && bNum:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		default:
			return 0
		}
	case aNum:
		return -1
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
