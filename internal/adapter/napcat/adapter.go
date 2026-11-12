package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	dedupFileName     = "napcat-dedup.log"
	dedupRetention    = 24 * time.Hour
	pendingBufferSize = 256
	pollLimit         = 10
	pollWait          = 25 * time.Second
	pollErrorDelay    = time.Second
	capsRefresh       = 5 * time.Minute
	ackTimeout        = 10 * time.Second
	backoffMin        = time.Second
	backoffMax        = 30 * time.Second
)

// Adapter connects one NapCat instance to the ConLoom core: it forwards
// message events to the core and delivers outbox actions to NapCat.
type Adapter struct {
	cfg  config.NapCatConfig
	log  *slog.Logger
	core *client.Client
	// logCfg is the zero ObservabilityConfig: message text is never logged.
	logCfg config.ObservabilityConfig

	submitDelays  []time.Duration
	retryInterval time.Duration

	dedup *Dedup

	mu          sync.Mutex
	enabled     map[string]bool
	privateChat string

	pendingMu sync.Mutex
	pending   []pendingEvent
}

type pendingEvent struct {
	key string
	ev  conloomapi.Event
}

// New validates cfg and returns an adapter. A nil logger discards logs.
func New(cfg config.NapCatConfig, logger *slog.Logger) (*Adapter, error) {
	var missing []string
	for _, f := range []struct{ name, v string }{
		{"ws_url", cfg.WSURL},
		{"core_url", cfg.CoreURL},
		{"core_token", cfg.CoreToken},
		{"bot_instance_id", cfg.BotInstanceID},
		{"state_dir", cfg.StateDir},
	} {
		if strings.TrimSpace(f.v) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("napcat: missing config: %s", strings.Join(missing, ", "))
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		cfg:           cfg,
		log:           logger,
		core:          client.New(cfg.CoreURL, cfg.CoreToken, nil),
		submitDelays:  []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second},
		retryInterval: 5 * time.Second,
	}, nil
}

// Run connects to NapCat and serves until ctx is done, reconnecting with
// exponential backoff (1s doubling up to 30s) after any connection failure.
// It returns nil when ctx is done and an error only if the local dedup log
// cannot be opened.
func (a *Adapter) Run(ctx context.Context) error {
	dedup, err := OpenDedup(filepath.Join(a.cfg.StateDir, dedupFileName), dedupRetention, time.Now)
	if err != nil {
		return err
	}
	defer dedup.Close()
	a.dedup = dedup

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.retryPending(ctx)
	}()
	defer wg.Wait()

	backoff := backoffMin
	for {
		connected, err := a.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if connected {
			backoff = backoffMin
		}
		a.log.Warn("napcat connection unavailable; reconnecting", "error", err, "retry_in", backoff.String())
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		backoff = min(backoff*2, backoffMax)
	}
}

// session runs one connection until it fails or ctx is done. connected
// reports whether the connection got as far as serving events.
func (a *Adapter) session(ctx context.Context) (connected bool, err error) {
	conn, err := Dial(ctx, a.cfg.WSURL, a.cfg.AccessToken)
	if err != nil {
		return false, err
	}
	defer conn.Close()

	data, err := conn.Call(ctx, "get_login_info", nil)
	if err != nil {
		return false, err
	}
	var login struct {
		UserID json.Number `json:"user_id"`
	}
	if err := decodeNumbers(data, &login); err != nil || !isNumericID(login.UserID.String()) {
		return false, fmt.Errorf("napcat: get_login_info: unexpected data %s", data)
	}
	selfID := login.UserID.String()

	caps, err := a.core.Capabilities(ctx)
	if err != nil {
		return false, fmt.Errorf("napcat: core capabilities: %w", err)
	}
	a.applyCaps(caps, selfID)
	a.log.Info("napcat connected", "ws_url", redactURL(a.cfg.WSURL), "self_id", selfID)

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sendFailed := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		a.forwardEvents(sctx, conn, selfID)
	}()
	go func() {
		defer wg.Done()
		if err := a.deliverOutbox(sctx, conn); err != nil {
			sendFailed <- err
		}
	}()
	go func() {
		defer wg.Done()
		a.refreshCaps(sctx, selfID)
	}()

	select {
	case <-conn.Done():
		err = conn.Err()
	case err = <-sendFailed:
	case <-ctx.Done():
	}
	cancel()
	_ = conn.Close()
	wg.Wait()
	if n := conn.Dropped(); n > 0 {
		a.log.Warn("napcat events dropped because the event buffer was full", "dropped", n)
	}
	return true, err
}

