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
	// daemon at startup. Zero/absent when ML is disabled or the deterministic
	// backend is in use. Lets `keld-agent metrics` reach the sidecar's /metrics.
	SidecarPort int `json:"sidecar_port,omitempty"`
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
func Write(info Info) error {
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
	return os.WriteFile(paths.AgentInfoPath(), buf.Bytes(), 0o600)
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
