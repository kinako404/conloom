// Package client is a Go client for the ConLoom Bot Interaction API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	defaultTimeout   = 45 * time.Second
	maxErrorBodySize = 64 << 10
)

// APIError is a non-2xx response. Body is the decoded error body; for a
// response without one, Body.Message holds the response text.
type APIError struct {
	Status int
	Body   conloomapi.ErrorBody
}

func (e *APIError) Error() string {
	if e.Body.Code == "" {
		return fmt.Sprintf("conloom: HTTP %d: %s", e.Status, e.Body.Message)
	}
	return fmt.Sprintf("conloom: HTTP %d %s: %s (trace_id %s)", e.Status, e.Body.Code, e.Body.Message, e.Body.TraceID)
}

// IsRetryable reports whether a request that failed with err may succeed when
// repeated: network errors, 5xx, 429 and error bodies marked retryable. A
// cancelled context is not retryable.
func IsRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if apiErr := (*APIError)(nil); errors.As(err, &apiErr) {
		return apiErr.Status >= 500 || apiErr.Status == http.StatusTooManyRequests || apiErr.Body.Retryable
	}
	urlErr := (*url.Error)(nil)
	return errors.As(err, &urlErr)
}

type Client struct {
	base  string
	token string
	hc    *http.Client
}

// New returns a client for the API at baseURL authenticating with token. A
// nil hc uses a client with a 45s timeout, longer than the longest outbox wait.
func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, hc: hc}
}

func (c *Client) newRequest(ctx context.Context, method, path string, in any) (*http.Request, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("conloom: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// send performs req and decodes a 2xx JSON response into out (if non-nil).
func (c *Client) send(req *http.Request, out any) error {
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return readAPIError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("conloom: decode response: %w", err)
	}
	return nil
}

func readAPIError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	apiErr := &APIError{Status: resp.StatusCode}
	var er conloomapi.ErrorResponse
	if err := json.Unmarshal(raw, &er); err == nil && er.Error.Code != "" {
		apiErr.Body = er.Error
		return apiErr
	}
	apiErr.Body.Message = strings.TrimSpace(string(raw))
	if apiErr.Body.Message == "" {
		apiErr.Body.Message = http.StatusText(resp.StatusCode)
	}
	return apiErr
}

func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	req, err := c.newRequest(ctx, method, path, in)
	if err != nil {
		return err
	}
	return c.send(req, out)
}

// SubmitEvent posts ev under the idempotency key idemKey.
func (c *Client) SubmitEvent(ctx context.Context, idemKey string, ev conloomapi.Event) (conloomapi.EventAccepted, error) {
	var out conloomapi.EventAccepted
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/events", ev)
	if err != nil {
		return out, err
	}
	req.Header.Set("Idempotency-Key", idemKey)
	return out, c.send(req, &out)
}

func (c *Client) GetTask(ctx context.Context, taskID string) (conloomapi.Task, error) {
	var out conloomapi.Task
	return out, c.call(ctx, http.MethodGet, "/api/v1/tasks/"+url.PathEscape(taskID), nil, &out)
}

func (c *Client) CancelTask(ctx context.Context, taskID string) (conloomapi.Task, error) {
	var out conloomapi.Task
	return out, c.call(ctx, http.MethodPost, "/api/v1/tasks/"+url.PathEscape(taskID)+"/cancel", nil, &out)
}

// PollOutbox leases up to limit actions, waiting up to wait for one to become
// available. A limit <= 0 uses the server default. The result is never nil.
func (c *Client) PollOutbox(ctx context.Context, limit int, wait time.Duration) ([]conloomapi.Action, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if wait > 0 {
		q.Set("wait", wait.String())
	}
	path := "/api/v1/outbox"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out conloomapi.OutboxResponse
	if err := c.call(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	if out.Actions == nil {
		out.Actions = []conloomapi.Action{}
	}
	return out.Actions, nil
}

func (c *Client) Ack(ctx context.Context, actionID string, req conloomapi.AckRequest) error {
	return c.call(ctx, http.MethodPost, "/api/v1/outbox/"+url.PathEscape(actionID)+"/ack", req, nil)
}

func (c *Client) Capabilities(ctx context.Context) (conloomapi.Capabilities, error) {
	var out conloomapi.Capabilities
	return out, c.call(ctx, http.MethodGet, "/api/v1/capabilities", nil, &out)
}

func conversationPath(convID, leaf string) string {
	return "/api/v1/conversations/" + url.PathEscape(convID) + "/" + leaf
}

func (c *Client) GetPersona(ctx context.Context, convID string) (conloomapi.PersonaState, error) {
	var out conloomapi.PersonaState
	return out, c.call(ctx, http.MethodGet, conversationPath(convID, "persona"), nil, &out)
}

func (c *Client) PutPersona(ctx context.Context, convID string, put conloomapi.PersonaPut) (conloomapi.PersonaState, error) {
	var out conloomapi.PersonaState
	return out, c.call(ctx, http.MethodPut, conversationPath(convID, "persona"), put, &out)
}

func (c *Client) GetPolicy(ctx context.Context, convID string) (conloomapi.PolicyState, error) {
	var out conloomapi.PolicyState
	return out, c.call(ctx, http.MethodGet, conversationPath(convID, "policy"), nil, &out)
}

func (c *Client) PutPolicy(ctx context.Context, convID string, put conloomapi.PolicyPut) (conloomapi.PolicyState, error) {
	var out conloomapi.PolicyState
	return out, c.call(ctx, http.MethodPut, conversationPath(convID, "policy"), put, &out)
}
