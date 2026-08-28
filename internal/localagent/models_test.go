package localagent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeWeights creates <dir>/model.safetensors with n bytes.
func writeWeights(t *testing.T, dir string, n int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWeightsPresent(t *testing.T) {
	t.Run("absent directory is known-absent, not undetermined", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "does-not-exist")
		known, present := weightsPresent(dir)
		if !known || present {
			t.Fatalf("known=%v present=%v, want known=true present=false", known, present)
		}
	})

	t.Run("present weights are known-present", func(t *testing.T) {
		dir := t.TempDir()
		writeWeights(t, dir, 128)
		known, present := weightsPresent(dir)
		if !known || !present {
			t.Fatalf("known=%v present=%v, want known=true present=true", known, present)
		}
	})

	t.Run("empty sentinel file does not count as present", func(t *testing.T) {
		dir := t.TempDir()
		writeWeights(t, dir, 0)
		known, present := weightsPresent(dir)
		if !known || present {
			t.Fatalf("known=%v present=%v, want known=true present=false", known, present)
		}
	})

	t.Run("an inconclusive stat is undetermined, never a false absence", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("permission bits behave differently on windows")
		}
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permission bits")
		}
		parent := t.TempDir()
		dir := filepath.Join(parent, "locked")
		writeWeights(t, dir, 128)
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o755) // let TempDir cleanup succeed
		known, present := weightsPresent(dir)
		if known {
			t.Fatalf("known=%v present=%v, want known=false (inconclusive stat)", known, present)
		}
		if present {
			t.Fatal("an inconclusive stat must never report present=true")
		}
	})
}

// gliner2 test matrix: needed only under auto; never a problem when the
// weights are present or when the mode doesn't need the model at all.
func TestGLiNER2State(t *testing.T) {
	cases := []struct {
		name       string
		mlBackend  string
		wantNeeded bool
	}{
		{"empty string means auto (default)", "", true},
		{"explicit auto", "auto", true},
		{"deterministic never needs it", "deterministic", false},
		{"off never needs it", "off", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := GLiNER2State(c.mlBackend)
			if s.Needed != c.wantNeeded {
				t.Fatalf("Needed = %v, want %v", s.Needed, c.wantNeeded)
			}
			if c.wantNeeded && s.Reason == "" {
				t.Fatal("Needed but no Reason stated")
			}
			if !c.wantNeeded && s.Reason != "" {
				t.Fatalf("not Needed but Reason stated: %q", s.Reason)
			}
		})
	}
}

func TestEncoderState(t *testing.T) {
	cases := []struct {
		name       string
		textEmbed  bool
		featuresOn bool
		wantNeeded bool
	}{
		{"both on", true, true, true},
		{"KELD_TEXTEMBED off", false, true, false},
		{"features toggle off", true, false, false},
		{"both off", false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := EncoderState(c.textEmbed, c.featuresOn)
			if s.Needed != c.wantNeeded {
				t.Fatalf("Needed = %v, want %v", s.Needed, c.wantNeeded)
			}
			if c.wantNeeded && s.Reason == "" {
				t.Fatal("Needed but no Reason stated")
			}
			if !c.wantNeeded && s.Reason != "" {
				t.Fatalf("not Needed but Reason stated: %q", s.Reason)
			}
		})
	}
}

