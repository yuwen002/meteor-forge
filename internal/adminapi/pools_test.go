package adminapi

import (
	"context"
	"sync"
	"testing"
)

func TestPoolManagerReusesAndInvalidatesPools(t *testing.T) {
	manager := NewPoolManager()
	t.Cleanup(func() { _ = manager.Close() })

	driver, dsn := "sqlite", ":memory:"
	load := func(context.Context, string) (string, string, error) { return driver, dsn, nil }

	first, err := manager.Get(context.Background(), "db-1", load)
	if err != nil { t.Fatal(err) }
	second, err := manager.Get(context.Background(), "db-1", load)
	if err != nil { t.Fatal(err) }
	if first != second { t.Fatal("expected repeated acquisition to reuse the same pool") }

	manager.Invalidate("db-1")
	if err := first.Ping(); err == nil { t.Fatal("expected invalidated pool to be closed") }

	third, err := manager.Get(context.Background(), "db-1", load)
	if err != nil { t.Fatal(err) }
	if third == first { t.Fatal("expected a new pool after invalidation") }

	if err := manager.Close(); err != nil { t.Fatal(err) }
	if _, err := manager.Get(context.Background(), "db-1", load); err != ErrPoolManagerClosed {
		t.Fatalf("Get() after Close() error = %v, want ErrPoolManagerClosed", err)
	}
}

func TestPoolManagerConcurrentGetReturnsOnePool(t *testing.T) {
	manager := NewPoolManager()
	t.Cleanup(func() { _ = manager.Close() })
	load := func(context.Context, string) (string, string, error) {
		return "sqlite", ":memory:", nil
	}

	const workers = 32
	results := make(chan any, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := manager.Get(context.Background(), "shared", load)
			if err != nil { results <- err; return }
			results <- db
		}()
	}
	wg.Wait()
	close(results)

	var first any
	for result := range results {
		if err, ok := result.(error); ok { t.Fatalf("Get() error: %v", err) }
		if first == nil { first = result; continue }
		if first != result { t.Fatal("concurrent acquisitions returned different pools") }
	}
}
