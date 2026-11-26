package adminapi

import (
	"sync"
	"time"
)

const (
	loginFailureLimit = 5
	loginWindow       = 15 * time.Minute
	loginMaxBlock     = time.Hour
)

type loginLimitEntry struct {
	failures     int
	blockedUntil time.Time
	updatedAt    time.Time
}

type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginLimitEntry
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: map[string]*loginLimitEntry{}}
}

func (l *loginLimiter) Allow(key string) (bool, time.Duration) {
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cleanup(now)
	entry := l.entries[key]
	if entry == nil || entry.blockedUntil.IsZero() {
		return true, 0
	}
	if now.Before(entry.blockedUntil) {
		return false, entry.blockedUntil.Sub(now)
	}
	delete(l.entries, key)
	return true, 0
}

func (l *loginLimiter) Failure(key string) time.Duration {
	now := time.Now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[key]
	if entry == nil || now.Sub(entry.updatedAt) > loginWindow {
		entry = &loginLimitEntry{}
		l.entries[key] = entry
	}
	entry.failures++
	entry.updatedAt = now
	if entry.failures < loginFailureLimit {
		return 0
	}
	exponent := entry.failures - loginFailureLimit
	if exponent > 2 {
		exponent = 2
	}
	block := loginWindow
	for i := 0; i < exponent; i++ {
		block *= 2
	}
	if block > loginMaxBlock {
		block = loginMaxBlock
	}
	entry.blockedUntil = now.Add(block)
	return block
}

func (l *loginLimiter) Success(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

func (l *loginLimiter) cleanup(now time.Time) {
	if len(l.entries) < 1000 {
		return
	}
	for key, entry := range l.entries {
		if now.Sub(entry.updatedAt) > 24*time.Hour {
			delete(l.entries, key)
		}
	}
}
