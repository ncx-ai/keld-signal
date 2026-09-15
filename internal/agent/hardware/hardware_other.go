//go:build !windows

package hardware

// Smart App Control is a Windows feature; there is nothing to report elsewhere.
// The field stays EMPTY rather than "absent": "absent" is a claim about a
// Windows machine that lacks the policy, and a Mac is not making that claim.
func collectWindows(info *Info) {}
