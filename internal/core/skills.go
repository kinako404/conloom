package core

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// maxToolRounds is how many rounds of tool calls a reply may make; the
	// request after the last round forbids further calls.
	maxToolRounds = 3
	// maxCallsPerRound tool calls of one response are run; the rest are
	// answered with copyTooManyCalls.
	maxCallsPerRound   = 4
	maxToolResultRunes = 2000
	toolCallTimeout    = 10 * time.Second

	copyToolFailed   = "调用失败。"
	copyTooManyCalls = "本轮调用太多。"

	// unknownToolLabel names, in metrics and logs, a tool the model made up.
	unknownToolLabel = "unknown"
)

// Skill call results, the result label of conloom_skill_calls_total.
const (
	skillCallOK      = "ok"
	skillCallError   = "error"
	skillCallUnknown = "unknown"
	skillCallInvalid = "invalid"
)

// skillRun is one task's use of skills: the toolset offered to the model, who
// the calls act for, and the results of the calls made so far, so a re-routed
// loop does not run a call twice.
type skillRun struct {
	set    skills.Toolset
	tools  []openai.Tool
	schema string
	inv    skills.Invocation
	done   map[string]string
	counts map[string]int
}

// newSkillRun returns the skills of a private chat or a group message
// addressed to the bot, or nil when the bot enables none for the
// conversation's type or the task is a turn or a join.
func (o *Orchestrator) newSkillRun(task store.TaskRecord, bot config.BotConfig, conv store.ConversationRecord,
	triggers []store.MessageRecord, out *outcome) *skillRun {
	if o.skills == nil || len(bot.Skills) == 0 {
		return nil
	}
	switch {
	case conv.Type == conloomapi.ConvPrivate && !out.proactive && !out.turn:
	case conv.Type == conloomapi.ConvGroup && out.addressed:
	default:
		return nil
	}
	set := o.skills.For(bot.Skills, conv.Type)
	if set.Empty() {
		return nil
	}
	run := &skillRun{set: set, done: map[string]string{}, counts: map[string]int{}}
	for _, t := range set.Tools {
		spec := t.Spec()
		run.tools = append(run.tools, openai.Tool{Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters})
	}
	schema, _ := json.Marshal(run.tools)
	run.schema = string(schema)
	run.inv = skills.Invocation{
		TaskID:           task.ID,
		BotInstanceID:    task.BotInstanceID,
		ConversationKey:  task.ConversationKey,
		ConversationType: conv.Type,
		PlatformUserID:   task.ActorPlatformUserID,
		DisplayName:      triggers[len(triggers)-1].DisplayName,
		IsAdmin:          slices.Contains(bot.Admins, task.ActorPlatformUserID),
		Location:         o.loc,
	}
	return run
}

// runTools answers the calls of one response in order: the first
// maxCallsPerRound are run, the rest are refused.
func (o *Orchestrator) runTools(ctx context.Context, task store.TaskRecord, run *skillRun, calls []openai.ToolCall) []openai.ChatMessage {
	msgs := make([]openai.ChatMessage, len(calls))
	for i, c := range calls {
		text := copyTooManyCalls
		if i < maxCallsPerRound {
			text = o.callTool(ctx, task, run, c)
		}
		msgs[i] = openai.ChatMessage{Role: "tool", ToolCallID: c.ID, Content: text}
	}
	return msgs
}

// callTool runs one call, or returns the result of the same call made earlier
// in the task, and records it. Neither the arguments nor the result are
// logged.
func (o *Orchestrator) callTool(ctx context.Context, task store.TaskRecord, run *skillRun, c openai.ToolCall) string {
	key := c.Name + "\x00" + c.Arguments
	if text, ok := run.done[key]; ok {
		return text
	}
	start := time.Now()
	label, result, text := o.execTool(ctx, run, c)
	o.metrics.SkillCalls.WithLabelValues(label, result).Inc()
	o.log.Info("skill call", "task_id", task.ID, "tool", label, "duration_ms", time.Since(start).Milliseconds(),
		"ok", result == skillCallOK, "result", result)
	run.counts[label]++
	run.done[key] = text
	return text
}

// execTool runs c and returns the tool's metric label, the call's result and
// the text for the model. Any failure is copyToolFailed.
func (o *Orchestrator) execTool(ctx context.Context, run *skillRun, c openai.ToolCall) (label, result, text string) {
	tool, ok := run.set.Lookup(c.Name)
	if !ok {
		return unknownToolLabel, skillCallUnknown, copyToolFailed
	}
	args := strings.TrimSpace(c.Arguments)
	if args == "" {
		args = "{}"
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &obj); err != nil || obj == nil {
		return c.Name, skillCallInvalid, copyToolFailed
	}
	inv := run.inv
	inv.Now = o.now()
	cctx, cancel := context.WithTimeout(ctx, toolCallTimeout)
	defer cancel()
	out, err := safeCall(cctx, tool, inv, json.RawMessage(args))
	if err != nil {
		return c.Name, skillCallError, copyToolFailed
	}
	return c.Name, skillCallOK, truncateRunes(out, maxToolResultRunes)
}

// safeCall turns a panicking tool into a failed call.
func safeCall(ctx context.Context, tool skills.Tool, inv skills.Invocation, args json.RawMessage) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool panicked: %v", r)
		}
	}()
	return tool.Call(ctx, inv, args)
}

func truncateRunes(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// skillCallSummary formats per-tool call counts as "now:1,remember:2".
func skillCallSummary(counts map[string]int) string {
	parts := make([]string, 0, len(counts))
	for _, name := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, name+":"+strconv.Itoa(counts[name]))
	}
	return strings.Join(parts, ",")
}
