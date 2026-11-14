package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/persona"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// runCommand executes a command task and completes it with one reply action.
// The caller must own the task. It is not cancelled with ctx, so a
// disconnecting client cannot leave a command half done.
func (o *Orchestrator) runCommand(ctx context.Context, task store.TaskRecord, conv store.ConversationRecord, triggers []store.MessageRecord) {
	ctx = context.WithoutCancel(ctx)
	meta := conloomapi.TaskMeta{TraceID: ids.New("trace")}
	status, code := conloomapi.TaskSucceeded, ""
	text, err := o.execCommand(ctx, task, triggers)
	if err != nil {
		o.log.Error("command failed", "task_id", task.ID, "command", task.Command, "error", err)
		status, code, text = conloomapi.TaskFailed, conloomapi.CodeInternal, CopyUnavailable
	}
	if err := o.complete(ctx, task, status, meta, code, []store.OutboxRecord{o.newAction(ctx, task, conv, triggers, text)}); err == nil {
		o.noteBotSpoke(task.ConversationKey, o.now())
	}
	o.log.Info("command finished", "task_id", task.ID, "conversation", task.ConversationKey,
		"command", task.Command, "status", status)
}

// runStoredCommand runs an unfinished command task recovered after a
// restart. The caller must own the task.
func (o *Orchestrator) runStoredCommand(ctx context.Context, task store.TaskRecord) error {
	conv, err := o.conversation(ctx, task.ConversationKey)
	if err != nil {
		return err
	}
	triggers, err := o.st.GetMessages(ctx, task.BotInstanceID, task.TriggerMessageIDs)
	if err != nil {
		return err
	}
	o.runCommand(ctx, task, conv, triggers)
	return nil
}

func (o *Orchestrator) execCommand(ctx context.Context, task store.TaskRecord, triggers []store.MessageRecord) (string, error) {
	bot, ok := o.cfg.Bot(task.BotInstanceID)
	if !ok {
		return "", fmt.Errorf("core: unknown bot %q", task.BotInstanceID)
	}
	isAdmin := isAdmin(bot, convPlatform(task.ConversationKey), task.ActorPlatformUserID)
	by := "command:" + task.ActorPlatformUserID
	switch task.Command {
	case "help":
		pv, err := o.personas.Current(ctx, task.ConversationKey, bot.PersonaTemplate)
		if err != nil {
			return "", err
		}
		return CopyHelp(persona.BotName(pv.Persona, bot.Name)), nil
	case "persona":
		return o.personaCommand(ctx, task, bot, isAdmin, by, commandArgs(triggers))
	case "stop":
		return o.stop(ctx, task, isAdmin)
	case "pause":
		if !isAdmin {
			return CopyAdminOnly, nil
		}
		pv, err := o.policies.Pause(ctx, task.ConversationKey, by)
		if err != nil && pv.Version == 0 {
			return "", err
		}
		o.logPolicyAuditError(task, err)
		// A change already ended the session, stopped the lull timer and
		// cancelled active tasks through PolicyChanged; this also clears a
		// session, timer or tasks that started while the conversation was
		// being paused.
		o.endSession(task.ConversationKey, "")
		o.stopLull(task.ConversationKey)
		o.cancelActive(ctx, task.ConversationKey)
		return CopyPaused, nil
	case "resume":
		if !isAdmin {
			return CopyAdminOnly, nil
		}
		pv, err := o.policies.Resume(ctx, task.ConversationKey, by)
		if err != nil && pv.Version == 0 {
			return "", err
		}
		o.logPolicyAuditError(task, err)
		return CopyResumed, nil
	case "主动", "proactive":
		return o.proactiveCommand(ctx, task, isAdmin, commandArgs(triggers))
	case "好感度", "affinity", "记忆", "memory", "记住", "remember", "忘记我", "forgetme":
		return o.memCmd(ctx, task, isAdmin, task.Command, triggers)
	}
	return "", fmt.Errorf("core: unknown command %q", task.Command)
}

