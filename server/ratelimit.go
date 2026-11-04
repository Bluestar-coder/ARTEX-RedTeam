package server

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Bluestar-coder/ARTEX-RedTeam/config"
)

// ipLimiter is a small per-IP token bucket in front of the auth endpoints.
//
// The published ARTEX takeover chain ("800 concurrent /api/auth/init") worked
// by manufacturing database errors; the fail-closed fix closes that specific
// window, but bounding request rate removes the ability to manufacture load in
// the first place. It also slows offline-ish brute force against /login, where
// every attempt costs a bcrypt comparison.
type ipLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	rate    float64 // tokens refilled per second
	burst   float64 // bucket capacity
	lastGC  time.Time
}

type ipBucket struct {
	tokens float64
	seen   time.Time
}

func newIPLimiter(perSecond, burst float64) *ipLimiter {
	if perSecond <= 0 {
		perSecond = 0.2
	}
	if burst < 1 {
		burst = 1
	}
	return &ipLimiter{buckets: map[string]*ipBucket{}, rate: perSecond, burst: burst, lastGC: time.Now()}
}

// allow consumes one token for key, refilling first. Stale buckets are swept
// every 10 minutes so a long scan cannot grow the map without bound.
func (l *ipLimiter) allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if now.Sub(l.lastGC) > 10*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastGC = now
	}

	b := l.buckets[key]
	if b == nil {
		b = &ipBucket{tokens: l.burst, seen: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.seen).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.seen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// clientIP is the peer address without the port. X-Forwarded-For is deliberately
// ignored: without a trusted-proxy configuration it is attacker-controlled, and
// honouring it would let a single client rotate through unlimited "IPs".
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// authRateLimit reads the optional ARTEX_REDTEAM_AUTH_RATE_PER_MIN override
// (legacy ARTEX_ prefix accepted) so operators behind a shared proxy can tune
// the per-IP budget. Default: 12 requests/minute with a burst of 10.
func authRateLimit() (perSecond, burst float64) {
	perMin := 12.0
	if v := config.Env("AUTH_RATE_PER_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 6000 {
			perMin = float64(n)
		}
	}
	burst = 10
	return perMin / 60, burst
}

// limitAuth wraps a state-changing auth handler with the per-IP budget.
func (s *Server) limitAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.authLimitOnce.Do(func() {
			rate, burst := authRateLimit()
			s.authLimiter = newIPLimiter(rate, burst)
		})
		if !s.authLimiter.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "30")
			writeErr(w, 429, "请求过于频繁，请稍后再试")
			return
		}
		next(w, r)
	}
}
