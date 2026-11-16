package panel

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// conversationView summarises a conversation. Its persona fields are null
// while the conversation has no persona version yet. ProactiveLevel is the
// effective private level (conloomapi.PrivateProactivePolicy.EffectiveLevel)
// of a private chat, empty for other conversations. MemoryScope is the key
// its memory, status and bars are kept under (memory.ScopeKey): the bot's
// shared scope for a panel chat, the conversation's own key otherwise.
type conversationView struct {
	Key                    string     `json:"key"`
	BotInstanceID          string     `json:"bot_instance_id"`
	BotName                string     `json:"bot_name"`
	Platform               string     `json:"platform"`
	Type                   string     `json:"type"`
	PlatformConversationID string     `json:"platform_conversation_id"`
	MessageCount           int        `json:"message_count"`
	LastMessageAt          *time.Time `json:"last_message_at"`
	Paused                 bool       `json:"paused"`
	ProactiveEnabled       bool       `json:"proactive_enabled"`
	ProactiveLevel         string     `json:"proactive_level"`
	MemoryEnabled          bool       `json:"memory_enabled"`
	MemoryScope            string     `json:"memory_scope"`
	PersonaTemplate        *string    `json:"persona_template"`
	PersonaVersion         *int       `json:"persona_version"`
}

// botView is a bot of the directory, one a panel chat can be started with;
// Source is "config" or "panel" (see botSource).
type botView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Source   string `json:"source"`
}

// listConversations lists every stored conversation and every bot.
func (s *Server) listConversations(w http.ResponseWriter, r *http.Request, _ *Session) {
	stats, err := s.d.Store.ConversationStats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]conversationView, 0, len(stats))
	for _, st := range stats {
		v, err := s.conversationView(r.Context(), st)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out = append(out, v)
	}
	all := s.d.Bots.All()
	bots := make([]botView, 0, len(all))
	for _, b := range all {
		bots = append(bots, botView{ID: b.ID, Name: b.Name, Platform: b.Platform, Source: s.botSource(b.ID)})
	}
	s.writeJSON(w, http.StatusOK, struct {
		Conversations []conversationView `json:"conversations"`
		Bots          []botView          `json:"bots"`
	}{out, bots})
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request, _ *Session) {
	conv, ok := s.conversation(w, r)
	if !ok {
		return
	}
	st := store.ConversationStats{ConversationRecord: conv}
	n, err := s.d.Store.CountMessagesAfter(r.Context(), conv.Key, 0)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st.MessageCount = n
	last, err := s.d.Store.MessagesBefore(r.Context(), conv.Key, 0, 1)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(last) > 0 {
		st.LastMessageAt = last[0].OccurredAt
	}
	v, err := s.conversationView(r.Context(), st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, v)
}

// memoryPolicy is the policy conv's memory follows (memory.Admin.Policy):
// own, conv's current policy, unless conv shares its memory scope, as a panel
// chat does, with other chats.
func (s *Server) memoryPolicy(ctx context.Context, conv store.ConversationRecord, own conloomapi.GroupPolicy) (conloomapi.GroupPolicy, error) {
	if memory.ScopeKey(conv) == conv.Key {
		return own, nil
	}
	return s.d.MemoryAdmin.Policy(ctx, conv)
}

// conversationView adds the conversation's current policy and persona
// version to st without writing either. MemoryEnabled is under the policy
// its memory follows (see memoryPolicy).
func (s *Server) conversationView(ctx context.Context, st store.ConversationStats) (conversationView, error) {
	pv, err := s.d.Policies.Current(ctx, st.Key)
	if err != nil {
		return conversationView{}, err
	}
	p := pv.Policy
	mp, err := s.memoryPolicy(ctx, st.ConversationRecord, p)
	if err != nil {
		return conversationView{}, err
	}
	v := conversationView{
		Key: st.Key, BotInstanceID: st.BotInstanceID, Platform: st.Platform, Type: st.Type,
		PlatformConversationID: st.PlatformConversationID, MessageCount: st.MessageCount,
		LastMessageAt:    nullTime(st.LastMessageAt),
		Paused:           p.Concurrency.MaxInteractiveRequests == 0,
		ProactiveEnabled: p.Proactive.Enabled,
		MemoryEnabled:    mp.Memory.Enabled && mp.Concurrency.AllowLongRequests,
		MemoryScope:      memory.ScopeKey(st.ConversationRecord),
	}
	if st.Type == conloomapi.ConvPrivate {
		v.ProactiveLevel = p.Proactive.Private.EffectiveLevel()
	}
	if bot, ok := s.d.Bots.Bot(st.BotInstanceID); ok {
		v.BotName = bot.Name
	}
	persona, err := s.d.Store.LatestPersona(ctx, st.Key)
	switch {
	case err == nil:
		v.PersonaTemplate, v.PersonaVersion = &persona.TemplateName, &persona.Version
	case !errors.Is(err, store.ErrNotFound):
		return conversationView{}, err
	}
	return v, nil
}
