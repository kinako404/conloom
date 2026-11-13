package telegram

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fakeBotID       = 30001
	fakeBotUsername = "zhiyu_test_bot"
	// fakeToken has the shape of a real bot token; it must never reach a log.
	fakeToken = "30001:AAFake-telegram_token-Value-x"
	userXM    = 20001
)

// tgCall is one Bot API request received by the fake.
type tgCall struct {
	Method string
	Params map[string]any
	At     time.Time
}

func (c tgCall) num(key string) int64 { return toInt(c.Params[key]) }

func (c tgCall) text() string { s, _ := c.Params["text"].(string); return s }

func toInt(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return i
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	}
	return 0
}

// tgResponder answers one sendMessage call. A zero status means the fake
// answers normally; a negative status means it never answers.
type tgResponder func(call tgCall, n int) (status int, body string)

// fakeTG is a Telegram Bot API server: getMe, getUpdates (long polling with
// offset confirmation), sendMessage and sendChatAction, all under
// /bot<token>/<method> like the real API.
type fakeTG struct {
	t   *testing.T
	srv *httptest.Server
	URL string

	done chan struct{}
	once sync.Once

	mu          sync.Mutex
	updates     []map[string]any
	calls       []tgCall
	nextMsgID   int64
	sends       int
	send        tgResponder
	failUpdates int
	wake        chan struct{}
}

