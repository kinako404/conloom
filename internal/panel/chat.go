package panel

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// ChatService lets panel users chat with a bot; *core.Orchestrator is one.
type ChatService interface {
	SubmitPanel(ctx context.Context, convKey, panelUser, text string) (messageID, suppressed string, err error)
	CreatePanelChat(ctx context.Context, botID, panelUser, displayName string) (string, error)
	DeletePanelChat(ctx context.Context, convKey string) error
	WithdrawPanel(ctx context.Context, convKey string, seq int64) (deleted int, err error)
	RegeneratePanel(ctx context.Context, convKey string, seq int64) (taskID string, err error)
	RegenerableSeq(ctx context.Context, convKey string) (int64, error)
	Busy(convKey string) bool
	Suggest(ctx context.Context, convKey string, afterSeq int64) ([]string, error)
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

// chatFail is fail for the chat service: a request it refuses for what was
// asked (not a panel chat, not private, bad text, a message that cannot be
// regenerated) is 400 with the reason; a paused chat is 409 PAUSED, a full
// queue or a regenerate the scheduler would merge 409 CHAT_BUSY; a seq that is
// not a message of the chat is 404 MESSAGE_NOT_FOUND, unlike a missing chat
// (404 NOT_FOUND).
func (s *Server) chatFail(w http.ResponseWriter, r *http.Request, err error) {
	for _, c := range []struct {
		err error
		msg string
	}{
		{core.ErrNotLatest, "只能重新生成最新一条回复"},
		{core.ErrNotReply, "只能重新生成 bot 对前面那几条消息的回复；它自己发起的消息（如主动找你说话）不能重新生成"},
		{core.ErrCommandReply, "命令的回复不能重新生成"},
		{core.ErrForgotten, "这条回复答的消息已经被忘记了，不能重新生成"},
	} {
		if errors.Is(err, c.err) {
			s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, c.msg, err)
			return
		}
	}
	for _, refused := range []error{core.ErrNotPrivate, core.ErrNotPanelChat, core.ErrBadText} {
		if errors.Is(err, refused) {
			s.respondError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
				strings.TrimPrefix(refused.Error(), "core: "), err)
			return
		}
	}
	switch {
	case errors.Is(err, core.ErrMessageNotFound):
		s.respondError(w, r, http.StatusNotFound, conloomapi.CodeMessageNotFound, "这条消息已经不在了", err)
	case errors.Is(err, core.ErrPaused):
		s.respondError(w, r, http.StatusConflict, conloomapi.CodePaused, "这个聊天暂停中。", err)
	case errors.Is(err, core.ErrQueueFull):
		s.respondError(w, r, http.StatusConflict, conloomapi.CodeChatBusy, "待回复的消息太多，稍后再试。", err)
	case errors.Is(err, core.ErrRegenMerged):
		s.respondError(w, r, http.StatusConflict, conloomapi.CodeChatBusy, "这个聊天正在变化，稍后再试。", err)
	default:
		s.fail(w, r, err)
	}
}

// postChatMessage sends text to the bot in a panel chat as the session's
// user, through the panel channel; the chat service refuses other
// conversations. It answers the message ID and, when the message is stored
// but will not be answered, suppressed_reason as in the API's 202 to POST
// /events. The audit records the message ID, not the text.
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
	id, suppressed, err := chat.SubmitPanel(r.Context(), conv.Key, sess.User, req.Text)
	if err != nil {
		s.chatFail(w, r, err)
		return
	}
	s.audit(r, sess, conv.BotInstanceID, "panel.message", conv.Key, struct {
		MessageID string `json:"message_id"`
	}{id})
	s.writeJSON(w, http.StatusAccepted, struct {
		MessageID        string `json:"message_id"`
		SuppressedReason string `json:"suppressed_reason,omitempty"`
	}{id, suppressed})
}

// withdrawChatMessage deletes a panel chat's messages from seq onward. The
// audit records seq and how many were deleted, not their text.
func (s *Server) withdrawChatMessage(w http.ResponseWriter, r *http.Request, sess *Session) {
	chat, ok := s.chatService(w, r)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq < 1 {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "seq must be a positive message seq")
		return
	}
	deleted, err := chat.WithdrawPanel(r.Context(), conv.Key, seq)
	if err != nil {
		s.chatFail(w, r, err)
		return
	}
	s.audit(r, sess, conv.BotInstanceID, "panel.message.withdraw", conv.Key, struct {
		Seq     int64 `json:"seq"`
		Deleted int   `json:"deleted"`
	}{seq, deleted})
	w.WriteHeader(http.StatusNoContent)
}

