// Package embedding is a minimal client for OpenAI-compatible embeddings
// endpoints. Errors never quote the API key.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/kinako404/conloom/internal/config"
)

const (
	maxErrorBody     = 512
	maxResponseBytes = 64 << 20
)

// Results of one embeddings request, as reported by Result.
const (
	ResultOK      = "ok"
	ResultError   = "error"
	ResultTimeout = "timeout"
)

// StatusError reports a non-2xx response. Body holds at most the first 512
// bytes of the response body, with the API key masked.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("embedding: unexpected status %d: %s", e.Code, e.Body)
}

type Client struct {
	cfg config.EmbeddingConfig
	hc  *http.Client
	url string
}

// New returns a client for cfg. A nil hc uses a client of its own whose
// connections are kept alive between requests.
func New(cfg config.EmbeddingConfig, hc *http.Client) *Client {
	if hc == nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.MaxIdleConnsPerHost = 4
		hc = &http.Client{Transport: tr}
	}
	return &Client{cfg: cfg, hc: hc, url: strings.TrimRight(cfg.Endpoint, "/") + "/embeddings"}
}

// Model is the embedding model named in every request.
func (c *Client) Model() string { return c.cfg.Model }

// BatchSize is the most texts callers should pass to one Embed call.
func (c *Client) BatchSize() int { return max(c.cfg.BatchSize, 1) }

// Result classifies the error of an Embed call as ResultOK, ResultTimeout
// (a deadline, the client's or the caller's, ran out) or ResultError.
func Result(err error) string {
	switch {
	case err == nil:
		return ResultOK
	case errors.Is(err, context.DeadlineExceeded):
		return ResultTimeout
	}
	var ne interface{ Timeout() bool }
	if errors.As(err, &ne) && ne.Timeout() {
		return ResultTimeout
	}
	return ResultError
}

type embeddingsResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

// Embed sends texts in one request bounded by the configured timeout and
// returns their vectors in input order. All vectors have the same, non-zero
// length and only finite float32 values; a response breaking that is an
// error. It makes no request for no texts.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, _, err := c.EmbedUsage(ctx, texts)
	return vecs, err
}

// EmbedUsage is Embed that also returns the prompt tokens the service
// reported (usage.prompt_tokens), 0 when it reported none or on an error.
func (c *Client) EmbedUsage(ctx context.Context, texts []string) ([][]float32, int, error) {
	if len(texts) == 0 {
		return nil, 0, nil
	}
	body, err := json.Marshal(map[string]any{"model": c.cfg.Model, "input": texts})
	if err != nil {
		return nil, 0, fmt.Errorf("embedding: encode request: %w", err)
	}
	if d := c.cfg.Timeout.Duration; d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("embedding: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, c.wrap(ctx, "request", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, c.wrap(ctx, "read response", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if len(raw) > maxErrorBody {
			raw = raw[:maxErrorBody]
		}
		return nil, 0, &StatusError{Code: resp.StatusCode, Body: c.mask(string(raw))}
	}
	var er embeddingsResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, 0, fmt.Errorf("embedding: decode response: %w", err)
	}
	if len(er.Data) != len(texts) {
		return nil, 0, fmt.Errorf("embedding: got %d vectors for %d texts", len(er.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range er.Data {
		if d.Index < 0 || d.Index >= len(texts) || out[d.Index] != nil {
			return nil, 0, fmt.Errorf("embedding: bad or repeated index %d", d.Index)
		}
		if len(d.Embedding) == 0 || len(d.Embedding) != len(er.Data[0].Embedding) {
			return nil, 0, fmt.Errorf("embedding: vector %d has %d dimensions, want %d", d.Index, len(d.Embedding), len(er.Data[0].Embedding))
		}
		vec := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			if math.IsNaN(f) || math.Abs(f) > math.MaxFloat32 {
				return nil, 0, fmt.Errorf("embedding: vector %d holds an invalid value", d.Index)
			}
			vec[i] = float32(f)
		}
		out[d.Index] = vec
	}
	return out, er.Usage.PromptTokens, nil
}

// wrap reports a transport error, marking it as a deadline when ctx ran out.
// The error of net/http quotes the URL, which never carries the key; it is
// masked anyway in case the endpoint embeds credentials.
func (c *Client) wrap(ctx context.Context, op string, err error) error {
	msg := c.mask(err.Error())
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || Result(err) == ResultTimeout {
		return fmt.Errorf("embedding: %s: %s: %w", op, msg, context.DeadlineExceeded)
	}
	return fmt.Errorf("embedding: %s: %s", op, msg)
}

func (c *Client) mask(s string) string {
	if c.cfg.APIKey == "" {
		return s
	}
	return strings.ReplaceAll(s, c.cfg.APIKey, "***")
}
