package singleflight

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoCoalescesConcurrentCalls(t *testing.T) {
	var group Group
	var calls atomic.Int64
	release := make(chan struct{})
	arrived := make(chan struct{})
	var arrivedOnce sync.Once

	const workers = 32
	results := make(chan int, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Go(func() {
			value, err := DoValue(&group, "key", func() (int, error) {
				if calls.Add(1) == 1 {
					arrivedOnce.Do(func() { close(arrived) })
				}
				<-release
				return 42, nil
			})
			if err != nil {
				t.Errorf("DoValue: %v", err)
			}
			results <- value
		})
	}

	<-arrived
	time.Sleep(50 * time.Millisecond)
	close(release)
	wait.Wait()
	close(results)

	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	for value := range results {
		if value != 42 {
			t.Fatalf("value = %d", value)
		}
	}
}

func TestDoRunsAgainAfterCompletion(t *testing.T) {
	var group Group
	var calls atomic.Int64
	for range 3 {
		value, err := DoValue(&group, "key", func() (string, error) {
			calls.Add(1)
			return "value", nil
		})
		if err != nil || value != "value" {
			t.Fatalf("DoValue = %q, %v", value, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestDoSharesError(t *testing.T) {
	var group Group
	var calls atomic.Int64
	release := make(chan struct{})
	arrived := make(chan struct{})
	var arrivedOnce sync.Once
	sentinel := errors.New("boom")

	const workers = 8
	errs := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Go(func() {
			_, err := group.Do("key", func() (any, error) {
				if calls.Add(1) == 1 {
					arrivedOnce.Do(func() { close(arrived) })
				}
				<-release
				return nil, sentinel
			})
			errs <- err
		})
	}
	<-arrived
	time.Sleep(50 * time.Millisecond)
	close(release)
	wait.Wait()
	close(errs)

	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	for err := range errs {
		if !errors.Is(err, sentinel) {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestDoRecoversFromPanic(t *testing.T) {
	var group Group
	func() {
		defer func() {
			if recovered := recover(); recovered != "boom" {
				t.Fatalf("recovered = %v", recovered)
			}
		}()
		_, _ = group.Do("key", func() (any, error) { panic("boom") })
	}()

	// The in-flight entry must be forgotten, so a later call runs again.
	value, err := DoValue(&group, "key", func() (int, error) { return 7, nil })
	if err != nil || value != 7 {
		t.Fatalf("DoValue after panic = %d, %v", value, err)
	}
}
