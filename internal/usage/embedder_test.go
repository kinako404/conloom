package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

type fakeEmbedClient struct {
	tokens int
	err    error
	delay  time.Duration
}

func (f *fakeEmbedClient) Model() string  { return "bge-m3" }
func (f *fakeEmbedClient) BatchSize() int { return 8 }

func (f *fakeEmbedClient) EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) {
	time.Sleep(f.delay)
	if f.err != nil {
		return nil, 0, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{1, 0}
	}
	return out, f.tokens, nil
}

func TestEmbedder_RecordsWithAttribution(t *testing.T) {
	s := newFakeSink()
	rec := NewRecorder(s, RecorderOptions{Batch: 200, Flush: time.Hour})
	client := &fakeEmbedClient{tokens: 7, delay: 5 * time.Millisecond}
	e := NewEmbedder(client, rec, store.PurposeEmbedQuery)
	if e.Model() != "bge-m3" || e.BatchSize() != 8 {
		t.Errorf("Model/BatchSize = %q/%d", e.Model(), e.BatchSize())
	}
	a := Attribution{BotInstanceID: "qq-main", ConversationKey: "qq-main:qq:group:10001", TaskID: "task_1"}
	ctx := WithAttribution(context.Background(), a)

	before := time.Now()
	vecs, err := e.Embed(ctx, []string{"小明", "猫"})
	if err != nil || len(vecs) != 2 {
		t.Fatalf("Embed = %v, %v", vecs, err)
	}
	if _, err := e.Embed(ctx, nil); err != nil {
		t.Fatalf("Embed(nil): %v", err)
	}
	client.err = errors.New("embedding: unexpected status 500")
	if _, err := e.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("Embed hid the client's error")
	}
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := s.all()
	if len(calls) != 2 {
		t.Fatalf("recorded %d calls, want 2 (none for no texts): %+v", len(calls), calls)
	}
	ok, failed := calls[0], calls[1]
	if ok.BotInstanceID != a.BotInstanceID || ok.ConversationKey != a.ConversationKey || ok.TaskID != a.TaskID ||
		ok.Purpose != store.PurposeEmbedQuery || ok.Role != RoleEmbedding || ok.Model != "bge-m3" || ok.PromptTokens != 7 ||
		ok.Outcome != store.OutcomeOK || ok.LatencyMS < 5 || ok.At.Before(before.Add(-time.Second)) {
		t.Errorf("ok call = %+v", ok)
	}
	if failed.Outcome != store.OutcomeError || failed.ConversationKey != "" || failed.TaskID != "" || failed.PromptTokens != 0 ||
		failed.Role != RoleEmbedding || failed.Model != "bge-m3" {
		t.Errorf("failed call = %+v", failed)
	}
}

func TestEmbedder_CanceledCall(t *testing.T) {
	s := newFakeSink()
	rec := NewRecorder(s, RecorderOptions{Batch: 200, Flush: time.Hour})
	e := NewEmbedder(&fakeEmbedClient{err: errors.New("embedding: request: context canceled")}, rec, store.PurposeEmbedIndex)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = e.Embed(ctx, []string{"x"})
	if err := rec.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls := s.all(); len(calls) != 1 || calls[0].Outcome != store.OutcomeCanceled || calls[0].Purpose != store.PurposeEmbedIndex {
		t.Errorf("calls = %+v, want one canceled embed_index call", calls)
	}
}
