package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/admission"
	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// Results of conloom_wake_total: wake tasks record the first three, the poll
// of due timers (pollWakes) the others.
const (
	wakeSent      = "sent"
	wakeSkipped   = "skipped"
	wakeCancelled = "cancelled"
	wakeDropped   = "dropped"
	wakePostponed = "postponed"
	wakeMissed    = "missed"
)

const (
	// quietLookback and quietMaxMessages bound the user's messages that quiet
	// hours are inferred from.
	quietLookback    = 30 * 24 * time.Hour
	quietMaxMessages = 1000
	// lastUserScan is how many of the user's newest message times are read to
	// find their last message.
	lastUserScan = 8
	// wakeRecentScan is how many of the chat's newest messages are searched
	// for one the user sent after a wake task was created or its timer armed.
	wakeRecentScan = 20
	// unnamedUser stands, in a directive, for a user without a usable name.
	unnamedUser = "对方"
	// wakeCancelUserMessage is the reason cancelWakes gives for a message of
	// the user.
	wakeCancelUserMessage = "user_message"
)

// errUserSpeaking stops fireTimer while a message of the chat's user is being
// accepted, errBusy while the chat has queued or running tasks; the timer is
// left as it is.
var (
	errUserSpeaking = errors.New("core: a message of the user is being accepted")
	errBusy         = errors.New("core: the conversation is busy")
)

// isWake reports whether trigger is that of a task the bot starts itself in a
// private chat when a conversation timer fires.
func isWake(trigger string) bool {
	switch trigger {
	case TriggerStatusReturn, TriggerInitiate, TriggerFollowUp:
		return true
	}
	return false
}

// wakeActor is the scheduler actor of the wake tasks of userID's private
// chat: neither their messages merge into those tasks nor supersede them.
func wakeActor(userID string) string { return "wake:" + userID }

// schedActor is the scheduler actor of task.
func schedActor(task store.TaskRecord) string {
	if isWake(task.Meta.Trigger) {
		return wakeActor(task.ActorPlatformUserID)
	}
	return task.ActorPlatformUserID
}

// wakeTrigger is the trigger of the wake task of a timer of kind.
func wakeTrigger(kind store.TimerKind) (string, bool) {
	switch kind {
	case store.TimerStatusReturn:
		return TriggerStatusReturn, true
	case store.TimerInitiate:
		return TriggerInitiate, true
	case store.TimerFollowUp:
		return TriggerFollowUp, true
	}
	return "", false
}

// wakeKind is the timer kind of a wake task's trigger.
func wakeKind(trigger string) store.TimerKind {
	switch trigger {
	case TriggerStatusReturn:
		return store.TimerStatusReturn
	case TriggerInitiate:
		return store.TimerInitiate
	case TriggerFollowUp:
		return store.TimerFollowUp
	}
	return ""
}

// timerEventKey is the internal idempotency key of the event that stores the
// wake task of t: firing the same timer twice stores one task.
func timerEventKey(t store.ConversationTimer) string {
	return "conloom:timer:" + t.ConversationKey + ":" + string(t.Kind) + ":" + t.DueAt.UTC().Format(time.RFC3339Nano)
}

// wakeRun is what a wake task's outcome carries: its timer kind, the name its
// user goes by, when they were last forgotten and, for a status return, when
// the status it returns from ended.
type wakeRun struct {
	kind        store.TimerKind
	name        string
	forgotten   time.Time
	statusUntil time.Time
}

// speakers counts, per conversation, the private messages of its user being
// accepted: from when their wake tasks are cancelled until the message is
// stored, fireTimer starts none (see submit).
type speakers struct {
	mu sync.Mutex
	n  map[string]int
}

// begin marks a message of convKey's user as being accepted until end is
// called.
func (s *speakers) begin(convKey string) (end func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == nil {
		s.n = map[string]int{}
	}
	s.n[convKey]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.n[convKey]--; s.n[convKey] <= 0 {
				delete(s.n, convKey)
			}
		})
	}
}

func (s *speakers) active(convKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n[convKey] > 0
}

// lockedSource makes a rand.Source safe for concurrent use, and with it the
// *rand.Rand built on it, which keeps no other state.
type lockedSource struct {
	mu  sync.Mutex
	src rand.Source
}

func (s *lockedSource) Uint64() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.src.Uint64()
}

