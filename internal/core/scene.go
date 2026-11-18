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

const (
	// skillScene is the skill whose bot keeps a scene, online or in person, in
	// its private chats.
	skillScene = "scene"
	// sceneIdleTimeout is how long an in-person scene lasts after the person's
	// last message before it falls back to online.
	sceneIdleTimeout = 3 * time.Hour
	// sceneReturnSlack is how much earlier than a status's start a switch to
	// online may be and still count as leaving for that status.
	sceneReturnSlack = 2 * time.Minute
)

// effectiveScene is the scene sc, the stored one when found, at now: online
// without a row, and online, after the in-person scene it replaces, from
// TouchedAt+sceneIdleTimeout on when the person has not spoken for
// sceneIdleTimeout. fellBack reports the latter, which is not stored yet;
// TouchedAt is kept.
func effectiveScene(sc store.ConversationScene, found bool, now time.Time) (eff store.ConversationScene, fellBack bool) {
	if !found {
		return store.ConversationScene{Mode: conloomapi.SceneOnline}, false
	}
	if sc.Mode != conloomapi.SceneInPerson || now.Sub(sc.TouchedAt) < sceneIdleTimeout {
		return sc, false
	}
	eff = sc
	eff.Mode, eff.Place = conloomapi.SceneOnline, ""
	eff.PrevMode, eff.PrevPlace = conloomapi.SceneInPerson, sc.Place
	eff.SinceAt = sc.TouchedAt.Add(sceneIdleTimeout)
	return eff, true
}

// idleOnline reports whether sc is online because an in-person scene fell
// back after sceneIdleTimeout of silence (see effectiveScene), stored or not.
func idleOnline(sc store.ConversationScene) bool {
	return sc.Mode == conloomapi.SceneOnline && sc.PrevMode == conloomapi.SceneInPerson &&
		sc.SinceAt.Equal(sc.TouchedAt.Add(sceneIdleTimeout))
}

// taskScene is the scene a private task runs in; on when the bot enables
// skillScene and the scene could be read.
type taskScene struct {
	on bool
	sc store.ConversationScene
}

// inPerson reports whether the task runs in an in-person scene.
func (s taskScene) inPerson() bool { return s.on && s.sc.Mode == conloomapi.SceneInPerson }

// state is the scene line's input to autonomy.RenderState, nil when the
// scene is off.
func (s taskScene) state() *autonomy.StateScene {
	if !s.on {
		return nil
	}
	return &autonomy.StateScene{InPerson: s.inPerson(), Place: s.sc.Place}
}

// readScene reads the effective scene (effectiveScene) of a private task
// whose bot enables skillScene from the conversation's memory scope. A
// fallback to online is stored first; then, unless the bot started the task
// itself (isWake), an in-person scene is touched, since the person spoke. A
// failed read is logged and leaves the scene off; a failed write is logged
// and keeps the scene read.
func (o *Orchestrator) readScene(ctx context.Context, task store.TaskRecord, bot config.BotConfig) taskScene {
	if convType(task.ConversationKey) != conloomapi.ConvPrivate || !slices.Contains(bot.Skills, skillScene) {
		return taskScene{}
	}
	now := o.now()
	scope := memory.ScopeKeyOf(task.ConversationKey)
	raw, err := o.st.GetScene(ctx, scope)
	found := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		o.logStateError(task, err)
		return taskScene{}
	}
	eff, fellBack := effectiveScene(raw, found, now)
	if fellBack {
		if _, err := o.st.EndIdleScene(ctx, scope, raw.TouchedAt, eff.SinceAt); err != nil {
			o.logSceneWriteError(task, err)
		}
	}
	if eff.Mode == conloomapi.SceneInPerson && !isWake(task.Meta.Trigger) {
		if touched, err := o.st.TouchScene(ctx, scope, now); err != nil {
			o.logSceneWriteError(task, err)
		} else if touched {
			eff.TouchedAt = now
		}
	}
	return taskScene{on: true, sc: eff}
}

func (o *Orchestrator) logSceneWriteError(task store.TaskRecord, err error) {
	o.log.Warn("update scene failed", "task_id", task.ID, "conversation", task.ConversationKey, "error", err)
}
