// Package fakeopenai is an in-process OpenAI-compatible chat completions
// server for tests.
package fakeopenai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"unicode/utf8"
)

// Server serves POST {URL}/chat/completions. URL already ends in "/v1" and
// can be used directly as a model profile endpoint.
type Server struct {
	URL string

	mu       sync.Mutex
	requests []map[string]any
}

// New starts a server that is closed when the test ends. Each request body is
// decoded and recorded before h is called, so Calls counts in-flight requests.
// h returns the HTTP status and, for 200, the assistant content; for any
// other status the content becomes the error message. h may block (for
// example to simulate a slow model), but it must be released before the
// server is closed: register the release with t.Cleanup after calling New.
func New(t testing.TB, h func(req map[string]any) (status int, content string)) *Server {
	t.Helper()
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("fakeopenai: decode request: %v", err)
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()

		status, content := h(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status != http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": content, "type": "fake_error"},
			})
			return
		}
		promptTokens := 0
		if msgs, ok := req["messages"].([]any); ok {
			for _, m := range msgs {
				if mm, ok := m.(map[string]any); ok {
					c, _ := mm["content"].(string)
					promptTokens += utf8.RuneCountInString(c)
				}
			}
		}
		completionTokens := utf8.RuneCountInString(content)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "chatcmpl-fake",
			"object": "chat.completion",
			"model":  req["model"],
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":             promptTokens,
				"completion_tokens":         completionTokens,
				"total_tokens":              promptTokens + completionTokens,
				"completion_tokens_details": map[string]any{"reasoning_tokens": 0},
			},
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.URL = srv.URL + "/v1"
	return s
}

// Requests returns a copy of the decoded request bodies received so far.
func (s *Server) Requests() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.requests...)
}

// Calls returns the number of chat completion requests received so far.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}
