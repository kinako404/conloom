package app_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/adapter/mock"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func BenchmarkIngestObserve(b *testing.B) {
	fake := fakeopenai.New(b, answer(fakeReply))
	e := newEnv(b, testConfig(b, configParams{modelURL: fake.URL}))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, _, err := e.mock.Say(ctx, group, alice, "普通聊天消息", mock.SayOptions{})
			if err != nil {
				b.Error(err)
				return
			}
			if resp.Disposition != conloomapi.DispositionObserve {
				b.Errorf("response %+v, want observe", resp)
				return
			}
		}
	})
	b.StopTimer()
	if n := fake.Calls(); n != 0 {
		b.Fatalf("model calls = %d, want 0", n)
	}
}

func BenchmarkMentionToAck(b *testing.B) {
	fake := fakeopenai.New(b, answer(fakeReply))
	e := newEnv(b, testConfig(b, configParams{modelURL: fake.URL, debounce: "0s", rateLimit: 1 << 30}))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp, _, err := e.mock.Say(ctx, group, alice, "问题 "+strconv.Itoa(i), mock.SayOptions{MentionBot: true})
		if err != nil {
			b.Fatal(err)
		}
		if resp.Disposition != conloomapi.DispositionReply {
			b.Fatalf("response %+v, want reply", resp)
		}
		actions, err := e.mock.Drain(ctx, 10*time.Second)
		if err != nil || len(actions) != 1 {
			b.Fatalf("Drain = %d actions, %v; want 1", len(actions), err)
		}
	}
}