func newLockedRand() *rand.Rand {
	return rand.New(&lockedSource{src: rand.NewPCG(rand.Uint64(), rand.Uint64())})
}

// fireTimer starts the wake task of t, a due timer of a private chat. In one
// transaction it stores the task, under the internal event timerEventKey(t),
// and deletes t: the task has t's kind as its trigger, no trigger messages,
// background priority and the current time as its event time, so a restart
// soon after does not find it stale. The task is queued under wakeActor. A
// timer cancelled, re-armed or postponed since it was read is gone: nothing
// is stored and fireTimer returns nil, as it does when the event exists
// already because t fired before (a timer armed again for that time is then
// deleted, so it is not claimed again and again). While a message of the
// chat's user is being accepted it stores nothing and returns
// errUserSpeaking, while the chat has queued or running tasks errBusy. It
// holds the conversation's proactive lock, as cancelWakes does, so a wake
// task it stored is already queued when cancelWakes looks for it.
func (o *Orchestrator) fireTimer(ctx context.Context, t store.ConversationTimer) error {
	trigger, ok := wakeTrigger(t.Kind)
	if !ok {
		return fmt.Errorf("core: timer of %s has unknown kind %q", t.ConversationKey, t.Kind)
	}
	if convType(t.ConversationKey) != conloomapi.ConvPrivate {
		return fmt.Errorf("core: timer of %s: not a private chat", t.ConversationKey)
	}
	if o.closing.Load() {
		return ErrSchedulerClosed
	}
	unlock := o.proactiveLocks.lock(t.ConversationKey)
	defer unlock()
	if o.speakers.active(t.ConversationKey) {
		return errUserSpeaking
	}
	if o.sched.Busy(t.ConversationKey) {
		return errBusy
	}
	now := o.now()
	id := ids.New("task")
	task := store.TaskRecord{
		ID: id, BotInstanceID: t.BotInstanceID, ConversationKey: t.ConversationKey, Status: conloomapi.TaskQueued,
		ActorPlatformUserID: t.PlatformUserID, Priority: int(admission.PriorityBackground),
		Meta: conloomapi.TaskMeta{Trigger: trigger}, EventOccurredAt: now, CreatedAt: now, UpdatedAt: now,
	}
	resp, err := json.Marshal(conloomapi.EventAccepted{Disposition: conloomapi.DispositionReply, TaskID: id, Status: conloomapi.TaskQueued})
	if err != nil {
		return err
	}
	attrs := []any{"conversation", t.ConversationKey, "kind", string(t.Kind), "due_at", t.DueAt.UTC()}
	ref := timerRef(t)
	existing, err := o.st.Ingest(ctx, store.Ingest{
		Event: store.EventRecord{BotInstanceID: t.BotInstanceID, IdempotencyKey: timerEventKey(t), EventID: "timer_" + id,
			Response: resp, CreatedAt: now},
		NewTask:   &task,
		FireTimer: &ref,
	})
	switch {
	case errors.Is(err, store.ErrTimerGone):
		o.log.Info("wake timer gone before it fired", attrs...)
		return nil
	case err != nil:
		return err
	case existing != nil:
		o.dropFiredTimer(ctx, t)
		return nil
	}
	at := time.Now()
	o.sched.Restore(QueuedTask{TaskID: id, ConversationKey: t.ConversationKey, ActorID: wakeActor(t.PlatformUserID),
		EnqueuedAt: at, ReadyAt: at})
	o.log.Info("wake timer fired", append(attrs, "task_id", id)...)
	return nil
}

// staleWake follows up a wake task that Recover failed as stale, without a
// message: an initiate, whose timer went when it fired, is planned again
// (replanInitiate); a status return ends the status it returned from, which
// had ended by the time the task was created, and leaves a newer one; a
// follow-up is not tried again. Failures are logged.
func (o *Orchestrator) staleWake(ctx context.Context, task store.TaskRecord) {
	switch task.Meta.Trigger {
	case TriggerInitiate:
		o.replanInitiate(ctx, task.ConversationKey)
	case TriggerStatusReturn:
		st, err := o.st.GetStatus(ctx, task.ConversationKey)
		if err == nil && !st.UntilAt.After(task.EventOccurredAt) {
			_, err = o.st.ClearStatus(ctx, task.ConversationKey, st.UntilAt)
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			o.log.Error("end status of a stale status return failed", "task_id", task.ID, "conversation", task.ConversationKey,
				"error", err)
		}
	}
}

