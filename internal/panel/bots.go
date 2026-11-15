package panel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// PanelBotService creates, changes and deletes the bots created in the panel
// and audits each change itself; Load publishes the stored bots again, as
// they take effect with the current persona templates. *core.PanelBotService
// is one.
type PanelBotService interface {
	Create(ctx context.Context, in core.PanelBotInput, actor string) (config.BotConfig, error)
	Update(ctx context.Context, id string, in core.PanelBotInput, actor string) (config.BotConfig, error)
	Delete(ctx context.Context, id, confirmName, actor string) error
	Load(ctx context.Context) error
	PrivateSkills() []core.SkillInfo
}

// maxBotChats bounds the chats the chat page lists of one bot.
const maxBotChats = 200

// The sources of a bot: the config file or the panel.
const (
	sourceConfig = "config"
	sourcePanel  = "panel"
)

// botSummary is a bot as the bot management lists it. PersonaTemplate and
// Skills are as they take effect; Problems notes what of a panel bot's stored
// settings no longer applies. Chats counts the bot's private chats and
// LastMessageAt is the time of their newest message, null without one.
type botSummary struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Source          string     `json:"source"`
	PersonaTemplate string     `json:"persona_template"`
	Skills          []string   `json:"skills"`
	Problems        []string   `json:"problems"`
	Chats           int        `json:"chats"`
	LastMessageAt   *time.Time `json:"last_message_at"`
}

// chatGroup is a bot with its panel chats as the chat page lists them.
type chatGroup struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Source string      `json:"source"`
	Chats  []chatEntry `json:"chats"`
}

// chatEntry is a panel chat on the chat page. Title is who the bot talks with
// there (see chatTitle).
type chatEntry struct {
	Key           string     `json:"key"`
	Title         string     `json:"title"`
	Platform      string     `json:"platform"`
	LastMessageAt *time.Time `json:"last_message_at"`
}

// botRequest is the body that creates or updates a panel bot: its name, the
// persona template its new chats start from and its skills. A field left out
// or null is nil.
type botRequest struct {
	Name            *string   `json:"name"`
	PersonaTemplate *string   `json:"persona_template"`
	Skills          *[]string `json:"skills"`
}

// complete reports whether r gives every field.
func (r botRequest) complete() bool {
	return r.Name != nil && r.PersonaTemplate != nil && r.Skills != nil
}

// input is r for the service, with left out fields empty.
func (r botRequest) input() core.PanelBotInput {
	var in core.PanelBotInput
	if r.Name != nil {
		in.Name = *r.Name
	}
	if r.PersonaTemplate != nil {
		in.PersonaTemplate = *r.PersonaTemplate
	}
	if r.Skills != nil {
		in.Skills = *r.Skills
	}
	return in
}

// panelBotService is Deps.PanelBots, writing 503 when there is none.
func (s *Server) panelBotService(w http.ResponseWriter, r *http.Request) (PanelBotService, bool) {
	if s.d.PanelBots == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, conloomapi.CodeInternal, "panel bots are not available")
		return nil, false
	}
	return s.d.PanelBots, true
}

// botSource is where the bot id is defined: sourcePanel or sourceConfig.
func (s *Server) botSource(id string) string {
	if s.d.Bots.IsPanel(id) {
		return sourcePanel
	}
	return sourceConfig
}

// privateChats maps each bot ID to the bot's stored private chats, the most
// recently active first (see chatActivity), then by key.
func (s *Server) privateChats(ctx context.Context) (map[string][]store.ConversationStats, error) {
	stats, err := s.d.Store.ConversationStats(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]store.ConversationStats{}
	for _, st := range stats {
		if st.Type == conloomapi.ConvPrivate {
			out[st.BotInstanceID] = append(out[st.BotInstanceID], st)
		}
	}
	for _, chats := range out {
		slices.SortFunc(chats, func(a, b store.ConversationStats) int {
			return cmp.Or(chatActivity(b).Compare(chatActivity(a)), cmp.Compare(a.Key, b.Key))
		})
	}
	return out, nil
}

// chatActivity is when st was last active: its newest message or, when it was
// stored later than that (a chat just created), its creation.
func chatActivity(st store.ConversationStats) time.Time {
	if st.CreatedAt.After(st.LastMessageAt) {
		return st.CreatedAt
	}
	return st.LastMessageAt
}

// describeBot describes b, whose private chats are chats.
func (s *Server) describeBot(b config.BotConfig, chats []store.ConversationStats) botSummary {
	v := botSummary{
		ID: b.ID, Name: b.Name, Source: s.botSource(b.ID), PersonaTemplate: b.PersonaTemplate,
		Skills: strs(b.Skills), Problems: strs(s.d.Bots.Problems(b.ID)), Chats: len(chats),
	}
	var last time.Time
	for _, c := range chats {
		if c.LastMessageAt.After(last) {
			last = c.LastMessageAt
		}
	}
	v.LastMessageAt = nullTime(last)
	return v
}

