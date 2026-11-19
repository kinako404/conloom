// Package tools holds the built-in tools that skills expose to the model:
// remember, recall, get_affinity, now, set_status, set_scene, adjust_buff,
// add_buff and remove_buff. Each acts only for the speaker and conversation of its
// skills.Invocation, never for anyone named in its arguments.
//
// recall, get_affinity and now are lookups: a reply needs their results.
// remember, set_status, set_scene, adjust_buff, add_buff and remove_buff
// change stored state and declare it (skills.StateChanger).
package tools

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"time"

	"github.com/kinako404/conloom/internal/review"
	"github.com/kinako404/conloom/internal/skills"
	"github.com/kinako404/conloom/internal/store"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

// PolicySource gives a conversation's current policy; *policy.Service is one.
type PolicySource interface {
	Current(ctx context.Context, convKey string) (store.PolicyVersion, error)
}

// PersonaSource gives a conversation's current persona, the one its bot
// replies with.
type PersonaSource interface {
	Persona(ctx context.Context, botInstanceID, convKey string) (conloomapi.PersonaDoc, error)
}

// StatusStore is the part of store.Store that set_status uses.
type StatusStore interface {
	SetStatus(ctx context.Context, s store.ConversationStatus, ret *store.ConversationTimer) error
	ClearStatus(ctx context.Context, convKey string, untilAt time.Time) (bool, error)
}

// SceneStore is the part of store.Store that set_scene uses.
type SceneStore interface {
	SetScene(ctx context.Context, s store.ConversationScene) (bool, error)
}

// BuffStore is the part of store.Store that adjust_buff, add_buff and
// remove_buff use.
type BuffStore interface {
	PanelChatKeys(ctx context.Context, botID string) ([]string, error)
	LatestPersona(ctx context.Context, convKey string) (store.PersonaVersion, error)
	UpdateBuff(ctx context.Context, convKey, name string, ev store.BuffEvent,
		apply func(cur store.BuffValue, found bool) (next store.BuffValue, before float64, err error)) (store.BuffValue, error)
	GetBuffs(ctx context.Context, convKey string) ([]store.BuffValue, error)
	AddCustomBar(ctx context.Context, b store.CustomBar, initial float64, limit int) error
	RemoveCustomBar(ctx context.Context, convKey, name, source, taskID string, keepValue bool, now time.Time) (bool, error)
	ListCustomBars(ctx context.Context, convKey string) ([]store.CustomBar, error)
	AddEffect(ctx context.Context, e store.Effect, limit int, now time.Time) error
	RemoveEffect(ctx context.Context, convKey, name, source, taskID string, now time.Time) (bool, error)
	ListEffects(ctx context.Context, convKey string, now time.Time) ([]store.Effect, error)
}

// Reviewer reviews text the model wrote before it is kept: Output in open
// content mode, OutputProtected otherwise. *review.Reviewer is one.
type Reviewer interface {
	Output(text string, pol conloomapi.ReviewPolicy) review.Result
	OutputProtected(text string, pol conloomapi.ReviewPolicy) review.Result
}

// privateOnly is the answer of the tools that only work in private chats.
const privateOnly = "只能在私聊里用。"

// clock returns the invocation's time and location, the current time and UTC
// when it has none.
func clock(inv skills.Invocation) (time.Time, *time.Location) {
	now, loc := inv.Now, inv.Location
	if now.IsZero() {
		now = time.Now()
	}
	if loc == nil {
		loc = time.UTC
	}
	return now, loc
}

// noArguments is the schema of tools that take no arguments.
var noArguments = json.RawMessage(`{"type":"object","properties":{}}`)

// shortResultRunes is the result limit (skills.ResultLimiter) of the tools
// that answer with one sentence: remember, get_affinity, now, set_status,
// set_scene, adjust_buff, add_buff and remove_buff.
const shortResultRunes = 100

// memoryOn reports whether memory is in effect for convKey, as for the
// memory commands: enabled, with long requests allowed.
func memoryOn(ctx context.Context, policies PolicySource, convKey string) (store.PolicyVersion, bool, error) {
	pv, err := policies.Current(ctx, convKey)
	if err != nil {
		return store.PolicyVersion{}, false, err
	}
	p := pv.Policy
	return pv, p.Memory.Enabled && p.Concurrency.AllowLongRequests, nil
}

// stringArg returns the string argument name of args, a JSON object; ok is
// false when args is not an object or name is missing or not a string.
func stringArg(args json.RawMessage, name string) (value string, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return "", false
	}
	raw, found := obj[name]
	if !found {
		return "", false
	}
	if err := json.Unmarshal(raw, &value); err != nil || string(raw) == "null" {
		return "", false
	}
	return value, true
}

// hasArg reports whether args, a JSON object, gives the argument name a
// value other than null.
func hasArg(args json.RawMessage, name string) bool {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return false
	}
	raw, found := obj[name]
	return found && string(raw) != "null"
}

// maxNumberArg bounds the magnitude of a numberArg, and so of an intArg.
const maxNumberArg = 1 << 30

// intArg returns the integer argument name of args, a JSON object, given as
// a number with no fractional part (20 or 20.0) or a string holding one
// ("20"); ok is false otherwise, including for "20分钟" and magnitudes over
// maxNumberArg.
func intArg(args json.RawMessage, name string) (value int, ok bool) {
	f, ok := numberArg(args, name)
	if !ok || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// numberArg returns the number argument name of args, a JSON object, given as
// a number (0.5) or a string holding one ("0.5"); ok is false otherwise,
// including for "6小时" and magnitudes over maxNumberArg.
func numberArg(args json.RawMessage, name string) (value float64, ok bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil {
		return 0, false
	}
	raw, found := obj[name]
	if !found {
		return 0, false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(strings.TrimSpace(s))
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, false
	}
	f, err := n.Float64()
	if err != nil || math.Abs(f) > maxNumberArg {
		return 0, false
	}
	return f, true
}
