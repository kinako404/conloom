// Package botdir is the bot directory: the bots of the config file and the
// bots created in the panel, which every component looks bots up in.
package botdir

import (
	"slices"
	"sync/atomic"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
)

// Directory holds the config bots, read as given when it is made, and the
// panel bots, each as it takes effect. SetConfig replaces how config bots
// take effect otherwise than configured, and SetPanel the panel bots, each as
// a whole. It is safe for concurrent use: a reader sees one whole snapshot of
// each, old or new. The slices in the bots and problems it returns are shared
// and must not be modified.
type Directory struct {
	configured []config.BotConfig
	// config holds the config bots that take effect otherwise than
	// configured, with their problems.
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
// taking effect as configured, and no panel bots.
func New(configBots []config.BotConfig) *Directory {
	d := &Directory{configured: configBots}
	d.config.Store(newSnapshot(0))
	d.panel.Store(newSnapshot(0))
	return d
}

// ConfigBots returns the config bots as configured, in config order.
func (d *Directory) ConfigBots() []config.BotConfig {
	return d.configured
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

// TemplateUsers returns the bots whose persona template, as it takes effect,
// is name: the config bots in config order, each as its name and ID (just the
// ID without a name), then the panel bots by name in snapshot order.
func (d *Directory) TemplateUsers(name string) []string {
	var out []string
	for _, e := range d.configEntries() {
		if e.Bot.PersonaTemplate != name {
			continue
		}
		if e.Bot.Name == "" {
			out = append(out, e.Bot.ID)
		} else {
			out = append(out, e.Bot.Name+"（"+e.Bot.ID+"）")
		}
	}
	for _, e := range d.panel.Load().entries {
		if e.Bot.PersonaTemplate == name {
			out = append(out, e.Bot.Name)
		}
	}
	return out
}

// SetConfig replaces how the config bots take effect: a config bot takes
// effect as the entry with its ID, with that entry's problems, or as
// configured without problems when there is none. An entry whose ID is no
// config bot's, or an earlier entry's, is left out. Entries are copied, so
// the caller may reuse them.
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
// conversations.
func PanelBotConfig(b store.PanelBot) config.BotConfig {
	return config.BotConfig{
		ID: b.ID, Platform: store.PlatformPanel, Name: b.Name, PrivateChat: "admins",
		PersonaTemplate: b.PersonaTemplate, Skills: slices.Clip(slices.Clone(b.Skills)),
	}
}
