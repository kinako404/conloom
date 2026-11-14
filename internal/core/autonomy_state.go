package core

import (
	"context"
	"errors"
	"slices"

	"github.com/kinako404/conloom/internal/autonomy"
	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// skillBuff is the skill whose bot gets its hidden bars in private replies;
// toolAdjustBuff is its tool, the only one a wake task offers.
const (
	skillBuff      = "buff"
	toolAdjustBuff = "adjust_buff"
)

// stateBlock returns the hidden state block (autonomy.RenderState) of a
// private task: the conversation's status while it lasts and, when the bot
// enables skillBuff, doc's bars (conloomapi.PersonaDoc.EffectiveBars) in
// their order, each at its stored value decayed to now, or at its baseline
// when none is stored. Stored bars doc does not have are left out. It is ""
// for other conversations, when there is nothing to show, or when a read
// fails, which is logged.
func (o *Orchestrator) stateBlock(ctx context.Context, task store.TaskRecord, bot config.BotConfig, doc conloomapi.PersonaDoc) string {
	if convType(task.ConversationKey) != conloomapi.ConvPrivate {
		return ""
	}
	now := o.now()
	var status *autonomy.StateStatus
	st, err := o.st.GetStatus(ctx, task.ConversationKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		o.logStateError(task, err)
		return ""
	case st.UntilAt.After(now):
		status = &autonomy.StateStatus{Activity: st.Activity, StartedAt: st.StartedAt, UntilAt: st.UntilAt}
	}
	var bars []autonomy.StateBar
	if slices.Contains(bot.Skills, skillBuff) {
		stored, err := o.st.GetBuffs(ctx, task.ConversationKey)
		if err != nil {
			o.logStateError(task, err)
			return ""
		}
		for _, b := range doc.EffectiveBars() {
			v := float64(b.Baseline)
			if i := slices.IndexFunc(stored, func(s store.BuffValue) bool { return s.Name == b.Name }); i >= 0 {
				v = autonomy.Decay(stored[i].Value, b.Baseline, b.HalfLife.Duration, now.Sub(stored[i].UpdatedAt))
			}
			bars = append(bars, autonomy.StateBar{Name: b.Name, Description: b.Description, Value: v})
		}
	}
	return autonomy.RenderState(status, bars, o.loc)
}

func (o *Orchestrator) logStateError(task store.TaskRecord, err error) {
	o.log.Warn("load hidden state failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
}
