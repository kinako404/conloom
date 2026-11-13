package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/provider/openai"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/internal/usage"
)

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

func (e *consEnv) recordUsage(t *testing.T) func() []store.ModelCall {
	t.Helper()
	sink := &callSink{}
	rec := usage.NewRecorder(sink, usage.RecorderOptions{Flush: time.Hour})
	e.c.d.Usage = rec
	return func() []store.ModelCall {
		t.Helper()
		if err := rec.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return append([]store.ModelCall(nil), sink.calls...)
	}
}

func TestConsolidate_RecordsMemoryCall(t *testing.T) {
	e := newConsEnv(t, consOpts{})
	written := e.recordUsage(t)
	consSeedThree(t, e)
	e.long.setReply(func(int, openai.ChatRequest) (openai.ChatResponse, error) {
		return openai.ChatResponse{Content: consReplyBoth, Model: "fake-long-v2", PromptTokens: 100, CompletionTokens: 20,
			ReasoningTokens: 5, FinishReason: "stop"}, nil
	})
	e.run(t, false)

	calls := written()
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1: %+v", len(calls), calls)
	}
	c := calls[0]
	if c.BotInstanceID != "bot_test" || c.ConversationKey != testConvKey || c.TaskID != "" || c.Purpose != store.PurposeMemory ||
		c.Role != "long_context" || c.Model != "fake-long-v2" || c.PromptTokens != 100 || c.CompletionTokens != 20 ||
		c.ReasoningTokens != 5 || c.EstimatedTokens != 0 || c.Outcome != store.OutcomeOK || c.FinishReason != "stop" || c.At.IsZero() {
		t.Errorf("call = %+v", c)
	}
}

func TestConsolidate_RecordsFailedMemoryCall(t *testing.T) {
	e := newConsEnv(t, consOpts{})
	written := e.recordUsage(t)
	consSeedThree(t, e)
	e.long.setReply(func(int, openai.ChatRequest) (openai.ChatResponse, error) {
		return openai.ChatResponse{}, errors.New("upstream 500")
	})
	if _, err := e.c.Run(context.Background(), e.convKey, false); err == nil {
		t.Fatal("Run succeeded, want the model error")
	}
	calls := written()
	if len(calls) != 1 || calls[0].Outcome != store.OutcomeError || calls[0].Model != "fake" || calls[0].Purpose != store.PurposeMemory {
		t.Errorf("calls = %+v, want one failed memory call of the configured model", calls)
	}
}
