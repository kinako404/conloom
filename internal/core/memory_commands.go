package core

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// MemoryTrigger starts a memory consolidation of a conversation in the
// background.
type MemoryTrigger interface {
	Trigger(convKey string)
}

// memCopy is the wording of the memory commands in a group or a private chat.
type memCopy struct {
	disabled, affinityOff, forgotten, paused, noSummary, usage string
	summary                                                    func(string) string
}

func memCopyFor(private bool) memCopy {
	if private {
		return memCopy{CopyMemoryDisabledPrivate, CopyAffinityOffPrivate, CopyMemoryForgottenPrivate,
			CopyMemoryPausedPrivate, CopyMemoryNoPrivateSummary, CopyMemoryUsagePrivate, CopyMemoryPrivateSummary}
	}
	return memCopy{CopyMemoryDisabled, CopyAffinityOff, CopyMemoryForgotten,
		CopyMemoryPaused, CopyMemoryNoSummary, CopyMemoryUsage, CopyMemoryGroupSummary}
}

// memCmd runs the memory commands /好感度, /记忆, /记住 and /忘记我 (and their
// English forms) in a group or a private chat. /忘记我 works whether or not
// memory is on; the others need it on, and /好感度 also needs affinity.
// Admins can set their own affinity with /好感度 设 n, +n or -n.
// /记忆 摘要 shows the summary: to anyone in a private chat, only to admins in
// a group, where /记忆 群 does the same. Admins can also start a
// consolidation (/记忆 整理, refused while the conversation is paused).
func (o *Orchestrator) memCmd(ctx context.Context, task store.TaskRecord, isAdmin bool, command string,
	triggers []store.MessageRecord) (string, error) {
	private := convType(task.ConversationKey) == conloomapi.ConvPrivate
	cp := memCopyFor(private)
	if command == "忘记我" || command == "forgetme" {
		return o.memCmdForget(ctx, task, cp)
	}
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return "", err
	}
	pol := pv.Policy
	if !pol.Memory.Enabled || !pol.Concurrency.AllowLongRequests {
		return cp.disabled, nil
	}
	args := commandArgs(triggers)
	switch command {
	case "好感度", "affinity":
		if !pol.Memory.Affinity {
			return cp.affinityOff, nil
		}
		if args != "" {
			return o.memCmdSetAffinity(ctx, task, isAdmin, args)
		}
		return o.memCmdAffinity(ctx, task)
	case "记住", "remember":
		return o.memCmdRemember(ctx, task, args, triggers, pol.Review)
	}
	fields := strings.Fields(strings.ToLower(args))
	switch {
	case len(fields) == 0:
		m, err := o.memCmdMember(ctx, task)
		if err != nil {
			return "", err
		}
		if m.Note == "" {
			return CopyMemoryNoNote, nil
		}
		return CopyMemoryNote(m.Note), nil
	case len(fields) > 1:
		return cp.usage, nil
	case fields[0] == "整理" || fields[0] == "consolidate":
		if !isAdmin {
			return CopyAdminOnly, nil
		}
		if pol.Concurrency.MaxInteractiveRequests == 0 {
			return cp.paused, nil
		}
		if o.memTrigger != nil {
			o.memTrigger.Trigger(task.ConversationKey)
		}
		return CopyMemoryConsolidating, nil
	case fields[0] == "摘要" || fields[0] == "summary" || !private && (fields[0] == "群" || fields[0] == "group"):
		if !private && !isAdmin {
			return CopyAdminOnly, nil
		}
		g, err := o.st.GetGroupMemory(ctx, task.ConversationKey)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if g.Summary == "" {
			return cp.noSummary, nil
		}
		return cp.summary(g.Summary), nil
	}
	return cp.usage, nil
}

func (o *Orchestrator) memCmdMember(ctx context.Context, task store.TaskRecord) (store.MemberMemory, error) {
	got, err := o.st.GetMemberMemories(ctx, task.ConversationKey, []string{task.ActorPlatformUserID})
	if err != nil {
		return store.MemberMemory{}, err
	}
	return got[task.ActorPlatformUserID], nil
}

func (o *Orchestrator) memCmdBotName(ctx context.Context, task store.TaskRecord) (string, error) {
	bot, _ := o.cfg.Bot(task.BotInstanceID)
	pv, err := o.personas.Current(ctx, task.ConversationKey, bot.PersonaTemplate)
	if err != nil {
		return "", err
	}
	return persona.BotName(pv.Persona, bot.Name), nil
}

