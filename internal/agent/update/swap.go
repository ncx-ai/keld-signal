package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Swap performs a set of file/tree replacements that can be undone as a unit.
//
// The pattern is DISPLACE, NEVER DELETE — the same one scripts/install.sh uses
// for the sidecar tree: the outgoing artifact is renamed to <name>.prev and
// only removed once the whole update is confirmed. A failure partway through
// therefore has somewhere to restore from, which is the difference between a
// failed update and a machine with no working binaries.
type Swap struct {
	moves []move
}

type move struct {
	target string // where the new artifact now lives
	prev   string // where the old one was parked ("" if the target did not exist)
}

func NewSwap() *Swap { return &Swap{} }

// Replace moves staged into target, parking whatever was at target as
// target+".prev".
//
// os.Rename is used throughout rather than a copy: on Unix a rename over a
// running binary leaves the live process holding its own inode, so the daemon
// replacing itself does not crash mid-swap. On Windows a running .exe cannot
// be OVERWRITTEN but can be RENAMED, which is why the order here is
// displace-then-place and never place-over.
//
// os.Lstat, not os.Stat: on a macOS pkg machine the target may be a root-owned
// SYMLINK pointing back at /usr/local/keld (the pkg's postinstall creates
// those). Following it would displace the pkg's own binary, which we do not
// own; the link itself lives in a user-owned directory and is ours to replace.
func (s *Swap) Replace(target, staged string) error {
	m := move{target: target}
	if _, err := os.Lstat(target); err == nil {
		prev := target + ".prev"
		// A stale .prev from an earlier update must not block this one.
		if err := os.RemoveAll(prev); err != nil {
			return fmt.Errorf("update: clearing %s: %w", prev, err)
		}
		if err := os.Rename(target, prev); err != nil {
			return fmt.Errorf("update: displacing %s: %w", target, err)
		}
		m.prev = prev
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("update: stat %s: %w", target, err)
	}
	if err := os.Rename(staged, target); err != nil {
		// Put the displaced copy straight back: this move never happened.
		if m.prev != "" {
			_ = os.Rename(m.prev, target)
		}
		return fmt.Errorf("update: installing %s: %w", target, err)
	}
	s.moves = append(s.moves, m)
	return nil
}

// Prev lists the parked copies, for the state marker.
func (s *Swap) Prev() []string {
	out := make([]string, 0, len(s.moves))
	for _, m := range s.moves {
		if m.prev != "" {
			out = append(out, m.prev)
		}
	}
	return out
}

// Rollback undoes every completed move, most recent first.
//
// It returns an error naming what it could not restore. That case — the swap
// failed AND the restore failed — is the worst state this package can reach,
// and it must never be swallowed: the caller must not go on to restart a
// machine it believes is intact.
func (s *Swap) Rollback() error {
	var errs []error
	for i := len(s.moves) - 1; i >= 0; i-- {
		m := s.moves[i]
		if m.prev == "" {
			// Nothing was displaced; the new artifact is simply removed.
			if err := os.RemoveAll(m.target); err != nil {
				errs = append(errs, fmt.Errorf("removing %s: %w", m.target, err))
			}
			continue
		}
		if _, err := os.Lstat(m.prev); err != nil {
			errs = append(errs, fmt.Errorf("cannot restore %s: the previous copy at %s is gone: %w", m.target, m.prev, err))
			continue
		}
		if err := os.RemoveAll(m.target); err != nil {
			errs = append(errs, fmt.Errorf("clearing %s: %w", m.target, err))
			continue
		}
		if err := os.Rename(m.prev, m.target); err != nil {
			errs = append(errs, fmt.Errorf("restoring %s: %w", m.target, err))
		}
	}
	s.moves = nil
	return errors.Join(errs...)
}

// Commit discards the parked copies. Called only once the new version has been
// confirmed healthy — never at swap time, which is the whole point.
func (s *Swap) Commit() {
	for _, m := range s.moves {
		if m.prev != "" {
			_ = os.RemoveAll(m.prev)
		}
	}
	s.moves = nil
}

// safeJoin resolves name under dir, refusing anything that would escape it.
// The archive arrived over the network; it does not get to choose where on the
// filesystem it lands.
func safeJoin(dir, name string) (string, error) {
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("update: archive entry %q is an absolute path", name)
	}
	// REFUSE a traversal rather than neutralizing it. Cleaning "../escape" into
	// "escape" would silently install a file the archive did not ask for, in a
	// place it did not name; an archive containing such an entry is a signal
	// that something is wrong with the release, not a thing to tidy up.
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "", fmt.Errorf("update: archive entry %q escapes the destination", name)
		}
	}
	p := filepath.Join(dir, filepath.Clean("/"+name))
	rel, err := filepath.Rel(dir, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("update: archive entry %q escapes the destination", name)
	}
	return p, nil
}

// ExtractTarGz unpacks archive into destDir.
func ExtractTarGz(archive, destDir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("update: %s is not a gzip stream: %w", filepath.Base(archive), err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("update: reading %s: %w", filepath.Base(archive), err)
		}
		p, err := safeJoin(destDir, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeExtracted(p, tr, os.FileMode(h.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			// A symlink inside the bundle may not point outside it either.
			if _, err := safeJoin(destDir, filepath.Join(filepath.Dir(h.Name), h.Linkname)); err != nil {
				return err
			}
			_ = os.Remove(p)
			if err := os.Symlink(h.Linkname, p); err != nil {
				return err
			}
		}
	}
}

// ExtractZip unpacks a zip (the Windows release archive) into destDir.
func ExtractZip(archive, destDir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("update: %s is not a zip: %w", filepath.Base(archive), err)
	}
	defer zr.Close()
	for _, e := range zr.File {
		p, err := safeJoin(destDir, e.Name)
		if err != nil {
			return err
		}
		if e.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		rc, err := e.Open()
		if err != nil {
			return err
		}
		err = writeExtracted(p, rc, e.Mode().Perm()|0o600)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeExtracted creates p with mode, preserving the executable bit — an
// extracted binary that lost it would swap in cleanly and then fail to start,
// which is exactly the failure the confirm pass would have to undo.
func writeExtracted(p string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
