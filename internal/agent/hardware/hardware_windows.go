//go:build windows

package hardware

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// Smart App Control states, as reported in `agent.hardware`'s `smart_app_control`.
//
// ⚠️ **"ABSENT" AND "UNKNOWN" ARE NOT "OFF", AND COLLAPSING THEM WOULD MAKE THE
// FLEET LOOK SAFER THAN IT IS.** This field exists to answer one question — how
// much of the fleet refuses to run unsigned Keld binaries — and the tempting
// simplification (anything that is not clearly On counts as Off) answers it
// wrongly in the reassuring direction. A machine whose registry could not be
// read has told us nothing; a Windows build with no such policy at all is a
// different fact again. Both are reported as themselves.
const (
	sacOff        = "off"        // present, disabled
	sacOn         = "on"         // present, ENFORCING — these machines block us
	sacEvaluation = "evaluation" // present, watching; may flip to on by itself
	sacAbsent     = "absent"     // no such policy on this Windows build
	sacUnknown    = "unknown"    // the read itself failed; we learned nothing
	sacUnexpected = "unexpected" // a value Microsoft has not documented
)

// smartAppControl reads the machine's Smart App Control state.
//
// The policy lives at HKLM\SYSTEM\CurrentControlSet\Control\CI\Policy as
// `VerifiedAndReputablePolicyState` — 0 off, 1 on (enforcement), 2 evaluation.
// It is the same value Microsoft's own testing documentation tells developers to
// set when forcing a mode, so it is the state SAC actually acts on rather than a
// proxy for it.
//
// Read-only, HKLM, no elevation needed, no shell-out — this runs synchronously in
// daemon startup and must not be able to block it.
func smartAppControl() string {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\CI\Policy`, registry.QUERY_VALUE)
	if err != nil {
		// The key is absent on Windows builds without the feature, and
		// unreadable in exotic configurations. Not the same as disabled.
		if err == registry.ErrNotExist {
			return sacAbsent
		}
		return sacUnknown
	}
	defer k.Close()

	v, _, err := k.GetIntegerValue("VerifiedAndReputablePolicyState")
	if err != nil {
		if err == registry.ErrNotExist {
			return sacAbsent
		}
		return sacUnknown
	}
	switch v {
	case 0:
		return sacOff
	case 1:
		return sacOn
	case 2:
		return sacEvaluation
	default:
		// A state Microsoft has added since this was written. Reporting it as
		// its own thing is what lets the next person notice, where folding it
		// into "off" would bury it.
		return sacUnexpected
	}
}

// windowsVersion returns a coarse OS version string, e.g. "10.0.26100 (24H2)".
//
// ⚠️ IT MATTERS ALONGSIDE THE SAC FIELD, not on its own: Smart App Control
// exists only on Windows 11 22H2 and later, so a fleet's exposure is a function
// of both. Registry rather than a shell-out for the reason the package header
// gives — every value here is one cheap read that degrades to "" in silence.
func windowsVersion() string {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()

	major, _, err := k.GetIntegerValue("CurrentMajorVersionNumber")
	if err != nil {
		return ""
	}
	minor, _, _ := k.GetIntegerValue("CurrentMinorVersionNumber")
	build, _, _ := k.GetStringValue("CurrentBuild")
	display, _, _ := k.GetStringValue("DisplayVersion")

	out := fmt.Sprintf("%d.%d", major, minor)
	if build != "" {
		out += "." + build
	}
	if display != "" {
		out += " (" + display + ")"
	}
	return out
}

func collectWindows(info *Info) {
	info.OSVersion = windowsVersion()
	info.SmartAppControl = smartAppControl()
}
