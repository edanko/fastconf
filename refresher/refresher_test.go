package refresher

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edanko/fastconf/layers"
)

// fakeRefresher records calls and delegates to fn.
type fakeRefresher struct {
	mutex sync.Mutex
	calls int
	fn    func(context.Context) error
}

func (f *fakeRefresher) Refresh(ctx context.Context) error {
	f.mutex.Lock()
	f.calls++
	fn := f.fn
	f.mutex.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (f *fakeRefresher) Calls() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.calls
}

func (f *fakeRefresher) SetFunc(fn func(context.Context) error) {
	f.mutex.Lock()
	f.fn = fn
	f.mutex.Unlock()
}

// collectAcks returns an ack function that reports errors on a buffered
// channel.
func collectAcks(count int) (func(error), <-chan error) {
	acks := make(chan error, count)
	var mutex sync.Mutex
	remaining := count
	ack := func(err error) {
		mutex.Lock()
		remaining--
		if remaining < 0 {
			mutex.Unlock()
			panic("ack called more often than requested")
		}
		mutex.Unlock()
		acks <- err
	}
	return ack, acks
}

func waitAcks(t *testing.T, acks <-chan error, count int) []error {
	t.Helper()
	errs := make([]error, 0, count)
	for len(errs) < count {
		select {
		case err := <-acks:
			errs = append(errs, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("received %d of %d acks", len(errs), count)
		}
	}
	return errs
}

func TestRefreshDebouncesBurst(t *testing.T) {
	t.Parallel()

	fake := &fakeRefresher{}
	async := Start(context.Background(), fake, WithDebounce(30*time.Millisecond))
	defer async.Stop()

	ack, acks := collectAcks(10)
	for range 10 {
		async.Refresh(context.Background(), nil, ack)
	}
	for _, err := range waitAcks(t, acks, 10) {
		if err != nil {
			t.Fatalf("ack received error: %v", err)
		}
	}
	if calls := fake.Calls(); calls != 1 {
		t.Fatalf("refresh calls = %d, want 1 for a burst", calls)
	}
}

func TestRefreshReportsError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("refresh failed")
	fake := &fakeRefresher{fn: func(context.Context) error { return sentinel }}
	async := Start(context.Background(), fake, WithDebounce(time.Millisecond))
	defer async.Stop()

	ack, acks := collectAcks(1)
	async.Refresh(context.Background(), nil, ack)
	errs := waitAcks(t, acks, 1)
	if !errors.Is(errs[0], sentinel) {
		t.Fatalf("ack received %v, want %v", errs[0], sentinel)
	}
}

func TestRefreshAppliesTimeout(t *testing.T) {
	t.Parallel()

	fake := &fakeRefresher{fn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	async := Start(context.Background(), fake,
		WithDebounce(time.Millisecond),
		WithTimeout(20*time.Millisecond),
	)
	defer async.Stop()

	ack, acks := collectAcks(1)
	started := time.Now()
	async.Refresh(context.Background(), nil, ack)
	errs := waitAcks(t, acks, 1)
	if !errors.Is(errs[0], context.DeadlineExceeded) {
		t.Fatalf("ack received %v, want context.DeadlineExceeded", errs[0])
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("refresh took %v, want the configured timeout", elapsed)
	}
}

func TestRefreshPassesModifiedAt(t *testing.T) {
	t.Parallel()

	modified := time.Date(2026, time.March, 4, 5, 6, 7, 0, time.UTC)
	received := make(chan time.Time, 1)
	present := make(chan bool, 1)
	fake := &fakeRefresher{fn: func(ctx context.Context) error {
		value, ok := layers.ModifiedAt(ctx)
		present <- ok
		received <- value
		return nil
	}}
	async := Start(context.Background(), fake, WithDebounce(time.Millisecond))
	defer async.Stop()

	ack, acks := collectAcks(1)
	async.Refresh(context.Background(), &modified, ack)
	if err := waitAcks(t, acks, 1)[0]; err != nil {
		t.Fatalf("ack received error: %v", err)
	}
	if ok := <-present; !ok {
		t.Fatal("refresh context does not carry a modification time")
	}
	if got := <-received; !got.Equal(modified) {
		t.Fatalf("modification time = %v, want %v", got, modified)
	}
}

func TestRefreshSerializesExecutions(t *testing.T) {
	t.Parallel()

	var running atomic.Int32
	var maximum atomic.Int32
	fake := &fakeRefresher{fn: func(ctx context.Context) error {
		current := running.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return nil
	}}
	async := Start(context.Background(), fake,
		WithDebounce(time.Millisecond),
		WithTimeout(5*time.Second),
	)
	defer async.Stop()

	ack, acks := collectAcks(4)
	for range 4 {
		async.Refresh(context.Background(), nil, ack)
		time.Sleep(30 * time.Millisecond)
	}
	waitAcks(t, acks, 4)
	if observed := maximum.Load(); observed != 1 {
		t.Fatalf("maximum concurrent refreshes = %d, want 1", observed)
	}
	if calls := fake.Calls(); calls < 1 {
		t.Fatalf("refresh calls = %d, want at least 1", calls)
	}
}

func TestRefreshCoalescesRequestsDuringExecution(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	fake := &fakeRefresher{fn: func(ctx context.Context) error {
		startOnce.Do(func() { close(started) })
		<-release
		return nil
	}}
	async := Start(context.Background(), fake, WithDebounce(time.Millisecond))
	defer async.Stop()

	ack, acks := collectAcks(2)
	async.Refresh(context.Background(), nil, ack)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first refresh never started")
	}
	// This request arrives while the first refresh runs; it must produce a
	// second execution instead of running concurrently.
	async.Refresh(context.Background(), nil, ack)
	close(release)
	for _, err := range waitAcks(t, acks, 2) {
		if err != nil {
			t.Fatalf("ack received error: %v", err)
		}
	}
	if calls := fake.Calls(); calls < 2 {
		t.Fatalf("refresh calls = %d, want at least 2", calls)
	}
}

