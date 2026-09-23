package agentcfg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ⚠️ THE INCIDENT THIS FILE EXISTS FOR (2026-09-18, the maintainer's machine).
// ~/.keld/agent.json held telemetry secret 26908e20…, while ~/.codex/config.toml
// and ~/.claude/settings.json both held a5629e92… — written at 17:34 by a
// keld 3.0.0-rc.3 binary still on PATH at /usr/local/keld/keld. A probe POST to
// the running proxy with the tools' token returned 401: Codex's telemetry was
// dead, and Claude Code survived only because its running process still held the
// older, correct secret in memory and would have broken on its next restart.
//
// The root cause is storage, not logic: the secret lived inside agent.json — a
// file several writers rewrite — preserved only by a rule inside Write. This
// test pins the repair: the secret has its own file, and the migration onto it
// must ADOPT whatever agent.json already holds rather than mint a new value.
// Minting here would have broken every already-configured tool on the machine,
// which is precisely the incident.
func TestMigrationAdoptsTheExistingSecretRatherThanMintingOne(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	// A pre-migration machine: agent.json carries the secret, the file does not
	// exist yet.
	const existing = "26908e20aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := Write(Info{Port: 4242, Secret: "ingress", TelemetrySecret: existing}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(paths.TelemetrySecretPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	got, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if got != existing {
		t.Fatalf("migration minted a new secret %q; every already-configured tool on the machine would 401 (want %q)", got, existing)
	}
	data, err := os.ReadFile(paths.TelemetrySecretPath())
	if err != nil {
		t.Fatalf("migration did not write the secret file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("migration wrote an empty secret file")
	}
	secrets, err := ReadTelemetrySecrets()
	if err != nil {
		t.Fatal(err)
	}
	if secrets.Secret != existing {
		t.Fatalf("secret file holds %q, want the migrated %q", secrets.Secret, existing)
	}
	if secrets.Previous != "" {
		t.Fatalf("a migration is not a rotation; previous = %q", secrets.Previous)
	}
}

// The file is the source of truth, so a stale or hand-edited agent.json must not
// be able to resurrect an old value.
func TestTheFileWinsOverAgentJSON(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Write(Info{Port: 4242, Secret: "ingress"}); err != nil {
		t.Fatal(err)
	}
	live, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	// Forge a disagreeing agent.json the way a stale binary would.
	info, err := Read()
	if err != nil || info == nil {
		t.Fatalf("read: %v", err)
	}
	raw, err := json.Marshal(map[string]any{
		"port": info.Port, "secret": info.Secret, "telemetry_secret": "a5629e92stale",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgentInfoPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if got != live {
		t.Fatalf("agent.json overrode the authoritative file: %q, want %q", got, live)
	}
}

// ⚠️ WRITE-THROUGH IS DELIBERATE. An older keld binary left on PATH — exactly
// the 3.0.0-rc.3 in the incident — reads only agent.json. Keeping the value
// mirrored there means such a binary configures tools with the SAME secret
// rather than minting a second one.
func TestTheSecretIsMirroredIntoAgentJSONForOlderBinaries(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Write(Info{Port: 1, Secret: "ingress"}); err != nil {
		t.Fatal(err)
	}
	sec, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	info, err := Read()
	if err != nil || info == nil {
		t.Fatalf("read: %v", err)
	}
	if info.TelemetrySecret != sec {
		t.Fatalf("agent.json mirror = %q, want %q", info.TelemetrySecret, sec)
	}
}

// ⚠️ THE DEFECT IN ONE SENTENCE: a file several writers rewrite is a file that
// loses a value. Write is the one those writers all go through, so it is where
// the loss has to be impossible rather than merely avoided.
func TestWriteWithNoTelemetrySecretLeavesTheFileByteIdentical(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if _, err := EnsureTelemetrySecret(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.TelemetrySecretPath())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		ingress, err := NewSecret()
		if err != nil {
			t.Fatal(err)
		}
		// Exactly the daemon's startup write: a fresh Info, no telemetry secret.
		if err := Write(Info{Port: 4242, Secret: ingress}); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.ReadFile(paths.TelemetrySecretPath())
	if err != nil {
		t.Fatalf("10 ordinary writes removed the secret file: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("secret file changed under ordinary writes:\n before %s\n after  %s", before, after)
	}
}

// A deliberate rotation is the ONE thing that may change the file, and it must
// leave the outgoing value behind so the proxy can keep honouring tools that
// have not been reconfigured yet.
func TestRotationRecordsThePreviousSecretAndTheInstant(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Write(Info{Port: 4242, Secret: "ingress"}); err != nil {
		t.Fatal(err)
	}
	first, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	second, err := RotateTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("rotation returned the same secret")
	}
	got, err := ReadTelemetrySecrets()
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret != second {
		t.Fatalf("current = %q, want %q", got.Secret, second)
	}
	if got.Previous != first {
		t.Fatalf("previous = %q, want the outgoing %q", got.Previous, first)
	}
	if got.RotatedAt.Before(before) || got.RotatedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("rotated_at = %v, not the rotation instant", got.RotatedAt)
	}
	// And the mirror keeps up, or an older binary would configure tools with the
	// retired value.
	info, err := Read()
	if err != nil || info == nil {
		t.Fatalf("read: %v", err)
	}
	if info.TelemetrySecret != second {
		t.Fatalf("agent.json mirror not rotated: %q", info.TelemetrySecret)
	}
}

// ⚠️ THE FILE MAY HAVE BEEN WRITTEN AS A BARE STRING. The first shape this file
// could plausibly take is the secret and nothing else; the grace window forced a
// small JSON object. Reading has to understand both, or an upgrade mints a new
// secret and reruns the incident.
func TestABareStringFileIsReadAsTheSecret(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.MkdirAll(paths.KeldHome(), 0o700); err != nil {
		t.Fatal(err)
	}
	const plain = "26908e20plainform"
	if err := os.WriteFile(paths.TelemetrySecretPath(), []byte(plain+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if got != plain {
		t.Fatalf("bare-string file read as %q, want %q", got, plain)
	}
}

func TestSecretFilePerms0600(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if _, err := EnsureTelemetrySecret(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(paths.TelemetrySecretPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

// ⚠️ `keld signal uninstall` removes ~/.keld/state wholesale. The secret must
// therefore NOT live under it: a machine that uninstalls one tool and reconfigures
// later would otherwise come back with a new secret and strand every tool it did
// not touch.
func TestTheSecretDoesNotLiveUnderTheStateDir(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if rel, err := filepath.Rel(paths.StateDir(), paths.TelemetrySecretPath()); err == nil && rel != "" && rel[0] != '.' {
		t.Fatalf("telemetry secret sits under the state dir (%s), which uninstall removes wholesale", rel)
	}
}