// dropFiredTimer deletes t if it is armed again for the time it already fired
// at.
func (o *Orchestrator) dropFiredTimer(ctx context.Context, t store.ConversationTimer) {
	deleted, err := o.st.DeleteTimer(ctx, timerRef(t))
	if err != nil {
		o.log.Error("drop a fired timer failed", "conversation", t.ConversationKey, "kind", string(t.Kind), "error", err)
		return
	}
	if deleted {
		o.log.Info("wake timer dropped: it already fired", "conversation", t.ConversationKey, "kind", string(t.Kind), "due_at", t.DueAt.UTC())
	}
}

// timerRef names t as armed now.
func timerRef(t store.ConversationTimer) store.TimerRef {
	return store.TimerRef{ConversationKey: t.ConversationKey, Kind: t.Kind, DueAt: t.DueAt}
}

// cancelWakes cancels the queued and running wake tasks of userID's private
// chat convKey, of every kind, and the actions of their replies not handed out
// yet; also those of the user's last reply (store.LastReplyTask) when a wake
// task wrote it, as cancelUnsentParts does for replies to the user. Each task
// it cancels or cuts short is audited as wake.cancelled with reason; an
// initiate or follow-up whose stored reply it cuts short no longer counts as
// started (uncountWake). A status return it cancels ends the status too. It
// holds the conversation's proactive lock, which a wake task's finish holds
// while it stores its reply, so that reply is either withheld there or
// cancelled here.
func (o *Orchestrator) cancelWakes(ctx context.Context, convKey, userID, reason string) {
	unlock := o.proactiveLocks.lock(convKey)
	defer unlock()
	botID, _, _ := strings.Cut(convKey, ":")
	type wake struct {
		task   store.TaskRecord
		active bool
	}
	var wakes []wake
	for _, qt := range o.sched.Active(convKey) {
		if qt.ActorID != wakeActor(userID) {
			continue
		}
		task, err := o.st.GetTask(ctx, botID, qt.TaskID)
		if err != nil {
			o.log.Error("load wake task to cancel failed", "task_id", qt.TaskID, "conversation", convKey, "error", err)
			continue
		}
		done, err := o.cancel(ctx, botID, convKey, qt.TaskID)
		if err != nil {
			o.log.Error("cancel wake task failed", "task_id", qt.TaskID, "conversation", convKey, "error", err)
		} else if done == nil && !taskFinished(task.Status) {
			// A runner would have recorded the cancellation itself.
			o.metrics.Wake.WithLabelValues(string(wakeKind(task.Meta.Trigger)), wakeCancelled).Inc()
		}
		wakes = append(wakes, wake{task, true})
	}
	last, err := o.st.LastReplyTask(ctx, botID, convKey, userID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		o.log.Error("load last reply to cancel wake parts failed", "conversation", convKey, "error", err)
	case !slices.ContainsFunc(wakes, func(w wake) bool { return w.task.ID == last }):
		task, err := o.st.GetTask(ctx, botID, last)
		if err != nil {
			o.log.Error("load last reply to cancel wake parts failed", "task_id", last, "conversation", convKey, "error", err)
		} else if isWake(task.Meta.Trigger) {
			wakes = append(wakes, wake{task: task})
		}
	}
	endStatus := false
	for _, w := range wakes {
		n, err := o.st.CancelPendingActions(ctx, w.task.ID, o.now().UTC())
		if err != nil {
			o.log.Error("cancel unsent wake reply parts failed", "task_id", w.task.ID, "error", err)
		}
		if !w.active && n == 0 {
			continue
		}
		endStatus = endStatus || w.active && w.task.Meta.Trigger == TriggerStatusReturn
		o.log.Info("wake task cancelled", "conversation", convKey, "task_id", w.task.ID, "kind", w.task.Meta.Trigger,
			"reason", reason, "cancelled_parts", n)
		o.auditWakeCancelled(ctx, botID, convKey, w.task, reason, n)
		if n > 0 {
			o.uncountWake(ctx, convKey, w.task)
		}
	}
	if endStatus {
		if _, err := o.st.ClearStatus(ctx, convKey, time.Time{}); err != nil {
			o.log.Error("end status of a cancelled status return failed", "conversation", convKey, "error", err)
		}
	}
}