// personaCommand shows the current persona to anyone; admins can also list
// the persona templates ("list") and apply one ("use <name>").
func (o *Orchestrator) personaCommand(ctx context.Context, task store.TaskRecord, bot config.BotConfig, isAdmin bool, by, args string) (string, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		pv, err := o.personas.Current(ctx, task.ConversationKey, bot.PersonaTemplate)
		if err != nil {
			return "", err
		}
		return CopyPersonaInfo(pv.Persona.PersonaName, pv.Version, strings.TrimSpace(pv.Persona.CharacterName)), nil
	}
	sub := strings.ToLower(fields[0])
	if sub != "list" && sub != "use" {
		return CopyPersonaUsage, nil
	}
	if !isAdmin {
		return CopyAdminOnly, nil
	}
	names := o.personas.TemplateNames()
	switch {
	case sub == "list" && len(fields) == 1:
		return CopyPersonaTemplates(names), nil
	case sub == "use" && len(fields) == 2:
		name := fields[1]
		if !slices.Contains(names, name) {
			return CopyPersonaUnknownTemplate(name, names), nil
		}
		pv, err := o.personas.Update(ctx, task.ConversationKey, bot.PersonaTemplate,
			conloomapi.PersonaPut{Template: name, Reason: "command"}, by)
		if err != nil && pv.Version == 0 {
			return "", err
		}
		if err != nil {
			o.log.Error("persona changed but audit failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
		}
		return CopyPersonaApplied(name, pv.Version), nil
	}
	return CopyPersonaUsage, nil
}

// commandArgs returns the arguments of the command message: the trimmed text
// after its first word.
func commandArgs(triggers []store.MessageRecord) string {
	if len(triggers) == 0 {
		return ""
	}
	text := strings.TrimSpace(triggers[0].Text)
	i := strings.IndexFunc(text, unicode.IsSpace)
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(text[i:])
}

// logPolicyAuditError logs an error returned together with a stored policy
// version (an audit write failure), which does not undo the change.
func (o *Orchestrator) logPolicyAuditError(task store.TaskRecord, err error) {
	if err != nil {
		o.log.Error("policy changed but audit failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
	}
}

// stop cancels the conversation's queued and running tasks started by the
// command's sender, or every one of them when the sender is an admin. It also
// cancels the parts of the sender's last reply not handed out yet (see
// cancelStoredParts).
func (o *Orchestrator) stop(ctx context.Context, task store.TaskRecord, isAdmin bool) (string, error) {
	active := o.sched.Active(task.ConversationKey)
	var targets []QueuedTask
	for _, t := range active {
		if isAdmin || t.ActorID == task.ActorPlatformUserID {
			targets = append(targets, t)
		}
	}
	for _, t := range targets {
		if _, err := o.cancel(ctx, task.BotInstanceID, t.ConversationKey, t.TaskID); err != nil {
			return "", err
		}
	}
	parts, err := o.cancelStoredParts(ctx, task)
	if err != nil {
		return "", err
	}
	switch {
	case len(targets) > 0 || parts > 0:
		return CopyStopDone, nil
	case len(active) > 0:
		return CopyStopForbidden, nil
	}
	return CopyStopNothing, nil
}

// cancelStoredParts cancels the actions not handed out yet of the command
// sender's last reply in the conversation (see store.LastReplyTask), such as
// the later parts of a reply split into several messages, and audits them as
// outbox.stopped. It returns how many it cancelled.
func (o *Orchestrator) cancelStoredParts(ctx context.Context, task store.TaskRecord) (int, error) {
	last, err := o.st.LastReplyTask(ctx, task.BotInstanceID, task.ConversationKey, task.ActorPlatformUserID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := o.st.CancelPendingActions(ctx, last, o.now().UTC())
	if err != nil || n == 0 {
		return n, err
	}
	o.log.Info("stored reply parts stopped", "conversation", task.ConversationKey, "task_id", last, "cancelled", n)
	detail, _ := json.Marshal(struct {
		TaskID    string `json:"task_id"`
		Cancelled int    `json:"cancelled"`
	}{last, n})
	err = o.st.InsertAudit(ctx, store.AuditEvent{
		ID: ids.New("aud"), BotInstanceID: task.BotInstanceID, Kind: "outbox.stopped", Actor: "command:" + task.ActorPlatformUserID,
		Target: task.ConversationKey, Detail: string(detail), CreatedAt: o.now().UTC(),
	})
	if err != nil {
		o.log.Error("audit stopped reply parts failed", "task_id", last, "error", err)
	}
	return n, nil
}
