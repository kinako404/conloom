package napcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const (
	fakeSelfID  = 30001
	fakeOBToken = "fake-onebot-token"
)

// obCall is one action request received by the fake OneBot server.
type obCall struct {
	Action string
	Params map[string]any
}

// sendMode controls how the fake answers send_group_msg and send_private_msg.
type sendMode int

const (
	sendReply sendMode = iota
	// sendReplyThenDrop answers, then closes the connection without a handshake.
	sendReplyThenDrop
	// sendNoReply never answers.
	sendNoReply
	// sendDelayedReply answers after the configured delay.
	sendDelayedReply
	// sendReject answers with a non-zero retcode, like NapCat's send timeouts.
	sendReject
)

// fakeOB is a OneBot 11 forward WebSocket server modelled on NapCat 4.x: it
// requires the access token as query parameter and bearer header, pushes a
// lifecycle meta event on connect, answers get_login_info, send_group_msg and
// send_private_msg, and answers test_echo requests in reverse order once ten
// of them are pending on a connection.
type fakeOB struct {
	t   *testing.T
	srv *httptest.Server
	URL string

	mu        sync.Mutex
	conns     map[*websocket.Conn]bool
	accepted  int
	calls     []obCall
	nextMsgID int64
	sendMode  sendMode
	sendDelay time.Duration
}

func newFakeOB(t *testing.T) *fakeOB {
	t.Helper()
	f := &fakeOB{t: t, conns: map[*websocket.Conn]bool{}, nextMsgID: 50000}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	f.URL = "ws" + strings.TrimPrefix(f.srv.URL, "http")
	t.Cleanup(func() {
		f.Drop()
		f.srv.Close()
	})
	return f
}

func (f *fakeOB) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("access_token") != fakeOBToken || r.Header.Get("Authorization") != "Bearer "+fakeOBToken {
		http.Error(w, "token check failed", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(4 << 20)
	f.mu.Lock()
	f.conns[c] = true
	f.accepted++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.conns, c)
		f.mu.Unlock()
		_ = c.CloseNow()
	}()

	ctx := context.Background()
	f.write(c, map[string]any{
		"time": time.Now().Unix(), "self_id": fakeSelfID, "post_type": "meta_event",
		"meta_event_type": "lifecycle", "sub_type": "connect",
	})
	var held []map[string]any
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req struct {
			Action string          `json:"action"`
			Params map[string]any  `json:"params"`
			Echo   json.RawMessage `json:"echo"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			f.t.Errorf("fakeob: bad request %s: %v", data, err)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, obCall{Action: req.Action, Params: req.Params})
		f.mu.Unlock()

		resp := map[string]any{"status": "ok", "retcode": 0, "message": "", "wording": "", "echo": req.Echo}
		switch req.Action {
		case "get_login_info":
			resp["data"] = map[string]any{"user_id": fakeSelfID, "nickname": "织语"}
		case "send_group_msg", "send_private_msg":
			f.mu.Lock()
			f.nextMsgID++
			resp["data"] = map[string]any{"message_id": f.nextMsgID}
			mode, delay := f.sendMode, f.sendDelay
			f.mu.Unlock()
			switch mode {
			case sendReplyThenDrop:
				f.write(c, resp)
				return
			case sendNoReply:
				continue
			case sendDelayedReply:
				time.Sleep(delay)
			case sendReject:
				resp["status"], resp["retcode"], resp["data"] = "failed", 1200, nil
				resp["message"], resp["wording"] = "Timeout: NTEvent", "Timeout: NTEvent"
			}
		case "test_echo":
			resp["data"] = req.Params
			held = append(held, resp)
			if len(held) < 10 {
				continue
			}
			for i := len(held) - 1; i >= 0; i-- {
				f.write(c, held[i])
			}
			held = nil
			continue
		default:
			resp["status"], resp["retcode"], resp["data"] = "failed", 1404, nil
			resp["message"], resp["wording"] = "不支持的Api "+req.Action, "不支持的Api "+req.Action
		}
		f.write(c, resp)
	}
}

func (f *fakeOB) write(c *websocket.Conn, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		f.t.Errorf("fakeob: marshal: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, data)
}

// Push sends an event to every connected client.
func (f *fakeOB) Push(ev map[string]any) {
	f.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(f.conns))
	for c := range f.conns {
		conns = append(conns, c)
	}
	f.mu.Unlock()
	if len(conns) == 0 {
		f.t.Errorf("fakeob: Push with no connected client")
	}
	for _, c := range conns {
		f.write(c, ev)
	}
}

// Drop closes every connection without a close handshake.
func (f *fakeOB) Drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.conns {
		_ = c.CloseNow()
	}
}

// SetSend sets how later send requests are answered; delay applies to
// sendDelayedReply.
func (f *fakeOB) SetSend(mode sendMode, delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendMode, f.sendDelay = mode, delay
}

func (f *fakeOB) Accepted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accepted
}

// Calls returns the received requests for action.
func (f *fakeOB) Calls(action string) []obCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []obCall
	for _, c := range f.calls {
		if c.Action == action {
			out = append(out, c)
		}
	}
	return out
}

// groupMessage builds a group message event in array format.
func groupMessage(groupID, userID, msgID int64, text string, atBot bool) map[string]any {
	var segs []any
	if atBot {
		segs = append(segs, map[string]any{"type": "at", "data": map[string]any{"qq": strconv.Itoa(fakeSelfID)}})
	}
	segs = append(segs, map[string]any{"type": "text", "data": map[string]any{"text": text}})
	return map[string]any{
		"time": time.Now().Unix(), "self_id": fakeSelfID, "post_type": "message", "message_type": "group",
		"sub_type": "normal", "message_id": msgID, "user_id": userID, "group_id": groupID,
		"message": segs, "raw_message": text, "font": 14,
		"sender": map[string]any{"user_id": userID, "nickname": "小明", "card": "", "role": "member"},
	}
}
