package core

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/memory"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// skillBuff is the skill whose bot gets its hidden bars and effects in
// private replies; wakeTools are its tools, the only ones a wake task offers.
const skillBuff = "buff"

var wakeTools = []string{"adjust_buff", "add_buff", "remove_buff"}

// stateBlock returns the hidden state block (autonomy.RenderState) of a
// private task, read from the conversation's memory scope (memory.ScopeKey,
// shared by a bot's panel chats): the status while it lasts and, when the bot
// enables skillBuff, the bars (scopeBars), each at the scope's stored value
// decayed to now with its baseline and half-life, or at its baseline when
// none is stored, then the effects still active. Stored bars that are neither
// doc's nor added by the bot are left out. It is "" for other conversations,
// when there is nothing to show, or when a read fails, which is logged.
func (o *Orchestrator) stateBlock(ctx context.Context, task store.TaskRecord, bot config.BotConfig, doc conloomapi.PersonaDoc) string {
	if convType(task.ConversationKey) != conloomapi.ConvPrivate {
		return ""
	}
	now := o.now()
	scope := memory.ScopeKeyOf(task.ConversationKey)
	var status *autonomy.StateStatus
	st, err := o.st.GetStatus(ctx, scope)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		o.logStateError(task, err)
		return ""
	case st.UntilAt.After(now):
		status = &autonomy.StateStatus{Activity: st.Activity, StartedAt: st.StartedAt, UntilAt: st.UntilAt}
	}
	var bars []autonomy.StateBar
	var effects []autonomy.StateEffect
	if slices.Contains(bot.Skills, skillBuff) {
		stored, err := o.st.GetBuffs(ctx, scope)
		if err != nil {
			o.logStateError(task, err)
			return ""
		}
		custom, err := o.st.ListCustomBars(ctx, scope)
		if err != nil {
			o.logStateError(task, err)
			return ""
		}
		active, err := o.st.ListEffects(ctx, scope, now)
		if err != nil {
			o.logStateError(task, err)
			return ""
		}
		for _, b := range scopeBars(doc, custom) {
			cur, found := storedBar(stored, b.Name)
			bars = append(bars, autonomy.StateBar{Name: b.Name, Description: b.Description, Value: barValue(b, cur, found, now)})
		}
		for _, e := range active {
			effects = append(effects, autonomy.StateEffect{Name: e.Name, Description: e.Description, UntilAt: e.UntilAt})
		}
	}
	return autonomy.RenderState(status, bars, effects, now, o.loc)
}

// scopeBars returns the hidden bars of a memory scope: doc's
// (conloomapi.PersonaDoc.EffectiveBars) in their order, then those of custom,
// the bars the bot added, whose name none of doc's has; doc's bar wins a
// name.
func scopeBars(doc conloomapi.PersonaDoc, custom []store.CustomBar) []conloomapi.PersonaBar {
	bars := doc.EffectiveBars()
	persona := len(bars)
	for _, c := range custom {
		if !slices.ContainsFunc(bars[:persona], func(b conloomapi.PersonaBar) bool { return b.Name == c.Name }) {
			bars = append(bars, conloomapi.PersonaBar{Name: c.Name, Baseline: c.Baseline,
				HalfLife: conloomapi.Duration{Duration: c.HalfLife}, Description: c.Description})
		}
	}
	return bars
}

// storedBar finds the bar name among the stored values of a conversation.
func storedBar(stored []store.BuffValue, name string) (store.BuffValue, bool) {
	if i := slices.IndexFunc(stored, func(s store.BuffValue) bool { return s.Name == name }); i >= 0 {
		return stored[i], true
	}
	return store.BuffValue{}, false
}

// barValue is the value of bar b at now: cur, its stored value, decayed to
// now when found, else b's baseline.
func barValue(b conloomapi.PersonaBar, cur store.BuffValue, found bool, now time.Time) float64 {
	if !found {
		return float64(b.Baseline)
	}
	return autonomy.Decay(cur.Value, b.Baseline, b.HalfLife.Duration, now.Sub(cur.UpdatedAt))
}

func (o *Orchestrator) logStateError(task store.TaskRecord, err error) {
	o.log.Warn("load hidden state failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
}
