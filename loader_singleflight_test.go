package fastconf_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edanko/fastconf"
	"github.com/edanko/fastconf/layers"
)

func TestConcurrentDecodeLoadsConfigurationOnce(t *testing.T) {
	var loads atomic.Int64
	release := make(chan struct{})
	arrived := make(chan struct{})
	var arrivedOnce sync.Once

	source := layers.LayerFunc(func(ctx context.Context, values map[string]any) (map[string]any, error) {
		loads.Add(1)
		arrivedOnce.Do(func() { close(arrived) })
		<-release
		return layers.NewInMemory(map[string]any{
			"config": map[string]any{"domain": "concurrent.example"},
		}).Load(ctx, values)
	})
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(source),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const workers = 32
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wait.Go(func() {
			var config applicationConfig
			errs <- loader.Decode(context.Background(), &config)
		})
	}

	<-arrived
	time.Sleep(100 * time.Millisecond)
	close(release)
	wait.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("source loads = %d, want 1", loads.Load())
	}
}

func TestConcurrentReloadsCoalesce(t *testing.T) {
	var loads atomic.Int64
	var reloading atomic.Bool
	release := make(chan struct{})
	arrived := make(chan struct{})
	var arrivedOnce sync.Once

	source := layers.LayerFunc(func(ctx context.Context, values map[string]any) (map[string]any, error) {
		loads.Add(1)
		if reloading.Load() {
			arrivedOnce.Do(func() { close(arrived) })
			<-release
		}
		return layers.NewInMemory(map[string]any{
			"config": map[string]any{"domain": "reload.example"},
		}).Load(ctx, values)
	})
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(source),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var config applicationConfig
	if err := loader.Decode(context.Background(), &config); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if loads.Load() != 1 {
		t.Fatalf("initial loads = %d, want 1", loads.Load())
	}

	reloading.Store(true)
	const workers = 16
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wait.Go(func() {
			errs <- loader.Reload(context.Background())
		})
	}
	<-arrived
	time.Sleep(100 * time.Millisecond)
	close(release)
	wait.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("Reload: %v", err)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("loads after concurrent reloads = %d, want 2", loads.Load())
	}

	// A later reload is a new flight and loads again.
	reloading.Store(false)
	if err := loader.Reload(context.Background()); err != nil {
		t.Fatalf("sequential Reload: %v", err)
	}
	if loads.Load() != 3 {
		t.Fatalf("loads after sequential reload = %d, want 3", loads.Load())
	}
}

func TestFailedInitialLoadIsSharedAndRetried(t *testing.T) {
	var loads atomic.Int64
	var failing atomic.Bool
	failing.Store(true)
	release := make(chan struct{})
	arrived := make(chan struct{})
	var arrivedOnce sync.Once

	source := layers.LayerFunc(func(ctx context.Context, values map[string]any) (map[string]any, error) {
		loads.Add(1)
		if failing.Load() {
			arrivedOnce.Do(func() { close(arrived) })
			<-release
			return nil, context.DeadlineExceeded
		}
		return layers.NewInMemory(map[string]any{
			"config": map[string]any{"domain": "recovered.example"},
		}).Load(ctx, values)
	})
	loader, err := fastconf.New(
		fastconf.WithoutEnvironmentVariables(),
		fastconf.WithSources(source),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const workers = 8
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wait.Go(func() {
			var config applicationConfig
			errs <- loader.Decode(context.Background(), &config)
		})
	}
	<-arrived
	time.Sleep(100 * time.Millisecond)
	close(release)
	wait.Wait()
	close(errs)

	for err := range errs {
		if err == nil {
			t.Fatal("expected the shared load error")
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loads = %d, want 1", loads.Load())
	}

	// A failed load is not cached; the next call retries.
	failing.Store(false)
	var config applicationConfig
	if err := loader.Decode(context.Background(), &config); err != nil {
		t.Fatalf("Decode after recovery: %v", err)
	}
	if config.Domain != "recovered.example" {
		t.Fatalf("domain = %q", config.Domain)
	}
	if loads.Load() != 2 {
		t.Fatalf("loads after recovery = %d, want 2", loads.Load())
	}
}
