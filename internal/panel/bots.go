package panel

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/botdir"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/core"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// PanelBotService creates, changes and deletes the bots created in the panel,
// changes the settings of the config bots and audits each change itself;
// Load publishes the stored bots and settings again, as they take effect
// with the current persona templates. *core.PanelBotService is one.
type PanelBotService interface {
	Create(ctx context.Context, in core.PanelBotInput, actor string) (config.BotConfig, error)
	Update(ctx context.Context, id string, in core.PanelBotInput, actor string) (config.BotConfig, error)
	UpdateConfig(ctx context.Context, id string, in core.ConfigBotInput, actor string) (config.BotConfig, error)
	Delete(ctx context.Context, id, confirmName, actor string) error
	Load(ctx context.Context) error
	PrivateSkills() []core.SkillInfo
	ConfigSkills() []core.SkillInfo
}

// maxBotChats bounds the chats the chat page lists of one bot.
const maxBotChats = 200

// The sources of a bot: the config file or the panel.
const (
	sourceConfig = "config"
	sourcePanel  = "panel"
)

// botSummary is a bot as the bot management lists it. The settings are as
// they take effect: PrivateTemplate and GroupTemplate the persona templates
// the bot's new private chats and new groups start from, PersonaTemplate the
// private one, which is a panel bot's only one (it sets both), and, for a
// config bot only, the settings of configBotSummary. Problems notes what of
// the bot's stored settings no longer applies. Chats counts the bot's private
// chats and LastMessageAt is the time of their newest message, null without
// one.
type botSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Source   string `json:"source"`
	Platform string `json:"platform"`
	*configBotSummary
	PersonaTemplate string     `json:"persona_template"`
	PrivateTemplate string     `json:"private_template"`
	GroupTemplate   string     `json:"group_template"`
	Skills          []string   `json:"skills"`
	Problems        []string   `json:"problems"`
	Chats           int        `json:"chats"`
	LastMessageAt   *time.Time `json:"last_message_at"`
}

// configBotSummary is what only a config bot's summary has: its platform self
// ID from the config file and the settings the panel edits besides the name,
// persona templates and skills.
type configBotSummary struct {
	PlatformSelfID   string   `json:"platform_self_id"`
	Admins           []string `json:"admins"`
	PrivateChat      string   `json:"private_chat"`
	PrivateAllowlist []string `json:"private_allowlist"`
	Groups           []string `json:"groups"`
}

// maxChatLastRunes bounds the text of a chat's last message on the chat page.
const maxChatLastRunes = 60

// chatGroup is a bot with its panel chats as the chat page lists them. Avatar
// is the avatar image of the bot's private persona template, else its
// appearance image, "" without either (see botAvatar).
type chatGroup struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Source string      `json:"source"`
	Avatar string      `json:"avatar"`
	Chats  []chatEntry `json:"chats"`
}

// chatEntry is a panel chat on the chat page. Title is who the bot talks with
// there (see chatTitle); Last is its newest message, null without one; Online
// is whether the bot shows as online there (see onlineChats).
type chatEntry struct {
	Key           string     `json:"key"`
	Title         string     `json:"title"`
	Platform      string     `json:"platform"`
	LastMessageAt *time.Time `json:"last_message_at"`
	MessageCount  int        `json:"message_count"`
	Last          *chatLast  `json:"last"`
	Online        bool       `json:"online"`
}

// chatLast is a chat's newest message as the chat page previews it: SenderID
// is the sender's platform user ID, as messageView's; Text is on one line and
// cut to maxChatLastRunes characters, with … when cut; Images are the IDs of
// its chat images as messageView's, left out when it has none.
type chatLast struct {
	Seq      int64     `json:"seq"`
	FromBot  bool      `json:"from_bot"`
	SenderID string    `json:"sender_id"`
	At       time.Time `json:"at"`
	Text     string    `json:"text"`
	Images   []string  `json:"images,omitempty"`
}

