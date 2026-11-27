package storage

import (
	"context"
	"sync"
)

type downloadLimiter struct {
	mu      sync.Mutex
	limit   int
	active  int
	waiters map[chan struct{}]struct{}
}

func newDownloadLimiter(limit int) *downloadLimiter {
	if limit <= 0 {
		limit = 1
	}
	return &downloadLimiter{limit: limit, waiters: map[chan struct{}]struct{}{}}
}

func (l *downloadLimiter) acquire(ctx context.Context) (func(), error) {
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return nil, err
		}
		if l.active < l.limit {
			l.active++
			l.mu.Unlock()
			var once sync.Once
			return func() {
				once.Do(l.release)
			}, nil
		}
		waiter := make(chan struct{})
		l.waiters[waiter] = struct{}{}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			l.mu.Lock()
			delete(l.waiters, waiter)
			l.mu.Unlock()
			return nil, ctx.Err()
		case <-waiter:
		}
	}
}

func (l *downloadLimiter) release() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.wakeWaitersLocked()
	l.mu.Unlock()
}

func (l *downloadLimiter) setLimit(limit int) {
	if limit <= 0 {
		limit = 1
	}
	l.mu.Lock()
	if l.limit != limit {
		l.limit = limit
		l.wakeWaitersLocked()
	}
	l.mu.Unlock()
}

func (l *downloadLimiter) count() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int64(l.active)
}

func (l *downloadLimiter) wakeWaitersLocked() {
	if l.active >= l.limit {
		return
	}
	for waiter := range l.waiters {
		delete(l.waiters, waiter)
		close(waiter)
	}
}
