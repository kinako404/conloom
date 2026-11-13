package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/internal/usage"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// callSink collects the model calls a Recorder writes.
type callSink struct {
	mu    sync.Mutex
	calls []store.ModelCall
}

func (s *callSink) InsertModelCalls(_ context.Context, calls []store.ModelCall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, calls...)
	return nil
}

// recordUsage makes env's orchestrator record model calls; the returned
// function closes the recorder and returns what it wrote.
func recordUsage(t *testing.T, env *testEnv) func() []store.ModelCall {
	t.Helper()
	sink := &callSink{}
	rec := usage.NewRecorder(sink, usage.RecorderOptions{Flush: time.Hour})
	env.o.usage = rec
	return func() []store.ModelCall {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := rec.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return append([]store.ModelCall(nil), sink.calls...)
	}
}

func TestRunner_RecordsModelCalls(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{})
	written := recordUsage(t, env)
	before := time.Now()
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "在吗", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	calls := written()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.BotInstanceID != testBot || c.ConversationKey != testConvKey || c.TaskID != task.TaskID ||
		c.Purpose != store.PurposeChat || c.Role != "short_context" || c.Model != "short-model" ||
		c.Outcome != store.OutcomeOK || c.FinishReason != "stop" {
		t.Errorf("call = %+v", c)
	}
	if c.PromptTokens <= 0 || c.CompletionTokens != utf8.RuneCountInString(fakeReply) || c.EstimatedTokens <= 0 {
		t.Errorf("tokens = prompt %d, completion %d, estimated %d", c.PromptTokens, c.CompletionTokens, c.EstimatedTokens)
	}
	if c.At.Before(before.Add(-time.Second)) || c.At.After(time.Now()) || c.LatencyMS < 0 {
		t.Errorf("at %v, latency %dms", c.At, c.LatencyMS)
	}
}

func TestRunner_RecordsFailedModelCalls(t *testing.T) {
	env := newTestOrchestrator(t, testOpts{short: func(map[string]any) (int, string) { return 500, "upstream exploded" }})
	written := recordUsage(t, env)
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "在吗", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v, want the long model's answer", task)
	}
	calls := written()
	if len(calls) != 2 {
		t.Fatalf("recorded %d calls, want the failed short and the long call: %+v", len(calls), calls)
	}
	failed, ok := calls[0], calls[1]
	if failed.Outcome != store.OutcomeError || failed.Model != "short-model" || failed.Role != "short_context" ||
		failed.TaskID != task.TaskID || failed.ConversationKey != testConvKey || failed.PromptTokens != 0 || failed.EstimatedTokens <= 0 {
		t.Errorf("failed call = %+v", failed)
	}
	if ok.Outcome != store.OutcomeOK || ok.Model != "long-model" || ok.Role != "long_context" || ok.TaskID != task.TaskID {
		t.Errorf("fallback call = %+v", ok)
	}
}

// attrTool is the now tool, remembering the attribution of the ctx it is
// called with.
type attrTool struct {
	mu  sync.Mutex
	got []usage.Attribution
}

func (a *attrTool) Spec() skills.Spec {
	return skills.Spec{Name: "now", Description: "fake now", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
}

func (a *attrTool) Call(ctx context.Context, _ skills.Invocation, _ json.RawMessage) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, usage.AttributionFrom(ctx))
	return nowResult, nil
}

func TestRunner_ToolCtxCarriesAttribution(t *testing.T) {
	tool := &attrTool{}
	env := skillEnv(t, testOpts{shortTools: callOnceThen("现在是下午四点半。", fakeopenai.ToolCall{ID: "call_1", Name: "now", Arguments: "{}"})},
		[]string{"time"}, tool)
	written := recordUsage(t, env)
	task := env.waitTask(t, env.submit(t, env.groupEvent("20001", "现在几点", mentionBot)).TaskID)
	if task.Status != conloomapi.TaskSucceeded {
		t.Fatalf("task = %+v", task)
	}
	tool.mu.Lock()
	got := tool.got
	tool.mu.Unlock()
	want := usage.Attribution{BotInstanceID: testBot, ConversationKey: testConvKey, TaskID: task.TaskID}
	if len(got) != 1 || got[0] != want {
		t.Errorf("tool saw attribution %+v, want [%+v]", got, want)
	}
	if calls := written(); len(calls) != 2 || calls[0].TaskID != task.TaskID || calls[1].TaskID != task.TaskID {
		t.Errorf("calls = %+v, want one per round of the task", calls)
	}
}
