// Package botdir is the bot directory: the bots of the config file, each with
// its settings stored in the database, and the bots created in the panel,
// which every component looks bots up in.
package botdir

import (
	"slices"
	"sync/atomic"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// Directory holds the config bots, read as given when it is made, and the
// panel bots, each as it takes effect. SetConfig replaces how config bots
// take effect otherwise than given, and SetPanel the panel bots, each as a
// whole. It is safe for concurrent use: a reader sees one whole snapshot of
// each, old or new. The slices in the bots and problems it returns are shared
// and must not be modified.
type Directory struct {
	configured []config.BotConfig
	// config holds the config bots that take effect otherwise than given,
	// with their problems.
	config atomic.Pointer[snapshot]
	panel  atomic.Pointer[snapshot]
}

// Entry is a bot as it takes effect, with Problems: notes in Chinese on what
// of its settings no longer applies, such as "模板已不存在".
type Entry struct {
	Bot      config.BotConfig
	Problems []string
}

type snapshot struct {
	entries []Entry
	index   map[string]int
}

func newSnapshot(n int) *snapshot {
	return &snapshot{entries: make([]Entry, 0, n), index: make(map[string]int, n)}
}

// add appends a copy of e unless its ID is taken.
func (s *snapshot) add(e Entry) {
	if _, taken := s.index[e.Bot.ID]; taken {
		return
	}
	e.Bot.Admins = slices.Clip(slices.Clone(e.Bot.Admins))
	e.Bot.EnabledConversations = slices.Clip(slices.Clone(e.Bot.EnabledConversations))
	e.Bot.Skills = slices.Clip(slices.Clone(e.Bot.Skills))
	e.Problems = slices.Clip(slices.Clone(e.Problems))
	s.index[e.Bot.ID] = len(s.entries)
	s.entries = append(s.entries, e)
}

func (s *snapshot) entry(id string) (Entry, bool) {
	if i, ok := s.index[id]; ok {
		return s.entries[i], true
	}
	return Entry{}, false
}

// New returns a directory of the config bots configBots, read as given and
// taking effect as given, and no panel bots. The app gives each config bot
// with its settings (ConfigBot): the stored ones or, without any,
// DefaultSettings.
func New(configBots []config.BotConfig) *Directory {
	d := &Directory{configured: configBots}
	d.config.Store(newSnapshot(0))
	d.panel.Store(newSnapshot(0))
	return d
}

// ConfigBots returns the config bots as given to New, in config order.
func (d *Directory) ConfigBots() []config.BotConfig {
	return d.configured
}

// DefaultSettings returns the settings of the config bot id while none are
// stored: named id, without admins, chatting privately with its admins
// only, in no groups, starting every new conversation from the persona
// template default and without skills.
func DefaultSettings(id string) store.BotSettings {
	return store.BotSettings{BotInstanceID: id, Name: id, Admins: []string{}, PrivateChat: "admins",
		PrivateAllowlist: []string{}, Groups: []string{}, PrivateTemplate: "default", GroupTemplate: "default",
		Skills: []string{}}
}

// WithDefaults returns the config bots bots, each with its DefaultSettings:
// how the app gives them to New, before their stored settings are published
// (core.PanelBotService.Load).
func WithDefaults(bots []config.BotConfig) []config.BotConfig {
	out := make([]config.BotConfig, 0, len(bots))
	for _, b := range bots {
		out = append(out, ConfigBot(b, DefaultSettings(b.ID)))
	}
	return out
}

// LegacySettings returns the settings of the config bots bots as their
// migration into the database stores them: those the version before the
// move ran each with (config.BotConfig.WithLegacySettings), updated by
// system.
func LegacySettings(bots []config.BotConfig) []store.BotSettings {
	out := make([]store.BotSettings, 0, len(bots))
	for _, b := range bots {
		s := Settings(b.WithLegacySettings())
		s.UpdatedBy = "system"
		out = append(out, s)
	}
	return out
}

// ConfigBot returns the config bot b with the settings s: b's identity (ID,
// platform and platform self ID) and s's settings, with the groups and then
// the private allowlist of s as its enabled conversations.
func ConfigBot(b config.BotConfig, s store.BotSettings) config.BotConfig {
	convs := make([]conloomapi.ConversationRef, 0, len(s.Groups)+len(s.PrivateAllowlist))
	for _, id := range s.Groups {
		convs = append(convs, conloomapi.ConversationRef{Type: conloomapi.ConvGroup, PlatformID: id})
	}
	for _, id := range s.PrivateAllowlist {
		convs = append(convs, conloomapi.ConversationRef{Type: conloomapi.ConvPrivate, PlatformID: id})
	}
	return config.BotConfig{
		ID: b.ID, Platform: b.Platform, PlatformSelfID: b.PlatformSelfID,
		Name: s.Name, Admins: slices.Clone(s.Admins), EnabledConversations: convs, PrivateChat: s.PrivateChat,
		PrivateTemplate: s.PrivateTemplate, GroupTemplate: s.GroupTemplate, Skills: slices.Clone(s.Skills),
	}
}

// Settings returns the settings of the config bot b, as ConfigBot reads them:
// the group entries of its enabled conversations as the groups and the
// private ones as the private allowlist. Nil lists are returned empty.
func Settings(b config.BotConfig) store.BotSettings {
	s := store.BotSettings{BotInstanceID: b.ID, Name: b.Name, Admins: []string{}, PrivateChat: b.PrivateChat,
		PrivateAllowlist: []string{}, Groups: []string{}, PrivateTemplate: b.PrivateTemplate,
		GroupTemplate: b.GroupTemplate, Skills: []string{}}
	s.Admins = append(s.Admins, b.Admins...)
	s.Skills = append(s.Skills, b.Skills...)
	for _, c := range b.EnabledConversations {
		if c.Type == conloomapi.ConvGroup {
			s.Groups = append(s.Groups, c.PlatformID)
		} else {
			s.PrivateAllowlist = append(s.PrivateAllowlist, c.PlatformID)
		}
	}
	return s
}

// configEntries returns the config bots as they take effect, in config order.
func (d *Directory) configEntries() []Entry {
	c := d.config.Load()
	out := make([]Entry, 0, len(d.configured))
	for _, b := range d.configured {
		e, ok := c.entry(b.ID)
		if !ok {
			e = Entry{Bot: b}
		}
		out = append(out, e)
	}
	return out
}

// Bot returns the config or panel bot with the given ID, as it takes effect.
func (d *Directory) Bot(id string) (config.BotConfig, bool) {
	for _, b := range d.configured {
		if b.ID == id {
			if e, ok := d.config.Load().entry(id); ok {
				return e.Bot, true
			}
			return b, true
		}
	}
	if e, ok := d.panel.Load().entry(id); ok {
		return e.Bot, true
	}
	return config.BotConfig{}, false
}

// All lists the config bots in config order, then the panel bots in the order
// given to SetPanel, each as it takes effect.
func (d *Directory) All() []config.BotConfig {
	c, p := d.configEntries(), d.panel.Load()
	out := make([]config.BotConfig, 0, len(c)+len(p.entries))
	for _, e := range c {
		out = append(out, e.Bot)
	}
	for _, e := range p.entries {
		out = append(out, e.Bot)
	}
	return out
}

// IsPanel reports whether id is a panel bot.
func (d *Directory) IsPanel(id string) bool {
	_, ok := d.panel.Load().index[id]
	return ok
}

// Problems returns the problems of the config or panel bot id, nil for none
// or for an unknown bot.
func (d *Directory) Problems(id string) []string {
	if e, ok := d.config.Load().entry(id); ok {
		return e.Problems
	}
	e, _ := d.panel.Load().entry(id)
	return e.Problems
}

// TemplateUsers returns the IDs of the bots whose private or group persona
// template, as it takes effect, is name: the config bots in config order,
// then the panel bots in snapshot order.
func (d *Directory) TemplateUsers(name string) []string {
	var out []string
	for _, e := range slices.Concat(d.configEntries(), d.panel.Load().entries) {
		if e.Bot.PrivateTemplate == name || e.Bot.GroupTemplate == name {
			out = append(out, e.Bot.ID)
		}
	}
	return out
}

// SetConfig replaces how the config bots take effect: a config bot takes
// effect as the entry with its ID, with that entry's problems, or as given
// to New without problems when there is none. An entry whose ID is no config
// bot's, or an earlier entry's, is left out. Entries are copied, so the
// caller may reuse them.
func (d *Directory) SetConfig(entries []Entry) {
	s := newSnapshot(len(entries))
	for _, e := range entries {
		if d.isConfig(e.Bot.ID) {
			s.add(e)
		}
	}
	d.config.Store(s)
}

// isConfig reports whether id is a config bot.
func (d *Directory) isConfig(id string) bool {
	return slices.ContainsFunc(d.configured, func(b config.BotConfig) bool { return b.ID == id })
}

// SetPanel replaces the panel bots with entries, in their order. An entry
// whose ID is a config bot's or an earlier entry's is left out. Entries are
// copied, so the caller may reuse them.
func (d *Directory) SetPanel(entries []Entry) {
	s := newSnapshot(len(entries))
	for _, e := range entries {
		if !d.isConfig(e.Bot.ID) {
			s.add(e)
		}
	}
	d.panel.Store(s)
}

// PanelBotConfig is b as a directory bot: on the panel platform, where its
// private chats are the panel's, without platform self ID, admins or enabled
// conversations, and with its persona template as both its private and its
// group template.
func PanelBotConfig(b store.PanelBot) config.BotConfig {
	return config.BotConfig{
		ID: b.ID, Platform: store.PlatformPanel, Name: b.Name, PrivateChat: "admins",
		PrivateTemplate: b.PersonaTemplate, GroupTemplate: b.PersonaTemplate, Skills: slices.Clip(slices.Clone(b.Skills)),
	}
}
