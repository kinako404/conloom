package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// maxResponseBytes bounds a Bot API response; getUpdates returns at most 100 updates.
const maxResponseBytes = 16 << 20

// errNotSent marks a request that failed before it was written, so Telegram
// cannot have acted on it.
var errNotSent = errors.New("request not sent")

// apiError is a Bot API response with "ok": false: Telegram handled the
// request and refused it. RetryAfter is set for flood control (429).
type apiError struct {
	Method      string
	Status      int
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram: %s: error %d: %s", e.Method, e.Code, e.Description)
}

// botAPI calls the Telegram Bot API. The token is part of every request URL,
// so every error it returns has the token replaced with REDACTED.
type botAPI struct {
	base  *url.URL
	token string
	hc    *http.Client
}

// newBotAPI returns a client for the API at base. A nil hc uses a client
// without a global timeout; every call is bounded by its context.
func newBotAPI(base, token string, hc *http.Client) *botAPI {
	if hc == nil {
		hc = &http.Client{}
	}
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		u = &url.URL{Scheme: "http", Host: "invalid.invalid"}
	}
	return &botAPI{base: u, token: token, hc: hc}
}

// redact replaces the token, in every form a URL or error may carry it, with REDACTED.
func (b *botAPI) redact(s string) string {
	if b.token == "" {
		return s
	}
	escaped := (&url.URL{Path: b.token}).EscapedPath()
	return strings.NewReplacer(
		b.token, "REDACTED",
		url.PathEscape(b.token), "REDACTED",
		url.QueryEscape(b.token), "REDACTED",
		escaped, "REDACTED",
	).Replace(s)
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call posts params as JSON to method and decodes the result into out (if
// non-nil). An error wrapping errNotSent means the request was never written;
// an *apiError means Telegram refused it; after any other error the outcome
// is unknown.
func (b *botAPI) call(ctx context.Context, method string, params, out any) error {
	body := []byte("{}")
	if params != nil {
		var err error
		if body, err = json.Marshal(params); err != nil {
			return fmt.Errorf("telegram: %s: %w: encode request: %v", method, errNotSent, err)
		}
	}
	u := *b.base
	u.Path = strings.TrimRight(u.Path, "/") + "/bot" + b.token + "/" + method
	u.RawPath = ""
	endpoint := u.String()
	var wrote atomic.Bool
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wrote.Store(true)
			}
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: %s: %w: %s", method, errNotSent, b.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.hc.Do(req)
	if err != nil {
		if !wrote.Load() {
			return fmt.Errorf("telegram: %s: %w: %s", method, errNotSent, b.redact(err.Error()))
		}
		return fmt.Errorf("telegram: %s: no response: %s", method, b.redact(err.Error()))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("telegram: %s: read response: %s", method, b.redact(err.Error()))
	}
	var r apiResponse
	if err := json.Unmarshal(data, &r); err != nil || (!r.OK && r.ErrorCode == 0) {
		return fmt.Errorf("telegram: %s: unexpected HTTP %d response", method, resp.StatusCode)
	}
	if !r.OK {
		return &apiError{
			Method: method, Status: resp.StatusCode, Code: r.ErrorCode, Description: b.redact(r.Description),
			RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second,
		}
	}
	if out != nil {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("telegram: %s: decode result: %w", method, err)
		}
	}
	return nil
}

type botUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// entity marks a part of a message's text; Offset and Length count UTF-16
// code units. User is set for text_mention.
type entity struct {
	Type   string   `json:"type"`
	Offset int      `json:"offset"`
	Length int      `json:"length"`
	User   *botUser `json:"user"`
}

type message struct {
	MessageID       int64           `json:"message_id"`
	From            *botUser        `json:"from"`
	Chat            chat            `json:"chat"`
	Date            int64           `json:"date"`
	Text            string          `json:"text"`
	Entities        []entity        `json:"entities"`
	Caption         string          `json:"caption"`
	CaptionEntities []entity        `json:"caption_entities"`
	Photo           json.RawMessage `json:"photo"`
	Sticker         json.RawMessage `json:"sticker"`
	Voice           json.RawMessage `json:"voice"`
	Audio           json.RawMessage `json:"audio"`
	Video           json.RawMessage `json:"video"`
	VideoNote       json.RawMessage `json:"video_note"`
	Animation       json.RawMessage `json:"animation"`
	Document        json.RawMessage `json:"document"`
	ReplyToMessage  *message        `json:"reply_to_message"`
}

type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message"`
}

type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}

// sendMessageParams is a plain-text sendMessage request: no parse_mode is
// ever set, so reply text cannot inject formatting or links.
type sendMessageParams struct {
	ChatID          int64            `json:"chat_id"`
	Text            string           `json:"text"`
	ReplyParameters *replyParameters `json:"reply_parameters,omitempty"`
}

type getUpdatesParams struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type chatActionParams struct {
	ChatID int64  `json:"chat_id"`
	Action string `json:"action"`
}
