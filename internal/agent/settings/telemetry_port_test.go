package settings

import (
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ AN ENV-ONLY KNOB IS UNREACHABLE FROM AN INSTALLED DAEMON, and this changeset
// made exactly that argument for `blocks` and then broke it here.
//
// No service definition on any OS carries an environment block: LaunchAgentPlist
// and SystemdUnit have none, and the Windows task is a bare /TR "<exe>" run. So
// an operator told to "set KELD_TELEMETRY_PORT to move it" — which is what the
// daemon's own bind-failure message says — has nowhere to set it that the daemon
// would see.
//
// It also has to be readable by the CLI, because `keld signal setup` writes the
// address into tool configs and must agree with the port the daemon bound.
func TestTelemetryPortEnvOverConfigOverDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv(TelemetryPortEnv, "")

	if got := Load().TelemetryPortOrDefault(14318); got != 14318 {
		t.Fatalf("no config, no env: %d, want the default", got)
	}

	if err := os.WriteFile(filepath.Join(home, "agent-config.json"),
		[]byte(`{"telemetry_port": 15001}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load().TelemetryPortOrDefault(14318); got != 15001 {
		t.Fatalf("config ignored: %d, want 15001", got)
	}

	// Env still wins, so a one-off move needs no file edit.
	t.Setenv(TelemetryPortEnv, "15002")
	if got := Load().TelemetryPortOrDefault(14318); got != 15002 {
		t.Fatalf("env did not win: %d, want 15002", got)
	}

	// Junk in either place falls back rather than binding something absurd.
	t.Setenv(TelemetryPortEnv, "banana")
	if got := Load().TelemetryPortOrDefault(14318); got != 15001 {
		t.Fatalf("junk env should fall through to the config: %d", got)
	}
}

// Out-of-range values are refused, not clamped: binding port 0 would hand the
// daemon an ephemeral port, which is the failure mode the FIXED port exists to
// prevent.
func TestTelemetryPortRejectsOutOfRange(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv(TelemetryPortEnv, "")
	for _, bad := range []string{"0", "-1", "70000"} {
		if err := os.WriteFile(filepath.Join(home, "agent-config.json"),
			[]byte(`{"telemetry_port": `+bad+`}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := Load().TelemetryPortOrDefault(14318); got != 14318 {
			t.Errorf("config port %s accepted (%d); an ephemeral or invalid port "+
				"reintroduces the staleness the fixed port prevents", bad, got)
		}
	}
}
