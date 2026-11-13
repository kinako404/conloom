package panel

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

type taskResp struct {
	ID              string    `json:"id"`
	BotInstanceID   string    `json:"bot_instance_id"`
	ConversationKey string    `json:"conversation_key"`
	Status          string    `json:"status"`
	Command         string    `json:"command"`
	Priority        int       `json:"priority"`
	Trigger         string    `json:"trigger"`
	ModelRole       string    `json:"model_role"`
	Model           string    `json:"model"`
	ErrorCode       string    `json:"error_code"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type callResp struct {
	ID               int64     `json:"id"`
	At               time.Time `json:"at"`
	BotInstanceID    string    `json:"bot_instance_id"`
	ConversationKey  string    `json:"conversation_key"`
	TaskID           string    `json:"task_id"`
	Purpose          string    `json:"purpose"`
	Role             string    `json:"role"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	ReasoningTokens  int       `json:"reasoning_tokens"`
	EstimatedTokens  int       `json:"estimated_tokens"`
	LatencyMS        int64     `json:"latency_ms"`
	Outcome          string    `json:"outcome"`
	FinishReason     string    `json:"finish_reason"`
}

// seedTask stores a task of conv with its trigger message.
func (dh *dataHarness) seedTask(conv store.ConversationRecord, id, status string, meta conloomapi.TaskMeta, at time.Time) {
	dh.t.Helper()
	dh.pmid++
	msgID := fmt.Sprintf("msg_%d", dh.pmid)
	_, err := dh.st.Ingest(context.Background(), store.Ingest{
		Event: store.EventRecord{BotInstanceID: dataBot, IdempotencyKey: fmt.Sprintf("seed:%d", dh.pmid), EventID: fmt.Sprintf("evt_%d", dh.pmid)},
		Message: &store.MessageRecord{
			ID: msgID, BotInstanceID: dataBot, ConversationKey: conv.Key, Platform: conv.Platform,
			PlatformMessageID: fmt.Sprint(dh.pmid), ActorID: "user_20001", PlatformUserID: "20001", DisplayName: "小明",
			Text: "在吗", OccurredAt: at, CreatedAt: at,
		},
		NewTask: &store.TaskRecord{
			ID: id, BotInstanceID: dataBot, ConversationKey: conv.Key, Status: status, ActorPlatformUserID: "20001",
			Command: "", TriggerMessageIDs: []string{msgID}, Priority: 1, Meta: meta, EventOccurredAt: at, CreatedAt: at, UpdatedAt: at,
		},
	})
	if err != nil {
		dh.t.Fatalf("seed task: %v", err)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func taskIDs(tasks []taskResp) []string {
	out := []string{}
	for _, t := range tasks {
		out = append(out, t.ID)
	}
	return out
}

func TestTasks_ListAndDetail(t *testing.T) {
	dh := newDataHarness(t)
	ctx := context.Background()

	if body := string(dh.get("/panel/api/tasks", nil)); body != "{\"tasks\":[],\"next_before\":null}\n" {
		t.Errorf("no tasks: body %q", body)
	}

	group := dh.seedConv("qq", conloomapi.ConvGroup, "10001")
	other := dh.seedConv("qq", conloomapi.ConvGroup, "10002")
	at := time.Date(2026, 11, 13, 7, 0, 0, 0, time.UTC)
	dh.seedTask(group, "task_01", conloomapi.TaskQueued, conloomapi.TaskMeta{}, at)
	dh.seedTask(other, "task_02", conloomapi.TaskQueued, conloomapi.TaskMeta{}, at.Add(time.Minute))
	meta := conloomapi.TaskMeta{SelectedModelRole: "chat", SelectedModel: "model-a", Trigger: "mention", TraceID: "trace_1"}
	dh.seedTask(group, "task_03", conloomapi.TaskQueued, meta, at.Add(2*time.Minute))
	actionAt := at.Add(3 * time.Minute)
	if err := dh.st.CompleteTask(ctx, store.TaskCompletion{TaskID: "task_03", Status: conloomapi.TaskSucceeded, Meta: meta,
		Actions: []store.OutboxRecord{{
			Action: conloomapi.Action{ActionID: "act_1", Type: conloomapi.ActionMessageSend, ConversationID: group.ID,
				Content: conloomapi.ActionContent{Text: "在的"}, CreatedAt: actionAt},
			BotInstanceID: dataBot, ConversationKey: group.Key, Platform: "qq", CreatedAt: actionAt, ExpiresAt: actionAt.Add(10 * time.Minute),
		}}}); err != nil {
		t.Fatal(err)
	}
	if err := dh.st.InsertModelCalls(ctx, []store.ModelCall{
		{At: actionAt, BotInstanceID: dataBot, ConversationKey: group.Key, TaskID: "task_03", Purpose: store.PurposeChat,
			Role: "chat", Model: "model-a", PromptTokens: 100, CompletionTokens: 20, LatencyMS: 900, Outcome: store.OutcomeOK, FinishReason: "stop"},
		{At: actionAt, BotInstanceID: dataBot, ConversationKey: group.Key, TaskID: "task_01", Purpose: store.PurposeChat,
			Role: "chat", Model: "model-a", Outcome: store.OutcomeError},
	}); err != nil {
		t.Fatal(err)
	}

	var page struct {
		Tasks      []taskResp `json:"tasks"`
		NextBefore *string    `json:"next_before"`
	}
	dh.get("/panel/api/tasks?limit=2", &page)
	if fmt.Sprint(taskIDs(page.Tasks)) != "[task_03 task_02]" || page.NextBefore == nil || *page.NextBefore != "task_02" {
		t.Fatalf("page 1 = %v next %v, want [task_03 task_02] next task_02", taskIDs(page.Tasks), page.NextBefore)
	}
	got := page.Tasks[0]
	if got.BotInstanceID != dataBot || got.ConversationKey != group.Key || got.Status != conloomapi.TaskSucceeded ||
		got.Priority != 1 || got.Trigger != "mention" || got.ModelRole != "chat" || got.Model != "model-a" ||
		!got.CreatedAt.Equal(at.Add(2*time.Minute)) || got.UpdatedAt.IsZero() {
		t.Errorf("task_03 = %+v", got)
	}
	dh.get("/panel/api/tasks?limit=2&before="+*page.NextBefore, &page)
	if fmt.Sprint(taskIDs(page.Tasks)) != "[task_01]" || page.NextBefore != nil {
		t.Fatalf("page 2 = %v next %v, want [task_01] and no next", taskIDs(page.Tasks), page.NextBefore)
	}
	dh.get("/panel/api/tasks?conversation="+url.QueryEscape(group.Key), &page)
	if fmt.Sprint(taskIDs(page.Tasks)) != "[task_03 task_01]" {
		t.Errorf("conversation filter = %v", taskIDs(page.Tasks))
	}
	dh.get("/panel/api/tasks?status=queued&bot="+dataBot, &page)
	if fmt.Sprint(taskIDs(page.Tasks)) != "[task_02 task_01]" {
		t.Errorf("status filter = %v", taskIDs(page.Tasks))
	}
	dh.get("/panel/api/tasks?bot=tg-main", &page)
	if len(page.Tasks) != 0 {
		t.Errorf("other bot = %v, want none", taskIDs(page.Tasks))
	}
	dh.getErr("/panel/api/tasks?limit=0", http.StatusBadRequest, conloomapi.CodeInvalidEvent)

	var detail struct {
		Task       taskResp            `json:"task"`
		Meta       conloomapi.TaskMeta `json:"meta"`
		Actions    []conloomapi.Action `json:"actions"`
		ModelCalls []callResp          `json:"model_calls"`
	}
	dh.get("/panel/api/tasks/task_03", &detail)
	if detail.Task.ID != "task_03" || detail.Task.Status != conloomapi.TaskSucceeded || detail.Meta != meta {
		t.Errorf("detail task %+v meta %+v", detail.Task, detail.Meta)
	}
	if len(detail.Actions) != 1 || detail.Actions[0].ActionID != "act_1" || detail.Actions[0].Content.Text != "在的" {
		t.Errorf("actions = %+v", detail.Actions)
	}
	if len(detail.ModelCalls) != 1 {
		t.Fatalf("model_calls = %+v, want the one call of task_03", detail.ModelCalls)
	}
	c := detail.ModelCalls[0]
	if c.ID == 0 || c.TaskID != "task_03" || c.Purpose != store.PurposeChat || c.Role != "chat" || c.Model != "model-a" ||
		c.PromptTokens != 100 || c.CompletionTokens != 20 || c.LatencyMS != 900 || c.Outcome != store.OutcomeOK ||
		c.FinishReason != "stop" || !c.At.Equal(actionAt) || c.ConversationKey != group.Key || c.BotInstanceID != dataBot {
		t.Errorf("model call = %+v", c)
	}

	if body := string(dh.get("/panel/api/tasks/task_02", nil)); !containsAll(body, `"actions":[]`, `"model_calls":[]`, `"meta":{}`) {
		t.Errorf("task without actions or calls: body %s", body)
	}
	dh.getErr("/panel/api/tasks/task_99", http.StatusNotFound, conloomapi.CodeNotFound)
}
