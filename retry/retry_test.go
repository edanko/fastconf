package retry

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultIsUsable(t *testing.T) {
	t.Parallel()

	policy := Default()
	if policy.MaxAttempts < 2 {
		t.Fatalf("Default().MaxAttempts = %d, want at least 2", policy.MaxAttempts)
	}
	if policy.InitialInterval <= 0 || policy.MaxInterval < policy.InitialInterval {
		t.Fatalf("Default() intervals are invalid: %+v", policy)
	}
	if policy.Multiplier < 1 {
		t.Fatalf("Default().Multiplier = %v, want at least 1", policy.Multiplier)
	}
	if policy.Jitter < 0 || policy.Jitter > 1 {
		t.Fatalf("Default().Jitter = %v, want [0, 1]", policy.Jitter)
	}
}

func TestDoSucceedsFirstTime(t *testing.T) {
	t.Parallel()

	attempts := 0
	err := Policy{MaxAttempts: 3, InitialInterval: time.Millisecond}.Do(context.Background(), func(context.Context) error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestDoRetriesUntilSuccess(t *testing.T) {
	t.Parallel()

	var attempts int
	policy := Policy{
		MaxAttempts:     5,
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		Multiplier:      2,
	}
	err := policy.Do(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 4 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if attempts != 4 {
		t.Fatalf("attempts = %d, want 4", attempts)
	}
}

func TestDoExhaustsAttemptsAndReturnsLastError(t *testing.T) {
	t.Parallel()

	first := errors.New("first")
	last := errors.New("last")
	var attempts int
	err := Policy{MaxAttempts: 3, InitialInterval: time.Millisecond}.Do(context.Background(), func(context.Context) error {
		attempts++
		if attempts == 1 {
			return first
		}
		return last
	})
	if !errors.Is(err, last) {
		t.Fatalf("Do returned %v, want the last error %v", err, last)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want MaxAttempts = 3", attempts)
	}
}

func TestDoStopsOnCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := Default().Do(ctx, func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v, want context.Canceled", err)
	}
	if called {
		t.Fatal("fn was called with an already cancelled context")
	}
}

func TestDoInterruptsBackoffWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	err := Policy{
		MaxAttempts:     5,
		InitialInterval: 5 * time.Second,
		MaxInterval:     5 * time.Second,
	}.Do(ctx, func(context.Context) error {
		return errors.New("still failing")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do returned %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Do waited %v after cancellation, want a prompt return", elapsed)
	}
}

func TestDoWithZeroPolicyUsesDefaults(t *testing.T) {
	t.Parallel()

	var attempts int
	policy := Policy{InitialInterval: time.Millisecond, Jitter: 0}
	err := policy.Do(context.Background(), func(context.Context) error {
		attempts++
		if attempts < 2 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestDelayStaysWithinBounds(t *testing.T) {
	t.Parallel()

	policy := Policy{
		MaxAttempts:     10,
		InitialInterval: time.Second,
		MaxInterval:     time.Minute,
		Multiplier:      1e300,
		Jitter:          0,
	}.withDefaults()

	for attempt := 1; attempt <= 1000; attempt++ {
		delay := policy.delay(attempt)
		if delay < 0 {
			t.Fatalf("delay(%d) = %v, want a non-negative duration", attempt, delay)
		}
		if delay > policy.MaxInterval {
			t.Fatalf("delay(%d) = %v, want at most %v", attempt, delay, policy.MaxInterval)
		}
	}
}

func TestDelayRejectsNonFiniteValues(t *testing.T) {
	t.Parallel()

	for name, policy := range map[string]Policy{
		"nan multiplier":   {InitialInterval: time.Millisecond, MaxInterval: time.Millisecond, Multiplier: math.NaN()},
		"infinite":         {InitialInterval: time.Millisecond, MaxInterval: time.Millisecond, Multiplier: math.Inf(1)},
		"negative jitter":  {InitialInterval: time.Millisecond, MaxInterval: time.Millisecond, Multiplier: 2, Jitter: -1},
		"excessive jitter": {InitialInterval: time.Millisecond, MaxInterval: time.Millisecond, Multiplier: 2, Jitter: 10},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			normalized := policy.withDefaults()
			if delay := normalized.delay(3); delay < 0 || delay > normalized.MaxInterval {
				t.Fatalf("delay(3) = %v, want [0, %v]", delay, normalized.MaxInterval)
			}
		})
	}
}

func TestDelayAppliesJitterWithinFraction(t *testing.T) {
	t.Parallel()

	base := 100 * time.Millisecond
	policy := Policy{
		InitialInterval: base,
		MaxInterval:     time.Second,
		Multiplier:      1,
		Jitter:          0.5,
	}.withDefaults()

	for range 100 {
		delay := policy.delay(1)
		if delay < base/2 || delay > base*3/2 {
			t.Fatalf("delay = %v, want within [%v, %v]", delay, base/2, base*3/2)
		}
	}
}

func TestDelayGrowsWithMultiplier(t *testing.T) {
	t.Parallel()

	policy := Policy{
		InitialInterval: 10 * time.Millisecond,
		MaxInterval:     time.Second,
		Multiplier:      2,
	}.withDefaults()

	previous := time.Duration(0)
	for attempt := 1; attempt <= 4; attempt++ {
		delay := policy.delay(attempt)
		if delay < previous {
			t.Fatalf("delay(%d) = %v shrank below %v", attempt, delay, previous)
		}
		previous = delay
	}
}

func TestDoRetriesEveryError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("sentinel")
	var attempts atomic.Int32
	err := Policy{MaxAttempts: 3, InitialInterval: time.Millisecond}.Do(context.Background(), func(context.Context) error {
		attempts.Add(1)
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do returned %v, want sentinel", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
}
