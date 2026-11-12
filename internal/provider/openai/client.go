// Package openai is a minimal client for OpenAI-compatible chat completions
// endpoints.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/config"
)

const (
	defaultBackoff   = 500 * time.Millisecond
	maxErrorBody     = 512
	maxResponseBytes = 16 << 20
)

// ErrEmptyOutput is returned when the model reply is empty after CleanOutput.
var ErrEmptyOutput = errors.New("openai: empty model output")

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Messages  []ChatMessage
	MaxTokens int
}

type ChatResponse struct {
	Content          string
	Model            string
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
}

// StatusError reports a non-2xx response. Body holds at most the first 512
// bytes of the response body.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("openai: unexpected status %d: %s", e.Code, e.Body)
}

type Client struct {
	profile config.ModelProfile
	hc      *http.Client
	url     string
	backoff time.Duration
}

// New returns a client for profile p. A nil hc uses http.DefaultClient.
func New(p config.ModelProfile, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{
		profile: p,
		hc:      hc,
		url:     strings.TrimRight(p.Endpoint, "/") + "/chat/completions",
		backoff: defaultBackoff,
	}
}

// Chat sends a non-streaming chat completion request. Each attempt is bounded
// by Runtime.Timeout; network errors, 429 and 5xx responses are retried up to
// Runtime.MaxRetries times with exponential backoff starting at 500ms.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	body, err := c.requestBody(req)
	if err != nil {
		return ChatResponse{}, err
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return ChatResponse{}, err
		}
		resp, retryable, err := c.attempt(ctx, body)
		if err == nil {
			return resp, nil
		}
		if !retryable || ctx.Err() != nil || attempt >= c.profile.Runtime.MaxRetries {
			return ChatResponse{}, err
		}
		t := time.NewTimer(c.backoff << attempt)
		select {
		case <-ctx.Done():
			t.Stop()
			return ChatResponse{}, ctx.Err()
		case <-t.C:
		}
	}
}

func (c *Client) requestBody(req ChatRequest) ([]byte, error) {
	m := map[string]any{
		"model":    c.profile.Model,
		"messages": req.Messages,
		"stream":   false,
	}
	if req.MaxTokens > 0 {
		m["max_tokens"] = req.MaxTokens
	}
	maps.Copy(m, c.profile.ExtraBody)
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	return b, nil
}

type completionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens            int `json:"prompt_tokens"`
		CompletionTokens        int `json:"completion_tokens"`
		CompletionTokensDetails struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
}

func (c *Client) attempt(ctx context.Context, body []byte) (ChatResponse, bool, error) {
	if d := c.profile.Runtime.Timeout.Duration; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, false, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.profile.APIKey)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return ChatResponse{}, true, fmt.Errorf("openai: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return ChatResponse{}, true, fmt.Errorf("openai: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if len(raw) > maxErrorBody {
			raw = raw[:maxErrorBody]
		}
		retryable := resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return ChatResponse{}, retryable, &StatusError{Code: resp.StatusCode, Body: string(raw)}
	}

	var cr completionResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return ChatResponse{}, false, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return ChatResponse{}, false, fmt.Errorf("%w: no choices", ErrEmptyOutput)
	}
	out := ChatResponse{
		Content:          CleanOutput(cr.Choices[0].Message.Content),
		Model:            cr.Model,
		FinishReason:     cr.Choices[0].FinishReason,
		PromptTokens:     cr.Usage.PromptTokens,
		CompletionTokens: cr.Usage.CompletionTokens,
		ReasoningTokens:  cr.Usage.CompletionTokensDetails.ReasoningTokens,
	}
	if out.Model == "" {
		out.Model = c.profile.Model
	}
	if out.Content == "" {
		return ChatResponse{}, false, ErrEmptyOutput
	}
	return out, false, nil
}
