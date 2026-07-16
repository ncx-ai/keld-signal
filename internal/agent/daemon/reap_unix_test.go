//go:build darwin || linux

package daemon

import (
	"reflect"
	"testing"
)

func TestReapStaleSidecarsWithBuildsPkill(t *testing.T) {
	var gotName string
	var gotArgs []string
	reapStaleSidecarsWith("/usr/local/keld/keld-agent-sidecar/keld-agent-sidecar",
		func(name string, args ...string) error { gotName = name; gotArgs = args; return nil })
	if gotName != "pkill" {
		t.Fatalf("name = %q, want pkill", gotName)
	}
	want := []string{"-f", "/usr/local/keld/keld-agent-sidecar/keld-agent-sidecar"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("args = %v, want %v", gotArgs, want)
	}
}
