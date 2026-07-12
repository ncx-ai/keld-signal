// Package retry runs an operation with configurable exponential backoff + jitter,
// retrying only faults classified TRANSIENT (network blips/timeouts, HTTP
// 408/429/5xx) and giving up immediately on PERMANENT ones (HTTP 4xx, unknown
// errors, or a cancelled context). One canonical classifier (IsTransient) keeps
// every adopter consistent — use it for pulling required dependencies over the
// network (e.g. the HF model download).
package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strconv"
	"syscall"
	"time"
)

type Policy struct {
	MaxAttempts int           // total tries incl. the first
	BaseDelay   time.Duration // first backoff
	MaxDelay    time.Duration // per-sleep cap
	Multiplier  float64       // growth factor
	Jitter      bool          // full jitter in [0, computed]
}

func DefaultPolicy() Policy {
	p := Policy{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: 30 * time.Second, Multiplier: 2.0, Jitter: true}
	if v, err := strconv.Atoi(os.Getenv("KELD_RETRY_MAX_ATTEMPTS")); err == nil && v > 0 {
		p.MaxAttempts = v
	}
	if v, err := strconv.Atoi(os.Getenv("KELD_RETRY_BASE_MS")); err == nil && v > 0 {
		p.BaseDelay = time.Duration(v) * time.Millisecond
	}
	if v, err := strconv.Atoi(os.Getenv("KELD_RETRY_MAX_MS")); err == nil && v > 0 {
		p.MaxDelay = time.Duration(v) * time.Millisecond
	}
	return p
}

// StatusError carries an HTTP status (+ optional Retry-After) so IsTransient can
// judge a non-2xx response. Return retry.HTTPStatus(code) from op on non-2xx.
type StatusError struct {
	Code       int
	RetryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("http status %d", e.Code) }

func HTTPStatus(code int) error { return &StatusError{Code: code} }

// IsTransient is the canonical transient-vs-permanent classifier.
func IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true // per-attempt timeout
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case 408, 429, 500, 502, 503, 504:
			return true
		default:
			return false
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var de *net.DNSError
	if errors.As(err, &de) && de.IsTemporary {
		return true
	}
	return false // unrecognized -> permanent (don't hammer)
}

func Do(ctx context.Context, p Policy, op func() error) error {
	return DoClassify(ctx, p, IsTransient, op)
}

func DoClassify(ctx context.Context, p Policy, classify func(error) bool, op func() error) error {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt >= p.MaxAttempts || !classify(err) {
			return fmt.Errorf("retry: gave up after %d attempt(s): %w", attempt, err)
		}
		var ra time.Duration
		var se *StatusError
		if errors.As(err, &se) {
			ra = se.RetryAfter
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff(p, attempt, ra)):
		}
	}
}

// backoff is the sleep before the (attempt+1)-th try. Pure + deterministic when
// Jitter is false, so it is unit-tested directly.
func backoff(p Policy, attempt int, retryAfter time.Duration) time.Duration {
	d := float64(p.BaseDelay)
	for i := 1; i < attempt; i++ {
		d *= p.Multiplier
	}
	dd := time.Duration(d)
	if dd < 0 { // float64 overflow wraps to a negative Duration — clamp to the cap
		dd = p.MaxDelay
	}
	if p.MaxDelay > 0 && dd > p.MaxDelay {
		dd = p.MaxDelay
	}
	if retryAfter > dd {
		dd = retryAfter
		if p.MaxDelay > 0 && dd > p.MaxDelay {
			dd = p.MaxDelay
		}
	}
	if p.Jitter && dd > 0 {
		dd = time.Duration(rand.Int63n(int64(dd) + 1))
	}
	return dd
}