// regenerateChatMessage replaces a panel chat's newest bot reply with a new
// reply task for the user messages it answered. The audit records seq and
// the new task's ID, not any text.
func (s *Server) regenerateChatMessage(w http.ResponseWriter, r *http.Request, sess *Session) {
	chat, ok := s.chatService(w, r)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq < 1 {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "seq must be a positive message seq")
		return
	}
	taskID, err := chat.RegeneratePanel(r.Context(), conv.Key, seq)
	if errors.Is(err, core.ErrPaused) {
		s.respondError(w, r, http.StatusConflict, conloomapi.CodePaused, "这个聊天暂停中，不能重新生成。", err)
		return
	}
	if err != nil {
		s.chatFail(w, r, err)
		return
	}
	s.audit(r, sess, conv.BotInstanceID, "panel.message.regenerate", conv.Key, struct {
		Seq    int64  `json:"seq"`
		TaskID string `json:"task_id"`
	}{seq, taskID})
	s.writeJSON(w, http.StatusAccepted, struct {
		TaskID string `json:"task_id"`
	}{taskID})
}

// getChat lists the conversation's messages after the seq cursor after,
// oldest first and at most 200, and whether the bot is still answering: the
// conversation has queued or running tasks, or panel-channel actions neither
// delivered nor expired. NextAfter is the cursor of the next poll and
// RegenerateSeq the seq of the newest message when POST …/regenerate would
// accept it now (see core.Orchestrator.RegenerableSeq), 0 otherwise and
// always while the bot is answering. Scene is the private chat's scene as the
// autonomy tab shows it (AutonomyService.Scene), null for a group or when
// there is no autonomy service.
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
		pending, err := s.d.Store.PendingPanelActions(r.Context(), conv.Key, s.now())
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
	var regen int64
	if !typing {
		if regen, err = chat.RegenerableSeq(r.Context(), conv.Key); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	var scene *conloomapi.AutonomyScene
	if s.d.Autonomy != nil && conv.Type == conloomapi.ConvPrivate {
		sc, err := s.d.Autonomy.Scene(r.Context(), conv.Key)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		scene = &sc
	}
	s.writeJSON(w, http.StatusOK, struct {
		Messages      []messageView             `json:"messages"`
		Typing        bool                      `json:"typing"`
		NextAfter     int64                     `json:"next_after"`
		RegenerateSeq int64                     `json:"regenerate_seq"`
		Scene         *conloomapi.AutonomyScene `json:"scene"`
	}{out, typing, after, regen, scene})
}

// postSuggestions answers {suggestions}: up to three lines the user might
// send next in a panel chat once the bot's turn is done, for after_seq, the
// seq of the chat's newest message (see core.Orchestrator.Suggest). The list
// is empty, not an error, when there are none or generating them failed;
// other conversations are refused with 400. Nothing changes, so nothing is
// audited.
func (s *Server) postSuggestions(w http.ResponseWriter, r *http.Request, _ *Session) {
	chat, ok := s.chatService(w, r)
	if !ok {
		return
	}
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	var req struct {
		AfterSeq int64 `json:"after_seq"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.AfterSeq < 1 {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "after_seq must be a positive message seq")
		return
	}
	items, err := chat.Suggest(r.Context(), conv.Key, req.AfterSeq)
	if err != nil {
		s.chatFail(w, r, err)
		return
	}
	if items == nil {
		items = []string{}
	}
	s.writeJSON(w, http.StatusOK, struct {
		Suggestions []string `json:"suggestions"`
	}{items})
}

// commandView is a command of a panel chat's command menu (core.MenuCommand).
type commandView struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Usage       string   `json:"usage"`
	Description string   `json:"description"`
	Args        bool     `json:"args"`
}

// getCommands answers {prefix, commands}: the conversation's command prefix
// (its policy's reply.command_prefix) and the commands the session's user may
// send in it, for the command menu of the chat pages (see core.MenuCommands),
// filtered by the conversation's type and by whether the user counts as an
// admin there (core.IsAdmin; every panel chat user does). The pages send only
// in panel chats, so other conversations are refused with 400 as by POST
// messages. An empty prefix turns commands off: the list is empty.
func (s *Server) getCommands(w http.ResponseWriter, r *http.Request, sess *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	if conv.Platform != store.PlatformPanel {
		s.chatFail(w, r, core.ErrNotPanelChat)
		return
	}
	pv, err := s.d.Policies.Current(r.Context(), conv.Key)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	bot, _ := s.d.Bots.Bot(conv.BotInstanceID)
	prefix := pv.Policy.Reply.CommandPrefix
	admin := core.IsAdmin(bot, conv.Platform, core.PanelActorID(sess.User))
	out := []commandView{}
	for _, c := range core.MenuCommands(prefix, conv.Type, admin) {
		out = append(out, commandView{Name: c.Name, Aliases: c.Aliases, Usage: c.Usage, Description: c.Description, Args: c.Args})
	}
	s.writeJSON(w, http.StatusOK, struct {
		Prefix   string        `json:"prefix"`
		Commands []commandView `json:"commands"`
	}{prefix, out})
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
