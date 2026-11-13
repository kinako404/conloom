package core

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/review"
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

func (o *Orchestrator) memCmdAffinity(ctx context.Context, task store.TaskRecord) (string, error) {
	bot, _ := o.cfg.Bot(task.BotInstanceID)
	pv, err := o.personas.Current(ctx, task.ConversationKey, bot.PersonaTemplate)
	if err != nil {
		return "", err
	}
	name := persona.BotName(pv.Persona, bot.Name)
	m, err := o.memCmdMember(ctx, task)
	if err != nil {
		return "", err
	}
	if m.Affinity == nil {
		return CopyAffinityNone(name), nil
	}
	return CopyAffinity(name, memory.TierName(*m.Affinity), *m.Affinity), nil
}

// memCmdRemember appends what the sender asked to remember to their note (see
// memory.AppendRemembered), within the conversation's note limit. Contact
// details and identity numbers are refused; the rest goes through the review
// of incoming messages and then the redaction and blocked words of outgoing
// ones, as it may be shown to the reply model.
func (o *Orchestrator) memCmdRemember(ctx context.Context, task store.TaskRecord, args string, triggers []store.MessageRecord,
	pol conloomapi.ReviewPolicy) (string, error) {
	content := strings.Join(strings.Fields(args), " ")
	switch n := utf8.RuneCountInString(content); {
	case n == 0:
		return CopyRememberUsage, nil
	case n > memory.RememberMaxRunes:
		return CopyRememberTooLong, nil
	case memory.LooksLikeContactOrID(content):
		return CopyRememberSensitive, nil
	}
	in := o.reviewer.Input(content, pol)
	o.metrics.Review.WithLabelValues("input", in.Verdict).Inc()
	out := o.reviewer.Output(in.Text, pol)
	o.metrics.Review.WithLabelValues("output", out.Verdict).Inc()
	if content = strings.Join(strings.Fields(out.Text), " "); out.Verdict == review.VerdictBlock || content == "" {
		return CopyRememberBlocked, nil
	}
	limit := memory.NoteLimit(convType(task.ConversationKey))
	user, name := task.ActorPlatformUserID, ""
	if len(triggers) > 0 {
		name = triggers[len(triggers)-1].DisplayName
	}
	_, err := memory.UpdateMember(ctx, o.st, task.ConversationKey, user, store.RevisionCommand,
		func(m *store.MemberMemory) (string, bool) {
			m.Note = memory.AppendRemembered(m.Note, content, limit)
			if name != "" && name != user {
				m.DisplayName = name
			}
			return memory.ReasonRemember, true
		})
	if err != nil {
		return "", err
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
