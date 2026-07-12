// Package clientevents captures structured client events for the keld-agent
// daemon (sub-project A: Signal Client telemetry capture).
package clientevents

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// InstallID returns a stable, random per-install identifier, generating and
// persisting one on first call. The id is not a secret and is safe to publish
// alongside telemetry — it only distinguishes one keld install from another.
func InstallID() (string, error) {
	path := paths.InstallIDPath()

	if existing, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(existing)), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)

	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return "", err
	}

	// Create exclusively so a concurrent first-writer race is benign: whoever
	// loses re-reads the winner's id instead of erroring or overwriting it.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return "", readErr
			}
			return strings.TrimSpace(string(existing)), nil
		}
		return "", err
	}
	defer f.Close()

	if _, err := f.WriteString(id); err != nil {
		return "", err
	}

	return id, nil
}
