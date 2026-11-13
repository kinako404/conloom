package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kinako404/conloom/internal/adapter/dedup"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/pkg/client"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	dedupFileName  = "telegram-dedup.log"
	dedupRetention = 24 * time.Hour
	outboxLimit    = 10
	ackTimeout     = 10 * time.Second
	callTimeout    = 30 * time.Second
	actionTimeout  = 10 * time.Second
	// pollSlack is how much longer than the long-poll timeout a getUpdates
	// request may take before it is abandoned.
	pollSlack = 15 * time.Second

	outcomeUnknown = "delivery outcome unknown; not resent to avoid a duplicate"
)

// Adapter connects one Telegram bot to the ConLoom core: it long-polls
// getUpdates, forwards private messages to the core and delivers outbox
// actions with sendMessage.
type Adapter struct {
	cfg  config.TelegramConfig
	log  *slog.Logger
	core *client.Client
	api  *botAPI
	// logCfg is the zero ObservabilityConfig: message text is never logged.
	logCfg config.ObservabilityConfig

	pollTimeout         time.Duration
	outboxWait          time.Duration
	outboxErrorDelay    time.Duration
	retryMin, retryMax  time.Duration
	submitMin           time.Duration
	submitMax           time.Duration
	stuckAttempts       int
	stuckAfter          time.Duration
	sendTimeout         time.Duration
	maxRateLimitRetries int
	typingInterval      time.Duration
	typingFor           time.Duration

	self   botUser
	dedup  *dedup.Log
	typing *typing
	// noTaskReads is set once the core refused a task read (no tasks:read).
	noTaskReads atomic.Bool

	mu             sync.Mutex
	disabledGroups map[int64]bool
}

// New validates cfg and returns an adapter. A nil logger discards logs.
func New(cfg config.TelegramConfig, logger *slog.Logger) (*Adapter, error) {
	var missing []string
	for _, f := range []struct{ name, v string }{
		{"token", cfg.Token},
		{"api_base", cfg.APIBase},
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
		return nil, fmt.Errorf("telegram: missing config: %s", strings.Join(missing, ", "))
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		cfg:                 cfg,
		log:                 logger,
		core:                client.New(cfg.CoreURL, cfg.CoreToken, nil),
		api:                 newBotAPI(cfg.APIBase, cfg.Token, &http.Client{}),
		pollTimeout:         max(cfg.PollTimeout.Duration.Truncate(time.Second), time.Second),
		outboxWait:          25 * time.Second,
		outboxErrorDelay:    time.Second,
		retryMin:            time.Second,
		retryMax:            30 * time.Second,
		submitMin:           500 * time.Millisecond,
		submitMax:           30 * time.Second,
		stuckAttempts:       10,
		stuckAfter:          5 * time.Minute,
		sendTimeout:         30 * time.Second,
		maxRateLimitRetries: 5,
		typingInterval:      4 * time.Second,
		typingFor:           60 * time.Second,
		disabledGroups:      map[int64]bool{},
	}, nil
}

// Run serves until ctx is done and then returns nil, once an in-flight send
// has its result. It returns an error if the dedup log cannot be opened,
// Telegram rejects the bot token, or the core token belongs to another bot
// instance. Other failures are retried with exponential backoff.
func (a *Adapter) Run(ctx context.Context) error {
	sent, err := dedup.Open(filepath.Join(a.cfg.StateDir, dedupFileName), dedupRetention, time.Now)
	if err != nil {
		return err
	}
	defer sent.Close()
	a.dedup = sent

	self, err := a.getMe(ctx)
	if err != nil || ctx.Err() != nil {
		return err
	}
	a.self = self
	if err := a.deleteWebhook(ctx); err != nil || ctx.Err() != nil {
		return err
	}
	if err := a.checkCore(ctx); err != nil || ctx.Err() != nil {
		return err
	}
	a.log.Info("telegram adapter started", "bot_id", self.ID, "bot_username", self.Username,
		"api_base", a.api.redact(a.cfg.APIBase))

	a.typing = newTyping(ctx, a.typingInterval, a.typingFor, a.sendTyping, a.taskFinished)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.deliverOutbox(ctx)
	}()
	a.receive(ctx)
	wg.Wait()
	a.typing.wait()
	return nil
}

// retry calls f until it succeeds, returns a permanent error, or ctx ends,
// waiting retryMin doubling up to retryMax between attempts. It returns nil
// when ctx ends.
func (a *Adapter) retry(ctx context.Context, what string, f func() (permanent bool, err error)) error {
	backoff := a.retryMin
	for {
		permanent, err := f()
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if permanent {
			return err
		}
		a.log.Warn(what+" failed; retrying", "error", err, "retry_in", backoff.String())
		if !sleep(ctx, backoff) {
			return nil
		}
		backoff = min(backoff*2, a.retryMax)
	}
}

