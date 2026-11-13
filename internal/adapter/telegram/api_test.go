package telegram

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// requireNoToken fails if s contains token in any of the forms a URL may carry it.
func requireNoToken(t *testing.T, what, s, token string) {
	t.Helper()
	for _, form := range []string{token, url.PathEscape(token), url.QueryEscape(token)} {
		if strings.Contains(s, form) {
			t.Errorf("%s contains the bot token: %s", what, s)
		}
	}
}

func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u := "http://" + ln.Addr().String()
	ln.Close()
	return u
}

func TestAPI_TransportErrorIsRedactedAndNotSent(t *testing.T) {
	for _, token := range []string{fakeToken, "30001:odd token+with/slash%"} {
		api := newBotAPI(closedURL(t), token, nil)
		err := api.call(context.Background(), "sendMessage", map[string]any{"chat_id": 1, "text": "x"}, nil)
		if err == nil {
			t.Fatal("call to a closed port succeeded")
		}
		requireNoToken(t, "error", err.Error(), token)
		if !strings.Contains(err.Error(), "REDACTED") {
			t.Errorf("error %q does not show where the token was redacted", err)
		}
		if !errors.Is(err, errNotSent) {
			t.Errorf("connection refused: err = %v, want errNotSent", err)
		}
	}
}

func TestAPI_DecodesResultsAndErrors(t *testing.T) {
	f := newFakeTG(t)
	api := newBotAPI(f.URL, fakeToken, nil)
	ctx := context.Background()

	var me botUser
	if err := api.call(ctx, "getMe", nil, &me); err != nil {
		t.Fatalf("getMe: %v", err)
	}
	if me.ID != fakeBotID || me.Username != fakeBotUsername {
		t.Errorf("getMe = %+v", me)
	}

	f.SetSend(func(call tgCall, n int) (int, string) {
		switch n {
		case 1:
			return http.StatusTooManyRequests, tgError(429, "Too Many Requests: retry after 3", 3)
		case 2:
			return http.StatusForbidden, tgError(403, "Forbidden: bot was blocked by the user", 0)
		default:
			return http.StatusBadGateway, "<html>502 Bad Gateway</html>"
		}
	})
	params := map[string]any{"chat_id": userXM, "text": "你好"}

	err := api.call(ctx, "sendMessage", params, nil)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != 429 || ae.RetryAfter != 3*time.Second {
		t.Fatalf("429: err = %#v, want an apiError with RetryAfter 3s", err)
	}

	err = api.call(ctx, "sendMessage", params, nil)
	if !errors.As(err, &ae) || ae.Status != http.StatusForbidden || ae.RetryAfter != 0 || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("403: err = %v, want an apiError carrying the description", err)
	}
	if errors.Is(err, errNotSent) {
		t.Error("403 reported as not sent")
	}

	err = api.call(ctx, "sendMessage", params, nil)
	if err == nil || errors.As(err, &ae) || errors.Is(err, errNotSent) {
		t.Fatalf("502 HTML: err = %v, want an outcome-unknown error", err)
	}
	requireNoToken(t, "502 error", err.Error(), fakeToken)
}

func TestAPI_NoResponseIsNotNotSent(t *testing.T) {
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-hold }))
	t.Cleanup(func() { close(hold); srv.Close() })
	api := newBotAPI(srv.URL, fakeToken, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := api.call(ctx, "sendMessage", map[string]any{"chat_id": 1, "text": "x"}, nil)
	if err == nil || errors.Is(err, errNotSent) {
		t.Fatalf("unanswered request: err = %v, want an error other than errNotSent", err)
	}
	requireNoToken(t, "timeout error", err.Error(), fakeToken)
}

func TestAPI_WrongTokenIsUnauthorized(t *testing.T) {
	f := newFakeTG(t)
	api := newBotAPI(f.URL, "30001:wrong-token", nil)
	err := api.call(context.Background(), "getMe", nil, nil)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want a 401 apiError", err)
	}
	requireNoToken(t, "401 error", err.Error(), "30001:wrong-token")
}
