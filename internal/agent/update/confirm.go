package update

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultConfirmDeadline bounds how long an update may stay unproven. Past it,
// the swap is undone regardless of which version is running.
const DefaultConfirmDeadline = 15 * time.Minute

// Confirm resolves any update left in flight. It runs at daemon startup,
// before anything else is wired.
//
// The marker is written before the restart and cleared only by a daemon that
// came up as the new version. Three outcomes:
//
//   - running the NEW version, within the deadline → the update worked. Clear
//     the marker and drop the parked copies.
//   - running the OLD version → the restart did not take. Restore and record
//     the failure.
//   - past the deadline, whichever version is running → the new binary never
//     got far enough to clear its own marker, i.e. it crashed on startup.
//     Restore. This is the case that makes a bad release self-healing instead
//     of a bricked fleet, and it is why the deadline exists at all.
//
// The failed version is recorded either way it fails, because Atlas still pins
// it: without that memory the next settings poll re-applies it and the machine
// loops — swap, crash, roll back, swap.
func Confirm(statePath, current string, now time.Time, deadline time.Duration, restart Restarter, emit func(code, sev string, fields map[string]any)) error {
	if emit == nil {
		emit = func(string, string, map[string]any) {}
	}
	if deadline <= 0 {
		deadline = DefaultConfirmDeadline
	}
	s, err := LoadState(statePath)
	if err != nil || !s.PendingConfirm {
		return err
	}

	stale := now.Sub(s.AttemptedAt) > deadline
	if NormalizeVersion(current) == NormalizeVersion(s.To) && !stale {
		return confirmed(statePath, s, emit)
	}

	reason := "wrong_version"
	if stale {
		reason = "unconfirmed_deadline"
	}
	return rollBack(statePath, s, current, reason, restart, emit)
}

// confirmed accepts the new version: the marker goes, and so do the parked
// copies — they are the only thing that could have undone this, so they are
// dropped only once the version is proven.
func confirmed(statePath string, s State, emit func(string, string, map[string]any)) error {
	for _, p := range s.Prev {
		_ = os.RemoveAll(p)
	}
	applied := s.To
	s.PendingConfirm = false
	s.Prev = nil
	s.LastOutcome = "applied"
	s.LastError = ""
	s.From = ""
	log.Printf("keld-agent: update to %s confirmed", applied)
	emit("update.applied", "info", map[string]any{"version": applied})
	return SaveState(statePath, s)
}

// rollBack restores each parked copy over its target.
//
// The marker is cleared even when the restore FAILS. Leaving it pending would
// mean rolling back again at every subsequent start, forever, over files that
// are not there — an unrecoverable state retried is still unrecoverable, and
// the loop hides the original error behind its own noise.
func rollBack(statePath string, s State, current, reason string, restart Restarter, emit func(string, string, map[string]any)) error {
	var errs []error
	restored := 0
	for _, prev := range s.Prev {
		target := strings.TrimSuffix(prev, ".prev")
		if target == prev {
			continue
		}
		if _, err := os.Lstat(prev); err != nil {
			errs = append(errs, fmt.Errorf("cannot restore %s: %w", filepath.Base(target), err))
			continue
		}
		if err := os.RemoveAll(target); err != nil {
			errs = append(errs, fmt.Errorf("clearing %s: %w", filepath.Base(target), err))
			continue
		}
		if err := os.Rename(prev, target); err != nil {
			errs = append(errs, fmt.Errorf("restoring %s: %w", filepath.Base(target), err))
			continue
		}
		restored++
	}

	failed := s.To
	s.PendingConfirm = false
	s.Prev = nil
	s.MarkFailed(failed)
	s.From = ""

	if len(errs) > 0 {
		joined := errors.Join(errs...)
		s.LastOutcome = "rollback_failed"
		s.LastError = joined.Error()
		_ = SaveState(statePath, s)
		log.Printf("keld-agent: UPDATE ROLLBACK FAILED — install may be inconsistent: %v", joined)
		emit("update.failed", "error", map[string]any{
			"stage": "rollback", "version": failed, "reason": reason, "error": joined.Error(),
		})
		// Deliberately no restart: the machine is in an unknown state and
		// bouncing the service cannot improve it, only obscure it.
		return fmt.Errorf("update: restore after a failed update to %s: %w", failed, joined)
	}

	s.LastOutcome = "rolled_back"
	s.LastError = reason
	if err := SaveState(statePath, s); err != nil {
		return err
	}
	log.Printf("keld-agent: update to %s did not come up (%s); restored the previous version (%d artifact(s))", failed, reason, restored)
	emit("update.rolled_back", "error", map[string]any{
		"version": failed, "reason": reason, "running": current, "restored": restored,
	})
	if restart != nil && restored > 0 {
		if err := restart.Restart(); err != nil {
			emit("update.restart_failed", "error", map[string]any{"stage": "rollback", "error": err.Error()})
		}
	}
	return nil
}
