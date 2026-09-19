// Package agentcfg reads/writes ~/.keld/agent.json — the discovery file the
// hook uses to locate and authenticate to the running daemon.
package agentcfg

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Info is the on-disk shape of ~/.keld/agent.json.
type Info struct {
	Port   int    `json:"port"`
	Secret string `json:"secret"`
	// SidecarPort is the loopback port of the GLiNER2 sidecar, allocated by the
	// daemon at startup. Zero/absent when ML is disabled (`ml_backend=off`) or
	// the sidecar isn't up yet. Lets `keld-agent metrics` reach the sidecar's
	// /metrics.
	SidecarPort int `json:"sidecar_port,omitempty"`
	// TelemetrySecret authenticates AI tools to the daemon's loopback telemetry
	// listener.
	//
	// ⚠️ STABLE AND NEVER ROTATED, which is its entire job. It is written into
	// every tool's config by `keld signal setup`, and tools read their config
	// once at startup — so a secret that changed would strand every running tool
	// exactly as a rotated Atlas ingest token does. Contrast Secret above, which
	// is regenerated on EVERY daemon start and must never reach a tool config.
	// Write preserves this field when a caller omits it, for the same reason.
	TelemetrySecret string `json:"telemetry_secret,omitempty"`
}

// NewSecret returns a 32-byte random secret as a 64-char hex string.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Write persists info to ~/.keld/agent.json (mode 0600).
//
// ⚠️ A CALLER THAT OMITS TelemetrySecret DOES NOT ERASE IT, AND CANNOT TOUCH THE
// SECRET FILE AT ALL. The daemon rewrites this file on every start with a freshly
// generated ingress secret and no telemetry secret in hand (daemon.go's
// Write(Info{Port, Secret})); without this rule that write would destroy the
// stable secret sitting in every AI tool's config, and the tools would go stale
// on the next daemon restart — the very bug the telemetry proxy exists to remove,
// rebuilt one layer down and firing daily instead of rarely.
//
// The omitted value is refilled from paths.TelemetrySecretPath FIRST and only
// then from the previous agent.json, because that file is the source of truth:
// on 2026-09-18 a stale agent.json was exactly what disagreed with the tools on a
// real machine, and a rule that trusted it would re-assert the wrong value here.
//
// An explicit non-empty value that DIFFERS from the file is a deliberate
// rotation: the file is updated and the outgoing value retired into `previous`
// with its instant, so teleproxy's grace window can keep already-configured
// tools alive. Nothing in the product takes that path on its own.
func Write(info Info) error {
	cur, curErr := ReadTelemetrySecrets()
	switch {
	case info.TelemetrySecret == "":
		if curErr == nil && cur.Secret != "" {
			info.TelemetrySecret = cur.Secret
		} else if prev, err := Read(); err == nil && prev != nil {
			info.TelemetrySecret = prev.TelemetrySecret
		}
	case curErr == nil && info.TelemetrySecret != cur.Secret:
		if err := rotateTo(cur, info.TelemetrySecret); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		return err
	}
	// Write to a temp file then rename, so a concurrent reader (e.g. the hook
	// reading the ingress port/secret) never observes a torn file — matters
	// because SetSidecarPort rewrites agent.json at daemon startup.
	tmp, err := os.CreateTemp(paths.KeldHome(), ".agent-*.json.tmp")
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
	return os.Rename(tmpName, paths.AgentInfoPath())
}

// SetSidecarPort updates the SidecarPort field of the existing agent.json,
// preserving the daemon port/secret. Errors if agent.json is absent — the
// daemon writes it (with port + secret) before the sidecar port is known.
func SetSidecarPort(port int) error {
	info, err := Read()
	if err != nil {
		return err
	}
	if info == nil {
		return errors.New("agentcfg: agent.json not found")
	}
	info.SidecarPort = port
	return Write(*info)
}

// Read returns the info, or (nil, nil) if the file is absent.
func Read() (*Info, error) {
	data, err := os.ReadFile(paths.AgentInfoPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, err
	}
	return &info, nil
}