func decodeNumbers(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(v)
}

func (a *Adapter) applyCaps(caps conloomapi.Capabilities, selfID string) {
	if caps.PlatformSelfID != selfID {
		a.log.Error("NapCat login account differs from the bot's configured platform_self_id; filtering by the NapCat account",
			"self_id", selfID, "platform_self_id", caps.PlatformSelfID)
	}
	if caps.BotInstanceID != a.cfg.BotInstanceID {
		a.log.Error("core token belongs to a different bot instance",
			"bot_instance_id", a.cfg.BotInstanceID, "token_bot_instance_id", caps.BotInstanceID)
	}
	enabled := map[string]bool{}
	for _, c := range caps.EnabledConversations {
		if c.Type == conloomapi.ConvGroup {
			enabled[c.PlatformID] = true
		}
	}
	a.mu.Lock()
	a.enabled, a.privateChat = enabled, caps.PrivateChat
	a.mu.Unlock()
}

func (a *Adapter) refreshCaps(ctx context.Context, selfID string) {
	t := time.NewTicker(capsRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		caps, err := a.core.Capabilities(ctx)
		if err != nil {
			if ctx.Err() == nil {
				a.log.Warn("refreshing core capabilities failed; keeping the previous ones", "error", err)
			}
			continue
		}
		a.applyCaps(caps, selfID)
	}
}

// accepts reports whether a message event should be forwarded to the core.
func (a *Adapter) accepts(ev OBEvent, selfID string) (bool, string) {
	if ev.UserID.String() == selfID {
		return false, "self message"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch ev.MessageType {
	case "group":
		if !a.enabled[ev.GroupID.String()] {
			return false, "group not enabled"
		}
	case "private":
		if a.privateChat == "off" {
			return false, "private chat off"
		}
	}
	return true, ""
}

// forwardEvents runs until the connection's event channel is closed, so events
// received before a disconnect are still forwarded or buffered.
func (a *Adapter) forwardEvents(ctx context.Context, conn *Conn, selfID string) {
	for raw := range conn.Events() {
		ob, err := ParseEvent(raw)
		if err != nil {
			a.log.Warn("dropping undecodable NapCat message", "error", err)
			continue
		}
		if ob.PostType != "message" {
			a.log.Debug("ignoring NapCat event", "post_type", ob.PostType)
			continue
		}
		if ok, reason := a.accepts(ob, selfID); !ok {
			a.log.Debug("not forwarding message", "reason", reason, "message_type", ob.MessageType,
				"group_id", ob.GroupID.String(), "message_id", ob.MessageID.String())
			continue
		}
		ev, key, err := ToStandard(ob, a.cfg.BotInstanceID)
		if err != nil {
			a.log.Debug("not forwarding message", "reason", err.Error())
			continue
		}
		a.forward(ctx, key, ev)
	}
}

// forward submits ev, retrying retryable failures, and buffers it for later
// retry if the core stays unavailable.
func (a *Adapter) forward(ctx context.Context, key string, ev conloomapi.Event) {
	log := a.log.With("event_id", ev.EventID, "conversation_id", ev.Conversation.ID)
	for attempt := 0; ; attempt++ {
		resp, err := a.core.SubmitEvent(ctx, key, ev)
		if err == nil {
			log.Debug("event submitted", "disposition", resp.Disposition, "task_id", resp.TaskID,
				obs.MessageAttr(a.logCfg, ev.Message.Text))
			return
		}
		if ctx.Err() != nil || !client.IsRetryable(err) {
			if ctx.Err() == nil {
				log.Error("core rejected event; dropping it", "error", err)
				return
			}
			a.buffer(key, ev)
			return
		}
		if attempt == len(a.submitDelays) {
			log.Warn("core unavailable; buffering event for retry", "error", err)
			a.buffer(key, ev)
			return
		}
		if !sleep(ctx, a.submitDelays[attempt]) {
			a.buffer(key, ev)
			return
		}
	}
}

func (a *Adapter) buffer(key string, ev conloomapi.Event) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	if len(a.pending) >= pendingBufferSize {
		dropped := a.pending[0]
		a.pending = a.pending[1:]
		a.log.Warn("event retry buffer full; dropping the oldest event", "event_id", dropped.ev.EventID)
	}
	a.pending = append(a.pending, pendingEvent{key: key, ev: ev})
}

