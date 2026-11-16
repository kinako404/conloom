package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/ids"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// autonomyEvents is how many of the newest bar changes of a chat's memory
	// scope State shows.
	autonomyEvents = 50
	// maxBarReasonRunes bounds the reason of a bar set through the panel or
	// the API, as adjust_buff bounds the model's.
	maxBarReasonRunes = 40
)

// ErrInvalidBar refuses a bar change through the panel or the API: setting a
// bar the chat does not have, a value outside 0–100 or a reason that is too
// long, or removing a bar of the persona. The error wrapping it says which;
// the panel and the API answer it with 400.
var ErrInvalidBar = errors.New("core: invalid bar change")

// AutonomyService shows and changes the hidden state of private chats, their
// status, bars and effects, for the panel and the API. The status, bars,
// effects and their changes are those of the chat's memory scope
// (memory.ScopeKey): all panel chats of a bot share them.
type AutonomyService struct{ o *Orchestrator }

// Autonomy returns the service over the orchestrator's private chats.
func (o *Orchestrator) Autonomy() *AutonomyService { return &AutonomyService{o: o} }

// State returns the hidden state of the private chat convKey: the status
// while it lasts; the bars (scopeBars), the persona's in its order then those
// the bot added, marked Custom, in the order added, at their values decayed
// to now with their own baselines and half-lives (their baselines when none
// is stored; stored bars that are neither are left out); the names of the
// bars the bot added that a persona bar has since replaced, in the order
// added; the effects active now; the newest 50 bar and effect changes first;
// and how the bot starts the chat (privateProactiveStatus, with the chat's
// own timers). It writes nothing, not even the persona version a reply would
// create. A chat that does not exist is store.ErrNotFound, another kind of
// conversation ErrNotPrivate.
func (s *AutonomyService) State(ctx context.Context, convKey string) (conloomapi.AutonomyState, error) {
	o := s.o
	conv, err := s.privateChat(ctx, convKey)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	scope := memory.ScopeKey(conv)
	now := o.now()
	out := conloomapi.AutonomyState{Bars: []conloomapi.AutonomyBar{}, ShadowedBars: []string{},
		Effects: []conloomapi.AutonomyEffect{}, Events: []conloomapi.AutonomyBarEvent{}}
	st, err := o.st.GetStatus(ctx, scope)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return conloomapi.AutonomyState{}, err
	case st.UntilAt.After(now):
		out.Status = &conloomapi.AutonomyStatus{Activity: st.Activity, StartedAt: st.StartedAt.UTC(), UntilAt: st.UntilAt.UTC()}
	}
	doc, custom, err := s.barDefs(ctx, conv)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	stored, err := o.st.GetBuffs(ctx, scope)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	persona := doc.EffectiveBars()
	for i, b := range scopeBars(doc, custom) {
		cur, found := storedBar(stored, b.Name)
		v := barValue(b, cur, found, now)
		out.Bars = append(out.Bars, conloomapi.AutonomyBar{
			Name: b.Name, Value: roundBar(v), Band: autonomy.Band(v), Baseline: b.Baseline, Description: b.Description,
			Custom: i >= len(persona),
		})
	}
	for _, c := range custom {
		if isBarOf(persona, c.Name) {
			out.ShadowedBars = append(out.ShadowedBars, c.Name)
		}
	}
	effects, err := o.st.ListEffects(ctx, scope, now)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	for _, e := range effects {
		out.Effects = append(out.Effects, conloomapi.AutonomyEffect{
			Name: e.Name, Description: e.Description, StartedAt: e.StartedAt.UTC(), UntilAt: e.UntilAt.UTC(),
		})
	}
	events, err := o.st.ListBuffEvents(ctx, scope, autonomyEvents)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	for _, e := range events {
		out.Events = append(out.Events, conloomapi.AutonomyBarEvent{
			Name: e.Name, Kind: e.Kind, Source: e.Source, Reason: e.Reason, Before: roundBar(e.Before), After: roundBar(e.After),
			At: e.CreatedAt.UTC(),
		})
	}
	if out.Proactive, err = o.privateProactiveStatus(ctx, convKey); err != nil {
		return conloomapi.AutonomyState{}, err
	}
	return out, nil
}

