package panel

import (
	"slices"
	"sync"
	"time"
)

// Login failures allowed within loginWindow, per source IP and in total.
const (
	loginWindow      = 15 * time.Minute
	loginFailsPerIP  = 5
	loginFailsGlobal = 30
)

// loginLimiter counts failed logins in a sliding window. An attempt reserves a
// slot before the password is checked, so attempts still in flight count
// against the limits too and a burst of concurrent attempts cannot exceed
// them. Rejected attempts reserve nothing, so the limiter holds at most
// loginFailsGlobal recent failures and attempts.
type loginLimiter struct {
	mu             sync.Mutex
	perIP          map[string]*ipAttempts
	global         []time.Time
	globalInFlight int
}

type ipAttempts struct {
	fails    []time.Time
	inFlight int
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{perIP: map[string]*ipAttempts{}}
}

// begin reserves an attempt for ip and returns 0, or returns how long ip must
// wait before trying again without reserving anything. Every reservation must
// be released by end.
func (l *loginLimiter) begin(ip string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	a := l.perIP[ip]
	var d time.Duration
	if a != nil && len(a.fails)+a.inFlight >= loginFailsPerIP {
		d = waitFor(a.fails, loginFailsPerIP, now)
	}
	if len(l.global)+l.globalInFlight >= loginFailsGlobal {
		d = max(d, waitFor(l.global, loginFailsGlobal, now))
	}
	if d > 0 {
		return d
	}
	if a == nil {
		a = &ipAttempts{}
		l.perIP[ip] = a
	}
	a.inFlight++
	l.globalInFlight++
	return 0
}

// end releases the attempt begin reserved for ip, recording it as a failure at
// now when failed.
func (l *loginLimiter) end(ip string, now time.Time, failed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.globalInFlight--
	a := l.perIP[ip]
	a.inFlight--
	if failed {
		a.fails = insertSorted(a.fails, now)
		l.global = insertSorted(l.global, now)
	}
	l.prune(now)
}

// insertSorted inserts t into ts in order: concurrent attempts may end out of
// order.
func insertSorted(ts []time.Time, t time.Time) []time.Time {
	i, _ := slices.BinarySearchFunc(ts, t, time.Time.Compare)
	return slices.Insert(ts, i, t)
}

// waitFor is how long until fewer than limit of fails remain in the window; a
// second when the limit is reached only with attempts in flight, which end soon.
func waitFor(fails []time.Time, limit int, now time.Time) time.Duration {
	if len(fails) < limit {
		return time.Second
	}
	return max(fails[len(fails)-limit].Add(loginWindow).Sub(now), time.Second)
}

// prune drops failures that have left the window and IPs with nothing left.
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
	for ip, a := range l.perIP {
		a.fails = recent(a.fails)
		if len(a.fails) == 0 && a.inFlight == 0 {
			delete(l.perIP, ip)
		}
	}
}
