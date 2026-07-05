package debuglog

import (
	"os"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestAppendWritesTimestampedLine(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	Append("hello %d", 7)
	data, err := os.ReadFile(paths.DebugLogPath())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "hello 7") {
		t.Fatalf("log missing line: %q", data)
	}
}

func TestRotateWhenOverCap(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	old := MaxBytes
	defer func() { MaxBytes = old }()
	MaxBytes = 20

	Append("first line, definitely over twenty bytes")
	Append("second")

	if _, err := os.Stat(paths.DebugLogPath() + ".1"); err != nil {
		t.Fatalf("expected rotated file .1: %v", err)
	}
	data, _ := os.ReadFile(paths.DebugLogPath())
	if !strings.Contains(string(data), "second") {
		t.Fatalf("active log should hold the newest line: %q", data)
	}
}
