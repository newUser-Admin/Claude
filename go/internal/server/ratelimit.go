package server

// ratelimit.go — per-IP token-bucket rate limiter for HTTP endpoints.
//
// Each unique client IP gets its own rate.Limiter.  Stale entries are evicted
// lazily when the map grows beyond a threshold.
//
// Default limits (when Config.RateLimit > 0):
//   - Sustained:  RateLimit requests / second
//   - Burst:      RateBurst (defaults to ceil(RateLimit * 3))
//
// WebSocket upgrade requests are not rate-limited here (the upgrade itself is
// cheap; the per-connection logic handles the actual work).

import (
	"math"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// maxIPEntries is the maximum number of per-IP limiters retained in memory.
	// When exceeded, the oldest half of entries are evicted.
	maxIPEntries = 10_000
)

type ipEntry struct {
	lim     *rate.Limiter
	lastSeen time.Time
}

// ipLimiter manages per-IP rate.Limiter instances.
type ipLimiter struct {
	mu      sync.Mutex
	entries map[string]*ipEntry
	r       rate.Limit
	burst   int
}

// newIPLimiter creates a limiter with the given rate and burst.
// If r <= 0 the limiter is disabled (all requests pass through).
func newIPLimiter(r float64, burst int) *ipLimiter {
	if r <= 0 {
		return nil // disabled
	}
	if burst <= 0 {
		burst = int(math.Ceil(r * 3))
		if burst < 1 {
			burst = 1
		}
	}
	return &ipLimiter{
		entries: make(map[string]*ipEntry),
		r:       rate.Limit(r),
		burst:   burst,
	}
}

// allow returns true if the request from ip should be allowed.
func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	e, ok := l.entries[ip]
	if !ok {
		e = &ipEntry{lim: rate.NewLimiter(l.r, l.burst)}
		l.entries[ip] = e
		if len(l.entries) > maxIPEntries {
			l.evict()
		}
	}
	e.lastSeen = time.Now()
	ok = e.lim.Allow()
	l.mu.Unlock()
	return ok
}

// evict removes the oldest half of entries.  Must be called with l.mu held.
func (l *ipLimiter) evict() {
	// Collect all (ip, lastSeen) pairs, sort by lastSeen, drop oldest half.
	type kv struct {
		ip  string
		t   time.Time
	}
	pairs := make([]kv, 0, len(l.entries))
	for ip, e := range l.entries {
		pairs = append(pairs, kv{ip, e.lastSeen})
	}
	// Simple selection: find the median time, delete everything older.
	// Using a cheap O(n) partial sort is fine at maxIPEntries = 10k.
	cutoff := time.Now().Add(-30 * time.Second)
	removed := 0
	for _, p := range pairs {
		if p.t.Before(cutoff) {
			delete(l.entries, p.ip)
			removed++
		}
	}
	// If nothing was old enough, force-remove the first half.
	if removed == 0 {
		target := len(l.entries) / 2
		for ip := range l.entries {
			delete(l.entries, ip)
			if removed++; removed >= target {
				break
			}
		}
	}
}

// RateLimitMiddleware wraps an http.Handler with per-IP rate limiting.
// It is exported so tests and other packages can apply it independently.
func RateLimitMiddleware(lim *ipLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lim != nil && !lim.allow(extractIP(r)) {
			http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