func newFakeTG(t *testing.T) *fakeTG {
	t.Helper()
	f := &fakeTG{t: t, done: make(chan struct{}), nextMsgID: 500, wake: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = f.srv.URL
	t.Cleanup(f.close)
	return f
}

func (f *fakeTG) close() {
	f.once.Do(func() {
		close(f.done)
		f.srv.Close()
	})
}

func writeTG(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func okResult(v any) string {
	b, _ := json.Marshal(map[string]any{"ok": true, "result": v})
	return string(b)
}

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+fakeToken+"/")
	if !ok {
		writeTG(w, http.StatusUnauthorized, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}
	params := map[string]any{}
	body, _ := io.ReadAll(r.Body)
	if len(bytes.TrimSpace(body)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&params); err != nil {
			writeTG(w, http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"Bad Request: can't parse JSON"}`)
			return
		}
	}
	call := tgCall{Method: method, Params: params, At: time.Now()}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	switch method {
	case "getMe":
		writeTG(w, http.StatusOK, okResult(map[string]any{
			"id": fakeBotID, "is_bot": true, "first_name": "织语", "username": fakeBotUsername,
		}))
	case "getUpdates":
		f.getUpdates(w, r, call)
	case "sendMessage":
		f.sendMessage(w, call)
	case "sendChatAction":
		writeTG(w, http.StatusOK, okResult(true))
	default:
		writeTG(w, http.StatusNotFound, `{"ok":false,"error_code":404,"description":"Not Found: method not found"}`)
	}
}

// getUpdates confirms (forgets) the updates before offset when the request
// arrives, like Telegram, then returns the remaining ones, waiting up to
// timeout seconds for one to be pushed.
func (f *fakeTG) getUpdates(w http.ResponseWriter, r *http.Request, call tgCall) {
	offset := call.num("offset")
	deadline := time.After(time.Duration(max(call.num("timeout"), 0)) * time.Second)
	f.mu.Lock()
	if f.failUpdates > 0 {
		f.failUpdates--
		f.mu.Unlock()
		writeTG(w, http.StatusBadGateway, "<html>Bad Gateway</html>")
		return
	}
	if offset > 0 {
		kept := f.updates[:0]
		for _, u := range f.updates {
			if toInt(u["update_id"]) >= offset {
				kept = append(kept, u)
			}
		}
		f.updates = kept
	}
	f.mu.Unlock()
	for {
		f.mu.Lock()
		var pending []map[string]any
		for _, u := range f.updates {
			if toInt(u["update_id"]) >= offset {
				pending = append(pending, u)
			}
		}
		wake := f.wake
		f.mu.Unlock()
		if len(pending) > 0 {
			writeTG(w, http.StatusOK, okResult(pending))
			return
		}
		select {
		case <-wake:
		case <-deadline:
			writeTG(w, http.StatusOK, okResult([]any{}))
			return
		case <-r.Context().Done():
			return
		case <-f.done:
			return
		}
	}
}

func (f *fakeTG) sendMessage(w http.ResponseWriter, call tgCall) {
	f.mu.Lock()
	f.sends++
	n, respond := f.sends, f.send
	f.mu.Unlock()
	if respond != nil {
		status, body := respond(call, n)
		if status < 0 {
			<-f.done
			return
		}
		if status != 0 {
			writeTG(w, status, body)
			return
		}
	}
	f.mu.Lock()
	f.nextMsgID++
	id := f.nextMsgID
	f.mu.Unlock()
	chatID := call.num("chat_id")
	writeTG(w, http.StatusOK, okResult(map[string]any{
		"message_id": id, "date": time.Now().Unix(), "text": call.text(),
		"chat": map[string]any{"id": chatID, "type": "private"},
		"from": map[string]any{"id": fakeBotID, "is_bot": true, "first_name": "织语", "username": fakeBotUsername},
	}))
}

// Push queues updates for getUpdates; they stay until an offset confirms them.
func (f *fakeTG) Push(updates ...map[string]any) {
	f.mu.Lock()
	f.updates = append(f.updates, updates...)
	close(f.wake)
	f.wake = make(chan struct{})
	f.mu.Unlock()
}

// SetSend installs a responder for sendMessage calls.
func (f *fakeTG) SetSend(r tgResponder) {
	f.mu.Lock()
	f.send = r
	f.mu.Unlock()
}

// FailUpdates makes the next n getUpdates calls answer 502 with an HTML body.
func (f *fakeTG) FailUpdates(n int) {
	f.mu.Lock()
	f.failUpdates = n
	f.mu.Unlock()
}

func (f *fakeTG) Calls(method string) []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// Offsets returns the offset of every getUpdates call so far.
func (f *fakeTG) Offsets() []int64 {
	var out []int64
	for _, c := range f.Calls("getUpdates") {
		out = append(out, c.num("offset"))
	}
	return out
}

func tgUser(id int64, first, last, username string) map[string]any {
	u := map[string]any{"id": id, "is_bot": false, "first_name": first}
	if last != "" {
		u["last_name"] = last
	}
	if username != "" {
		u["username"] = username
	}
	return u
}

// privateMessage is a message from 小明 in his private chat with the bot.
func privateMessage(messageID int64, fields map[string]any) map[string]any {
	m := map[string]any{
		"message_id": messageID,
		"from":       tgUser(userXM, "小明", "", "xiaoming"),
		"chat":       map[string]any{"id": userXM, "type": "private", "first_name": "小明"},
		"date":       1794470400,
	}
	for k, v := range fields {
		m[k] = v
	}
	return m
}

func tgUpdate(updateID int64, msg map[string]any) map[string]any {
	return map[string]any{"update_id": updateID, "message": msg}
}

func textUpdate(updateID, messageID int64, text string) map[string]any {
	return tgUpdate(updateID, privateMessage(messageID, map[string]any{"text": text}))
}

// testGroupID is a supergroup chat ID: negative, and beyond 32 bits.
const testGroupID = -1001234567890

// groupMessage is a message from 小明 in the test supergroup.
func groupMessage(messageID int64, fields map[string]any) map[string]any {
	msg := privateMessage(messageID, fields)
	msg["chat"] = map[string]any{"id": testGroupID, "type": "supergroup", "title": "测试群"}
	return msg
}

func groupUpdate(updateID, messageID int64, text string) map[string]any {
	return tgUpdate(updateID, groupMessage(messageID, map[string]any{"text": text}))
}

func channelUpdate(updateID, messageID int64, text string) map[string]any {
	msg := privateMessage(messageID, map[string]any{"text": text})
	msg["chat"] = map[string]any{"id": -1009876543210, "type": "channel", "title": "频道"}
	return tgUpdate(updateID, msg)
}

// tgEntity is a message entity at a UTF-16 offset and length.
func tgEntity(typ string, offset, length int) map[string]any {
	return map[string]any{"type": typ, "offset": offset, "length": length}
}

func tgError(code int, description string, retryAfter int) string {
	m := map[string]any{"ok": false, "error_code": code, "description": description}
	if retryAfter > 0 {
		m["parameters"] = map[string]any{"retry_after": retryAfter}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (f *fakeTG) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, c := range f.calls {
		fmt.Fprintf(&b, "%s %v\n", c.Method, c.Params)
	}
	return b.String()
}
