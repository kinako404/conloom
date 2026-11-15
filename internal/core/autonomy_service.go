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
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

const (
	// autonomyEvents is how many of a chat's newest bar changes State shows.
	autonomyEvents = 50
	// maxBarReasonRunes bounds the reason of a bar set through the panel or
	// the API, as adjust_buff bounds the model's.
	maxBarReasonRunes = 40
)

// ErrInvalidBar refuses a bar change through the panel or the API: a bar the
// persona does not have, a value outside 0–100 or a reason that is too long.
// The error wrapping it says which; the panel and the API answer it with 400.
var ErrInvalidBar = errors.New("core: invalid bar change")

// AutonomyService shows and changes the hidden state of private chats, their
// status and bars, for the panel and the API.
type AutonomyService struct{ o *Orchestrator }

// Autonomy returns the service over the orchestrator's private chats.
func (o *Orchestrator) Autonomy() *AutonomyService { return &AutonomyService{o: o} }

// State returns the hidden state of the private chat convKey: the status
// while it lasts, the bars of the chat's persona in its order at their values
// decayed to now (their baselines when none is stored; stored bars the
// persona no longer has are left out), the newest 50 bar changes first, and
// how the bot starts the chat (privateProactiveStatus). It writes nothing,
// not even the persona version a reply would create. A chat that does not
// exist is store.ErrNotFound, another kind of conversation ErrNotPrivate.
func (s *AutonomyService) State(ctx context.Context, convKey string) (conloomapi.AutonomyState, error) {
	o := s.o
	conv, err := s.privateChat(ctx, convKey)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	now := o.now()
	out := conloomapi.AutonomyState{Bars: []conloomapi.AutonomyBar{}, Events: []conloomapi.AutonomyBarEvent{}}
	st, err := o.st.GetStatus(ctx, convKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return conloomapi.AutonomyState{}, err
	case st.UntilAt.After(now):
		out.Status = &conloomapi.AutonomyStatus{Activity: st.Activity, StartedAt: st.StartedAt.UTC(), UntilAt: st.UntilAt.UTC()}
	}
	bars, err := s.bars(ctx, conv)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	stored, err := o.st.GetBuffs(ctx, convKey)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	for _, b := range bars {
		cur, found := storedBar(stored, b.Name)
		v := barValue(b, cur, found, now)
		out.Bars = append(out.Bars, conloomapi.AutonomyBar{
			Name: b.Name, Value: roundBar(v), Band: autonomy.Band(v), Baseline: b.Baseline, Description: b.Description,
		})
	}
	events, err := o.st.ListBuffEvents(ctx, convKey, autonomyEvents)
	if err != nil {
		return conloomapi.AutonomyState{}, err
	}
	for _, e := range events {
		out.Events = append(out.Events, conloomapi.AutonomyBarEvent{
			Name: e.Name, Source: e.Source, Reason: e.Reason, Before: roundBar(e.Before), After: roundBar(e.After), At: e.CreatedAt.UTC(),
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
// the bar today). name must be one of the bars of the chat's current persona,
// and reason, trimmed, at most 40 characters; otherwise ErrInvalidBar. The
// change is recorded as a bar event, from the bar's value decayed to now (its
// baseline when none is stored), and audited as autonomy.buff; a failed audit
// is only logged. A chat that does not exist is store.ErrNotFound, another
// kind of conversation ErrNotPrivate.
func (s *AutonomyService) SetBar(ctx context.Context, convKey, name string, value int, reason, source, actor string) error {
	if source != store.BuffSourcePanel && source != store.BuffSourceAPI {
		return fmt.Errorf("core: bar change from unknown source %q", source)
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
	bars, err := s.bars(ctx, conv)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(bars, func(b conloomapi.PersonaBar) bool { return b.Name == name })
	if i < 0 {
		names := make([]string, len(bars))
		for j, b := range bars {
			names[j] = b.Name
		}
		return fmt.Errorf("%w: no bar named %q; the persona's bars are %s", ErrInvalidBar, name, strings.Join(names, ", "))
	}
	bar := bars[i]
	now := o.now()
	day := now.In(o.loc).Format(time.DateOnly)
	var before float64
	after, err := o.st.UpdateBuff(ctx, convKey, bar.Name, store.BuffEvent{Source: source, Reason: reason, CreatedAt: now},
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

// ClearStatus ends the status of the private chat convKey for actor at once,
// as set_status with minutes 0 does: the status, expired or not, is deleted
// with its status_return timer, so the bot does not come back to talk, and a
// status return already queued finds no status and ends without a message.
// It is audited as autonomy.status.clear, also when there was no status; a
// failed audit is only logged. A chat that does not exist is
// store.ErrNotFound, another kind of conversation ErrNotPrivate.
func (s *AutonomyService) ClearStatus(ctx context.Context, convKey, actor string) error {
	o := s.o
	conv, err := s.privateChat(ctx, convKey)
	if err != nil {
		return err
	}
	st, err := o.st.GetStatus(ctx, convKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	cleared, err := o.st.ClearStatus(ctx, convKey, time.Time{})
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

// bars returns the bars (conloomapi.PersonaDoc.EffectiveBars) of conv's
// current persona version, or, while it has none, of the catalog's persona
// template its bot's first version will copy, without creating that version.
func (s *AutonomyService) bars(ctx context.Context, conv store.ConversationRecord) ([]conloomapi.PersonaBar, error) {
	pv, err := s.o.st.LatestPersona(ctx, conv.Key)
	switch {
	case err == nil:
		return pv.Persona.EffectiveBars(), nil
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	name := "default"
	if bot, ok := s.o.bots.Bot(conv.BotInstanceID); ok && bot.PersonaTemplate != "" {
		name = bot.PersonaTemplate
	}
	doc, ok := s.o.personas.Template(name)
	if !ok {
		return nil, fmt.Errorf("core: unknown persona template %q", name)
	}
	return doc.EffectiveBars(), nil
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
