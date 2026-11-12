// Package outbox hands stored actions to adapters via long polling and
// records their acknowledgements.
package outbox

import (
	"context"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// Config zero values fall back to Lease 60s, MaxWait 30s, RecheckEvery 2s.
type Config struct {
	Lease        time.Duration
	MaxWait      time.Duration
	RecheckEvery time.Duration
}

type Service struct {
	st      store.Store
	cfg     Config
	metrics *obs.Metrics
	now     func() time.Time

	mu      sync.Mutex
	signals map[string]chan struct{}
}

func New(st store.Store, cfg Config, m *obs.Metrics, now func() time.Time) *Service {
	if cfg.Lease <= 0 {
		cfg.Lease = 60 * time.Second
	}
	if cfg.MaxWait <= 0 {
		cfg.MaxWait = 30 * time.Second
	}
	if cfg.RecheckEvery <= 0 {
		cfg.RecheckEvery = 2 * time.Second
	}
	if now == nil {
		now = time.Now
	}
	return &Service{st: st, cfg: cfg, metrics: m, now: now, signals: map[string]chan struct{}{}}
}

// Notify wakes every Poll currently waiting for botID.
func (s *Service) Notify(botID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.signals[botID]; ok {
		close(ch)
		delete(s.signals, botID)
	}
}

func (s *Service) signal(botID string) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.signals[botID]
	if !ok {
		ch = make(chan struct{})
		s.signals[botID] = ch
	}
	return ch
}

// Poll leases up to limit actions for botID, waiting up to wait (capped at
// MaxWait) for some to become available. On timeout it returns an empty,
// non-nil slice.
func (s *Service) Poll(ctx context.Context, botID string, limit int, wait time.Duration) ([]conloomapi.Action, error) {
	wait = min(max(wait, 0), s.cfg.MaxWait)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		woken := s.signal(botID)
		actions, err := s.lease(ctx, botID, limit)
		if err != nil || len(actions) > 0 {
			return actions, err
		}
		recheck := time.NewTimer(s.cfg.RecheckEvery)
		select {
		case <-ctx.Done():
			recheck.Stop()
			return nil, ctx.Err()
		case <-deadline.C:
			recheck.Stop()
			return []conloomapi.Action{}, nil
		case <-woken:
		case <-recheck.C:
		}
		recheck.Stop()
	}
}

func (s *Service) lease(ctx context.Context, botID string, limit int) ([]conloomapi.Action, error) {
	recs, err := s.st.LeaseOutbox(ctx, botID, limit, s.now(), s.cfg.Lease)
	if err != nil {
		return nil, err
	}
	actions := make([]conloomapi.Action, 0, len(recs))
	for _, r := range recs {
		actions = append(actions, r.Action)
		if s.metrics != nil {
			s.metrics.OutboxDelivered.Inc()
			if r.Attempts > 1 {
				s.metrics.OutboxRedelivered.Inc()
			}
		}
	}
	return actions, nil
}

// Ack records the adapter's result for an action. A sent action is stored as
// the bot's own message; acking releases the conversation's next action.
func (s *Service) Ack(ctx context.Context, botID, actionID string, req conloomapi.AckRequest) error {
	rec, err := s.st.GetOutbox(ctx, botID, actionID)
	if err != nil {
		return err
	}
	now := s.now()
	var botMsg *store.MessageRecord
	if req.Status != conloomapi.AckUnsupported && req.Status != conloomapi.AckFailed {
		id := "msg_" + req.PlatformMessageID
		if req.PlatformMessageID == "" {
			id = "msg_bot_" + actionID
		}
		botMsg = &store.MessageRecord{
			ID:                id,
			BotInstanceID:     botID,
			ConversationKey:   rec.ConversationKey,
			Platform:          rec.Platform,
			PlatformMessageID: req.PlatformMessageID,
			Text:              rec.Action.Content.Text,
			ReplyTo:           rec.Action.ReplyToMessageID,
			FromBot:           true,
			OccurredAt:        now,
			CreatedAt:         now,
		}
	}
	if _, err := s.st.AckOutbox(ctx, botID, actionID, req, botMsg, now); err != nil {
		return err
	}
	s.Notify(botID)
	return nil
}
