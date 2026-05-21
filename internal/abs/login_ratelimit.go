package abs

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// loginLimitBurst caps the number of body-creds /abs/api/login attempts a
// single source IP can make in quick succession. The token bucket refills at
// loginLimitPerToken (10/min ≈ one every 6s), so a bursty client can spend
// the burst and then must wait. Tuned to be invisible to legitimate listeners
// (who attempt login once and succeed) while making credential-stuffing
// expensive.
const (
	loginLimitBurst    = 10
	loginLimitPerToken = 6 * time.Second
	loginLimitIdle     = 10 * time.Minute
	loginLimitGCEvery  = 5 * time.Minute
)

type loginLimiterEntry struct {
	limiter *rate.Limiter
	last    time.Time
}

// LoginLimiter is a process-local per-IP rate limiter dedicated to the
// standalone-port body-creds login path. The header-authenticated path is
// not gated by this limiter — that traffic comes from the trusted host proxy.
//
// Construct one per process (in main.go) and inject via Deps.LoginLimiter.
// Constructing one per Handler would leak a janitor goroutine on every
// plugin reconfigure, since the SDK calls NewHandler again on each Configure.
type LoginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*loginLimiterEntry
	stopCh  chan struct{}
}

// NewLoginLimiter builds a limiter and starts its background janitor. The
// janitor exits when Stop() is called; until then it sweeps idle buckets
// every loginLimitGCEvery.
func NewLoginLimiter() *LoginLimiter {
	l := &LoginLimiter{
		buckets: make(map[string]*loginLimiterEntry),
		stopCh:  make(chan struct{}),
	}
	go l.janitor()
	return l
}

// Stop terminates the janitor goroutine. Safe to call once; calling twice
// will close a closed channel and panic, which is what we want — it signals
// a leak.
func (l *LoginLimiter) Stop() { close(l.stopCh) }

func (l *LoginLimiter) allow(key string) bool {
	if key == "" {
		return true
	}
	l.mu.Lock()
	e, ok := l.buckets[key]
	if !ok {
		e = &loginLimiterEntry{
			limiter: rate.NewLimiter(rate.Every(loginLimitPerToken), loginLimitBurst),
		}
		l.buckets[key] = e
	}
	e.last = time.Now()
	lim := e.limiter
	l.mu.Unlock()
	return lim.Allow()
}

func (l *LoginLimiter) janitor() {
	ticker := time.NewTicker(loginLimitGCEvery)
	defer ticker.Stop()
	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-loginLimitIdle)
			l.mu.Lock()
			for k, e := range l.buckets {
				if e.last.Before(cutoff) {
					delete(l.buckets, k)
				}
			}
			l.mu.Unlock()
		}
	}
}

// clientIP returns the rate-limit key for a request. The standalone listener
// terminates the TCP connection directly with the listener's client, so
// r.RemoteAddr is the real client IP. When the request is host-proxied the
// X-Continuum-* headers are present and the body-creds path is never reached;
// we still honour X-Forwarded-For's first hop as a defensive fallback for
// operators who insert their own reverse proxy in front of the standalone
// listener. Only the first hop is trusted — the rest is client-supplied.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if v := strings.TrimSpace(xff); v != "" {
			return v
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
