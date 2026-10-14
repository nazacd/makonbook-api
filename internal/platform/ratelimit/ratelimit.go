// Package ratelimit counts attempts per key (a client IP, a login name) in
// fixed windows. State is in memory: fine for a single API instance.
package ratelimit

import (
	gosync "sync"
	"time"
)

type Limiter struct {
	mu     gosync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*window
	calls  int
}

type window struct {
	start time.Time
	count int
}

// New allows limit attempts per key per window.
func New(limit int, every time.Duration) *Limiter {
	return &Limiter{limit: limit, window: every, hits: map[string]*window{}}
}

// Window is the length of a counting window (for Retry-After).
func (l *Limiter) Window() time.Duration { return l.window }

// Allow records an attempt for key at now and reports whether it is within
// the limit.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.calls++
	if l.calls%1000 == 0 { // drop stale entries now and then
		for k, w := range l.hits {
			if now.Sub(w.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}

	w, ok := l.hits[key]
	if !ok || now.Sub(w.start) >= l.window {
		l.hits[key] = &window{start: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.limit
}