func (a *Adapter) getMe(ctx context.Context) (botUser, error) {
	var me botUser
	err := a.retry(ctx, "telegram getMe", func() (bool, error) {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		err := a.api.call(cctx, "getMe", nil, &me)
		if ae := (*apiError)(nil); errors.As(err, &ae) && (ae.Code == http.StatusUnauthorized || ae.Code == http.StatusNotFound) {
			return true, fmt.Errorf("telegram: the bot token was rejected (%s); check adapters.telegram.token", ae.Description)
		}
		return false, err
	})
	return me, err
}

// deleteWebhook removes a webhook set for the bot, which would make every
// getUpdates fail with 409. Pending updates are kept.
func (a *Adapter) deleteWebhook(ctx context.Context) error {
	return a.retry(ctx, "telegram deleteWebhook", func() (bool, error) {
		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		return false, a.api.call(cctx, "deleteWebhook", deleteWebhookParams{DropPendingUpdates: false}, nil)
	})
}

// checkCore reads the core's capabilities for the adapter's token and stops
// the adapter if the token belongs to another bot instance, since every
// event it submitted would be refused and its update dropped.
func (a *Adapter) checkCore(ctx context.Context) error {
	return a.retry(ctx, "reading core capabilities", func() (bool, error) {
		caps, err := a.core.Capabilities(ctx)
		if err != nil {
			return !client.IsRetryable(err), err
		}
		if caps.BotInstanceID != a.cfg.BotInstanceID {
			return true, fmt.Errorf("telegram: core_token belongs to bot instance %q, not %q", caps.BotInstanceID, a.cfg.BotInstanceID)
		}
		if self := strconv.FormatInt(a.self.ID, 10); caps.PlatformSelfID != self {
			a.log.Error("the Telegram bot differs from the bot instance's platform_self_id",
				"bot_id", self, "platform_self_id", caps.PlatformSelfID)
		}
		return false, nil
	})
}

// receive long-polls getUpdates until ctx is done. The offset that confirms an
// update to Telegram moves past it only once the update was handled, so an
// update the core has not accepted is fetched again after a restart.
func (a *Adapter) receive(ctx context.Context) {
	var offset int64
	backoff := a.retryMin
	for ctx.Err() == nil {
		raws, err := a.getUpdates(ctx, offset)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			wait := backoff
			ae := (*apiError)(nil)
			isAPIError := errors.As(err, &ae)
			if isAPIError && ae.RetryAfter > 0 {
				wait = ae.RetryAfter
			}
			if isAPIError && ae.Code == http.StatusConflict {
				a.log.Error("telegram getUpdates conflict: another process may be polling this bot with the same token, or a webhook was set again; retrying",
					"error", err, "retry_in", wait.String())
			} else {
				a.log.Warn("telegram getUpdates failed; retrying", "error", err, "retry_in", wait.String())
			}
			if !sleep(ctx, wait) {
				return
			}
			backoff = min(backoff*2, a.retryMax)
			continue
		}
		backoff = a.retryMin
		for _, raw := range raws {
			var u update
			if err := json.Unmarshal(raw, &u); err != nil {
				var id struct {
					UpdateID int64 `json:"update_id"`
				}
				_ = json.Unmarshal(raw, &id)
				a.log.Warn("dropping an undecodable telegram update", "update_id", id.UpdateID, "error", err)
				offset = max(offset, id.UpdateID+1)
				continue
			}
			if !a.handle(ctx, u) {
				return
			}
			offset = u.UpdateID + 1
		}
	}
}

func (a *Adapter) getUpdates(ctx context.Context, offset int64) ([]json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, a.pollTimeout+pollSlack)
	defer cancel()
	var raws []json.RawMessage
	err := a.api.call(cctx, "getUpdates", getUpdatesParams{
		Offset: offset, Timeout: int(a.pollTimeout / time.Second), AllowedUpdates: []string{"message"},
	}, &raws)
	return raws, err
}

