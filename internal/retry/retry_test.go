package retry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"syscall"
	"testing"
	"time"
)

func fast() Policy {
	return Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, Multiplier: 2, Jitter: false}
}

func TestDoRetriesTransientThenSucceeds(t *testing.T) {
	n := 0
	err := Do(context.Background(), fast(), func() error {
		n++
		if n < 3 {
			return HTTPStatus(503)
		}
		return nil
	})
	if err != nil || n != 3 {
		t.Fatalf("err=%v attempts=%d, want nil/3", err, n)
	}
}

func TestDoPermanentFailsFast(t *testing.T) {
	n := 0
	err := Do(context.Background(), fast(), func() error { n++; return HTTPStatus(404) })
	if err == nil || n != 1 {
		t.Fatalf("err=%v attempts=%d, want error/1", err, n)
	}
}

func TestDoUnknownErrorNotRetried(t *testing.T) {
	n := 0
	_ = Do(context.Background(), fast(), func() error { n++; return errors.New("mystery") })
	if n != 1 {
		t.Fatalf("attempts=%d, want 1 (unknown=permanent)", n)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	n := 0
	err := Do(context.Background(), fast(), func() error { n++; return HTTPStatus(500) })
	if err == nil || n != 4 {
		t.Fatalf("err=%v attempts=%d, want error/4", err, n)
	}
}

func TestDoStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Policy{MaxAttempts: 5, BaseDelay: time.Hour, MaxDelay: time.Hour, Multiplier: 2}
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	start := time.Now()
	err := Do(ctx, p, func() error { return HTTPStatus(503) })
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%s, want prompt context.Canceled", err, time.Since(start))
	}
}

func TestIsTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{HTTPStatus(503), true}, {HTTPStatus(429), true}, {HTTPStatus(404), false},
		{HTTPStatus(401), false}, {context.Canceled, false}, {context.DeadlineExceeded, true},
		{syscall.ECONNREFUSED, true}, {io.ErrUnexpectedEOF, true}, {errors.New("x"), false},
	}
	for _, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("IsTransient(%v)=%v want %v", c.err, got, c.want)
		}
	}
}

func TestBackoffSequenceAndCap(t *testing.T) {
	p := Policy{BaseDelay: time.Second, MaxDelay: 5 * time.Second, Multiplier: 2, Jitter: false}
	got := []time.Duration{backoff(p, 1, 0), backoff(p, 2, 0), backoff(p, 3, 0), backoff(p, 4, 0)}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second} // capped
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("backoff attempt %d = %s, want %s", i+1, got[i], want[i])
		}
	}
}

func TestBackoffClampsOverflowToMaxDelay(t *testing.T) {
	// A large attempt count overflows the float64 exponentiation; the result must
	// still clamp to MaxDelay, never a negative/zero delay (which would busy-retry).
	p := Policy{BaseDelay: time.Second, MaxDelay: 30 * time.Second, Multiplier: 2, Jitter: false}
	if got := backoff(p, 60, 0); got != 30*time.Second {
		t.Fatalf("backoff(attempt=60) = %s, want MaxDelay 30s (overflow must clamp)", got)
	}
}

func TestBackoffHonorsRetryAfter(t *testing.T) {
	p := Policy{BaseDelay: time.Second, MaxDelay: 30 * time.Second, Multiplier: 2, Jitter: false}
	if got := backoff(p, 1, 10*time.Second); got != 10*time.Second {
		t.Fatalf("backoff w/ Retry-After=%s, want 10s", got)
	}
}

func TestBackoffJitterBounded(t *testing.T) {
	p := Policy{BaseDelay: time.Second, MaxDelay: time.Second, Multiplier: 2, Jitter: true}
	for i := 0; i < 100; i++ {
		if d := backoff(p, 1, 0); d < 0 || d > time.Second {
			t.Fatalf("jittered backoff %s out of [0,1s]", d)
		}
	}
}

// ⚠️ A *url.Error CARRYING io.EOF MEANS THE REQUEST NEVER COMPLETED — the server
// closed the connection before answering, which is what a dropped keep-alive
// looks like to Go (and which it will not retry itself for a POST). That is a
// transport fault, not a verdict, so it must classify as transient.
//
// It cost a real flow: the CLI's device-login poll aborted on its first attempt
// against a local Atlas (`Post ".../device/poll": EOF`) while the server logged
// the same request as 202, which in the macOS installer made the approval page
// appear and vanish before anyone could use it.
//
// Bare io.EOF is deliberately NOT transient: outside a round trip it is how an
// empty body reads to a decoder, and retrying that would hammer a server that
// answered perfectly well.
func TestIsTransientOnAConnectionClosedBeforeTheResponse(t *testing.T) {
	roundTrip := &url.Error{Op: "Post", URL: "https://atlas.example/v1/cli/device/poll", Err: io.EOF}
	if !IsTransient(roundTrip) {
		t.Fatal("a url.Error wrapping io.EOF must be transient: the request never completed")
	}
	if !IsTransient(fmt.Errorf("network error contacting Atlas: %w", roundTrip)) {
		t.Fatal("it must still classify when wrapped by a caller's message")
	}
	if IsTransient(io.EOF) {
		t.Fatal("a bare io.EOF is an empty body, not a transport failure — retrying it hammers a healthy server")
	}
}
