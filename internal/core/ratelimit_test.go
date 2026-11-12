package core

import (
	"testing"
	"time"
)

func TestRateLimiter(t *testing.T) {
	r := NewRateLimiter()
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	key := "bot_main:20001"
	for i := 0; i < 6; i++ {
		if !r.Allow(key, 6, now.Add(time.Duration(i)*time.Second)) {
			t.Fatalf("call %d denied, want allowed", i+1)
		}
	}
	if r.Allow(key, 6, now.Add(6*time.Second)) {
		t.Fatal("call 7 allowed, want denied")
	}
	if !r.Allow("bot_main:20002", 6, now.Add(6*time.Second)) {
		t.Fatal("other key denied, want allowed")
	}
	if !r.Allow(key, 6, now.Add(61*time.Second)) {
		t.Fatal("call after 61s denied, want allowed")
	}
}

func TestRateLimiter_SlidingWindow(t *testing.T) {
	r := NewRateLimiter()
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	key := "bot_main:20001"
	if !r.Allow(key, 2, now) {
		t.Fatal("first call denied")
	}
	if !r.Allow(key, 2, now.Add(30*time.Second)) {
		t.Fatal("second call denied")
	}
	if r.Allow(key, 2, now.Add(59*time.Second)) {
		t.Fatal("third call within window allowed")
	}
	if !r.Allow(key, 2, now.Add(60*time.Second)) {
		t.Fatal("call after oldest expired denied")
	}
	if r.Allow(key, 2, now.Add(61*time.Second)) {
		t.Fatal("call with two hits in window allowed")
	}
}

func TestRateLimiter_CleansEmptyKeys(t *testing.T) {
	r := NewRateLimiter()
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	r.Allow("bot_main:20001", 6, now)
	r.Allow("bot_main:20002", 6, now)
	r.Allow("bot_main:20001", 6, now.Add(2*time.Minute))
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.hits["bot_main:20002"]; ok {
		t.Fatal("expired key not cleaned up")
	}
	if len(r.hits) != 1 {
		t.Fatalf("len(hits) = %d, want 1", len(r.hits))
	}
}
