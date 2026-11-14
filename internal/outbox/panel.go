package outbox

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	defaultPanelInterval = time.Second
	// panelBatch is the most panel actions one lease takes.
	panelBatch = 50
)

// PanelDeliverer delivers the actions of the panel channel, which no adapter
// sends: every interval, and whenever woken, it leases the due ones (see
// store.Store.LeasePanelOutbox) and acknowledges each as sent, which stores
// it as the bot's message on the panel channel with the platform message ID
// panel_<action ID>.
type PanelDeliverer struct {
	svc      *Service
	st       store.Store
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger

	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	start  sync.Once
}

// NewPanelDeliverer returns a deliverer acknowledging through svc. A zero
// interval is 1s, a nil now is time.Now and a nil log discards.
func NewPanelDeliverer(svc *Service, st store.Store, interval time.Duration, now func() time.Time, log *slog.Logger) *PanelDeliverer {
	if interval <= 0 {
		interval = defaultPanelInterval
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &PanelDeliverer{svc: svc, st: st, interval: interval, now: now, log: log,
		wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

// Start starts delivering; later calls do nothing.
func (d *PanelDeliverer) Start() {
	d.start.Do(func() { go d.loop() })
}

// Wake makes the deliverer look for due actions now.
func (d *PanelDeliverer) Wake() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Notify wakes the deliverer, whichever bot has new actions; it makes the
// deliverer a notifier of the orchestrator.
func (d *PanelDeliverer) Notify(string) { d.Wake() }

// Close stops delivering and waits for the delivery in progress to end.
func (d *PanelDeliverer) Close() {
	d.cancel()
	d.start.Do(func() { close(d.done) })
	<-d.done
}

func (d *PanelDeliverer) loop() {
	defer close(d.done)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		d.deliver()
		select {
		case <-d.ctx.Done():
			return
		case <-d.wake:
		case <-ticker.C:
		}
	}
}

// deliver acknowledges the due panel actions until none is left; each
// acknowledgement may make the next part of a reply the head of its
// conversation.
func (d *PanelDeliverer) deliver() {
	for d.ctx.Err() == nil {
		recs, err := d.st.LeasePanelOutbox(d.ctx, panelBatch, d.now(), d.svc.cfg.Lease)
		if err != nil {
			if d.ctx.Err() == nil {
				d.log.Error("lease panel actions failed", "error", err)
			}
			return
		}
		if len(recs) == 0 {
			return
		}
		for _, r := range recs {
			err := d.svc.Ack(d.ctx, r.BotInstanceID, r.Action.ActionID, conloomapi.AckRequest{
				Status: conloomapi.AckSent, PlatformMessageID: "panel_" + r.Action.ActionID,
			})
			if err != nil && d.ctx.Err() == nil {
				d.log.Error("acknowledge panel action failed", "action_id", r.Action.ActionID,
					"conversation", r.ConversationKey, "error", err)
			}
		}
	}
}
