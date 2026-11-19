package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// ipLimiter is a fixed-window counter per remote IP for unauthenticated endpoints.
// It trusts only the TCP peer address; forwarding headers are client-controlled.
type ipLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	now    func() time.Time
	hits   map[string]*ipWindow
}

type ipWindow struct {
	start time.Time
	count int
}

func newIPLimiter(limit int, window time.Duration) *ipLimiter {
	return &ipLimiter{limit: limit, window: window, now: time.Now, hits: make(map[string]*ipWindow)}
}

func (l *ipLimiter) allow(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	// Drop stale windows so the map stays bounded by recently active peers.
	if len(l.hits) > 4096 {
		for k, w := range l.hits {
			if now.Sub(w.start) >= l.window {
				delete(l.hits, k)
			}
		}
	}
	w := l.hits[ip]
	if w == nil || now.Sub(w.start) >= l.window {
		l.hits[ip] = &ipWindow{start: now, count: 1}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}
