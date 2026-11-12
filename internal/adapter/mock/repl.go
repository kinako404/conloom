package mock

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	replyWait  = 60 * time.Second
	drainSlice = time.Second
)

// RunREPL sends each non-empty line read from in as a message from user in
// conv; a line starting with "@" mentions the bot. After a line that starts a
// reply task it delivers the bot's actions, printing each as
// "<bot name>> <text>", until that task has finished and its replies were
// printed, or 60s have passed. It returns at EOF or when ctx is done.
//
// Message numbering starts at the current Unix time in milliseconds, so
// separate sessions against the same database never reuse message IDs or
// idempotency keys.
func RunREPL(ctx context.Context, m *Mock, conv Conv, user User, in io.Reader, out io.Writer) error {
	caps, err := m.c.Capabilities(ctx)
	if err != nil {
		return fmt.Errorf("mock: capabilities: %w", err)
	}
	if m.msgSeq.Load() == 0 && m.botSeq.Load() == 0 {
		base := time.Now().UnixMilli()
		m.msgSeq.Store(base)
		m.botSeq.Store(base)
	}

	lines := make(chan string)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				readErr <- ctx.Err()
				return
			}
		}
		readErr <- sc.Err()
	}()

	for {
		var line string
		select {
		case <-ctx.Done():
			return ctx.Err()
		case l, ok := <-lines:
			if !ok {
				return <-readErr
			}
			line = strings.TrimSpace(l)
		}
		if line == "" {
			continue
		}
		var o SayOptions
		if rest, ok := strings.CutPrefix(line, "@"); ok {
			o.MentionBot = true
			line = strings.TrimSpace(rest)
		}
		resp, _, err := m.Say(ctx, conv, user, line, o)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(out, "（发送失败：%v）\n", err)
			continue
		}
		if resp.Disposition != conloomapi.DispositionReply || resp.TaskID == "" {
			if resp.SuppressedReason != "" {
				fmt.Fprintf(out, "（不回复：%s）\n", resp.SuppressedReason)
			}
			continue
		}
		if err := m.awaitReply(ctx, resp.TaskID, caps.BotName, out); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			fmt.Fprintf(out, "（接收回复失败：%v）\n", err)
		}
	}
}

// awaitReply delivers and prints actions until task taskID has finished and
// all of its actions were printed, or replyWait has passed.
func (m *Mock) awaitReply(ctx context.Context, taskID, botName string, out io.Writer) error {
	deadline := time.Now().Add(replyWait)
	printed := map[string]bool{}
	for {
		actions, err := m.Drain(ctx, min(max(time.Until(deadline), 0), drainSlice))
		for _, a := range actions {
			fmt.Fprintf(out, "%s> %s\n", botName, a.Content.Text)
			printed[a.ActionID] = true
		}
		if err != nil {
			return err
		}
		task, err := m.c.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if finished(task, printed) {
			if len(task.Actions) == 0 {
				reason := task.Status
				if task.Error != nil {
					reason += " " + task.Error.Code
				}
				fmt.Fprintf(out, "（没有回复：%s）\n", reason)
			}
			return nil
		}
		if !time.Now().Before(deadline) {
			fmt.Fprintln(out, "（等待回复超时）")
			return nil
		}
	}
}

func finished(task conloomapi.Task, printed map[string]bool) bool {
	switch task.Status {
	case conloomapi.TaskSucceeded, conloomapi.TaskFailed, conloomapi.TaskCancelled:
	default:
		return false
	}
	for _, a := range task.Actions {
		if !printed[a.ActionID] {
			return false
		}
	}
	return true
}
