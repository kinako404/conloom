// Package botdir is the bot directory: the bots of the config file and the
// bots created in the panel, which every component looks bots up in.
package botdir

import (
	"slices"
	"sync/atomic"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
)

// Directory holds the config bots, fixed when it is made, and a snapshot of
// the panel bots that SetPanel replaces as a whole. It is safe for concurrent
// use: a reader sees one whole snapshot of the panel bots, old or new. The
// slices in the bots and problems it returns are shared and must not be
// modified.
type Directory struct {
	config []config.BotConfig
	panel  atomic.Pointer[panelSnapshot]
}

// PanelEntry is a panel bot as it takes effect, with Problems: notes in
// Chinese on what of its stored settings no longer applies, such as
// "模板已不存在".
type PanelEntry struct {
	Bot      config.BotConfig
	Problems []string
}

type panelSnapshot struct {
	entries []PanelEntry
	index   map[string]int
}

// New returns a directory of the config bots configBots, read as given, and
// no panel bots.
func New(configBots []config.BotConfig) *Directory {
	d := &Directory{config: configBots}
	d.panel.Store(&panelSnapshot{})
	return d
}

// Bot returns the config or panel bot with the given ID.
func (d *Directory) Bot(id string) (config.BotConfig, bool) {
	for _, b := range d.config {
		if b.ID == id {
			return b, true
		}
	}
	p := d.panel.Load()
	if i, ok := p.index[id]; ok {
		return p.entries[i].Bot, true
	}
	return config.BotConfig{}, false
}

// All lists the config bots in config order, then the panel bots in the order
// given to SetPanel.
func (d *Directory) All() []config.BotConfig {
	p := d.panel.Load()
	out := make([]config.BotConfig, 0, len(d.config)+len(p.entries))
	out = append(out, d.config...)
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

// Problems returns the problems of the panel bot id, nil for none or for a
// bot that is not a panel bot.
func (d *Directory) Problems(id string) []string {
	p := d.panel.Load()
	if i, ok := p.index[id]; ok {
		return p.entries[i].Problems
	}
	return nil
}

// PanelTemplateUsers returns the names of the panel bots whose persona
// template, as it takes effect, is name, in snapshot order.
func (d *Directory) PanelTemplateUsers(name string) []string {
	var out []string
	for _, e := range d.panel.Load().entries {
		if e.Bot.PersonaTemplate == name {
			out = append(out, e.Bot.Name)
		}
	}
	return out
}

// SetPanel replaces the panel bots with entries, in their order. An entry
// whose ID is a config bot's or an earlier entry's is left out. Entries are
// copied, so the caller may reuse them.
func (d *Directory) SetPanel(entries []PanelEntry) {
	p := &panelSnapshot{entries: make([]PanelEntry, 0, len(entries)), index: make(map[string]int, len(entries))}
	for _, e := range entries {
		if _, taken := p.index[e.Bot.ID]; taken || slices.ContainsFunc(d.config, func(b config.BotConfig) bool { return b.ID == e.Bot.ID }) {
			continue
		}
		e.Bot.Skills = slices.Clip(slices.Clone(e.Bot.Skills))
		e.Problems = slices.Clip(slices.Clone(e.Problems))
		p.index[e.Bot.ID] = len(p.entries)
		p.entries = append(p.entries, e)
	}
	d.panel.Store(p)
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
