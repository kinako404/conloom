package core

import (
	"context"
	"errors"
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

// memCmd runs the memory commands /好感度, /记忆 and /忘记我 (and their
// English forms) in a group. /忘记我 works whether or not memory is on; the
// others need it on, and /好感度 also needs affinity. Admins can also start a
// consolidation (/记忆 整理) and see the group summary (/记忆 群).
func (o *Orchestrator) memCmd(ctx context.Context, task store.TaskRecord, isAdmin bool, command, args string) (string, error) {
	if convType(task.ConversationKey) != conloomapi.ConvGroup {
		return CopyMemoryGroupOnly, nil
	}
	if command == "忘记我" || command == "forgetme" {
		return o.memCmdForget(ctx, task)
	}
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		return "", err
	}
	pol := pv.Policy
	if !pol.Memory.Enabled || !pol.Concurrency.AllowLongRequests {
		return CopyMemoryDisabled, nil
	}
	if command == "好感度" || command == "affinity" {
		if !pol.Memory.Affinity {
			return CopyAffinityOff, nil
		}
		return o.memCmdAffinity(ctx, task)
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
		return CopyMemoryUsage, nil
	case fields[0] == "整理" || fields[0] == "consolidate":
		if !isAdmin {
			return CopyAdminOnly, nil
		}
		if o.memTrigger != nil {
			o.memTrigger.Trigger(task.ConversationKey)
		}
		return CopyMemoryConsolidating, nil
	case fields[0] == "群" || fields[0] == "group":
		if !isAdmin {
			return CopyAdminOnly, nil
		}
		g, err := o.st.GetGroupMemory(ctx, task.ConversationKey)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if g.Summary == "" {
			return CopyMemoryNoSummary, nil
		}
		return CopyMemoryGroupSummary(g.Summary), nil
	}
	return CopyMemoryUsage, nil
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

// memCmdForget clears the sender's note and affinity, counting an affinity
// change when they had one.
func (o *Orchestrator) memCmdForget(ctx context.Context, task store.TaskRecord) (string, error) {
	m, err := o.memCmdMember(ctx, task)
	if err != nil {
		return "", err
	}
	if err := o.st.ForgetMember(ctx, task.ConversationKey, task.ActorPlatformUserID, o.now()); err != nil {
		return "", err
	}
	if m.Affinity != nil {
		o.metrics.MemoryAffinityChanges.WithLabelValues(store.RevisionCommand).Inc()
	}
	return CopyMemoryForgotten, nil
}
