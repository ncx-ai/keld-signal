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

	// A non-empty existing file is authoritative. An empty/whitespace-only file
	// (e.g. a crash between create and flush from an older non-atomic writer) is
	// treated as absent and regenerated, so a torn write can never pin the id to
	// "" forever.
	if existing, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(existing)); id != "" {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)

	dir := paths.KeldHome()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	// Write to a temp file in the destination directory, flush + close it, then
	// atomically rename into place. The id only ever becomes visible at its final
	// path fully written — a crash or write/close failure leaves the temp file (or
	// nothing), never an empty install-id.
	tmp, err := os.CreateTemp(dir, "install-id-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.WriteString(id); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		cleanup()
		return "", err
	}

	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return "", err
	}

	// A concurrent first-writer may have renamed its own (equally valid) id into
	// place; rename is last-writer-wins, so re-read to return whatever is now
	// authoritative and converge on a single stable id.
	if existing, err := os.ReadFile(path); err == nil {
		if winner := strings.TrimSpace(string(existing)); winner != "" {
			return winner, nil
		}
	}
	return id, nil
}
