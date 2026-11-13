package telegram

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kinako404/conloom/pkg/conloomapi"
)

const testCoreToken = "fake-tg-adapter-token"

// submitRecord is one event submission. Resp and Replayed (the
// Idempotent-Replayed header) are only recorded in front of the real core.
type submitRecord struct {
	Key      string
	Event    conloomapi.Event
	Code     int
	At       time.Time
	Resp     conloomapi.EventAccepted
	Replayed bool
}

type ackRecord struct {
	ActionID string
	Req      conloomapi.AckRequest
	Code     int
}

// fakeCore implements the parts of the Bot Interaction API the adapter uses.
// Outbox actions are leased for lease and handed out again unless ACKed.
type fakeCore struct {
	t   *testing.T
	srv *httptest.Server
	URL string

	mu          sync.Mutex
	botID       string
	disposition string
	suppressed  string
	failEvents  int
	failStatus  int // status of failed submissions; 503 if 0
	rejectNext  int
	failAcks    bool
	lease       time.Duration
	onEvent     func(ev conloomapi.Event)
	submits     []submitRecord
	actions     []*leasedAction
	acks        []ackRecord
	wake        chan struct{}
	// taskStatus is the status GET /tasks/{id} reports, "running" if unset;
	// denyTasks answers it 403 as for a token without tasks:read.
	taskStatus map[string]string
	denyTasks  bool
	taskReads  int
}

type leasedAction struct {
	action conloomapi.Action
	until  time.Time
	acked  bool
}