func (o *Orchestrator) memCmdAffinity(ctx context.Context, task store.TaskRecord) (string, error) {
	name, err := o.memCmdBotName(ctx, task)
	if err != nil {
		return "", err
	}
	m, err := o.memCmdMember(ctx, task)
	if err != nil {
		return "", err
	}
	if m.Affinity == nil {
		return CopyAffinityNone(name), nil
	}
	return CopyAffinity(name, memory.TierName(*m.Affinity), *m.Affinity), nil
}

// memCmdSetAffinity lets an admin override their own affinity in the
// conversation: "设 n" / "set n" sets it to n in [0, 100], "+n" / "-n" moves
// it, clamped to [0, 100], from the current value or, without one, from
// memory.InitialAffinity. Daily caps and run deltas do not apply. The change
// goes through memory.Admin.EditMember as a command revision audited by the
// sender. Other arguments get the usage.
func (o *Orchestrator) memCmdSetAffinity(ctx context.Context, task store.TaskRecord, isAdmin bool, args string) (string, error) {
	fields := strings.Fields(args)
	var num string
	set := false
	switch {
	case len(fields) == 2 && (fields[0] == "设" || strings.ToLower(fields[0]) == "set"):
		num, set = fields[1], true
	case len(fields) == 1 && (strings.HasPrefix(fields[0], "+") || strings.HasPrefix(fields[0], "-")):
		num = fields[0]
	default:
		return CopyAffinityUsage, nil
	}
	if !isAdmin {
		return CopyAdminOnly, nil
	}
	n, err := strconv.Atoi(num)
	if err != nil || set && memory.Clamp(n) != n {
		return CopyAffinityUsage, nil
	}
	if !set {
		m, err := o.memCmdMember(ctx, task)
		if err != nil {
			return "", err
		}
		base := memory.InitialAffinity
		if m.Affinity != nil {
			base = *m.Affinity
		}
		n = memory.Clamp(base + n)
	}
	conv, err := o.conversation(ctx, task.ConversationKey)
	if err != nil {
		return "", err
	}
	put := conloomapi.MemberMemoryPut{Affinity: &n, Reason: memory.ReasonAffinityCommand}
	got, err := o.memAdmin.EditMember(ctx, conv, task.ActorPlatformUserID, put, store.RevisionCommand,
		"command:"+task.ActorPlatformUserID)
	if errors.Is(err, memory.ErrAudit) {
		o.log.Error("affinity changed but audit failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
	} else if err != nil {
		return "", err
	}
	if got.Affinity != nil {
		n = *got.Affinity
	}
	name, err := o.memCmdBotName(ctx, task)
	if err != nil {
		return "", err
	}
	return CopyAffinityAdjusted(name, memory.TierName(n), n), nil
}

// memCmdRemember remembers what the sender asked (memory.Remember) and
// answers with the copy for its outcome.
func (o *Orchestrator) memCmdRemember(ctx context.Context, task store.TaskRecord, args string, triggers []store.MessageRecord,
	pol conloomapi.ReviewPolicy) (string, error) {
	name := ""
	if len(triggers) > 0 {
		name = triggers[len(triggers)-1].DisplayName
	}
	status, err := memory.Remember(ctx, memory.RememberDeps{Store: o.st, Reviewer: o.reviewer, Metrics: o.metrics},
		memory.RememberRequest{ConversationKey: task.ConversationKey, UserID: task.ActorPlatformUserID,
			DisplayName: name, Content: args, Review: pol})
	if err != nil {
		return "", err
	}
	switch status {
	case memory.RememberEmpty:
		return CopyRememberUsage, nil
	case memory.RememberTooLong:
		return CopyRememberTooLong, nil
	case memory.RememberSensitive:
		return CopyRememberSensitive, nil
	case memory.RememberBlocked:
		return CopyRememberBlocked, nil
	}
	return CopyRemembered, nil
}

// memCmdForget forgets the sender (see memory.Forget), counting an affinity
// change when they had one.
func (o *Orchestrator) memCmdForget(ctx context.Context, task store.TaskRecord, cp memCopy) (string, error) {
	m, err := o.memCmdMember(ctx, task)
	if err != nil {
		return "", err
	}
	if err := memory.Forget(ctx, o.st, task.ConversationKey, task.ActorPlatformUserID, o.now()); err != nil {
		return "", err
	}
	if m.Affinity != nil {
		o.metrics.MemoryAffinityChanges.WithLabelValues(store.RevisionCommand).Inc()
	}
	return cp.forgotten, nil
}
