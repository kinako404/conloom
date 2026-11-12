package core

import (
	"sync"
	"time"
)

const (
	rateWindow        = time.Minute
	rateSweepInterval = time.Minute
)

// RateLimiter is a per-key sliding-window limiter over the last 60 seconds.
// It is safe for concurrent use.
type RateLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	lastSweep time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{hits: make(map[string][]time.Time)}
}

// Allow records a hit for key and reports whether it is within perMinute hits
// in the 60s window ending at now. Denied calls are not recorded.
// perMinute <= 0 disables the limit.
func (r *RateLimiter) Allow(key string, perMinute int, now time.Time) bool {
	if perMinute <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-rateWindow)
	if now.Sub(r.lastSweep) >= rateSweepInterval {
		r.sweep(cutoff)
		r.lastSweep = now
	}
	hits := prune(r.hits[key], cutoff)
	if len(hits) >= perMinute {
		r.hits[key] = hits
		return false
	}
	r.hits[key] = append(hits, now)
	return true
}

func (r *RateLimiter) sweep(cutoff time.Time) {
	for k, hits := range r.hits {
		if hits = prune(hits, cutoff); len(hits) == 0 {
			delete(r.hits, k)
		} else {
			r.hits[k] = hits
		}
	}
}

func prune(hits []time.Time, cutoff time.Time) []time.Time {
	i := 0
	for i < len(hits) && !hits[i].After(cutoff) {
		i++
	}
	return hits[i:]
}