func newFakeCore(t *testing.T) *fakeCore {
	t.Helper()
	c := &fakeCore{
		t: t, botID: testBotID, disposition: conloomapi.DispositionReply, lease: 300 * time.Millisecond,
		wake: make(chan struct{}), taskStatus: map[string]string{},
	}
	c.srv = httptest.NewServer(http.HandlerFunc(c.serve))
	c.URL = c.srv.URL
	t.Cleanup(c.srv.Close)
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(code string, retryable bool) conloomapi.ErrorResponse {
	return conloomapi.ErrorResponse{Error: conloomapi.ErrorBody{Code: code, Message: code, Retryable: retryable}}
}

func (c *fakeCore) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testCoreToken {
		writeJSON(w, http.StatusUnauthorized, apiErr(conloomapi.CodeUnauthorized, false))
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/capabilities":
		c.mu.Lock()
		botID := c.botID
		c.mu.Unlock()
		writeJSON(w, http.StatusOK, conloomapi.Capabilities{
			SchemaVersion: conloomapi.SchemaVersion, BotInstanceID: botID, PlatformSelfID: "30001", PrivateChat: "admins",
		})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/tasks/"):
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
		c.mu.Lock()
		c.taskReads++
		deny, status := c.denyTasks, c.taskStatus[id]
		c.mu.Unlock()
		if deny {
			writeJSON(w, http.StatusForbidden, apiErr(conloomapi.CodeForbidden, false))
			return
		}
		if status == "" {
			status = conloomapi.TaskRunning
		}
		writeJSON(w, http.StatusOK, conloomapi.Task{TaskID: id, Status: status, Actions: []conloomapi.Action{}})
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/events":
		c.postEvent(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/outbox":
		c.poll(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
		c.ack(w, r)
	default:
		writeJSON(w, http.StatusNotFound, apiErr(conloomapi.CodeNotFound, false))
	}
}

func (c *fakeCore) postEvent(w http.ResponseWriter, r *http.Request) {
	var ev conloomapi.Event
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		c.t.Errorf("fake core: bad event: %v", err)
	}
	c.mu.Lock()
	rec := submitRecord{Key: r.Header.Get("Idempotency-Key"), Event: ev, At: time.Now()}
	var status int
	var body any
	switch {
	case c.failEvents > 0:
		c.failEvents--
		switch c.failStatus {
		case http.StatusUnauthorized:
			status, body = c.failStatus, apiErr(conloomapi.CodeUnauthorized, false)
		case http.StatusForbidden:
			status, body = c.failStatus, apiErr(conloomapi.CodeForbidden, false)
		default:
			status, body = http.StatusServiceUnavailable, apiErr(conloomapi.CodeInternal, true)
		}
	case c.rejectNext > 0:
		c.rejectNext--
		status, body = http.StatusBadRequest, apiErr(conloomapi.CodeInvalidEvent, false)
	default:
		status = http.StatusAccepted
		body = conloomapi.EventAccepted{EventID: ev.EventID, Disposition: c.disposition, TaskID: "task_1", Status: conloomapi.TaskQueued,
			SuppressedReason: c.suppressed}
	}
	rec.Code = status
	c.submits = append(c.submits, rec)
	onEvent := c.onEvent
	c.mu.Unlock()
	writeJSON(w, status, body)
	if status == http.StatusAccepted && onEvent != nil {
		onEvent(ev)
	}
}

func (c *fakeCore) poll(w http.ResponseWriter, r *http.Request) {
	wait, _ := time.ParseDuration(r.URL.Query().Get("wait"))
	deadline := time.After(min(wait, 2*time.Second))
	for {
		c.mu.Lock()
		now := time.Now()
		var out []conloomapi.Action
		for _, a := range c.actions {
			if !a.acked && now.After(a.until) {
				a.until = now.Add(c.lease)
				out = append(out, a.action)
			}
		}
		wake := c.wake
		c.mu.Unlock()
		if len(out) > 0 {
			writeJSON(w, http.StatusOK, conloomapi.OutboxResponse{Actions: out})
			return
		}
		select {
		case <-wake:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			writeJSON(w, http.StatusOK, conloomapi.OutboxResponse{Actions: []conloomapi.Action{}})
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (c *fakeCore) ack(w http.ResponseWriter, r *http.Request) {
	var req conloomapi.AckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		c.t.Errorf("fake core: bad ack: %v", err)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/outbox/"), "/ack")
	c.mu.Lock()
	code := http.StatusOK
	if c.failAcks {
		code = http.StatusBadGateway
	} else {
		for _, a := range c.actions {
			if a.action.ActionID == id {
				a.acked = true
			}
		}
	}
	c.acks = append(c.acks, ackRecord{ActionID: id, Req: req, Code: code})
	c.mu.Unlock()
	if code != http.StatusOK {
		writeJSON(w, code, apiErr(conloomapi.CodeInternal, true))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Enqueue makes action available to the next outbox poll.
func (c *fakeCore) Enqueue(action conloomapi.Action) {
	c.mu.Lock()
	c.actions = append(c.actions, &leasedAction{action: action})
	close(c.wake)
	c.wake = make(chan struct{})
	c.mu.Unlock()
}

func (c *fakeCore) set(f func(c *fakeCore)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
}

func (c *fakeCore) Submits() []submitRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]submitRecord(nil), c.submits...)
}

// Accepted returns the keys of submissions the core accepted, in order.
func (c *fakeCore) Accepted() []string {
	var keys []string
	for _, s := range c.Submits() {
		if s.Code == http.StatusAccepted {
			keys = append(keys, s.Key)
		}
	}
	return keys
}

func (c *fakeCore) Acks() []ackRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ackRecord(nil), c.acks...)
}

// replyAction is a reply to 小明's private chat.
func replyAction(id, text, replyToPMID string) conloomapi.Action {
	a := conloomapi.Action{
		ActionID: id, Type: conloomapi.ActionMessageSend,
		ConversationID: "private_20001", ConversationType: conloomapi.ConvPrivate, PlatformConversationID: "20001",
		Content: conloomapi.ActionContent{Text: text}, CreatedAt: time.Now().UTC(),
	}
	if replyToPMID != "" {
		a.ReplyToMessageID, a.ReplyToPlatformMessageID = "msg_"+replyToPMID, replyToPMID
	}
	return a
}
