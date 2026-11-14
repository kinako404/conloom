package core

import (
	"context"
	"errors"
	"slices"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// injectMaxOthers bounds the members besides the speaker shown in the memory
// block.
const injectMaxOthers = 2

// injectBlockShare is the share of the reply model's soft input budget the
// memory block may take, so that long summaries and notes still leave room
// for the recent history.
const injectBlockShare = 0.3

// memoryBlockMaxRunes caps the memory block at injectBlockShare of the short
// model's input budget (the long model's without a short one): the smaller
// of its soft input budget and its largest prompt under the conversation's
// max_context_tokens groupCap (router.EffectiveLimits), counted as runes of
// factor tokens each. It is 0, uncapped, without either model, and at least
// 1, which leaves the block out, when the budget is gone.
func memoryBlockMaxRunes(profiles map[string]config.ModelProfile, factor float64, groupCap int) int {
	p, ok := profiles[string(router.RoleShort)]
	if !ok {
		p, ok = profiles[string(router.RoleLong)]
	}
	if !ok || p.Context.SoftInputBudget <= 0 {
		return 0
	}
	_, maxPrompt := router.EffectiveLimits(p.Context, groupCap)
	budget := min(p.Context.SoftInputBudget, maxPrompt)
	return max(1, int(float64(budget)*injectBlockShare/factor))
}

// injectPerson is a member whose memory may be shown, with the name to show.
type injectPerson struct{ userID, name string }

// injectMemory renders the memory block for a task whose policy has memory
// on. In a group (memory.RenderBlock) it holds the group summary and the notes
// of the speaker, then of the member the current message replies to and the
// members it mentions, at most injectMaxOthers of them; in a private chat
// (memory.RenderPrivateBlock) the conversation summary and only the speaker's
// note. Others' notes leave out what they asked the bot to remember
// (memory.WithoutRemembered); only the speaker's own note shows it. Tiers are
// shown when the policy's affinity is on. The block is capped by
// memoryBlockMaxRunes under the policy's max_context_tokens (see
// memory.RenderBlock). targets and names
// are the task's reply targets and mention names; known, when not nil, holds
// a private sender's row already read. A read error is logged and leaves the
// block out.
func (o *Orchestrator) injectMemory(ctx context.Context, task store.TaskRecord, pol conloomapi.GroupPolicy,
	triggers []store.MessageRecord, targets map[string]store.MessageRecord, names map[string]string,
	known map[string]store.MemberMemory) string {
	if !pol.Memory.Enabled || !pol.Concurrency.AllowLongRequests || len(triggers) == 0 {
		return ""
	}
	private := false
	switch convType(task.ConversationKey) {
	case conloomapi.ConvGroup:
	case conloomapi.ConvPrivate:
		private = true
	default:
		return ""
	}
	warn := func(what string, err error) string {
		o.log.Warn("memory not injected: "+what+" failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		return ""
	}
	speaker := task.ActorPlatformUserID
	people := []injectPerson{{speaker, injectName(triggers[len(triggers)-1].DisplayName, speaker)}}
	if !private {
		var err error
		if people, err = o.injectPeople(ctx, task, triggers, targets, names); err != nil {
			return warn("resolve mentioned members", err)
		}
	}
	summary := ""
	switch g, err := o.st.GetGroupMemory(ctx, task.ConversationKey); {
	case err == nil:
		summary = g.Summary
	case !errors.Is(err, store.ErrNotFound):
		return warn("load group memory", err)
	}
	ids := make([]string, len(people))
	for i, p := range people {
		ids[i] = p.userID
	}
	rows := known
	if !private || rows == nil {
		var err error
		if rows, err = o.st.GetMemberMemories(ctx, task.ConversationKey, ids); err != nil {
			return warn("load member memories", err)
		}
	}
	entries := make([]memory.BlockEntry, 0, len(people))
	for _, p := range people {
		m := rows[p.userID]
		e := memory.BlockEntry{Name: p.name, Note: m.Note}
		if p.userID != speaker {
			e.Note = memory.WithoutRemembered(m.Note)
		}
		if pol.Memory.Affinity && m.Affinity != nil {
			e.Tier = memory.TierName(*m.Affinity)
		}
		entries = append(entries, e)
	}
	limit := memoryBlockMaxRunes(o.cfg.ModelProfiles, o.tokenFactor, pol.Context.MaxContextTokens)
	if private {
		return memory.RenderPrivateBlock(summary, entries[0], limit)
	}
	return memory.RenderBlock(summary, entries, limit)
}

// injectPeople returns the task's speaker, then the member the current
// message replies to (not the bot) and the members it mentions in order, each
// once and at most injectMaxOthers besides the speaker. Mentioned actor IDs
// are mapped to user IDs; mentioned members never seen in the conversation are
// left out.
func (o *Orchestrator) injectPeople(ctx context.Context, task store.TaskRecord, triggers []store.MessageRecord,
	targets map[string]store.MessageRecord, names map[string]string) ([]injectPerson, error) {
	speaker := task.ActorPlatformUserID
	people := []injectPerson{{speaker, injectName(triggers[len(triggers)-1].DisplayName, speaker)}}
	add := func(userID, name string) {
		if userID == "" || len(people) > injectMaxOthers ||
			slices.ContainsFunc(people, func(p injectPerson) bool { return p.userID == userID }) {
			return
		}
		people = append(people, injectPerson{userID, injectName(name, userID)})
	}
	for _, t := range triggers {
		if t.ReplyTo == "" {
			continue
		}
		if target, ok := targets[t.ReplyTo]; ok && !target.FromBot {
			add(target.PlatformUserID, target.DisplayName)
		}
		break
	}
	var actors []string
	for _, t := range triggers {
		for _, m := range t.Mentions {
			if m.Type == "user" && m.ID != "" && !slices.Contains(actors, m.ID) {
				actors = append(actors, m.ID)
			}
		}
	}
	if len(actors) == 0 || len(people) > injectMaxOthers {
		return people, nil
	}
	userIDs, err := o.st.UserIDsForActors(ctx, task.ConversationKey, actors)
	if err != nil {
		return nil, err
	}
	for _, a := range actors {
		add(userIDs[a], names[a])
	}
	return people, nil
}

// injectName is a member's display name made safe for the memory block, or
// unnamedMember when it is empty or only their user ID.
func injectName(name, userID string) string {
	if n := directiveName(name); n != userID {
		return n
	}
	return unnamedMember
}

// noteInteraction applies the affinity rules (memory.ApplyInteraction) to the
// sender of a group task addressed to the bot, or of a private chat task,
// whose reply was sent, when the policy has memory and affinity on. The day is the local date in o.loc; the
// sender's display name is refreshed from the newest trigger. Errors are only
// logged.
func (o *Orchestrator) noteInteraction(ctx context.Context, task store.TaskRecord, out outcome, sent bool) {
	direct := out.conv.Type == conloomapi.ConvPrivate || out.conv.Type == conloomapi.ConvGroup && out.addressed
	if !direct || !sent || out.status != conloomapi.TaskSucceeded || len(out.triggers) == 0 {
		return
	}
	pv, err := o.policies.Current(ctx, task.ConversationKey)
	if err != nil {
		o.log.Warn("affinity: load policy failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		return
	}
	if pol := pv.Policy; !pol.Memory.Enabled || !pol.Concurrency.AllowLongRequests || !pol.Memory.Affinity {
		return
	}
	day := memory.LocalDay(o.now(), o.loc)
	name := out.triggers[len(out.triggers)-1].DisplayName
	u, err := memory.UpdateMember(ctx, o.st, task.ConversationKey, task.ActorPlatformUserID, store.RevisionRule,
		func(m *store.MemberMemory) (string, bool) {
			reason := memory.ReasonDaily
			if m.Affinity == nil {
				reason = memory.ReasonFirstInteraction
			}
			next, changed := memory.ApplyInteraction(*m, day)
			if name != "" && next.DisplayName != name {
				next.DisplayName, changed = name, true
			}
			*m = next
			return reason, changed
		})
	if err != nil {
		o.log.Warn("affinity: record interaction failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		return
	}
	if u.AffinityChanged() {
		o.metrics.MemoryAffinityChanges.WithLabelValues(store.RevisionRule).Inc()
	}
}