func TestRefreshAfterStopReportsError(t *testing.T) {
	t.Parallel()

	fake := &fakeRefresher{}
	async := Start(context.Background(), fake, WithDebounce(time.Millisecond))
	async.Stop()
	select {
	case <-async.Done():
	case <-time.After(time.Second):
		t.Fatal("Done was not closed after Stop")
	}

	ack, acks := collectAcks(1)
	async.Refresh(context.Background(), nil, ack)
	errs := waitAcks(t, acks, 1)
	if !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("ack received %v, want context.Canceled", errs[0])
	}

	// Stop must be idempotent.
	async.Stop()
}

func TestStopFlushesPendingAcks(t *testing.T) {
	t.Parallel()

	fake := &fakeRefresher{}
	async := Start(context.Background(), fake, WithDebounce(time.Hour))

	ack, acks := collectAcks(1)
	async.Refresh(context.Background(), nil, ack)
	// Give the worker time to arm its debounce timer with the pending ack.
	time.Sleep(10 * time.Millisecond)
	async.Stop()

	errs := waitAcks(t, acks, 1)
	if !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("pending ack received %v, want context.Canceled", errs[0])
	}
	if calls := fake.Calls(); calls != 0 {
		t.Fatalf("refresh calls = %d, want 0 after an immediate stop", calls)
	}
}

func TestContextCancellationStopsWorker(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	async := Start(ctx, &fakeRefresher{}, WithDebounce(time.Millisecond))
	cancel()
	select {
	case <-async.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after its context was cancelled")
	}
}

func TestRefreshWithCancelledCallerContext(t *testing.T) {
	t.Parallel()

	fake := &fakeRefresher{}
	async := Start(context.Background(), fake, WithDebounce(time.Millisecond))
	defer async.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ack, acks := collectAcks(1)
	async.Refresh(ctx, nil, ack)
	errs := waitAcks(t, acks, 1)
	if !errors.Is(errs[0], context.Canceled) {
		t.Fatalf("ack received %v, want context.Canceled", errs[0])
	}
	if calls := fake.Calls(); calls != 0 {
		t.Fatalf("refresh calls = %d, want 0", calls)
	}
}

func TestRefreshWithNilRefresher(t *testing.T) {
	t.Parallel()

	async := Start(context.Background(), nil, WithDebounce(time.Millisecond))
	defer async.Stop()

	ack, acks := collectAcks(1)
	async.Refresh(context.Background(), nil, ack)
	errs := waitAcks(t, acks, 1)
	if errs[0] == nil {
		t.Fatal("ack received nil, want an error for a nil refresher")
	}
}

func TestRefreshWithNilAck(t *testing.T) {
	t.Parallel()

	called := make(chan struct{}, 1)
	fake := &fakeRefresher{fn: func(context.Context) error {
		called <- struct{}{}
		return nil
	}}
	async := Start(context.Background(), fake, WithDebounce(time.Millisecond))
	defer async.Stop()

	async.Refresh(context.Background(), nil, nil)
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh was not executed for a nil ack")
	}
}
