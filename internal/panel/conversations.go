package panel

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/store"
)

// conversationView summarises a conversation. Its persona fields are null
// while the conversation has no persona version yet.
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
	MemoryEnabled          bool       `json:"memory_enabled"`
	PersonaTemplate        *string    `json:"persona_template"`
	PersonaVersion         *int       `json:"persona_version"`
}

// botView is a configured bot, one a panel chat can be started with.
type botView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

// listConversations lists every stored conversation and the configured bots.
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
	bots := make([]botView, 0, len(s.d.Config.Bots))
	for _, b := range s.d.Config.Bots {
		bots = append(bots, botView{ID: b.ID, Name: b.Name, Platform: b.Platform})
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

// conversationView adds the conversation's current policy and persona
// version to st without writing either.
func (s *Server) conversationView(ctx context.Context, st store.ConversationStats) (conversationView, error) {
	pv, err := s.d.Policies.Current(ctx, st.Key)
	if err != nil {
		return conversationView{}, err
	}
	p := pv.Policy
	v := conversationView{
		Key: st.Key, BotInstanceID: st.BotInstanceID, Platform: st.Platform, Type: st.Type,
		PlatformConversationID: st.PlatformConversationID, MessageCount: st.MessageCount,
		LastMessageAt:    nullTime(st.LastMessageAt),
		Paused:           p.Concurrency.MaxInteractiveRequests == 0,
		ProactiveEnabled: p.Proactive.Enabled,
		MemoryEnabled:    p.Memory.Enabled && p.Concurrency.AllowLongRequests,
	}
	if bot, ok := s.d.Config.Bot(st.BotInstanceID); ok {
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