// SetBar sets the bar name of the private chat convKey to value, an integer
// 0–100, for actor through source (store.BuffSourcePanel or BuffSourceAPI).
// Unlike adjust_buff it is bound by neither the limit of one change nor the
// daily limit, and it does not count towards the latter: the bar's net change
// on the local day stays the model's own (zero when the model has not changed
// the bar today). name must be one of the chat's bars (scopeBars: its current
// persona's or one the bot added; the persona's wins a name), and reason,
// trimmed, at most 40 characters; otherwise ErrInvalidBar. The change is
// recorded as a bar event, from the bar's value decayed to now with its own
// baseline and half-life (its baseline when none is stored), and audited as
// autonomy.buff; a failed audit is only logged. A chat that does not exist is
// store.ErrNotFound, another kind of conversation ErrNotPrivate.
func (s *AutonomyService) SetBar(ctx context.Context, convKey, name string, value int, reason, source, actor string) error {
	if err := checkPanelSource(source); err != nil {
		return err
	}
	o := s.o
	conv, err := s.privateChat(ctx, convKey)
	if err != nil {
		return err
	}
	if value < 0 || value > 100 {
		return fmt.Errorf("%w: value must be an integer in [0, 100], got %d", ErrInvalidBar, value)
	}
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); n > maxBarReasonRunes {
		return fmt.Errorf("%w: reason must be at most %d characters, got %d", ErrInvalidBar, maxBarReasonRunes, n)
	}
	doc, custom, err := s.barDefs(ctx, conv)
	if err != nil {
		return err
	}
	bars := scopeBars(doc, custom)
	i := slices.IndexFunc(bars, func(b conloomapi.PersonaBar) bool { return b.Name == name })
	if i < 0 {
		names := make([]string, len(bars))
		for j, b := range bars {
			names[j] = b.Name
		}
		return fmt.Errorf("%w: no bar named %q; the bars are %s", ErrInvalidBar, name, strings.Join(names, ", "))
	}
	bar := bars[i]
	now := o.now()
	day := now.In(o.loc).Format(time.DateOnly)
	var before float64
	after, err := o.st.UpdateBuff(ctx, memory.ScopeKey(conv), bar.Name, store.BuffEvent{Source: source, Reason: reason, CreatedAt: now},
		func(cur store.BuffValue, found bool) (store.BuffValue, float64, error) {
			before = barValue(bar, cur, found, now)
			next := store.BuffValue{Value: float64(value), Day: day, UpdatedAt: now}
			if found && cur.Day == day {
				next.DayDelta = cur.DayDelta
			}
			return next, before, nil
		})
	if err != nil {
		return err
	}
	o.log.Info("bar set", "conversation", convKey, "bar", bar.Name, "source", source)
	s.audit(ctx, conv, "autonomy.buff", actor, struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Reason string `json:"reason"`
		Before int    `json:"before"`
		After  int    `json:"after"`
	}{bar.Name, source, reason, roundBar(before), roundBar(after.Value)})
	return nil
}

// RemoveBar removes the bar name the bot added itself (add_buff) from the
// private chat convKey, with its value, for actor through source
// (store.BuffSourcePanel or BuffSourceAPI). When the chat's persona has since
// gained a bar of that name, which wins the name, or another chat of its
// shared panel scope has a persona with one (otherPersonaBar), only the bot's
// bar goes and the persona's keeps the value. The removal is recorded as a bar change
// and audited as autonomy.bar.remove with the name only; a failed audit is
// only logged. A bar the persona alone has cannot be removed: ErrInvalidBar.
// A name the bot has no bar of is store.ErrNotFound, as is a chat that does
// not exist; another kind of conversation is ErrNotPrivate.
func (s *AutonomyService) RemoveBar(ctx context.Context, convKey, name, source, actor string) error {
	if err := checkPanelSource(source); err != nil {
		return err
	}
	o := s.o
	conv, err := s.privateChat(ctx, convKey)
	if err != nil {
		return err
	}
	doc, err := s.persona(ctx, conv)
	if err != nil {
		return err
	}
	persona := isBarOf(doc.EffectiveBars(), name)
	keepValue := persona
	if !keepValue {
		if keepValue, err = s.otherPersonaBar(ctx, conv, name); err != nil {
			return err
		}
	}
	removed, err := o.st.RemoveCustomBar(ctx, memory.ScopeKey(conv), name, source, "", keepValue, o.now())
	switch {
	case err != nil:
		return err
	case !removed && persona:
		return fmt.Errorf("%w: %q is one of the persona's bars, which cannot be removed", ErrInvalidBar, name)
	case !removed:
		return fmt.Errorf("core: no bar %q added by the bot: %w", name, store.ErrNotFound)
	}
	o.log.Info("bar removed", "conversation", convKey, "bar", name, "source", source)
	s.audit(ctx, conv, "autonomy.bar.remove", actor, nameDetail{name})
	return nil
}

