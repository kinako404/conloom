package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/testutil/fakeopenai"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func testProfile(endpoint string) config.ModelProfile {
	return config.ModelProfile{
		Provider: "openai_compatible",
		Endpoint: endpoint,
		Model:    "m1",
		APIKey:   "k",
		Runtime: config.ProfileRuntime{
			Timeout:    conloomapi.Duration{Duration: 5 * time.Second},
			MaxRetries: 2,
		},
	}
}

func newTestClient(p config.ModelProfile) *Client {
	c := New(p, nil)
	c.backoff = time.Millisecond
	return c
}

func userRequest() ChatRequest {
	return ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "你好"}}, MaxTokens: 100}
}

func TestCleanOutput(t *testing.T) {
	cases := []struct{ in, want string }{
		{"\n\n你好", "你好"},
		{"<think>x\ny</think>答", "答"},
		{"<think>未闭合", ""},
		{"前<think>a</think>中<think>b</think>后  ", "前中后"},
		{"答案<think>没有结束\n还在想", "答案"},
		{"  普通回复 ", "普通回复"},
		{"用户在问天气…系统提示说我是织语\n</think>\n\n今天晴。", "今天晴。"},
		{"想一想</think>先<think>再想</think>后", "先后"},
		{"推理</think>假答案</think>真答案", "真答案"},
		{"<THINK>大写推理</THINK>答", "答"},
		{"<Think>混合大小写\n</Think>答", "答"},
		{"大写裸推理</THINK>答", "答"},
		{"<thinking>思考中</thinking>答", "答"},
		{"<THINKING>思考中</Thinking> 答 ", "答"},
		{"裸思考</thinking>\n答", "答"},
		{"答案<thinking>没有结束", "答案"},
		{"推理</think>答<think>没有结束", "答"},
		{"只有推理</think>", ""},
	}
	for _, c := range cases {
		if got := CleanOutput(c.in); got != c.want {
			t.Errorf("CleanOutput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestChat_SendsModelExtraBodyAndAuth(t *testing.T) {
	type captured struct {
		auth, path string
		body       map[string]any
	}
	got := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := captured{auth: r.Header.Get("Authorization"), path: r.URL.Path}
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &c.body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		got <- c
		_, _ = io.WriteString(w, `{"model":"m1","choices":[{"message":{"role":"assistant","content":"好"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	p := testProfile(srv.URL + "/v1/")
	p.ExtraBody = map[string]any{"enable_thinking": false, "stream": false, "top_k": 20}
	resp, err := newTestClient(p).Chat(context.Background(), userRequest())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "好" {
		t.Errorf("Content = %q", resp.Content)
	}
	c := <-got
	gotAuth, gotPath, gotBody := c.auth, c.path, c.body
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer k" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "m1" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(100) {
		t.Errorf("max_tokens = %v", gotBody["max_tokens"])
	}
	if gotBody["enable_thinking"] != false {
		t.Errorf("enable_thinking = %v", gotBody["enable_thinking"])
	}
	if gotBody["top_k"] != float64(20) {
		t.Errorf("top_k = %v", gotBody["top_k"])
	}
	if gotBody["stream"] != false {
		t.Errorf("stream = %v", gotBody["stream"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", gotBody["messages"])
	}
	m, _ := msgs[0].(map[string]any)
	if m["role"] != "user" || m["content"] != "你好" {
		t.Errorf("message = %v", m)
	}
}

func TestChat_ExtraBodyOverridesBaseKeys(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return http.StatusOK, "好" })
	p := testProfile(fake.URL)
	p.ExtraBody = map[string]any{"model": "m2"}
	if _, err := newTestClient(p).Chat(context.Background(), userRequest()); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := fake.Requests()[0]["model"]; got != "m2" {
		t.Errorf("model = %v, want m2", got)
	}
}

func TestChat_RetriesOn5xxThenSucceeds(t *testing.T) {
	var n atomic.Int32
	fake := fakeopenai.New(t, func(map[string]any) (int, string) {
		if n.Add(1) <= 2 {
			return http.StatusBadGateway, "upstream down"
		}
		return http.StatusOK, "好的"
	})
	resp, err := newTestClient(testProfile(fake.URL)).Chat(context.Background(), userRequest())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "好的" {
		t.Errorf("Content = %q", resp.Content)
	}
	if fake.Calls() != 3 {
		t.Errorf("Calls() = %d, want 3", fake.Calls())
	}
}

func TestChat_RetriesOn429(t *testing.T) {
	var n atomic.Int32
	fake := fakeopenai.New(t, func(map[string]any) (int, string) {
		if n.Add(1) == 1 {
			return http.StatusTooManyRequests, "slow down"
		}
		return http.StatusOK, "好"
	})
	if _, err := newTestClient(testProfile(fake.URL)).Chat(context.Background(), userRequest()); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if fake.Calls() != 2 {
		t.Errorf("Calls() = %d, want 2", fake.Calls())
	}
}

func TestChat_GivesUpAfterMaxRetries(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return http.StatusServiceUnavailable, "busy" })
	_, err := newTestClient(testProfile(fake.URL)).Chat(context.Background(), userRequest())
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want StatusError 503", err)
	}
	if fake.Calls() != 3 {
		t.Errorf("Calls() = %d, want 3 (1 + MaxRetries)", fake.Calls())
	}
}

func TestChat_NoRetryOn400(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return http.StatusBadRequest, "bad" })
	_, err := newTestClient(testProfile(fake.URL)).Chat(context.Background(), userRequest())
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if se.Code != http.StatusBadRequest {
		t.Errorf("Code = %d, want 400", se.Code)
	}
	if fake.Calls() != 1 {
		t.Errorf("Calls() = %d, want 1", fake.Calls())
	}
}

func TestChat_StatusErrorBodyTruncated(t *testing.T) {
	big := make([]byte, 2000)
	for i := range big {
		big[i] = 'x'
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(big)
	}))
	defer srv.Close()
	_, err := newTestClient(testProfile(srv.URL)).Chat(context.Background(), userRequest())
	var se *StatusError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v, want *StatusError", err)
	}
	if len(se.Body) != 512 {
		t.Errorf("len(Body) = %d, want 512", len(se.Body))
	}
}

func TestChat_NoRetryOnAttemptTimeout(t *testing.T) {
	release := make(chan struct{})
	var n atomic.Int32
	fake := fakeopenai.New(t, func(map[string]any) (int, string) {
		if n.Add(1) == 1 {
			<-release
		}
		return http.StatusOK, "好"
	})
	t.Cleanup(func() { close(release) })
	p := testProfile(fake.URL)
	p.Runtime.Timeout = conloomapi.Duration{Duration: 100 * time.Millisecond}
	_, err := newTestClient(p).Chat(context.Background(), userRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if fake.Calls() != 1 {
		t.Errorf("Calls() = %d, want 1 (attempt timeout is not retried)", fake.Calls())
	}
}

func TestChat_NoRetryOnAttemptTimeoutDuringBody(t *testing.T) {
	release := make(chan struct{})
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"choices":[`)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	p := testProfile(srv.URL)
	p.Runtime.Timeout = conloomapi.Duration{Duration: 100 * time.Millisecond}
	_, err := newTestClient(p).Chat(context.Background(), userRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if got := n.Load(); got != 1 {
		t.Errorf("calls = %d, want 1 (attempt timeout is not retried)", got)
	}
}

func TestChat_RetriesOnConnectionError(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"model":"m1","choices":[{"message":{"role":"assistant","content":"好"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(srv.Close)
	resp, err := newTestClient(testProfile(srv.URL)).Chat(context.Background(), userRequest())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "好" || n.Load() != 2 {
		t.Errorf("Content = %q, calls = %d; want 好, 2", resp.Content, n.Load())
	}
}

func TestChat_ParentDeadlineReturnsContextError(t *testing.T) {
	release := make(chan struct{})
	fake := fakeopenai.New(t, func(map[string]any) (int, string) {
		<-release
		return http.StatusOK, "好"
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := newTestClient(testProfile(fake.URL)).Chat(ctx, userRequest())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if fake.Calls() != 1 {
		t.Errorf("Calls() = %d, want 1", fake.Calls())
	}
}

func TestChat_CanceledContextStopsRetrying(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return http.StatusBadGateway, "down" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTestClient(testProfile(fake.URL)).Chat(ctx, userRequest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if fake.Calls() != 0 {
		t.Errorf("Calls() = %d, want 0", fake.Calls())
	}
}

func TestChat_EmptyAfterCleanIsError(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return http.StatusOK, "<think>只有推理</think>" })
	_, err := newTestClient(testProfile(fake.URL)).Chat(context.Background(), userRequest())
	if !errors.Is(err, ErrEmptyOutput) {
		t.Fatalf("err = %v, want ErrEmptyOutput", err)
	}
	if fake.Calls() != 1 {
		t.Errorf("Calls() = %d, want 1 (empty output is not retried)", fake.Calls())
	}
}

func TestChat_ParsesReasoningTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"model": "m1-served",
			"choices": [{"index":0,"message":{"role":"assistant","content":"<think>想</think>\n答"},"finish_reason":"length"}],
			"usage": {"prompt_tokens": 120, "completion_tokens": 45, "total_tokens": 165,
			          "completion_tokens_details": {"reasoning_tokens": 30}}
		}`)
	}))
	defer srv.Close()
	resp, err := newTestClient(testProfile(srv.URL)).Chat(context.Background(), userRequest())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	want := ChatResponse{Content: "答", Model: "m1-served", FinishReason: "length", PromptTokens: 120, CompletionTokens: 45, ReasoningTokens: 30}
	if !reflect.DeepEqual(resp, want) {
		t.Errorf("resp = %+v, want %+v", resp, want)
	}
}

func TestChat_FakeServerUsage(t *testing.T) {
	fake := fakeopenai.New(t, func(map[string]any) (int, string) { return http.StatusOK, "好的呀" })
	resp, err := newTestClient(testProfile(fake.URL)).Chat(context.Background(), userRequest())
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Model != "m1" || resp.FinishReason != "stop" || resp.PromptTokens != 2 || resp.CompletionTokens != 3 {
		t.Errorf("resp = %+v", resp)
	}
}
