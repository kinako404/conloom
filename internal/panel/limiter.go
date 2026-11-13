package panel

import (
	"sync"
	"time"
)

// Login failures allowed within loginWindow, per source IP and in total.
const (
	loginWindow      = 15 * time.Minute
	loginFailsPerIP  = 5
	loginFailsGlobal = 30
)

// loginLimiter counts failed logins in a sliding window. Rejected attempts are
// not failures, so the failures it holds stay near loginFailsGlobal.
type loginLimiter struct {
	mu     sync.Mutex
	perIP  map[string][]time.Time
	global []time.Time
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{perIP: map[string][]time.Time{}}
}

// wait returns how long ip must wait before trying again; 0 when it may try now.
func (l *loginLimiter) wait(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	var d time.Duration
	if fails := l.perIP[ip]; len(fails) >= loginFailsPerIP {
		d = fails[len(fails)-loginFailsPerIP].Add(loginWindow).Sub(now)
	}
	if len(l.global) >= loginFailsGlobal {
		d = max(d, l.global[len(l.global)-loginFailsGlobal].Add(loginWindow).Sub(now))
	}
	return d
}

// fail records a failed login from ip.
func (l *loginLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	l.perIP[ip] = append(l.perIP[ip], now)
	l.global = append(l.global, now)
}

// prune drops failures that have left the window.
func (l *loginLimiter) prune(now time.Time) {
	cutoff := now.Add(-loginWindow)
	recent := func(ts []time.Time) []time.Time {
		i := 0
		for i < len(ts) && !ts[i].After(cutoff) {
			i++
		}
		return ts[i:]
	}
	l.global = recent(l.global)
	for ip, ts := range l.perIP {
		if ts = recent(ts); len(ts) == 0 {
			delete(l.perIP, ip)
		} else {
			l.perIP[ip] = ts
		}
	}
}
