package agentcfg

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// TelemetrySecrets is the machine's telemetry credential state: the live secret,
// the one it replaced, and when the replacement happened.
//
// Previous/RotatedAt exist for the proxy's grace window — a tool configured
// moments before a rotation must not 401 until someone reconfigures it. They are
// meaningless together with an empty Previous, which is never accepted.
type TelemetrySecrets struct {
	Secret    string
	Previous  string
	RotatedAt time.Time
}

// telemetrySecretFile is the on-disk shape of ~/.keld/telemetry-secret.
//
// ⚠️ A SMALL JSON OBJECT RATHER THAN A BARE STRING, and the reason is the grace
// window alone: accepting the outgoing secret for a bounded period needs the
// instant it went out, and an instant cannot live in a file that is one opaque
// token. readSecretFile therefore still understands the bare-string form — a
// file written that way must be adopted, never replaced, because replacing it is
// minting a new secret, which is the 2026-09-18 incident (see
// paths.TelemetrySecretPath).
type telemetrySecretFile struct {
	Secret    string     `json:"secret"`
	Previous  string     `json:"previous,omitempty"`
	RotatedAt *time.Time `json:"rotated_at,omitempty"`
}

// ReadTelemetrySecrets returns the on-disk secret state. A missing file is the
// zero value with a nil error — "not provisioned yet" is a normal state on a
// machine that has never run setup, not a fault.
func ReadTelemetrySecrets() (TelemetrySecrets, error) {
	data, err := os.ReadFile(paths.TelemetrySecretPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return TelemetrySecrets{}, nil
		}
		return TelemetrySecrets{}, err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return TelemetrySecrets{}, nil
	}
	// The bare-string form. Anything that is not an object is the secret itself.
	if trimmed[0] != '{' {
		return TelemetrySecrets{Secret: string(trimmed)}, nil
	}
	var f telemetrySecretFile
	if err := json.Unmarshal(trimmed, &f); err != nil {
		return TelemetrySecrets{}, err
	}
	out := TelemetrySecrets{Secret: strings.TrimSpace(f.Secret), Previous: strings.TrimSpace(f.Previous)}
	if f.RotatedAt != nil {
		out.RotatedAt = *f.RotatedAt
	}
	return out, nil
}

// EnsureTelemetrySecret returns the machine's stable telemetry secret,
// provisioning one on first use.
func EnsureTelemetrySecret() (string, error) {
	s, err := EnsureTelemetrySecrets()
	if err != nil {
		return "", err
	}
	return s.Secret, nil
}

// EnsureTelemetrySecrets resolves the secret state, migrating and provisioning
// as needed. The file at paths.TelemetrySecretPath is AUTHORITATIVE; agent.json's
// telemetry_secret is a mirror.
//
// ⚠️ THE MIGRATION MUST ADOPT, NEVER MINT. On a machine upgrading from the
// agent.json-only layout, every AI tool on disk already holds the old value.
// Generating a fresh secret here 401s all of them at once — which is exactly the
// 2026-09-18 outage, caused the other way round (a stale binary minting into
// agent.json while the tools kept a different value). So: file first, agent.json
// second, and a new secret only when neither exists.
func EnsureTelemetrySecrets() (TelemetrySecrets, error) {
	cur, err := ReadTelemetrySecrets()
	if err != nil {
		return TelemetrySecrets{}, err
	}
	if cur.Secret != "" {
		// Re-assert the mirror: a machine whose agent.json was rewritten by a
		// writer that did not know about this field would otherwise hand an older
		// binary a blank.
		if err := mirrorIntoAgentInfo(cur.Secret); err != nil {
			return TelemetrySecrets{}, err
		}
		return cur, nil
	}

	next := TelemetrySecrets{}
	info, err := Read()
	if err != nil {
		return TelemetrySecrets{}, err
	}
	if info != nil && info.TelemetrySecret != "" {
		next.Secret = info.TelemetrySecret // migrate, do not mint
	} else {
		sec, err := NewSecret()
		if err != nil {
			return TelemetrySecrets{}, err
		}
		next.Secret = sec
	}
	if err := writeTelemetrySecrets(next); err != nil {
		return TelemetrySecrets{}, err
	}
	if err := mirrorIntoAgentInfo(next.Secret); err != nil {
		return TelemetrySecrets{}, err
	}
	return next, nil
}

// RotateTelemetrySecret replaces the secret with a fresh one, recording the
// outgoing value and the instant.
//
// ⚠️ NOTHING IN THE PRODUCT CALLS THIS ON ITS OWN, and that is the design.
// Info.Secret is regenerated on every daemon start; doing the same here would
// rebuild the stale-credential bug the telemetry proxy exists to remove, one
// layer down and firing daily instead of rarely. It exists so that a rotation
// forced by an operator is a bounded, survivable event rather than an outage —
// see teleproxy's grace window.
func RotateTelemetrySecret() (string, error) {
	cur, err := ReadTelemetrySecrets()
	if err != nil {
		return "", err
	}
	sec, err := NewSecret()
	if err != nil {
		return "", err
	}
	if err := rotateTo(cur, sec); err != nil {
		return "", err
	}
	if err := mirrorIntoAgentInfo(sec); err != nil {
		return "", err
	}
	return sec, nil
}

// rotateTo persists next as the live secret, retiring cur.Secret into Previous.
// It does NOT mirror: callers either mirror themselves (RotateTelemetrySecret)
// or are already in the middle of writing agent.json (Write).
func rotateTo(cur TelemetrySecrets, next string) error {
	out := TelemetrySecrets{Secret: next}
	if cur.Secret != "" {
		out.Previous = cur.Secret
		out.RotatedAt = time.Now().UTC()
	}
	return writeTelemetrySecrets(out)
}

// writeTelemetrySecrets persists the state at 0600 via temp file + rename, so a
// concurrent reader (the daemon binding its proxy while a `keld signal setup`
// runs) never observes a torn file.
func writeTelemetrySecrets(s TelemetrySecrets) error {
	if s.Secret == "" {
		return errors.New("agentcfg: refusing to write an empty telemetry secret")
	}
	f := telemetrySecretFile{Secret: s.Secret, Previous: s.Previous}
	if !s.RotatedAt.IsZero() {
		t := s.RotatedAt.UTC()
		f.RotatedAt = &t
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.KeldHome(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(paths.KeldHome(), ".telemetry-secret-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, paths.TelemetrySecretPath())
}

// mirrorIntoAgentInfo keeps agent.json's telemetry_secret equal to the file.
//
// ⚠️ WRITE-THROUGH, AND IT IS NOT REDUNDANT. An older keld binary left on the
// machine reads agent.json and nothing else — in the 2026-09-18 incident that
// was a 3.0.0-rc.3 at /usr/local/keld/keld, ahead of the current install on
// PATH. Mirroring means such a binary configures tools with the SAME secret the
// running proxy accepts, instead of minting a second one. The file, not this
// mirror, is what every current reader resolves.
//
// An ABSENT agent.json is left absent: the daemon creates it with the ingress
// port and secret, and a file holding only a telemetry secret would advertise
// port 0 to the hook.
func mirrorIntoAgentInfo(secret string) error {
	info, err := Read()
	if err != nil || info == nil {
		// A corrupt or missing agent.json is not a reason to fail setup; the
		// daemon rewrites it on its next start and Write re-asserts the mirror
		// from the file at that point.
		return nil
	}
	if info.TelemetrySecret == secret {
		return nil
	}
	info.TelemetrySecret = secret
	return Write(*info)
}
