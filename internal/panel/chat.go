package panel

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ChatService lets panel users chat with a bot; *core.Orchestrator is one.
type ChatService interface {
	SubmitPanel(ctx context.Context, convKey, panelUser, text string) (string, error)
	CreatePanelChat(ctx context.Context, botID, panelUser, displayName string) (string, error)
	DeletePanelChat(ctx context.Context, convKey string) error
	Busy(convKey string) bool
}

// maxChatMessages bounds the messages of one chat poll.
const maxChatMessages = 200

// chatService is Deps.Chat, writing 503 when there is none.
func (s *Server) chatService(w http.ResponseWriter, r *http.Request) (ChatService, bool) {
	if s.d.Chat == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "panel chat is not available")
		return nil, false
	}
	return s.d.Chat, true
}

// chatFail is fail for the chat service: a request it refuses is 400 with
// the reason.
func (s *Server) chatFail(w http.ResponseWriter, r *http.Request, err error) {
	for _, refused := range []error{core.ErrNotPrivate, core.ErrNotPanelChat, core.ErrNoUserYet, core.ErrBadText} {
		if errors.Is(err, refused) {
			s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
				strings.TrimPrefix(refused.Error(), "core: "), err)
			return
		}
	}
	s.fail(w, r, err)
}

// postChatMessage sends text to the bot as the conversation's user, through
// the panel channel. The audit records the message ID, not the text.
func (s *Server) postChatMessage(w http.ResponseWriter, r *http.Request, sess *Session) {
	chat, ok := s.chatService(w, r)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	id, err := chat.SubmitPanel(r.Context(), conv.Key, sess.User, req.Text)
	if err != nil {
		s.chatFail(w, r, err)
		return
	}
	s.audit(r, sess, conv.BotInstanceID, "panel.message", conv.Key, struct {
		MessageID string `json:"message_id"`
	}{id})
	s.writeJSON(w, http.StatusAccepted, struct {
		MessageID string `json:"message_id"`
	}{id})
}

// getChat lists the conversation's messages after the seq cursor after,
// oldest first and at most 200, and whether the bot is still answering: the
// conversation has queued or running tasks, or panel-channel actions not yet
// delivered. NextAfter is the cursor of the next poll.
func (s *Server) getChat(w http.ResponseWriter, r *http.Request, _ *Session) {
	chat, ok := s.chatService(w, r)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		var err error
		if after, err = strconv.ParseInt(v, 10, 64); err != nil || after < 0 {
			s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "after must be a non-negative message seq")
			return
		}
	}
	// typing is read before the messages: a reply delivered in between then
	// shows next to a stale indicator rather than neither showing.
	typing := chat.Busy(conv.Key)
	if !typing {
		pending, err := s.d.Store.PendingPanelActions(r.Context(), conv.Key)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		typing = pending > 0
	}
	msgs, err := s.d.Store.MessagesAfter(r.Context(), conv.Key, after, maxChatMessages)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]messageView, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, newMessageView(m))
	}
	if len(msgs) > 0 {
		after = msgs[len(msgs)-1].Seq
	}
	s.writeJSON(w, http.StatusOK, struct {
		Messages  []messageView `json:"messages"`
		Typing    bool          `json:"typing"`
		NextAfter int64         `json:"next_after"`
	}{out, typing, after})
}

// createChat starts a panel chat of the session's user with a bot.
func (s *Server) createChat(w http.ResponseWriter, r *http.Request, sess *Session) {
	chat, ok := s.chatService(w, r)
	if !ok {
		return
	}
	var req struct {
		BotInstanceID string `json:"bot_instance_id"`
		DisplayName   string `json:"display_name"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	key, err := chat.CreatePanelChat(r.Context(), req.BotInstanceID, sess.User, req.DisplayName)
	if err != nil {
		s.chatFail(w, r, err)
		return
	}
	s.audit(r, sess, req.BotInstanceID, "panel.chat.create", key, struct{}{})
	s.writeJSON(w, http.StatusCreated, struct {
		Key string `json:"key"`
	}{key})
}

// deleteChat deletes a panel chat and what is stored under it.
func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request, sess *Session) {
	chat, ok := s.chatService(w, r)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	if err := chat.DeletePanelChat(r.Context(), conv.Key); err != nil {
		s.chatFail(w, r, err)
		return
	}
	s.audit(r, sess, conv.BotInstanceID, "panel.chat.delete", conv.Key, struct{}{})
	w.WriteHeader(http.StatusNoContent)
}
