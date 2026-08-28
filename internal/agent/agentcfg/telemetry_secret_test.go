package agentcfg

import "testing"

// ⚠️ THE WHOLE POINT IS THAT IT DOES NOT CHANGE. This secret is written into
// every AI tool's config by `keld signal setup`, and those tools read their
// config once at startup — so a secret that rotated would strand every running
// tool exactly as the Atlas ingest token did. Contrast Info.Secret, which is
// regenerated on every daemon start and must never reach a tool config.
func TestTelemetrySecretIsStableAcrossCalls(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	first, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 32 {
		t.Fatalf("secret too short: %d chars", len(first))
	}
	second, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("secret changed between calls: %q -> %q", first, second)
	}
}

// The daemon rewrites agent.json on EVERY start with a fresh ingress secret
// (daemon.go: agentcfg.Write(Info{Port, Secret})). If that wipes the telemetry
// secret, every tool config goes stale on the next restart — the original bug,
// rebuilt one layer down and firing daily instead of rarely.
func TestTelemetrySecretSurvivesTheDaemonsStartupWrite(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	tele, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}

	ingress, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	// Exactly what the daemon does at startup: a fresh Info with no telemetry
	// secret in it.
	if err := Write(Info{Port: 4242, Secret: ingress}); err != nil {
		t.Fatal(err)
	}

	after, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if after != tele {
		t.Fatalf("the daemon's startup write destroyed the telemetry secret: %q -> %q", tele, after)
	}
	info, err := Read()
	if err != nil || info == nil {
		t.Fatalf("read: %v", err)
	}
	if info.Secret != ingress {
		t.Fatalf("preserving the telemetry secret clobbered the ingress secret: %q", info.Secret)
	}
	if info.Port != 4242 {
		t.Fatalf("port lost: %d", info.Port)
	}
}

// An explicit new value must still win — preservation is for the ABSENT case
// only, or the secret could never be rotated deliberately if it ever had to be.
func TestAnExplicitTelemetrySecretOverwrites(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if _, err := EnsureTelemetrySecret(); err != nil {
		t.Fatal(err)
	}
	if err := Write(Info{Port: 1, Secret: "s", TelemetrySecret: "deliberate"}); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if got != "deliberate" {
		t.Fatalf("explicit value did not win: %q", got)
	}
}
