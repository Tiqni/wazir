package retry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// always classifies every error as retryable with no extra delay.
func always(error) (bool, time.Duration) { return true, 0 }

func TestDoSucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 4}, always, func() error {
		calls++
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d, want nil/1", err, calls)
	}
}

func TestDoRetriesThenSucceeds(t *testing.T) {
	old := jitter
	jitter = func(time.Duration) time.Duration { return 0 } // no real sleeping
	defer func() { jitter = old }()

	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		always, func() error {
			calls++
			if calls < 3 {
				return errors.New("boom")
			}
			return nil
		})
	if err != nil || calls != 3 {
		t.Fatalf("err=%v calls=%d, want nil/3", err, calls)
	}
}

func TestDoExhausts(t *testing.T) {
	old := jitter
	jitter = func(time.Duration) time.Duration { return 0 }
	defer func() { jitter = old }()

	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		always, func() error { calls++; return errors.New("boom") })
	if calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
	if err == nil || !strings.Contains(err.Error(), "gave up after 3") {
		t.Fatalf("want a give-up error mentioning 3 attempts, got %v", err)
	}
}

func TestDoStopsWhenClassifierDeclines(t *testing.T) {
	calls := 0
	err := Do(context.Background(), Policy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		func(error) (bool, time.Duration) { return false, 0 },
		func() error { calls++; return errors.New("permanent") })
	if calls != 1 || err == nil || err.Error() != "permanent" {
		t.Fatalf("calls=%d err=%v, want 1/permanent (unwrapped)", calls, err)
	}
}

func TestDoHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done, so the backoff select takes the ctx branch
	calls := 0
	err := Do(ctx, Policy{MaxAttempts: 4, BaseDelay: 10 * time.Second, MaxDelay: time.Minute},
		always, func() error { calls++; return errors.New("boom") })
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d err=%v, want 1 call and a Canceled error", calls, err)
	}
}

func TestBackoffCapsAndZeroes(t *testing.T) {
	old := jitter
	jitter = func(d time.Duration) time.Duration { return d } // identity: return the pre-jitter value
	defer func() { jitter = old }()

	p := Policy{BaseDelay: time.Second, MaxDelay: 4 * time.Second}
	if got := Backoff(p, 1); got != time.Second {
		t.Errorf("attempt 1: got %v want 1s", got)
	}
	if got := Backoff(p, 2); got != 2*time.Second {
		t.Errorf("attempt 2: got %v want 2s", got)
	}
	if got := Backoff(p, 10); got != 4*time.Second { // capped at MaxDelay
		t.Errorf("attempt 10: got %v want 4s (cap)", got)
	}
	if got := Backoff(Policy{}, 1); got != 0 { // zero BaseDelay => 0
		t.Errorf("zero policy: got %v want 0", got)
	}
}
