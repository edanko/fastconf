// Package singleflight coalesces concurrent calls that want the same result.
//
// It is a small, dependency-free implementation of the classic singleflight
// pattern: the first caller for a key executes the function, every concurrent
// caller for that key waits and receives the same result. A completed call is
// forgotten immediately, so a later call runs the function again.
package singleflight

import "sync"

// Group coalesces calls by key. The zero value is ready for use.
type Group struct {
	mutex sync.Mutex
	calls map[string]*call
}

type call struct {
	wait sync.WaitGroup
	val  any
	err  error
}

// Do executes fn for key unless an equivalent call is already in flight, in
// which case it waits for the in-flight result and returns it.
//
// The first caller's panic is propagated to that caller after the flight is
// completed; waiting callers receive the zero result. The function is run
// synchronously, so a cancelled context of the first caller cancels the shared
// work for every waiter.
func (g *Group) Do(key string, fn func() (any, error)) (any, error) {
	g.mutex.Lock()
	if existing, ok := g.calls[key]; ok {
		g.mutex.Unlock()
		existing.wait.Wait()
		return existing.val, existing.err
	}
	current := &call{}
	current.wait.Add(1)
	if g.calls == nil {
		g.calls = make(map[string]*call, 1)
	}
	g.calls[key] = current
	g.mutex.Unlock()

	defer func() {
		g.mutex.Lock()
		delete(g.calls, key)
		g.mutex.Unlock()
		current.wait.Done()
	}()

	current.val, current.err = fn()
	return current.val, current.err
}

// DoValue executes fn for key and returns a typed result.
func DoValue[T any](g *Group, key string, fn func() (T, error)) (T, error) {
	value, err := g.Do(key, func() (any, error) {
		return fn()
	})
	if err != nil {
		var zero T
		return zero, err
	}
	typed, ok := value.(T)
	if !ok {
		var zero T
		return zero, nil
	}
	return typed, nil
}

// Forget removes the in-flight entry for key without affecting callers that
// already received the result. It is exposed for tests and diagnostics.
func (g *Group) Forget(key string) {
	g.mutex.Lock()
	delete(g.calls, key)
	g.mutex.Unlock()
}
