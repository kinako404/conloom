package panel

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// taskView is a task; Trigger, ModelRole and Model come from its meta.
type taskView struct {
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

func newTaskView(t store.TaskRecord) taskView {
	return taskView{
		ID: t.ID, BotInstanceID: t.BotInstanceID, ConversationKey: t.ConversationKey, Status: t.Status,
		Command: t.Command, Priority: t.Priority, Trigger: t.Meta.Trigger, ModelRole: t.Meta.SelectedModelRole,
		Model: t.Meta.SelectedModel, ErrorCode: t.ErrorCode, CreatedAt: t.CreatedAt.UTC(), UpdatedAt: t.UpdatedAt.UTC(),
	}
}

type modelCallView struct {
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

func newModelCallView(c store.ModelCall) modelCallView {
	return modelCallView{
		ID: c.ID, At: c.At.UTC(), BotInstanceID: c.BotInstanceID, ConversationKey: c.ConversationKey, TaskID: c.TaskID,
		Purpose: c.Purpose, Role: c.Role, Model: c.Model, PromptTokens: c.PromptTokens,
		CompletionTokens: c.CompletionTokens, ReasoningTokens: c.ReasoningTokens, EstimatedTokens: c.EstimatedTokens,
		LatencyMS: c.LatencyMS, Outcome: c.Outcome, FinishReason: c.FinishReason,
	}
}

// listTasks pages through tasks newest first, filtered by status, bot and
// conversation key.
func (s *Server) listTasks(w http.ResponseWriter, r *http.Request, _ *Session) {
	limit, err := pageLimit(r)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, err.Error())
		return
	}
	q := r.URL.Query()
	tasks, err := s.d.Store.ListTasks(r.Context(), store.TaskFilter{
		Status: q.Get("status"), BotInstanceID: q.Get("bot"), ConversationKey: q.Get("conversation"),
		Before: q.Get("before"), Limit: limit,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]taskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, newTaskView(t))
	}
	var next *string
	if len(tasks) == limit {
		next = &tasks[len(tasks)-1].ID
	}
	s.writeJSON(w, http.StatusOK, struct {
		Tasks      []taskView `json:"tasks"`
		NextBefore *string    `json:"next_before"`
	}{out, next})
}

// maxTaskCalls bounds the model calls listed with a task.
const maxTaskCalls = maxPageLimit

// getTask returns a task of a configured bot with its actions and model
// calls (newest first).
func (s *Server) getTask(w http.ResponseWriter, r *http.Request, _ *Session) {
	id := r.PathValue("id")
	task, err := s.findTask(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	actions, err := s.d.Store.ActionsForTask(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if actions == nil {
		actions = []conloomapi.Action{}
	}
	calls, err := s.d.Store.ListModelCalls(r.Context(), store.ModelCallFilter{TaskID: id, Limit: maxTaskCalls})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	callViews := make([]modelCallView, 0, len(calls))
	for _, c := range calls {
		callViews = append(callViews, newModelCallView(c))
	}
	s.writeJSON(w, http.StatusOK, struct {
		Task       taskView            `json:"task"`
		Meta       conloomapi.TaskMeta `json:"meta"`
		Actions    []conloomapi.Action `json:"actions"`
		ModelCalls []modelCallView     `json:"model_calls"`
	}{newTaskView(task), task.Meta, actions, callViews})
}

// findTask looks the task up among the configured bots' tasks; tasks are
// stored per bot.
func (s *Server) findTask(ctx context.Context, id string) (store.TaskRecord, error) {
	for _, b := range s.d.Config.Bots {
		t, err := s.d.Store.GetTask(ctx, b.ID, id)
		if !errors.Is(err, store.ErrNotFound) {
			return t, err
		}
	}
	return store.TaskRecord{}, store.ErrNotFound
}
