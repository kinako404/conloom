package obs

import (
	"maps"
	"sync"
	"time"
)

// Presence records when each bot instance last called the core. A nil
// *Presence records nothing and reports no bots.
type Presence struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewPresence() *Presence {
	return &Presence{seen: map[string]time.Time{}}
}

// Seen records that botID called the core at at.
func (p *Presence) Seen(botID string, at time.Time) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen[botID] = at
}

// Snapshot returns a copy of the last time each bot was seen.
func (p *Presence) Snapshot() map[string]time.Time {
	if p == nil {
		return map[string]time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.seen)
}
