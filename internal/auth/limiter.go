package auth

import (
	gosync "sync"
	"time"
)

// ipLimiter caps login attempts per client IP in fixed windows. It backs up
// the per-account lockout against someone trying many accounts. The limit is
// generous because a whole classroom shares one IP behind the centre's
// router. State is in memory: fine for a single API instance.
type ipLimiter struct {
	mu     gosync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*ipWindow
	calls  int
}

type ipWindow struct {
	start time.Time
	count int
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, hits: map[string]*ipWindow{}}
}

// allow records an attempt from ip at now and reports whether it is within
// the limit.
func (l *ipLimiter) allow(ip string, now time.Time) bool {
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

	w, ok := l.hits[ip]
	if !ok || now.Sub(w.start) >= l.window {
		l.hits[ip] = &ipWindow{start: now, count: 1}
		return true
	}
	w.count++
	return w.count <= l.limit
}
