// internal/config/writer.go
package config

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ncx-ai/keld-cli/internal/paths"
)

// WriteAtomic writes text to path atomically: mkdir parent dirs, optionally
// create a one-time .keld.bak sibling if backup is true and the file exists,
// write to a temp file in the same directory, then rename over the target.
// The temp file is cleaned up on any error path.
func WriteAtomic(path, text string, backup bool) error {
	parentDir := filepath.Dir(path)
	if err := os.MkdirAll(parentDir, 0o755); err != nil {
		return err
	}

	if backup {
		if _, err := os.Stat(path); err == nil {
			bak := path + ".keld.bak"
			if _, err := os.Stat(bak); os.IsNotExist(err) {
				if err := copyFile(path, bak); err != nil {
					return err
				}
			}
		}
	}

	tmp, err := os.CreateTemp(parentDir, ".keld-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()

	_, writeErr := io.WriteString(tmp, text)
	closeErr := tmp.Close()

	if writeErr != nil {
		os.Remove(tmpPath)
		return writeErr
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return closeErr
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return nil
}

// DeleteIfEmpty deletes the file at path if text trims to "" or "{}".
// Returns true when the condition holds (file deleted or absent), false otherwise.
func DeleteIfEmpty(path, text string) (bool, error) {
	if strings.TrimSpace(text) == "" || strings.TrimSpace(text) == "{}" {
		if _, err := os.Stat(path); err == nil {
			if err := os.Remove(path); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	return false, nil
}

// BackupConfig copies path into paths.BackupsDir()/<toolName>/<basename>
// before Keld modifies it. One-time: if the destination already exists the
// pristine pre-Keld copy is preserved and "" is returned. Returns "" if the
// source is missing or a backup already exists, or the destination path on
// success.
func BackupConfig(path, toolName string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}

	dest := filepath.Join(paths.BackupsDir(), toolName, filepath.Base(path))
	if _, err := os.Stat(dest); err == nil {
		// backup already exists — preserve pristine copy
		return "", nil
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}

	if err := copyFile(path, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// copyFile copies src to dst, preserving file contents byte-for-byte.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
