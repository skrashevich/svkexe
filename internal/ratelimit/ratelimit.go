package ratelimit

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter manages per-user token bucket rate limiters.
type Limiter struct {
	mu       sync.Mutex
	limiters map[string]*entry
	rps      rate.Limit
	burst    int
}

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// New creates a Limiter with the given requests-per-second and burst size.
// It starts a background goroutine that removes stale entries every 10 minutes.
func New(rps float64, burst int) *Limiter {
	l := &Limiter{
		limiters: make(map[string]*entry),
		rps:      rate.Limit(rps),
		burst:    burst,
	}
	go l.cleanup()
	return l
}

// Allow reports whether the user identified by key is allowed to proceed.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	e, ok := l.limiters[key]
	if !ok {
		e = &entry{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.limiters[key] = e
	}
	e.lastSeen = time.Now()
	allowed := e.limiter.Allow()
	l.mu.Unlock()
	return allowed
}

// cleanup removes entries not seen in the last 10 minutes.
func (l *Limiter) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-10 * time.Minute)
		l.mu.Lock()
		for key, e := range l.limiters {
			if e.lastSeen.Before(cutoff) {
				delete(l.limiters, key)
			}
		}
		l.mu.Unlock()
	}
}

// Middleware returns a Chi-compatible middleware that enforces the rate limit.
// The user is identified by the X-ExeDev-Userid header (set by the auth proxy).
// On limit exceeded it responds with 429 Too Many Requests and a Retry-After header.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-ExeDev-Userid")
		if key == "" {
			key = r.RemoteAddr
		}
		if !l.Allow(key) {
			retryAfter := int(1 / float64(l.rps))
			if retryAfter < 1 {
				retryAfter = 1
			}
			w.Header().Set("Retry-After", http.TimeFormat)
			w.Header().Set("X-RateLimit-Limit", "exceeded")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
