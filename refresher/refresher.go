// Package refresher runs asynchronous, debounced configuration refreshes.
//
// A refresher is typically started once and then called whenever a change is
// signalled (for example by a file watcher). Calls made in a burst are
// coalesced into a single refresh, refreshes never overlap, and the context
// passed to Refresher carries the modification time of the last known document
// so remote sources can send conditional requests.
package refresher

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/edanko/fastconf/layers"
)

// Refresher reloads configuration from its sources.
//
// Refresh is called from a single background goroutine, one call at a time,
// with a context that is cancelled when the refresher stops or its timeout
// elapses.
type Refresher interface {
	Refresh(ctx context.Context) error
}

// Option configures an Async refresher.
type Option func(*Async)

const (
	defaultDebounce       = 100 * time.Millisecond
	defaultRefreshTimeout = 30 * time.Second
)

var errNilRefresher = errors.New("refresher: nil refresher")

// WithTimeout bounds a single refresh. Non-positive values are ignored and the
// default of thirty seconds stays in effect.
func WithTimeout(timeout time.Duration) Option {
	return func(a *Async) {
		if timeout > 0 {
			a.timeout = timeout
		}
	}
}

// WithDebounce sets how long the refresher waits for a burst of requests to
// settle before running. Non-positive values are ignored and the default of one
// hundred milliseconds stays in effect.
func WithDebounce(window time.Duration) Option {
	return func(a *Async) {
		if window > 0 {
			a.debounce = window
		}
	}
}

// Async coalesces refresh requests into serialized background executions.
//
// The zero value is not usable; create instances with Start. Stop must be
// called to release the worker goroutine, unless the context given to Start is
// cancelled.
type Async struct {
	refresher Refresher
	debounce  time.Duration
	timeout   time.Duration

	ctx     context.Context
	cancel  context.CancelFunc
	trigger chan struct{}
	done    chan struct{}

	mutex      sync.Mutex
	acks       []func(error)
	modifiedAt *time.Time
	stopped    bool
	exitErr    error
}

// Start launches the background worker for refresher. The worker stops when
// ctx is cancelled or Stop is called; both cases are safe to combine.
func Start(ctx context.Context, refresher Refresher, options ...Option) *Async {
	if ctx == nil {
		ctx = context.Background()
	}
	async := &Async{
		refresher: refresher,
		debounce:  defaultDebounce,
		timeout:   defaultRefreshTimeout,
		trigger:   make(chan struct{}, 1),
		done:      make(chan struct{}),
	}
	for _, option := range options {
		if option != nil {
			option(async)
		}
	}
	async.ctx, async.cancel = context.WithCancel(ctx)
	go async.run()
	return async
}

// Refresh requests a refresh and reports its outcome to ack.
//
// The call does not block on the refresh itself: it returns after the request
// is queued. Bursts of calls are debounced into one execution, and every ack
// receives the error of the execution that covered its request. If the caller
// context is already cancelled, or the refresher has stopped, ack is called
// immediately. A nil ack is ignored.
//
// ack runs on the worker goroutine, so it must not call Stop or Wait.
func (a *Async) Refresh(ctx context.Context, modifiedAt *time.Time, ack func(error)) {
	if ack == nil {
		ack = func(error) {}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			ack(err)
			return
		}
	}

	a.mutex.Lock()
	if a.stopped {
		err := a.exitErr
		if err == nil {
			err = context.Canceled
		}
		a.mutex.Unlock()
		ack(err)
		return
	}
	a.acks = append(a.acks, ack)
	if modifiedAt != nil {
		copied := *modifiedAt
		a.modifiedAt = &copied
	}
	a.mutex.Unlock()

	// A single pending token is enough: the worker drains every queued ack per
	// execution, and the token stays buffered while the worker is busy.
	select {
	case a.trigger <- struct{}{}:
	default:
	}
}

// Stop cancels the worker and waits for it to exit. Pending acks receive the
// cancellation error. Stop is idempotent.
func (a *Async) Stop() {
	a.mutex.Lock()
	stopped := a.stopped
	a.mutex.Unlock()
	if !stopped {
		a.cancel()
	}
	<-a.done
}

// Done is closed once the worker has exited.
func (a *Async) Done() <-chan struct{} {
	return a.done
}

// run executes the worker loop: wait for a request, debounce a burst, run one
// refresh and deliver its error to every request collected so far.
func (a *Async) run() {
	defer close(a.done)
	for {
		select {
		case <-a.ctx.Done():
			a.shutdown()
			return
		case <-a.trigger:
		}
		if !a.waitForQuietPeriod() {
			a.shutdown()
			return
		}
		acks, modifiedAt := a.takePending()
		if len(acks) == 0 {
			continue
		}
		err := a.execute(modifiedAt)
		for _, ack := range acks {
			ack(err)
		}
	}
}

// waitForQuietPeriod waits until requests stop arriving for the debounce
// window. It reports false when the refresher is stopped while waiting.
func (a *Async) waitForQuietPeriod() bool {
	timer := time.NewTimer(a.debounce)
	defer timer.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return false
		case <-a.trigger:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(a.debounce)
		case <-timer.C:
			return true
		}
	}
}

// execute runs one refresh with the configured timeout and modification time.
func (a *Async) execute(modifiedAt *time.Time) error {
	if a.refresher == nil {
		return errNilRefresher
	}
	ctx := a.ctx
	if modifiedAt != nil {
		ctx = layers.WithModifiedAt(ctx, modifiedAt)
	}
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	return a.refresher.Refresh(ctx)
}

// takePending removes and returns the queued acks and the latest known
// modification time.
func (a *Async) takePending() ([]func(error), *time.Time) {
	a.mutex.Lock()
	defer a.mutex.Unlock()
	acks := a.acks
	a.acks = nil
	modifiedAt := a.modifiedAt
	a.modifiedAt = nil
	return acks, modifiedAt
}

// shutdown marks the refresher stopped and reports the exit reason to every
// pending ack.
func (a *Async) shutdown() {
	a.mutex.Lock()
	a.stopped = true
	a.exitErr = a.ctx.Err()
	if a.exitErr == nil {
		a.exitErr = context.Canceled
	}
	acks := a.acks
	a.acks = nil
	a.modifiedAt = nil
	a.mutex.Unlock()
	for _, ack := range acks {
		ack(a.exitErr)
	}
}
