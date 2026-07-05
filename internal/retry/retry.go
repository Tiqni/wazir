// Package retry provides bounded exponential-backoff retries for transient
// failures. It is a leaf utility with no provider imports, so packages that use
// it keep the orchestrator core provider-agnostic (CLAUDE.md rule 1).
package retry

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// Policy configures Do/Backoff. MaxAttempts is the total number of tries
// including the first, so a value <= 1 means "run once, never retry".
type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Classifier reports whether err is worth retrying and, optionally, a minimum
// delay before the next attempt (e.g. a rate-limit reset). A retryAfter <= 0
// means "use the computed backoff".
type Classifier func(err error) (retry bool, retryAfter time.Duration)

// jitter draws a delay in [0, d]. A package var so tests can pin it.
var jitter = func(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d)))
}

// Do runs fn until it returns nil, the classifier declines, attempts are
// exhausted, or ctx is done. Between attempts it sleeps Backoff(p, attempt), or
// the classifier's retryAfter when that is larger. On a classifier-declined
// error it returns that error unwrapped; on exhaustion it wraps the last error
// with the attempt count.
func Do(ctx context.Context, p Policy, classify Classifier, fn func() error) error {
	attempts := p.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if attempt == attempts {
			break
		}
		ok, retryAfter := classify(err)
		if !ok {
			return err
		}
		delay := Backoff(p, attempt)
		if retryAfter > delay {
			delay = retryAfter
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("retry cancelled after %d attempt(s): %w", attempt, ctx.Err())
		case <-time.After(delay):
		}
	}
	return fmt.Errorf("gave up after %d attempt(s): %w", attempts, err)
}

// Backoff returns the jittered delay to wait before the (attempt+1)-th try.
// attempt is 1-based. The pre-jitter delay is BaseDelay*2^(attempt-1) capped at
// MaxDelay; full jitter then draws a value in [0, that].
func Backoff(p Policy, attempt int) time.Duration {
	if p.BaseDelay <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	d := p.BaseDelay << (attempt - 1) // BaseDelay * 2^(attempt-1)
	if d <= 0 || d > p.MaxDelay {      // overflow, or over the cap
		d = p.MaxDelay
	}
	return jitter(d)
}
