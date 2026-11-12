package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/kinako404/conloom/internal/ids"
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
	isAdmin := slices.Contains(bot.Admins, task.ActorPlatformUserID)
	by := "command:" + task.ActorPlatformUserID
	switch task.Command {
	case "help":
		return CopyHelp(bot.Name), nil
	case "persona":
		pv, err := o.personas.Current(ctx, task.ConversationKey, bot.PersonaTemplate)
		if err != nil {
			return "", err
		}
		return CopyPersonaInfo(pv.Persona.PersonaName, pv.Version), nil
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
	}
	return "", fmt.Errorf("core: unknown command %q", task.Command)
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
// command's sender, or every one of them when the sender is an admin.
func (o *Orchestrator) stop(ctx context.Context, task store.TaskRecord, isAdmin bool) (string, error) {
	active := o.sched.Active(task.ConversationKey)
	if len(active) == 0 {
		return CopyStopNothing, nil
	}
	var targets []QueuedTask
	for _, t := range active {
		if isAdmin || t.ActorID == task.ActorPlatformUserID {
			targets = append(targets, t)
		}
	}
	if len(targets) == 0 {
		return CopyStopForbidden, nil
	}
	for _, t := range targets {
		if _, err := o.cancel(ctx, task.BotInstanceID, t.ConversationKey, t.TaskID); err != nil {
			return "", err
		}
	}
	return CopyStopDone, nil
}