// uncountWake takes back an initiate or follow-up whose stored reply
// cancelWakes cut short: its proactive log entry is deleted, so it counts
// neither toward the daily cap nor as unanswered, and so is the follow-up an
// initiate armed.
func (o *Orchestrator) uncountWake(ctx context.Context, convKey string, task store.TaskRecord) {
	kind := wakeKind(task.Meta.Trigger)
	if kind != store.TimerInitiate && kind != store.TimerFollowUp {
		return
	}
	if _, err := o.st.DeleteProactiveLog(ctx, convKey, task.ID); err != nil {
		o.log.Error("take back a cancelled started message failed", "task_id", task.ID, "conversation", convKey, "error", err)
	}
	if kind != store.TimerInitiate {
		return
	}
	if _, err := o.st.CancelTimers(ctx, convKey, store.TimerFollowUp); err != nil {
		o.log.Error("cancel the follow-up of a cancelled initiate failed", "task_id", task.ID, "conversation", convKey, "error", err)
	}
}

// auditWakeCancelled records that cancelWakes cancelled task for reason,
// with the number of its reply's actions it cancelled.
func (o *Orchestrator) auditWakeCancelled(ctx context.Context, botID, convKey string, task store.TaskRecord, reason string, cancelled int) {
	detail, _ := json.Marshal(struct {
		TaskID    string `json:"task_id"`
		Kind      string `json:"kind"`
		Reason    string `json:"reason"`
		Cancelled int    `json:"cancelled"`
	}{task.ID, task.Meta.Trigger, reason, cancelled})
	err := o.st.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: botID, Kind: "wake.cancelled", Actor: "system",
		Target: convKey, Detail: string(detail), CreatedAt: o.now().UTC(),
	})
	if err != nil {
		o.log.Error("audit cancelled wake task failed", "task_id", task.ID, "error", err)
	}
}

// wakeName is the name the user of a wake task goes by: that of their newest
// message in history, else the one their memory row keeps.
func wakeName(history []store.MessageRecord, row store.MemberMemory) string {
	for i := len(history) - 1; i >= 0; i-- {
		if m := history[i]; !m.FromBot && m.DisplayName != "" {
			return m.DisplayName
		}
	}
	return row.DisplayName
}

// wakeDirectiveName makes a wake task's user name safe for a directive (see
// directiveName), unnamedUser when nothing usable is left.
func wakeDirectiveName(name string) string {
	if n := directiveName(name); n != unnamedMember {
		return n
	}
	return unnamedUser
}

// wakeDirective is the hint that ends a wake task's context, by kind: an
// initiate names the user, the local weekday and time and how long ago the
// chat's last message in history was; a status return names the activity of
// the conversation's status, which must have ended (a newer one may have
// replaced it), and records its end in w. Initiates and follow-ups outside
// ContentOpen get copyNoRomance. It reports false, after logging why, when
// there is nothing to return from.
func (o *Orchestrator) wakeDirective(ctx context.Context, task store.TaskRecord, w *wakeRun, mode conloomapi.ContentMode,
	history []store.MessageRecord) (string, bool) {
	name := wakeDirectiveName(w.name)
	protected := mode != conloomapi.ContentOpen
	switch w.kind {
	case store.TimerInitiate:
		now := o.now()
		gap := ""
		if n := len(history); n > 0 {
			gap = approxGap(now.Sub(history[n-1].OccurredAt))
		}
		return CopyDirectiveInitiate(name, weekdayClock(now, o.loc), gap, protected), true
	case store.TimerFollowUp:
		return CopyDirectiveFollowUp(name, protected), true
	case store.TimerStatusReturn:
		st, err := o.st.GetStatus(ctx, task.ConversationKey)
		switch {
		case errors.Is(err, store.ErrNotFound):
			o.log.Info("status return dropped: no status", "task_id", task.ID, "conversation", task.ConversationKey)
			return "", false
		case err != nil:
			o.log.Warn("status return dropped: load status failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
			return "", false
		case st.UntilAt.After(o.now()):
			o.log.Info("status return dropped: a newer status lasts", "task_id", task.ID, "conversation", task.ConversationKey)
			return "", false
		}
		w.statusUntil = st.UntilAt
		return CopyDirectiveStatusReturn(st.Activity), true
	}
	return "", false
}

var weekdays = [...]string{"星期日", "星期一", "星期二", "星期三", "星期四", "星期五", "星期六"}

// weekdayClock formats t in loc as "星期四 16:00".
func weekdayClock(t time.Time, loc *time.Location) string {
	l := t.In(loc)
	return weekdays[l.Weekday()] + " " + l.Format("15:04")
}

// approxGap words how long ago something was: "不到 1 小时", "约 N 小时"
// below two days, else "约 N 天".
func approxGap(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "不到 1 小时"
	case d < 48*time.Hour:
		return "约 " + strconv.Itoa(int(math.Round(d.Hours()))) + " 小时"
	}
	return "约 " + strconv.Itoa(int(math.Round(d.Hours()/24))) + " 天"
}