// RemoveEffect ends the effect name the bot gave itself (add_buff) in the
// private chat convKey early, for actor through source (store.BuffSourcePanel
// or BuffSourceAPI). The removal is recorded as a change and audited as
// autonomy.effect.remove with the name only; a failed audit is only logged.
// A name that is no active effect is store.ErrNotFound (one that has run out
// is left to the hourly purge), as is a chat that does not exist; another
// kind of conversation is ErrNotPrivate.
func (s *AutonomyService) RemoveEffect(ctx context.Context, convKey, name, source, actor string) error {
	if err := checkPanelSource(source); err != nil {
		return err
	}
	o := s.o
	conv, err := s.privateChat(ctx, convKey)
	if err != nil {
		return err
	}
	scope := memory.ScopeKey(conv)
	now := o.now()
	active, err := o.st.ListEffects(ctx, scope, now)
	if err != nil {
		return err
	}
	removed := false
	if slices.ContainsFunc(active, func(e store.Effect) bool { return e.Name == name }) {
		if removed, err = o.st.RemoveEffect(ctx, scope, name, source, "", now); err != nil {
			return err
		}
	}
	if !removed {
		return fmt.Errorf("core: no active effect %q: %w", name, store.ErrNotFound)
	}
	o.log.Info("effect removed", "conversation", convKey, "effect", name, "source", source)
	s.audit(ctx, conv, "autonomy.effect.remove", actor, nameDetail{name})
	return nil
}

// nameDetail is the audit detail of a removed bar or effect.
type nameDetail struct {
	Name string `json:"name"`
}

// ClearStatus ends the status of the private chat convKey for actor at once,
// as set_status with minutes 0 does: the status, expired or not, is deleted
// with the status_return timer of the chat it was set in, so the bot does not
// come back to talk, and a status return already queued, or waiting for its
// model, finds no status and ends without a message. It is audited as autonomy.status.clear, also when
// there was no status; a failed audit is only logged. A chat that does not
// exist is store.ErrNotFound, another kind of conversation ErrNotPrivate.
func (s *AutonomyService) ClearStatus(ctx context.Context, convKey, actor string) error {
	o := s.o
	conv, err := s.privateChat(ctx, convKey)
	if err != nil {
		return err
	}
	scope := memory.ScopeKey(conv)
	st, err := o.st.GetStatus(ctx, scope)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	cleared, err := o.st.ClearStatus(ctx, scope, time.Time{})
	if err != nil {
		return err
	}
	detail := struct {
		Cleared  bool      `json:"cleared"`
		Activity string    `json:"activity,omitempty"`
		UntilAt  time.Time `json:"until_at,omitzero"`
	}{Cleared: cleared}
	if cleared {
		detail.Activity, detail.UntilAt = st.Activity, st.UntilAt.UTC()
	}
	o.log.Info("status cleared", "conversation", convKey, "cleared", cleared)
	s.audit(ctx, conv, "autonomy.status.clear", actor, detail)
	return nil
}

// privateChat loads the private chat convKey: store.ErrNotFound when there is
// none, ErrNotPrivate when it is another kind of conversation.
func (s *AutonomyService) privateChat(ctx context.Context, convKey string) (store.ConversationRecord, error) {
	conv, err := s.o.panelConversation(ctx, convKey)
	if err != nil {
		return store.ConversationRecord{}, err
	}
	if conv.Type != conloomapi.ConvPrivate {
		return store.ConversationRecord{}, ErrNotPrivate
	}
	return conv, nil
}