// listBots lists every bot, config bots first, and the persona templates a
// panel bot's chats may start from.
func (s *Server) listBots(w http.ResponseWriter, r *http.Request, _ *Session) {
	chats, err := s.privateChats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	all := s.d.Bots.All()
	out := make([]botSummary, 0, len(all))
	for _, b := range all {
		out = append(out, s.describeBot(b, chats[b.ID]))
	}
	s.writeJSON(w, http.StatusOK, struct {
		Bots      []botSummary `json:"bots"`
		Templates []string     `json:"templates"`
	}{out, strs(s.d.Personas.TemplateNames())})
}

// listBotSkills lists the skills a panel bot may enable, by name.
func (s *Server) listBotSkills(w http.ResponseWriter, r *http.Request, _ *Session) {
	bots, ok := s.panelBotService(w, r)
	if !ok {
		return
	}
	type skillView struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	skills := bots.PrivateSkills()
	out := make([]skillView, 0, len(skills))
	for _, sk := range skills {
		out = append(out, skillView{sk.Name, sk.Description})
	}
	s.writeJSON(w, http.StatusOK, struct {
		Skills []skillView `json:"skills"`
	}{out})
}

// createBot creates a panel bot; the service audits it.
func (s *Server) createBot(w http.ResponseWriter, r *http.Request, sess *Session) {
	bots, ok := s.panelBotService(w, r)
	if !ok {
		return
	}
	var req botRequest
	if !s.decode(w, r, &req) {
		return
	}
	b, err := bots.Create(r.Context(), req.input(), panelActor(sess))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, s.describeBot(b, nil))
}

// updateBot replaces the name, persona template and skills of a panel bot,
// which the request must all give; the service audits it.
func (s *Server) updateBot(w http.ResponseWriter, r *http.Request, sess *Session) {
	bots, ok := s.panelBotService(w, r)
	if !ok {
		return
	}
	var req botRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !req.complete() {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent, "name, persona_template and skills are all required")
		return
	}
	chats, err := s.privateChats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	b, err := bots.Update(r.Context(), r.PathValue("id"), req.input(), panelActor(sess))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, s.describeBot(b, chats[b.ID]))
}

// deleteBot deletes a panel bot and its chats once the request confirms the
// bot's name; the service audits it.
func (s *Server) deleteBot(w http.ResponseWriter, r *http.Request, sess *Session) {
	bots, ok := s.panelBotService(w, r)
	if !ok {
		return
	}
	var req struct {
		ConfirmName string `json:"confirm_name"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := bots.Delete(r.Context(), r.PathValue("id"), req.ConfirmName, panelActor(sess)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listChats lists the panel chats (store.PlatformPanel) of every bot for the
// chat page, grouped by bot in directory order: each bot's most recently
// active first (its newest message or, if later, its creation; see
// privateChats), cut to maxBotChats after sorting. Private chats on the
// platforms are not listed.
func (s *Server) listChats(w http.ResponseWriter, r *http.Request, _ *Session) {
	chats, err := s.privateChats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	all := s.d.Bots.All()
	out := make([]chatGroup, 0, len(all))
	for _, b := range all {
		list := slices.DeleteFunc(chats[b.ID], func(st store.ConversationStats) bool { return st.Platform != store.PlatformPanel })
		list = list[:min(len(list), maxBotChats)]
		g := chatGroup{ID: b.ID, Name: b.Name, Source: s.botSource(b.ID), Chats: make([]chatEntry, 0, len(list))}
		for _, st := range list {
			title, err := s.chatTitle(r.Context(), st)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			g.Chats = append(g.Chats, chatEntry{
				Key: st.Key, Title: title, Platform: st.Platform, LastMessageAt: nullTime(st.LastMessageAt),
			})
		}
		out = append(out, g)
	}
	s.writeJSON(w, http.StatusOK, struct {
		Bots []chatGroup `json:"bots"`
	}{out})
}

// chatTitle names the panel chat st after the panel user the bot talks with
// there: the newest display name in the chat of the first panel user, in
// config order, who wrote in it or, before any of them did, the name it was
// created with; failing both, the chat's platform conversation ID.
func (s *Server) chatTitle(ctx context.Context, st store.ConversationStats) (string, error) {
	actors := make([]string, 0, len(s.d.Config.Panel.Users))
	for _, u := range s.d.Config.Panel.Users {
		actors = append(actors, core.PanelActorID(u.Name))
	}
	names, err := s.d.Store.LatestDisplayNames(ctx, st.Key, actors)
	if err != nil {
		return "", err
	}
	for _, a := range actors {
		if name := names[a]; name != "" {
			return name, nil
		}
	}
	// A panel chat keeps the name it was created with in its creation event,
	// stored under the conversation ID until the events are purged.
	rec, err := s.d.Store.GetEvent(ctx, st.BotInstanceID, st.ID)
	switch {
	case err == nil:
		var ev conloomapi.Event
		if json.Unmarshal(rec.RawEvent, &ev) == nil && ev.Actor.DisplayName != "" {
			return ev.Actor.DisplayName, nil
		}
	case !errors.Is(err, store.ErrNotFound):
		return "", err
	}
	return st.PlatformConversationID, nil
}
