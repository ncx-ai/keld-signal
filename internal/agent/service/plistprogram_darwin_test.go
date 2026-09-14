package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installedProgram must read back exactly what LaunchAgentPlist wrote, or the
// "keep what is installed" rule silently degrades to "guess from
// os.Executable()" — which is the defect.
func TestInstalledProgramReadsBackWhatWeWrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := "/usr/local/keld/" + agentFileName()
	plist := LaunchAgentPlist(want, "/tmp/out.log", "/tmp/err.log")
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := installedProgram(); got != want {
		t.Fatalf("installedProgram() = %q, want %q — the reader and the writer disagree, "+
			"so a restart cannot keep the program it is supposed to keep", got, want)
	}
}

// NEGATIVE: no plist, or an unrecognised one, reads as "" rather than as
// something. A parser that returned a partial guess here would feed a wrong
// program into the very rule meant to preserve the right one.
func TestInstalledProgramIsEmptyWhenThereIsNothingToRead(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if got := installedProgram(); got != "" {
		t.Fatalf("with no plist on disk, installedProgram() = %q, want empty", got)
	}

	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, junk := range []string{"", "not a plist", "<plist><dict></dict></plist>",
		"<key>ProgramArguments</key><array>"} {
		if err := os.WriteFile(p, []byte(junk), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := installedProgram(); got != "" {
			t.Fatalf("junk plist %q read as program %q, want empty", junk, got)
		}
	}
}

// THE STORY, end to end on the platform that had the bug: the plist this
// package would write can never name the CLI.
//
// ⚠️ macOS was the ONLY platform affected — Linux's Start/Restart shell out to
// `systemctl` and Windows' to `schtasks`, neither of which rewrites the
// definition. It is also the platform that ships as the signed .pkg, so this is
// the one that reached users.
func TestTheWrittenPlistNeverNamesTheCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(bin, agentFileName())
	cli := filepath.Join(bin, keldCLIName())
	for _, p := range []string{agent, cli} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Resolve as the CLI would: this is `keld signal restart` on a machine with
	// nothing installed yet.
	exe, err := agentExecutable(installedProgram, func() (string, error) { return cli, nil })
	if err != nil {
		t.Fatalf("agentExecutable: %v", err)
	}
	plist := LaunchAgentPlist(exe, "/tmp/out.log", "/tmp/err.log")

	if strings.Contains(plist, "<string>"+cli+"</string>") {
		t.Fatalf("the plist names the CLI:\n%s", plist)
	}
	if !strings.Contains(plist, "<string>"+agent+"</string>") {
		t.Fatalf("the plist does not name the daemon:\n%s", plist)
	}
	// And the pairing is what matters: the program must be the one that
	// understands the `run` verb sitting next to it in ProgramArguments.
	if !strings.Contains(plist, "<string>"+agent+"</string><string>run</string>") {
		t.Fatalf("program and verb are not paired as expected:\n%s", plist)
	}
}