// privateAllowed reports whether userID may chat privately with bot on
// platform under its private_chat setting.
func privateAllowed(bot config.BotConfig, platform, userID string) bool {
	return !privateDenied(DecideInput{
		Platform: platform, PlatformUserID: userID, PrivateChat: bot.PrivateChat,
		IsAdmin: isAdmin(bot, platform, userID), PrivateListed: privateListed(bot, userID),
	})
}

// wakeAllowed reports whether the bot may still start the private chat of an
// initiate or follow-up task: proactive replies are on, the private level is
// not off, the chat is not paused and its user may still chat privately; the
// bot started fewer messages today than the level's daily cap; and it
// started none since the user's last message before an initiate, exactly one
// (the initiate) before a follow-up.
func (o *Orchestrator) wakeAllowed(ctx context.Context, task store.TaskRecord, kind store.TimerKind) (bool, error) {
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return false, err
	}
	pol := pv.Policy
	if !o.mayStart(pol, task.BotInstanceID, convPlatform(task.ConversationKey), task.ActorPlatformUserID) {
		return false, nil
	}
	limit := autonomy.DailyCap(pol.Proactive.Private.EffectiveLevel())
	today, err := o.privateStarted(ctx, task.ConversationKey, o.dayStart(o.now()))
	if err != nil || today >= limit {
		return false, err
	}
	n, err := o.unanswered(ctx, task.ConversationKey)
	if err != nil {
		return false, err
	}
	want := 0
	if kind == store.TimerFollowUp {
		want = 1
	}
	return n == want, nil
}

// mayStart reports whether pol lets bot start messages in the private chat of
// userID on platform: proactive replies are on, the private level is not off
// and the chat is not paused, and the user may still chat privately.
func (o *Orchestrator) mayStart(pol conloomapi.GroupPolicy, botID, platform, userID string) bool {
	bot, ok := o.cfg.Bot(botID)
	return ok && pol.Proactive.Enabled && autonomy.DailyCap(pol.Proactive.Private.EffectiveLevel()) > 0 &&
		pol.Concurrency.MaxInteractiveRequests != 0 && privateAllowed(bot, platform, userID)
}

