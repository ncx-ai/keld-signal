//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// jobHandle is held for the life of the process on purpose: closing it is what
// kills the children, so it must not be garbage-collected early.
var jobHandle windows.Handle

// limitChildrenToOurLifetime puts THIS process into a job object marked
// kill-on-close. Child processes inherit the job, so anything we start dies when
// we do.
//
// ⚠️ **THIS IS THE ENTIRE CANCELLATION STORY, AND IT HAS TO BE, BECAUSE INNO
// GIVES OUT NO PIDS.** `Exec(…, ewNoWait)` returns a boolean; there is no handle
// to terminate and no pid to look up. Meanwhile a device-flow `keld login` polls
// Atlas until the code expires — so without this, a person who cancels the
// wizard mid-sign-in leaves a `keld.exe` polling in the background with nothing
// to report to, invisible except in Task Manager.
//
// ⚠️ **AND IT IS ASSIGNED TO US, NOT TO EACH CHILD, DELIBERATELY.** Assigning a
// freshly-started child needs it created suspended and then resumed, and Go's
// os/exec exposes no thread handle to resume. Inheritance gets the same
// guarantee with none of that: we are in the job before any child exists, so
// every child is in it from its first instruction.
//
// Failure is non-fatal. A machine where job objects are unavailable (or where we
// are already in a job that forbids nesting) still gets a working installer; it
// just loses the reaping guarantee, which is worth strictly less than refusing
// to run at all.
func limitChildrenToOurLifetime() {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(h)
		return
	}
	if err := windows.AssignProcessToJobObject(h, windows.CurrentProcess()); err != nil {
		windows.CloseHandle(h)
		return
	}
	jobHandle = h
}

// processAlive reports whether pid is still running. Used to notice the wizard
// being killed outright, which writes no sentinel.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	const stillActive = 259
	return code == stillActive
}