// handle submits u to the core, retrying with backoff until the core accepts
// it; the update's offset is not confirmed meanwhile, so Telegram keeps it.
// Failures that may pass are retried: network errors, 5xx and 429, and also
// 401 and 403, which mean the adapter's token or scopes are wrong and are
// logged as errors. An update failing for more than stuckAttempts attempts
// or stuckAfter is still retried, but each failure is then logged as an
// error. An update that is not a message from a handled chat, or that the
// core rejects otherwise (400, 409), is skipped. It returns false only if ctx
// ended first.
func (a *Adapter) handle(ctx context.Context, u update) bool {
	ev, key, err := toStandard(u, a.self, a.cfg.BotInstanceID)
	if err != nil {
		a.log.Debug("skipping telegram update", "update_id", u.UpdateID, "reason", err.Error())
		return true
	}
	log := a.log.With("update_id", u.UpdateID, "event_id", ev.EventID, "conversation_id", ev.Conversation.ID)
	chatID := u.Message.Chat.ID
	delay := a.submitMin
	started := time.Now()
	for failures := 1; ; failures++ {
		submitted := time.Now()
		resp, err := a.core.SubmitEvent(ctx, key, ev)
		if err == nil {
			log.Debug("event submitted", "disposition", resp.Disposition, "task_id", resp.TaskID,
				"suppressed_reason", resp.SuppressedReason, obs.MessageAttr(a.logCfg, ev.Message.Text))
			if resp.Disposition == conloomapi.DispositionReply {
				a.typing.start(chatID, submitted, resp.TaskID)
			}
			if resp.SuppressedReason == conloomapi.SuppressedNotEnabled && ev.Conversation.Type == conloomapi.ConvGroup {
				a.noteGroupNotEnabled(chatID, ev.Conversation.ID)
			}
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		apiErr := (*client.APIError)(nil)
		refused := errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden)
		if !refused && !client.IsRetryable(err) {
			log.Error("core rejected the update; skipping it", "error", err)
			return true
		}
		stuckFor := time.Since(started)
		switch {
		case refused:
			log.Error("the core refused the adapter's token; check adapters.telegram.core_token, its bot_instance_id and its events:write scope; retrying the update",
				"error", err, "attempts", failures, "retry_in", delay.String())
		case failures > a.stuckAttempts || stuckFor > a.stuckAfter:
			log.Error("telegram update stuck: the core keeps failing it; it stays unconfirmed at Telegram for up to 24h and later updates wait behind it; retrying the update",
				"error", err, "attempts", failures, "failing_for", stuckFor.Round(time.Second).String(), "retry_in", delay.String())
		default:
			log.Warn("core unavailable; retrying the update", "error", err, "attempts", failures, "retry_in", delay.String())
		}
		if !sleep(ctx, delay) {
			return false
		}
		delay = min(delay*2, a.submitMax)
	}
}

// noteGroupNotEnabled logs, once per group and process, that the core ignores
// a group the bot is in, with the chat ID to list in enabled_conversations.
func (a *Adapter) noteGroupNotEnabled(chatID int64, conversationID string) {
	a.mu.Lock()
	seen := a.disabledGroups[chatID]
	a.disabledGroups[chatID] = true
	a.mu.Unlock()
	if !seen {
		a.log.Info("the bot is in a Telegram group the core does not serve; list it in bots[].enabled_conversations to enable it",
			"chat_id", chatID, "conversation_id", conversationID)
	}
}

// deliverOutbox polls the outbox and sends actions until ctx is done.
func (a *Adapter) deliverOutbox(ctx context.Context) {
	for {
		actions, err := a.core.PollOutbox(ctx, outboxLimit, a.outboxWait)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			a.log.Warn("polling the outbox failed", "error", err)
			if !sleep(ctx, a.outboxErrorDelay) {
				return
			}
			continue
		}
		for _, act := range actions {
			if ctx.Err() != nil {
				return
			}
			if err := a.deliver(ctx, act); err != nil {
				if !sleep(ctx, a.outboxErrorDelay) {
					return
				}
				break
			}
		}
	}
}

func (a *Adapter) sendTyping(ctx context.Context, chatID int64) {
	cctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := a.api.call(cctx, "sendChatAction", chatActionParams{ChatID: chatID, Action: "typing"}, nil); err != nil && ctx.Err() == nil {
		a.log.Debug("sending the typing action failed", "error", err)
	}
}

// taskFinished reports whether the core has finished task taskID. When the
// core token lacks tasks:read it warns once and stops asking, so typing lasts
// until the reply is sent or the time limit; other failures count as not
// finished.
func (a *Adapter) taskFinished(ctx context.Context, taskID string) bool {
	if taskID == "" || a.noTaskReads.Load() {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	task, err := a.core.GetTask(cctx, taskID)
	if apiErr := (*client.APIError)(nil); errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
		if !a.noTaskReads.Swap(true) {
			a.log.Warn("the core token lacks the tasks:read scope; typing is shown until the reply is sent or the time limit passes",
				"limit", a.typingFor.String())
		}
		return false
	}
	if err != nil {
		if ctx.Err() == nil {
			a.log.Debug("reading the task failed", "task_id", taskID, "error", err)
		}
		return false
	}
	switch task.Status {
	case conloomapi.TaskSucceeded, conloomapi.TaskFailed, conloomapi.TaskCancelled:
		return true
	}
	return false
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