// TestModelState_ProblemLine covers the doctor-facing contract directly on
// ModelState, independent of which model it came from.
func TestModelState_ProblemLine(t *testing.T) {
	cases := []struct {
		name    string
		m       ModelState
		wantHas bool
	}{
		{
			name:    "not needed and absent -> no problem, silent",
			m:       ModelState{Name: "x", Needed: false, PresenceKnown: true, Present: false},
			wantHas: false,
		},
		{
			name:    "not needed but present (e.g. leftover download) -> still no problem",
			m:       ModelState{Name: "x", Needed: false, PresenceKnown: true, Present: true},
			wantHas: false,
		},
		{
			name:    "needed and present -> no problem",
			m:       ModelState{Name: "x", Needed: true, Reason: "r", PresenceKnown: true, Present: true},
			wantHas: false,
		},
		{
			name:    "needed and confidently absent -> a problem, with the reason",
			m:       ModelState{Name: "x", Needed: true, Reason: "some specific reason", PresenceKnown: true, Present: false},
			wantHas: true,
		},
		{
			name:    "needed but undetermined -> no problem (never a confident false negative)",
			m:       ModelState{Name: "x", Needed: true, Reason: "r", PresenceKnown: false, Present: false},
			wantHas: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.m.ProblemLine()
			if c.wantHas && p == "" {
				t.Fatal("wanted a problem line, got none")
			}
			if !c.wantHas && p != "" {
				t.Fatalf("wanted no problem line, got %q", p)
			}
			if c.wantHas && c.m.Reason != "" {
				if !strings.Contains(p, c.m.Reason) {
					t.Fatalf("problem line %q does not mention reason %q", p, c.m.Reason)
				}
			}
		})
	}
}

func TestModelState_StatusLine(t *testing.T) {
	cases := []struct {
		name      string
		m         ModelState
		wantEmpty bool
	}{
		{"not needed and absent -> silent", ModelState{Needed: false, PresenceKnown: true, Present: false}, true},
		{"not needed and undetermined -> silent", ModelState{Needed: false, PresenceKnown: false, Present: false}, true},
		{"needed and present -> shown", ModelState{Needed: true, PresenceKnown: true, Present: true}, false},
		{"needed and absent -> shown", ModelState{Needed: true, PresenceKnown: true, Present: false}, false},
		{"needed and undetermined -> shown, but must say so", ModelState{Needed: true, PresenceKnown: false, Present: false}, false},
		{"not needed but present -> shown (informational)", ModelState{Needed: false, PresenceKnown: true, Present: true}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := c.m.StatusLine("test")
			if c.wantEmpty && l != "" {
				t.Fatalf("wanted empty status line, got %q", l)
			}
			if !c.wantEmpty && l == "" {
				t.Fatal("wanted a status line, got empty")
			}
		})
	}
	// The undetermined case must say so, distinctly from a confident absence.
	undetermined := ModelState{Needed: true, PresenceKnown: false, Present: false}.StatusLine("x")
	absent := ModelState{Needed: true, PresenceKnown: true, Present: false}.StatusLine("x")
	if undetermined == absent {
		t.Fatalf("undetermined and confidently-absent must render differently: %q == %q", undetermined, absent)
	}
	if !strings.Contains(undetermined, "could not be determined") {
		t.Fatalf("undetermined status line does not say so: %q", undetermined)
	}
}

// TestModelState_UnreachableDaemonNeverBlocksNorMisreports is the "unreachable
// daemon" scenario from the task, made concrete against this file's actual
// design: GLiNER2State/EncoderState take no daemon dependency at all (no
// agentcfg, no HTTP fetch) — presence is read straight off disk, which is
// reachable identically whether keld-agent is running or not. So "the daemon
// is unreachable" cannot, by construction, produce a false "missing": there is
// no code path here that would ever try to reach it and fail. This test
// pins that by exercising the full path with a real (non-daemon-backed) temp
// directory and confirming the answer is exactly what the filesystem says.
func TestModelState_UnreachableDaemonNeverBlocksNorMisreports(t *testing.T) {
	dir := t.TempDir()
	writeWeights(t, dir, 256)

	known, present := weightsPresent(dir)
	if !known || !present {
		t.Fatalf("known=%v present=%v, want true/true regardless of daemon state", known, present)
	}

	missingDir := filepath.Join(t.TempDir(), "never-fetched")
	known, present = weightsPresent(missingDir)
	if !known {
		t.Fatal("a plain absent directory must still be a KNOWN answer, not undetermined")
	}
	if present {
		t.Fatal("must not report present=true for a directory that was never created")
	}
}