// barDefs returns what defines the hidden bars of conv (scopeBars): conv's
// persona (s.persona) and the bars the bot added to conv's memory scope.
func (s *AutonomyService) barDefs(ctx context.Context, conv store.ConversationRecord) (conloomapi.PersonaDoc, []store.CustomBar, error) {
	doc, err := s.persona(ctx, conv)
	if err != nil {
		return conloomapi.PersonaDoc{}, nil, err
	}
	custom, err := s.o.st.ListCustomBars(ctx, memory.ScopeKey(conv))
	if err != nil {
		return conloomapi.PersonaDoc{}, nil, err
	}
	return doc, custom, nil
}

// isBarOf reports whether bars has a bar named name.
func isBarOf(bars []conloomapi.PersonaBar, name string) bool {
	return slices.ContainsFunc(bars, func(b conloomapi.PersonaBar) bool { return b.Name == name })
}

// otherPersonaBar reports whether the latest persona version of another chat
// of conv's memory scope (memory.ScopeChats) has a bar called name: the panel
// chats of a bot keep their bars' values in their shared scope by name,
// whatever persona each has. A chat without a persona version is left out.
func (s *AutonomyService) otherPersonaBar(ctx context.Context, conv store.ConversationRecord, name string) (bool, error) {
	chats, err := memory.ScopeChats(ctx, s.o.st, memory.ScopeKey(conv))
	if err != nil {
		return false, err
	}
	for _, chat := range chats {
		if chat == conv.Key {
			continue
		}
		pv, err := s.o.st.LatestPersona(ctx, chat)
		switch {
		case errors.Is(err, store.ErrNotFound):
			continue
		case err != nil:
			return false, err
		}
		if isBarOf(pv.Persona.EffectiveBars(), name) {
			return true, nil
		}
	}
	return false, nil
}

// persona returns conv's current persona version, or, while it has none, the
// catalog's persona template its bot's first version will copy, without
// creating that version.
func (s *AutonomyService) persona(ctx context.Context, conv store.ConversationRecord) (conloomapi.PersonaDoc, error) {
	pv, err := s.o.st.LatestPersona(ctx, conv.Key)
	switch {
	case err == nil:
		return pv.Persona, nil
	case !errors.Is(err, store.ErrNotFound):
		return conloomapi.PersonaDoc{}, err
	}
	name := "default"
	if bot, ok := s.o.bots.Bot(conv.BotInstanceID); ok && bot.TemplateFor(conv.Type) != "" {
		name = bot.TemplateFor(conv.Type)
	}
	doc, ok := s.o.personas.Template(name)
	if !ok {
		return conloomapi.PersonaDoc{}, fmt.Errorf("core: unknown persona template %q", name)
	}
	return doc, nil
}

// checkPanelSource refuses a change of the hidden state through a source
// other than the panel or the API.
func checkPanelSource(source string) error {
	if source != store.BuffSourcePanel && source != store.BuffSourceAPI {
		return fmt.Errorf("core: hidden state change from unknown source %q", source)
	}
	return nil
}

// audit records a change of conv's hidden state by actor as kind. A failed
// write is only logged: the change is already stored.
func (s *AutonomyService) audit(ctx context.Context, conv store.ConversationRecord, kind, actor string, detail any) {
	b, err := json.Marshal(detail)
	if err == nil {
		err = s.o.st.InsertAudit(context.WithoutCancel(ctx), store.AuditEvent{
			ID: ids.New("aud"), BotInstanceID: conv.BotInstanceID, Kind: kind, Actor: actor, Target: conv.Key,
			Detail: string(b), CreatedAt: s.o.now().UTC(),
		})
	}
	if err != nil {
		s.o.log.Error("hidden state changed but audit failed", "conversation", conv.Key, "kind", kind, "error", err)
	}
}

// roundBar is a bar value as the panel shows it.
func roundBar(v float64) int { return int(math.Round(v)) }