func newChatLast(m store.MessageRecord) *chatLast {
	text := strings.Join(strings.Fields(m.Text), " ")
	if runes := []rune(text); len(runes) > maxChatLastRunes {
		text = string(runes[:maxChatLastRunes]) + "…"
	}
	return &chatLast{Seq: m.Seq, FromBot: m.FromBot, SenderID: m.PlatformUserID, At: m.OccurredAt.UTC(), Text: text,
		Images: messageImages(m)}
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

// configBotRequest is the body that updates the settings of a config bot,
// which must give every field; the bot's identity stays the config file's.
// A field left out or null is nil.
type configBotRequest struct {
	Name             *string   `json:"name"`
	Admins           *[]string `json:"admins"`
	PrivateChat      *string   `json:"private_chat"`
	PrivateAllowlist *[]string `json:"private_allowlist"`
	Groups           *[]string `json:"groups"`
	PrivateTemplate  *string   `json:"private_template"`
	GroupTemplate    *string   `json:"group_template"`
	Skills           *[]string `json:"skills"`
}

// input is r for the service, or false when r leaves out a field.
func (r configBotRequest) input() (core.ConfigBotInput, bool) {
	if r.Name == nil || r.Admins == nil || r.PrivateChat == nil || r.PrivateAllowlist == nil || r.Groups == nil ||
		r.PrivateTemplate == nil || r.GroupTemplate == nil || r.Skills == nil {
		return core.ConfigBotInput{}, false
	}
	return core.ConfigBotInput{
		Name: *r.Name, Admins: *r.Admins, PrivateChat: *r.PrivateChat, PrivateAllowlist: *r.PrivateAllowlist,
		Groups: *r.Groups, PrivateTemplate: *r.PrivateTemplate, GroupTemplate: *r.GroupTemplate, Skills: *r.Skills,
	}, true
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

// isConfigBot reports whether id is a bot of the config file.
func (s *Server) isConfigBot(id string) bool {
	_, ok := s.d.Bots.Bot(id)
	return ok && !s.d.Bots.IsPanel(id)
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
		ID: b.ID, Name: b.Name, Source: s.botSource(b.ID), Platform: b.Platform, PersonaTemplate: b.PrivateTemplate,
		PrivateTemplate: b.PrivateTemplate, GroupTemplate: b.GroupTemplate,
		Skills: strs(b.Skills), Problems: strs(s.d.Bots.Problems(b.ID)), Chats: len(chats),
	}
	if v.Source == sourceConfig {
		settings := botdir.Settings(b)
		v.configBotSummary = &configBotSummary{PlatformSelfID: b.PlatformSelfID, Admins: settings.Admins,
			PrivateChat: b.PrivateChat, PrivateAllowlist: settings.PrivateAllowlist, Groups: settings.Groups}
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

// listBotSkills lists the skills a panel bot may enable and those a config
// bot may enable, each by name with the conversation types it applies to.
func (s *Server) listBotSkills(w http.ResponseWriter, r *http.Request, _ *Session) {
	bots, ok := s.panelBotService(w, r)
	if !ok {
		return
	}
	type skillView struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Conversations []string `json:"conversations"`
	}
	views := func(skills []core.SkillInfo) []skillView {
		out := make([]skillView, 0, len(skills))
		for _, sk := range skills {
			out = append(out, skillView{sk.Name, sk.Description, strs(sk.Conversations)})
		}
		return out
	}
	s.writeJSON(w, http.StatusOK, struct {
		Skills       []skillView `json:"skills"`
		ConfigSkills []skillView `json:"config_skills"`
	}{views(bots.PrivateSkills()), views(bots.ConfigSkills())})
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
// which the request must all give, or the settings of a config bot (see
// updateConfigBot); the service audits it.
func (s *Server) updateBot(w http.ResponseWriter, r *http.Request, sess *Session) {
	bots, ok := s.panelBotService(w, r)
	if !ok {
		return
	}
	if s.isConfigBot(r.PathValue("id")) {
		s.updateConfigBot(w, r, sess, bots)
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

// updateConfigBot replaces the settings of a config bot, which the request
// must all give; its identity (ID, platform, platform self ID) stays the
// config file's, and a field the request may not set is refused like any
// unknown field.
func (s *Server) updateConfigBot(w http.ResponseWriter, r *http.Request, sess *Session, bots PanelBotService) {
	var req configBotRequest
	if !s.decode(w, r, &req) {
		return
	}
	in, ok := req.input()
	if !ok {
		s.writeError(w, r, http.StatusBadRequest, conloomapi.CodeInvalidEvent,
			"name, admins, private_chat, private_allowlist, groups, private_template, group_template and skills are all required")
		return
	}
	chats, err := s.privateChats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	b, err := bots.UpdateConfig(r.Context(), r.PathValue("id"), in, panelActor(sess))
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
// privateChats), cut to maxBotChats after sorting, each with its message count,
// newest message and whether the bot is online there, and each bot with its
// avatar. Private chats on the platforms are not listed.
func (s *Server) listChats(w http.ResponseWriter, r *http.Request, _ *Session) {
	chats, err := s.privateChats(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	all := s.d.Bots.All()
	lists := make([][]store.ConversationStats, len(all))
	var keys []string
	for i, b := range all {
		list := slices.DeleteFunc(chats[b.ID], func(st store.ConversationStats) bool { return st.Platform != store.PlatformPanel })
		lists[i] = list[:min(len(list), maxBotChats)]
		for _, st := range lists[i] {
			keys = append(keys, st.Key)
		}
	}
	last, err := s.d.Store.LastMessages(r.Context(), keys)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	online, err := s.onlineChats(r.Context(), keys)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]chatGroup, 0, len(all))
	for i, b := range all {
		g := chatGroup{ID: b.ID, Name: b.Name, Source: s.botSource(b.ID), Avatar: s.botAvatar(b),
			Chats: make([]chatEntry, 0, len(lists[i]))}
		for _, st := range lists[i] {
			title, err := s.chatTitle(r.Context(), st)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			e := chatEntry{
				Key: st.Key, Title: title, Platform: st.Platform, LastMessageAt: nullTime(st.LastMessageAt),
				MessageCount: st.MessageCount, Online: online[st.Key],
			}
			if m, ok := last[st.Key]; ok {
				e.Last = newChatLast(m)
			}
			g.Chats = append(g.Chats, e)
		}
		out = append(out, g)
	}
	s.writeJSON(w, http.StatusOK, struct {
		Bots []chatGroup `json:"bots"`
	}{out})
}

// botAvatar is the avatar image of the persona template b's private chats
// start from (the default one when b names none), else its appearance image,
// or "" when the template has neither or is not in the catalog (the bot's
// problems note that). The catalog is read from memory, so this cannot fail.
func (s *Server) botAvatar(b config.BotConfig) string {
	name := b.TemplateFor(conloomapi.ConvPrivate)
	if name == "" {
		name = "default"
	}
	doc, _ := s.d.Personas.Template(name)
	return templateAvatar(doc)
}

// templateAvatar is the image the panel shows as doc's avatar: its avatar
// image, else its appearance image, else "".
func templateAvatar(doc conloomapi.PersonaDoc) string {
	return cmp.Or(doc.AvatarImage, doc.AppearanceImage)
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
