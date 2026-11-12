package core

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// injectMaxOthers bounds the members besides the speaker shown in the memory
// block.
const injectMaxOthers = 2

// injectPerson is a member whose memory may be shown, with the name to show.
type injectPerson struct{ userID, name string }

// injectMemory renders the memory block (see memory.RenderBlock) for a group
// task whose policy has memory on: the group summary and the notes of the
// speaker, then of the member the current message replies to and the members
// it mentions, at most injectMaxOthers of them. Tiers are shown when the
// policy's affinity is on. targets and names are the task's reply targets and
// mention names. A read error is logged and leaves the block out.
func (o *Orchestrator) injectMemory(ctx context.Context, task store.TaskRecord, pol conloomapi.GroupPolicy,
	triggers []store.MessageRecord, targets map[string]store.MessageRecord, names map[string]string) string {
	if !pol.Memory.Enabled || !pol.Concurrency.AllowLongRequests || len(triggers) == 0 {
		return ""
	}
	if parts := strings.SplitN(task.ConversationKey, ":", 4); len(parts) != 4 || parts[2] != conloomapi.ConvGroup {
		return ""
	}
	warn := func(what string, err error) string {
		o.log.Warn("memory not injected: "+what+" failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		return ""
	}
	people, err := o.injectPeople(ctx, task, triggers, targets, names)
	if err != nil {
		return warn("resolve mentioned members", err)
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
	rows, err := o.st.GetMemberMemories(ctx, task.ConversationKey, ids)
	if err != nil {
		return warn("load member memories", err)
	}
	entries := make([]memory.BlockEntry, 0, len(people))
	for _, p := range people {
		m := rows[p.userID]
		e := memory.BlockEntry{Name: p.name, Note: m.Note}
		if pol.Memory.Affinity && m.Affinity != nil {
			e.Tier = memory.TierName(*m.Affinity)
		}
		entries = append(entries, e)
	}
	return memory.RenderBlock(summary, entries)
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
// sender of a group task addressed to the bot whose reply was sent, when the
// policy has memory and affinity on. The day is the local date in o.loc; the
// sender's display name is refreshed from the newest trigger. Errors are only
// logged.
func (o *Orchestrator) noteInteraction(ctx context.Context, task store.TaskRecord, out outcome, sent bool) {
	if out.conv.Type != conloomapi.ConvGroup || !out.addressed || !sent || out.status != conloomapi.TaskSucceeded || len(out.triggers) == 0 {
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
