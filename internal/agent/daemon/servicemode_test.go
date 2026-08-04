package daemon

import (
	"strings"
	"testing"
)

func TestBindAddrDefaultsToLoopbackEphemeral(t *testing.T) {
	if got := bindAddr(); got != "127.0.0.1:0" {
		t.Fatalf("default bind = %q, want 127.0.0.1:0", got)
	}
}

func TestBindAddrFromEnv(t *testing.T) {
	t.Setenv("KELD_AGENT_BIND", "0.0.0.0:7788")
	if got := bindAddr(); got != "0.0.0.0:7788" {
		t.Fatalf("bind = %q", got)
	}
}

func TestOffLoopbackBindRequiresASecret(t *testing.T) {
	t.Setenv("KELD_AGENT_BIND", "0.0.0.0:7788")
	_, err := serviceSecret()
	if err == nil {
		t.Fatal("binding off-loopback without a secret must refuse to start")
	}
	if !strings.Contains(err.Error(), "KELD_AGENT_SECRET") {
		t.Fatalf("error should name the missing variable, got %v", err)
	}
}

func TestOffLoopbackRejectsAWeakSecret(t *testing.T) {
	t.Setenv("KELD_AGENT_BIND", "0.0.0.0:7788")
	t.Setenv("KELD_AGENT_SECRET", "short")
	if _, err := serviceSecret(); err == nil {
		t.Fatal("off-loopback makes the secret the sole control; a weak one must be refused")
	}
}

func TestOffLoopbackAcceptsAStrongSecret(t *testing.T) {
	strong := strings.Repeat("k", 32)
	t.Setenv("KELD_AGENT_BIND", "0.0.0.0:7788")
	t.Setenv("KELD_AGENT_SECRET", strong)
	got, err := serviceSecret()
	if err != nil || got != strong {
		t.Fatalf("secret = %q err = %v", got, err)
	}
}

func TestLoopbackNeedsNoEnvSecret(t *testing.T) {
	if _, err := serviceSecret(); err != nil {
		t.Fatalf("loopback keeps the generated agent.json secret: %v", err)
	}
}
