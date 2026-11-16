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
	"regexp"
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

// ChatMessage is one message of the conversation. An assistant message that
// calls tools carries ToolCalls (its Content may be empty); a "tool" message
// answers the call ToolCallID. Images are data URLs sent before Content as
// image parts; without them the message is sent with plain text content.
type ChatMessage struct {
	Role       string
	Content    string
	Images     []string
	ToolCalls  []ToolCall
	ToolCallID string
}

// ChatRequest is a chat completion request. Tools, when set, are offered to
// the model; ToolChoice (e.g. "none") is sent only together with Tools.
// ExtraBody is merged over the profile's extra_body for this request only:
// where both have a JSON object under a key, the two are merged key by key in
// turn, so a request can change one key of e.g. chat_template_kwargs and keep
// the profile's others; the profile's own map is never changed.
type ChatRequest struct {
	Messages   []ChatMessage
	MaxTokens  int
	Tools      []Tool
	ToolChoice string
	ExtraBody  map[string]any
}

// ChatResponse is the model's answer: its cleaned Content and the ToolCalls it
// requested. Content may be empty only when ToolCalls is not.
type ChatResponse struct {
	Content          string
	ToolCalls        []ToolCall
	Model            string
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
	ReasoningTokens  int
}

// StatusError reports a non-2xx response. Body holds at most the first 512
// bytes of the response body after omitBase64, so an echoed image never
// reaches the logs.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("openai: unexpected status %d: %s", e.Code, e.Body)
}

// transientError marks an error of a failed attempt that may pass by itself
// (see IsTransient) without changing its text.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// IsTransient reports whether err, from Chat, is a failure of the model
// service that may pass by itself: a network error, an attempt that timed
// out, or a 429 or 5xx response. A request the service refuses (any other
// status), an answer that cannot be decoded or is empty, and a cancelled call
// are not.
func IsTransient(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if se := (*StatusError)(nil); errors.As(err, &se) {
		return retryableStatus(se.Code)
	}
	var te *transientError
	return errors.As(err, &te)
}

// retryableStatus reports whether a response with status code is worth
// retrying: 429 and 5xx.
func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// Base64 data in an error body: the payload of a data URL, and any other run
// of at least 200 base64 characters. A backslash counts as one, so a payload
// echoed with JSON-escaped slashes is matched whole.
var (
	dataURLPayload = regexp.MustCompile(`(data:[^;,\s"]+;base64,)[A-Za-z0-9+/=\\]+`)
	base64Run      = regexp.MustCompile(`[A-Za-z0-9+/=\\]{200,}`)
)

// omitBase64 replaces the base64 data in an error body with "[omitted]".
func omitBase64(body []byte) []byte {
	body = dataURLPayload.ReplaceAll(body, []byte("${1}[omitted]"))
	return base64Run.ReplaceAll(body, []byte("[omitted]"))
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
// Runtime.MaxRetries times with exponential backoff starting at 500ms. An
// attempt that hits its own timeout is not retried.
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

// MaxChatDuration is the longest Chat takes with runtime rt when none of its
// attempts is cut short: each of the 1+MaxRetries attempts up to
// Runtime.Timeout, and the backoff before every retry (500ms, doubling). An
// admission lease held for a call must last this long.
func MaxChatDuration(rt config.ProfileRuntime) time.Duration {
	retries := max(rt.MaxRetries, 0)
	return rt.Timeout.Duration*time.Duration(retries+1) + defaultBackoff*(1<<retries-1)
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
	if len(req.Tools) > 0 {
		m["tools"] = req.Tools
		if req.ToolChoice != "" {
			m["tool_choice"] = req.ToolChoice
		}
	}
	maps.Copy(m, c.profile.ExtraBody)
	for k, v := range req.ExtraBody {
		m[k] = mergeExtra(m[k], v)
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("openai: encode request: %w", err)
	}
	return b, nil
}

// mergeExtra lays over on base: when both are JSON objects, a new object with
// each of over's keys merged into base's in turn; otherwise over. Neither
// argument is changed.
func mergeExtra(base, over any) any {
	b, ok := base.(map[string]any)
	o, ok2 := over.(map[string]any)
	if !ok || !ok2 {
		return over
	}
	out := maps.Clone(b)
	for k, v := range o {
		out[k] = mergeExtra(out[k], v)
	}
	return out
}

type completionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
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

// attempt performs one request bounded by Runtime.Timeout. A transport error
// is retryable unless the attempt's own deadline caused it: a model that hung
// for the whole timeout is not worth waiting on again.
func (c *Client) attempt(ctx context.Context, body []byte) (ChatResponse, bool, error) {
	actx := ctx
	if d := c.profile.Runtime.Timeout.Duration; d > 0 {
		var cancel context.CancelFunc
		actx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	timedOut := func() bool {
		return ctx.Err() == nil && errors.Is(actx.Err(), context.DeadlineExceeded)
	}
	httpReq, err := http.NewRequestWithContext(actx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, false, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.profile.APIKey)

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return ChatResponse{}, !timedOut(), &transientError{fmt.Errorf("openai: %w", err)}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		if timedOut() {
			if !errors.Is(err, context.DeadlineExceeded) {
				err = fmt.Errorf("%w (%w)", context.DeadlineExceeded, err)
			}
			return ChatResponse{}, false, &transientError{fmt.Errorf("openai: read response: %w", err)}
		}
		return ChatResponse{}, true, &transientError{fmt.Errorf("openai: read response: %w", err)}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw = omitBase64(raw)
		if len(raw) > maxErrorBody {
			raw = raw[:maxErrorBody]
		}
		return ChatResponse{}, retryableStatus(resp.StatusCode), &StatusError{Code: resp.StatusCode, Body: string(raw)}
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
		ToolCalls:        cr.Choices[0].Message.ToolCalls,
		Model:            cr.Model,
		FinishReason:     cr.Choices[0].FinishReason,
		PromptTokens:     cr.Usage.PromptTokens,
		CompletionTokens: cr.Usage.CompletionTokens,
		ReasoningTokens:  cr.Usage.CompletionTokensDetails.ReasoningTokens,
	}
	if out.Model == "" {
		out.Model = c.profile.Model
	}
	if out.Content == "" && len(out.ToolCalls) == 0 {
		return ChatResponse{}, false, ErrEmptyOutput
	}
	return out, false, nil
}