// retryPending resubmits buffered events every retryInterval until ctx is done.
func (a *Adapter) retryPending(ctx context.Context) {
	t := time.NewTicker(a.retryInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		a.flushPending(ctx)
	}
}

// flushPending submits buffered events in order, stopping at the first
// retryable failure.
func (a *Adapter) flushPending(ctx context.Context) {
	for {
		a.pendingMu.Lock()
		if len(a.pending) == 0 {
			a.pendingMu.Unlock()
			return
		}
		p := a.pending[0]
		a.pendingMu.Unlock()

		resp, err := a.core.SubmitEvent(ctx, p.key, p.ev)
		if err != nil && (ctx.Err() != nil || client.IsRetryable(err)) {
			return
		}
		a.pendingMu.Lock()
		if len(a.pending) > 0 && a.pending[0].key == p.key {
			a.pending = a.pending[1:]
		}
		a.pendingMu.Unlock()
		if err != nil {
			a.log.Error("core rejected buffered event; dropping it", "event_id", p.ev.EventID, "error", err)
			continue
		}
		a.log.Info("buffered event submitted", "event_id", p.ev.EventID, "disposition", resp.Disposition, "task_id", resp.TaskID)
	}
}

// deliverOutbox polls the outbox and sends actions until ctx is done. It
// returns an error when sending over the WebSocket fails, ending the session.
func (a *Adapter) deliverOutbox(ctx context.Context, conn *Conn) error {
	for {
		actions, err := a.core.PollOutbox(ctx, pollLimit, pollWait)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			a.log.Warn("polling the outbox failed", "error", err)
			if !sleep(ctx, pollErrorDelay) {
				return nil
			}
			continue
		}
		for _, act := range actions {
			if err := a.deliver(ctx, conn, act); err != nil {
				return err
			}
		}
	}
}

// deliver sends one action unless the dedup log shows it was already sent,
// then ACKs it. Only a failure to send over the WebSocket is returned; the
// action is left un-ACKed so it is redelivered when its lease expires.
func (a *Adapter) deliver(ctx context.Context, conn *Conn, act conloomapi.Action) error {
	log := a.log.With("action_id", act.ActionID, "conversation_id", act.ConversationID)
	if pmid, ok := a.dedup.Seen(act.ActionID); ok {
		log.Info("action already sent; acknowledging again", "platform_message_id", pmid)
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: pmid})
		return nil
	}
	action, params, err := SendRequest(act)
	if errors.Is(err, ErrUnsupportedAction) {
		log.Info("unsupported action", "type", act.Type)
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckUnsupported, Error: err.Error()})
		return nil
	}
	if err != nil {
		log.Error("cannot build the NapCat request", "error", err)
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckFailed, Error: err.Error()})
		return nil
	}

	data, err := conn.Call(ctx, action, params)
	if cerr := (*callError)(nil); errors.As(err, &cerr) {
		log.Error("NapCat rejected the message", "error", err, obs.MessageAttr(a.logCfg, act.Content.Text))
		a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckFailed, Error: err.Error()})
		return nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		log.Warn("sending over the NapCat connection failed; leaving the action for redelivery", "error", err)
		return err
	}
	var sent struct {
		MessageID json.Number `json:"message_id"`
	}
	if err := decodeNumbers(data, &sent); err != nil || sent.MessageID == "" {
		log.Warn("send response has no message_id", "data", string(data))
	}
	pmid := sent.MessageID.String()
	if err := a.dedup.Mark(act.ActionID, pmid); err != nil {
		log.Error("recording the sent action failed", "error", err)
	}
	log.Info("message sent", "platform_message_id", pmid, obs.MessageAttr(a.logCfg, act.Content.Text))
	a.ack(ctx, log, act.ActionID, conloomapi.AckRequest{Status: conloomapi.AckSent, PlatformMessageID: pmid})
	return nil
}

// ack reports an action result. A failure is only logged: the action is
// redelivered after its lease expires and the dedup log prevents a resend.
func (a *Adapter) ack(ctx context.Context, log *slog.Logger, actionID string, req conloomapi.AckRequest) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
	defer cancel()
	if err := a.core.Ack(actx, actionID, req); err != nil {
		log.Warn("acknowledging the action failed; it will be redelivered", "status", req.Status, "error", err)
	}
}

// sleep waits for d and reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
