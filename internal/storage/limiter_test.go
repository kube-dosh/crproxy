package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestDownloadLimiterAppliesDynamicLimit(t *testing.T) {
	limiter := newDownloadLimiter(1)
	firstRelease, err := limiter.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := limiter.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a blocked acquisition to time out, got %v", err)
	}
	if count := limiter.count(); count != 1 {
		t.Fatalf("unexpected active count %d", count)
	}

	acquired := make(chan func(), 1)
	go func() {
		release, err := limiter.acquire(context.Background())
		if err != nil {
			acquired <- nil
			return
		}
		acquired <- release
	}()
	time.Sleep(20 * time.Millisecond)
	limiter.setLimit(2)
	select {
	case release := <-acquired:
		if release == nil {
			t.Fatal("waiter failed after raising the limit")
		}
		if count := limiter.count(); count != 2 {
			t.Fatalf("unexpected active count after limit increase: %d", count)
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("waiter was not woken after raising the limit")
	}
	firstRelease()
	if count := limiter.count(); count != 0 {
		t.Fatalf("unexpected active count after release: %d", count)
	}
	limiter.setLimit(1)
	if limiter.downloadLimit() != 1 {
		t.Fatalf("limit was not lowered")
	}
}

func TestDownloadLimiterConcurrentLimitChanges(t *testing.T) {
	limiter := newDownloadLimiter(2)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			release, err := limiter.acquire(ctx)
			if err != nil {
				t.Errorf("acquire failed: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
			release()
			limiter.setLimit(index%4 + 1)
		}(i)
	}
	wg.Wait()
	if count := limiter.count(); count != 0 {
		t.Fatalf("active count leaked: %d", count)
	}
}

func (l *downloadLimiter) downloadLimit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}
