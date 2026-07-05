// Package auth manages Keld's stored authentication credentials.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// AuthData holds the credentials persisted to ~/.keld/auth.json.
type AuthData struct {
	AccessToken string `json:"access_token"`
	Principal   string `json:"principal"`
	Org         string `json:"org"`
	APIURL      string `json:"api_url"`
}

// Save writes auth to ~/.keld/auth.json with 0600 permissions.
// The file is written with 2-space indent, no HTML escaping, and a trailing newline.
func Save(auth AuthData) error {
	if err := os.MkdirAll(paths.KeldHome(), 0o755); err != nil {
		return err
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(auth); err != nil {
		return err
	}
	// json.Encoder.Encode already appends a newline.
	return os.WriteFile(paths.AuthPath(), buf.Bytes(), 0o600)
}

// Load reads ~/.keld/auth.json. If the file is missing, (nil, nil) is returned.
func Load() (*AuthData, error) {
	data, err := os.ReadFile(paths.AuthPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var auth AuthData
	if err := json.Unmarshal(data, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

// Clear removes ~/.keld/auth.json. Returns (true, nil) if the file existed and was
// removed, (false, nil) if it did not exist, or (false, err) on other errors.
func Clear() (bool, error) {
	err := os.Remove(paths.AuthPath())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}
