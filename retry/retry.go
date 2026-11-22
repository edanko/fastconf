// Package retry runs an operation repeatedly with exponential backoff until it
// succeeds, the attempt budget is exhausted or the context is cancelled.
//
// The package is intentionally small: it works on plain error-returning
// functions so the same policy can be shared by HTTP sources, token endpoints
// and any other remote call.
package retry

import (
	"context"
	"math"
	"math/rand/v2"
	"time"
)

const (
	defaultMaxAttempts     = 3
	defaultInitialInterval = 100 * time.Millisecond
	defaultMaxInterval     = 5 * time.Second
	defaultMultiplier      = 2.0
	defaultJitter          = 0.2
)

// Policy configures how many attempts are made and how long the caller waits
// between them.
//
// The zero value is usable: every non-positive field is replaced by its
// default value, so Policy{}.Do retries three times with a growing interval.
//
// Attempt counts include the first call, so MaxAttempts = 1 disables retries.
type Policy struct {
	// MaxAttempts is the total number of calls, including the first one.
	MaxAttempts int
	// InitialInterval is the delay after the first failed attempt.
	InitialInterval time.Duration
	// MaxInterval caps every delay, including the jitter applied to it.
	MaxInterval time.Duration
	// Multiplier grows the delay after every failed attempt. Values below one
	// would shrink the delay, so they are treated as the default.
	Multiplier float64
	// Jitter randomises each delay by plus or minus this fraction, keeping
	// concurrent clients from retrying in lockstep. Valid range is [0, 1].
	Jitter float64
}

// Default returns the policy used by remote sources: three attempts, a short
// initial pause and a two-fold increase capped at five seconds.
func Default() Policy {
	return Policy{
		MaxAttempts:     defaultMaxAttempts,
		InitialInterval: defaultInitialInterval,
		MaxInterval:     defaultMaxInterval,
		Multiplier:      defaultMultiplier,
		Jitter:          defaultJitter,
	}
}

// Do calls fn until it returns nil, the attempt budget is exhausted or ctx is
// cancelled. Every error returned by fn is retried; the error of the last
// attempt is returned unchanged so callers can inspect it with errors.Is.
//
// When ctx is cancelled while waiting between attempts its error is returned
// instead of the last attempt error. An already cancelled context prevents the
// first call entirely.
func (p Policy) Do(ctx context.Context, fn func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fn == nil {
		return nil
	}
	policy := p.withDefaults()
	if err := ctx.Err(); err != nil {
		return err
	}
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		lastErr = fn(ctx)
		if lastErr == nil {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if attempt == policy.MaxAttempts {
			break
		}
		if err := sleep(ctx, policy.delay(attempt)); err != nil {
			return err
		}
	}
	return lastErr
}

// withDefaults returns a copy of p where invalid fields are replaced by the
// documented defaults. It also keeps MaxInterval from being smaller than
// InitialInterval, which would make the first pause unpredictable.
func (p Policy) withDefaults() Policy {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = defaultMaxAttempts
	}
	if p.InitialInterval <= 0 {
		p.InitialInterval = defaultInitialInterval
	}
	if p.MaxInterval <= 0 {
		p.MaxInterval = defaultMaxInterval
	}
	if p.MaxInterval < p.InitialInterval {
		p.MaxInterval = p.InitialInterval
	}
	if p.Multiplier < 1 || math.IsNaN(p.Multiplier) || math.IsInf(p.Multiplier, 0) {
		p.Multiplier = defaultMultiplier
	}
	if math.IsNaN(p.Jitter) || p.Jitter < 0 {
		p.Jitter = 0
	}
	if p.Jitter > 1 {
		p.Jitter = 1
	}
	return p
}

// delay returns the pause after the given failed attempt. The result never
// exceeds MaxInterval and never converts a float value that is out of the
// time.Duration range, so large multipliers cannot overflow.
func (p Policy) delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	interval := float64(p.InitialInterval)
	limit := float64(p.MaxInterval)
	for range attempt - 1 {
		// Stop growing as soon as the cap is reached; further multiplication
		// could overflow the float or produce an infinity.
		if interval >= limit {
			break
		}
		interval *= p.Multiplier
	}
	interval = min(interval, limit)
	if p.Jitter > 0 {
		factor := 1 + (rand.Float64()*2-1)*p.Jitter
		interval = min(max(interval*factor, 0), limit)
	}
	if interval > float64(math.MaxInt64) {
		interval = float64(math.MaxInt64)
	}
	return time.Duration(interval)
}

// sleep waits for d or until ctx is cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