// privateStarted counts the messages the bot started in the private chat
// (initiates and follow-ups) since since.
func (o *Orchestrator) privateStarted(ctx context.Context, convKey string, since time.Time) (int, error) {
	total := 0
	for _, kind := range []string{store.ProactivePrivateInitiate, store.ProactivePrivateFollowUp} {
		n, err := o.st.CountProactive(ctx, convKey, "", kind, since)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// unanswered counts the messages the bot started in the private chat since
// the user's last message; all those still logged when none of the chat's
// newest messages is the user's (see store.UserMessageTimes).
func (o *Orchestrator) unanswered(ctx context.Context, convKey string) (int, error) {
	since, _, err := o.lastUserMessage(ctx, convKey)
	if err != nil {
		return 0, err
	}
	return o.privateStarted(ctx, convKey, since)
}

// lastUserMessage returns when the private chat's user sent their newest
// message, and false when none of the chat's newest messages is theirs (see
// store.UserMessageTimes).
func (o *Orchestrator) lastUserMessage(ctx context.Context, convKey string) (time.Time, bool, error) {
	times, err := o.st.UserMessageTimes(ctx, convKey, time.Time{}, lastUserScan)
	if err != nil || len(times) == 0 {
		return time.Time{}, false, err
	}
	return times[0], true, nil
}

// quietFor returns the private chat's quiet hours: its policy's, or else
// those inferred from userTimes (autonomy.ResolveQuiet). A read error is
// logged and leaves the policy's or the default quiet hours.
func (o *Orchestrator) quietFor(ctx context.Context, convKey string, forgotten time.Time) autonomy.Quiet {
	var p conloomapi.PrivateProactivePolicy
	if pv, err := o.policies.Current(ctx, convKey); err != nil {
		o.log.Warn("quiet hours: load policy failed", "conversation", convKey, "error", err)
	} else {
		p = pv.Policy.Proactive.Private
	}
	times, err := o.userTimes(ctx, convKey, forgotten)
	if err != nil {
		o.log.Warn("quiet hours: load message times failed", "conversation", convKey, "error", err)
		times = nil
	}
	return autonomy.ResolveQuiet(p, times, o.loc)
}

// userTimes returns when the private chat's user sent their newest messages
// (at most quietMaxMessages) of the last 30 days that came after forgotten,
// newest first.
func (o *Orchestrator) userTimes(ctx context.Context, convKey string, forgotten time.Time) ([]time.Time, error) {
	since := o.now().Add(-quietLookback)
	if forgotten.After(since) {
		since = forgotten
	}
	return o.st.UserMessageTimes(ctx, convKey, since, quietMaxMessages)
}

// newestSeq is the seq of the conversation's newest message, 0 without one.
func (o *Orchestrator) newestSeq(ctx context.Context, convKey string) (int64, error) {
	msgs, err := o.st.RecentMessages(ctx, convKey, 1)
	if err != nil || len(msgs) == 0 {
		return 0, err
	}
	return msgs[0].Seq, nil
}

// ownerCancelled reports whether the party running taskID was told to stop
// (see cancel).
func (o *Orchestrator) ownerCancelled(taskID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	ow := o.owners[taskID]
	return ow != nil && ow.cancelled
}

// userWroteSince reports whether the private chat has a message of its user
// received after at among its newest wakeRecentScan messages.
func (o *Orchestrator) userWroteSince(ctx context.Context, convKey string, at time.Time) (bool, error) {
	msgs, err := o.st.RecentMessages(ctx, convKey, wakeRecentScan)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(msgs, func(m store.MessageRecord) bool { return !m.FromBot && m.CreatedAt.After(at) }), nil
}

// withholdWake drops the reply of a wake task that must not be sent, checked
// in finishWake under the conversation's proactive lock: one told to stop
// (cancelWakes, /stop) ends cancelled; with a reply, one whose user wrote or
// was forgotten after it was created ends cancelled too, and an initiate or
// follow-up the bot may no longer send (wakeAllowed) ends skipped. A failed
// check is logged and skips the reply.
func (o *Orchestrator) withholdWake(ctx context.Context, task store.TaskRecord, out *outcome) {
	withhold := func(reason string, cancel bool) {
		o.log.Info("wake reply withheld", "task_id", task.ID, "conversation", task.ConversationKey, "reason", reason)
		out.text = ""
		if cancel {
			out.status, out.code, out.skipped = conloomapi.TaskCancelled, conloomapi.CodeTaskCancelled, false
			return
		}
		out.skipped, out.meta.ReviewResult = true, reviewResultSkipped
	}
	fail := func(err error) {
		o.log.Error("wake send check failed; not sending", "task_id", task.ID, "error", err)
		withhold("error", false)
	}
	if o.ownerCancelled(task.ID) {
		withhold("cancelled", true)
		return
	}
	if out.text == "" {
		return
	}
	wrote, err := o.userWroteSince(ctx, task.ConversationKey, task.CreatedAt)
	if err != nil {
		fail(err)
		return
	}
	if wrote {
		withhold("user_message", true)
		return
	}
	rows, err := o.st.GetMemberMemories(ctx, task.ConversationKey, []string{task.ActorPlatformUserID})
	if err != nil {
		fail(err)
		return
	}
	if rows[task.ActorPlatformUserID].ForgottenAt.After(task.CreatedAt) {
		withhold("forgotten", true)
		return
	}
	if out.wake.kind == store.TimerStatusReturn {
		return
	}
	ok, err := o.wakeAllowed(ctx, task, out.wake.kind)
	if err != nil {
		fail(err)
		return
	}
	if !ok {
		withhold("not_allowed", false)
	}
}

// finishWake stores a wake task's outcome under the conversation's proactive
// lock, which cancelWakes holds too, after withholdWake. It then records it:
// a sent initiate or follow-up is logged as such, and an initiate arms its
// follow-up (armFollowUp); any other initiate or follow-up that was not
// cancelled is logged as skipped, and an initiate is planned again
// (replanInitiate, after the lock is released); a status return ends the
// status it returned from, if it is still the conversation's, and once sent
// plans the next initiate again with the level's gap counted from now
// (replanAfter), as its message is not stored until it is delivered.
func (o *Orchestrator) finishWake(ctx context.Context, task store.TaskRecord, out *outcome) (sent bool, err error) {
	replan := false
	var spoke time.Time
	defer func() {
		if replan {
			o.replanAfter(ctx, task.ConversationKey, spoke)
		}
	}()
	unlock := o.proactiveLocks.lock(task.ConversationKey)
	defer unlock()
	if out.status != conloomapi.TaskCancelled {
		o.withholdWake(ctx, task, out)
	}
	var actions []store.OutboxRecord
	if out.text != "" {
		actions = o.newActions(ctx, task, out.conv, nil, splitParts(out.text, out.maxParts))
	}
	if err := o.complete(ctx, task, out.status, out.meta, out.code, actions); err != nil {
		return false, err
	}
	sent = len(actions) > 0
	w := out.wake
	result := wakeSkipped
	switch {
	case sent:
		result = wakeSent
	case out.status == conloomapi.TaskCancelled:
		result = wakeCancelled
	}
	o.metrics.Wake.WithLabelValues(string(w.kind), result).Inc()
	switch w.kind {
	case store.TimerStatusReturn:
		if !w.statusUntil.IsZero() {
			if _, err := o.st.ClearStatus(ctx, task.ConversationKey, w.statusUntil); err != nil {
				o.log.Error("end status after its return failed", "task_id", task.ID, "error", err)
			}
		}
		if sent {
			replan, spoke = true, o.now()
		}
	case store.TimerInitiate, store.TimerFollowUp:
		kind := store.ProactivePrivateSkip
		switch {
		case sent && w.kind == store.TimerInitiate:
			kind = store.ProactivePrivateInitiate
		case sent:
			kind = store.ProactivePrivateFollowUp
		case out.status == conloomapi.TaskCancelled:
			return false, nil
		}
		if err := o.st.AppendProactiveLog(ctx, store.ProactiveLogEntry{
			ConversationKey: task.ConversationKey, PlatformUserID: task.ActorPlatformUserID, Kind: kind,
			TaskID: task.ID, CreatedAt: o.now().UTC(),
		}); err != nil {
			o.log.Error("record started message failed", "task_id", task.ID, "kind", kind, "error", err)
		}
		switch {
		case sent && w.kind == store.TimerInitiate:
			o.armFollowUp(ctx, task, w)
		case !sent:
			replan = w.kind == store.TimerInitiate
		}
	}
	return sent, nil
}

// armFollowUp arms the follow-up of an initiate task just sent, at
// autonomy.PlanFollowUp outside the chat's quiet hours, unless the bot
// already started autonomy.MaxUnanswered messages since the user's last one.
func (o *Orchestrator) armFollowUp(ctx context.Context, task store.TaskRecord, w *wakeRun) {
	fail := func(err error) {
		o.log.Error("arm follow-up failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
	}
	n, err := o.unanswered(ctx, task.ConversationKey)
	if err != nil {
		fail(err)
		return
	}
	if n >= autonomy.MaxUnanswered {
		return
	}
	anchor, err := o.newestSeq(ctx, task.ConversationKey)
	if err != nil {
		fail(err)
		return
	}
	now := o.now()
	due := autonomy.PlanFollowUp(now, o.quietFor(ctx, task.ConversationKey, w.forgotten), o.loc, o.rand)
	if err := o.st.ArmTimer(ctx, store.ConversationTimer{
		ConversationKey: task.ConversationKey, Kind: store.TimerFollowUp, BotInstanceID: task.BotInstanceID,
		PlatformUserID: task.ActorPlatformUserID, DueAt: due, AnchorSeq: anchor, CreatedAt: now.UTC(),
	}); err != nil {
		fail(err)
		return
	}
	o.log.Info("wake timer armed", "conversation", task.ConversationKey, "kind", string(store.TimerFollowUp), "due_at", due.UTC())
}
